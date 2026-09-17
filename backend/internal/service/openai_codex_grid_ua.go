package service

import (
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// codex-official-client-identity Phase 1：格子收敛（design.md D2）。
//
// 入站客户端 UA 分类落格 (os×arch, family, version∈窗口)；可分类的流量出站
// 使用格子身份，UA 跟随用户真实平台（请求体内 cwd / shell 输出等 OS 指纹与
// UA 一致）；不可分类（非官方家族 / OS 无法识别 / 版本出窗 / 未实测家族）
// 回退 Phase 0 的每账号 profile canonical。
//
// 格子规则（design D2 定稿 + kimi 审查加固）：
//   - family：CLI（codex-tui/codex_cli_rs/codex_exec）整串生成；`Codex ` 家族
//     仅放行已实测白名单（当前唯一 "Codex Desktop"），按「重造而非透传」
//     处理——terminal 槽钉死 unknown、trailer 仅在 name==originator 且
//     build 号为数字点分时保留，杜绝客户端可控 freeform 槽铸造 UA 基数。
//   - os×arch：Mac 钉 arm64、Windows 钉 x86_64、Linux 保留用户真实 arch
//     （上界 24 格：4 个 os×arch × 2 family × 3 version）。
//   - version：窗口 [当前生效版本-2, +2]（预发布按基版本判定，出站逐字
//     保留），且 ≥ 上游硬下界；自称未来版本（>+2）与陈旧版本（<-2）都回退。
//   - terminal：freeform 可控字段，不保留用户值，按 (账号 namespace, os)
//     确定性选一个高人口值（防轮换铸造）。
//   - OS 版本：归一化为「当前主流」，随发版更新表；实现 os_info 三段形态。
//
// 已知边界（design 记录）：版本下界暂未复用全局 min_codex_version 设置；
// WS 池化连接的握手身份分桶随 1b（installation 对齐）解决。

const (
	codexGridOSMac     = "mac"
	codexGridOSLinux   = "linux"
	codexGridOSWindows = "windows"
)

// codexGridNormalizedOS 是各 OS 的归一化段（os_info 三段形态），随发版更新。
// 维护规则与 codexUAProfileTable 一致：只随发版改，不做动态跟随。
var codexGridNormalizedOS = map[string]string{
	codexGridOSMac:     "Mac OS 26.6.0",
	codexGridOSLinux:   "Ubuntu 24.4.0",
	codexGridOSWindows: "Windows 10.0.26100",
}

// codexGridTerminalByOS 是各平台的高人口终端 token 池（等权），按
// (namespace, os) 确定性选取，稳定不变。
var codexGridTerminalByOS = map[string][]string{
	codexGridOSMac: {
		"Apple_Terminal/455.1.1",
		"iTerm.app/3.7.1",
		"vscode/1.103.0",
		"xterm-256color",
	},
	codexGridOSLinux: {
		"xterm-256color",
		"vscode/1.103.0",
		"tmux-256color",
		"WindowsTerminal",
	},
	codexGridOSWindows: {
		"WindowsTerminal",
		"vscode/1.103.0",
	},
}

// codexGridCLIFamilies 是参与 CLI 整串格子化的官方家族。
var codexGridCLIFamilies = map[string]bool{
	"codex-tui":    true,
	"codex_cli_rs": true,
	"codex_exec":   true,
}

// codexGridDesktopFamilies 是 `Codex ` 空格家族的已实测白名单（大小写敏感，
// 上游按大小写敏感 starts_with("Codex ") 判定）。未实测名（Codex IDE 等）
// 回退 profile，避免为未经验证的客户端凭空生成 UA 形态。
var codexGridDesktopFamilies = map[string]bool{
	"Codex Desktop": true,
}

// codexGridAppFamilyPrefix 是 `Codex ` 家族前缀（保留大小写比较）。
const codexGridAppFamilyPrefix = "Codex "

// codexGridDesktopTerminal 是 Desktop（GUI 环境）的固定 terminal token，
// 实测样本（2026-09 macOS Codex Desktop）为 unknown。
const codexGridDesktopTerminal = "unknown"

// codexGridOSPattern 匹配 UA 的首个 `(os; arch)` 括号组。Desktop UA 的尾部
// trailer 组（`(Codex Desktop; 26.x)`）因含空格名而非 os;arch 形态，且非
// 首组，不会被误取。
var codexGridOSPattern = regexp.MustCompile(`\(([^;()]+);\s*([^;()]+)\)`)

// codexGridTrailerVersionPattern 校验 trailer build 号（数字点分）。
var codexGridTrailerVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// codexGridKnownLinuxHints 是无 "linux" 子串的常见发行版 OS 串提示。
var codexGridKnownLinuxHints = []string{"ubuntu", "debian", "fedora", "centos", "arch", "alpine", "rocky", "opensuse", "gentoo", "kali", "zorin", "pop!_os", "linuxmint"}

// codexGridOSKind 把 OS 段归到 mac / linux / windows；无法识别返回空
//（该请求回退 profile canonical，不做无依据的平台改写）。
func codexGridOSKind(osSegment string) string {
	lower := strings.ToLower(strings.TrimSpace(osSegment))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "mac os"), strings.HasPrefix(lower, "macos"):
		return codexGridOSMac
	case strings.Contains(lower, "windows"):
		return codexGridOSWindows
	}
	if strings.Contains(lower, "linux") {
		return codexGridOSLinux
	}
	for _, hint := range codexGridKnownLinuxHints {
		if strings.Contains(lower, hint) {
			return codexGridOSLinux
		}
	}
	return ""
}

// codexGridOSArch 把 arch 归到格：Mac 钉 arm64、Windows 钉 x86_64、
// Linux 保留真实值（未知回 x86_64，人口大头）。
func codexGridOSArch(osKind, clientArch string) string {
	switch osKind {
	case codexGridOSMac:
		return "arm64"
	case codexGridOSWindows:
		return "x86_64"
	default:
		if clientArch == "arm64" {
			return "arm64"
		}
		return "x86_64"
	}
}

// codexGridVersionWindow 是允许的版本落后/领先数（[last-2, last+2]，design
// D2 原为 [last-2, last]，kimi 审查后加上界：真实客户端领先自动同步值几个
// minor 是常态，但自称 +∞ 的未来版本（0.999.0）比陈旧版本更显眼）。
const codexGridVersionWindow = 2

// codexGridVersionBounds 返回版本窗口 [floor, ceiling]，锚点为 canonical
// 解析链的当前生效版本（面板版本号 → 自动同步 → 内置），不取用户自报值。
func codexGridVersionBounds() (floor, ceiling string) {
	current := codexClientVersionFromUA(codexCanonicalUserAgent()) // 已含 <0.144.0 回退
	parts := strings.SplitN(current, ".", 3)
	if len(parts) < 2 {
		return codexUpstreamMinVersion, ""
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || major < 0 || minor < 0 {
		return codexUpstreamMinVersion, ""
	}
	lowMinor, highMinor := minor-codexGridVersionWindow, minor+codexGridVersionWindow
	if lowMinor < 0 {
		lowMinor = 0
	}
	floor = strconv.Itoa(major) + "." + strconv.Itoa(lowMinor) + ".0"
	if CompareVersions(floor, codexUpstreamMinVersion) < 0 {
		floor = codexUpstreamMinVersion
	}
	ceiling = strconv.Itoa(major) + "." + strconv.Itoa(highMinor) + ".999999"
	return floor, ceiling
}

// codexGridIdentity 是格子化后的出站身份。
type codexGridIdentity struct {
	userAgent  string
	originator string
	version    string
}

// buildCodexGridIdentity 把客户端真实 UA 分类落格并生成出站身份。
// ok=false 表示不可格子化（调用方回退账号 profile canonical）：
// 非官方家族、未实测家族、OS 无法识别、版本无法解析或出窗。
//
// 注意：入参必须是「客户端真实 UA」。网关合成的身份（canonical/profile）
// 进入本函数会造成探针与推理路径的身份分裂（kimi P1），合成路径调用方
// 不得触发格子分支。
func buildCodexGridIdentity(clientUA, identityNamespace string) (codexGridIdentity, bool) {
	originator, pairedUA, ok := openai.PairCodexClientIdentity(clientUA)
	if !ok || pairedUA == "" {
		return codexGridIdentity{}, false
	}

	// 家族路由先于版本/OS 解析：vscode / sdk 等不可格子化流量不付解析成本。
	if codexGridDesktopFamilies[originator] {
		return buildCodexGridDesktopIdentity(originator, pairedUA)
	}
	if !codexGridCLIFamilies[originator] {
		return codexGridIdentity{}, false
	}

	match := codexGridOSPattern.FindStringSubmatch(pairedUA)
	if len(match) != 3 {
		return codexGridIdentity{}, false
	}
	osKind := codexGridOSKind(match[1])
	if osKind == "" {
		return codexGridIdentity{}, false
	}
	arch := codexGridOSArch(osKind, strings.TrimSpace(match[2]))

	// 版本：窗口判定用引擎基版本（剥离预发布后缀；语义上 base 恰在边界上的
	// 预发布（如 0.144.0-alpha.1）视作等于边界值，与 pairCodexIdentityHeaders
	// 既有语义一致），出站逐字保留客户端原段（version 头必须与 UA 版本段
	// 逐字一致）。
	verbatimVersion := openai.CodexUserAgentVersion(clientUA)
	baseVersion, parsed := openai.ParseCodexEngineVersion(clientUA)
	if !parsed || NormalizeCodexClientVersion(verbatimVersion) == "" {
		return codexGridIdentity{}, false
	}
	floor, ceiling := codexGridVersionBounds()
	if CompareVersions(baseVersion, floor) < 0 {
		return codexGridIdentity{}, false
	}
	if ceiling != "" && CompareVersions(baseVersion, ceiling) > 0 {
		return codexGridIdentity{}, false
	}

	// CLI 家族：整串生成。family 名沿用配对出的官方 originator（覆盖场景下
	// 由尾部 trailer 恢复真实客户端），terminal 按 (namespace, os) 钉死。
	normalizedOS := codexGridNormalizedOS[osKind]
	if normalizedOS == "" {
		return codexGridIdentity{}, false
	}
	return codexGridIdentity{
		userAgent:  originator + "/" + verbatimVersion + " (" + normalizedOS + "; " + arch + ") " + pickCodexGridTerminal(identityNamespace, osKind),
		originator: originator,
		version:    verbatimVersion,
	}, true
}

// buildCodexGridDesktopIdentity 重造 Desktop 家族出站 UA：版本段取客户端
// 原值，OS 段归一化，terminal 槽钉死 unknown，trailer 仅在 name==originator
// 且 build 号为数字点分时保留（kimi P1：客户端可控 freeform 槽不得逐字放行，
// 否则 UA 基数可被按请求铸造）。
//
// 基底必须用 PairCodexClientIdentity 返回的 pairedUA 而非原始 clientUA
//（kimi P0）：override 场景（cccc/… (Codex Desktop; 26.x)）下原始首段是
// 第三方名，直接透传会造出 originator 与 UA 首段错配的身份（issue #3901）。
func buildCodexGridDesktopIdentity(originator, pairedUA string) (codexGridIdentity, bool) {
	match := codexGridOSPattern.FindStringSubmatch(pairedUA)
	if len(match) != 3 {
		return codexGridIdentity{}, false
	}
	osKind := codexGridOSKind(match[1])
	if osKind == "" {
		return codexGridIdentity{}, false
	}
	arch := codexGridOSArch(osKind, strings.TrimSpace(match[2]))
	normalizedOS := codexGridNormalizedOS[osKind]
	if normalizedOS == "" {
		return codexGridIdentity{}, false
	}
	verbatimVersion := openai.CodexUserAgentVersion(pairedUA)
	if NormalizeCodexClientVersion(verbatimVersion) == "" {
		return codexGridIdentity{}, false
	}
	baseVersion, parsed := openai.ParseCodexEngineVersion(pairedUA)
	if !parsed {
		return codexGridIdentity{}, false
	}
	floor, ceiling := codexGridVersionBounds()
	if CompareVersions(baseVersion, floor) < 0 {
		return codexGridIdentity{}, false
	}
	if ceiling != "" && CompareVersions(baseVersion, ceiling) > 0 {
		return codexGridIdentity{}, false
	}

	ua := originator + "/" + verbatimVersion + " (" + normalizedOS + "; " + arch + ") " + codexGridDesktopTerminal + codexGridValidatedTrailer(pairedUA, originator)
	return codexGridIdentity{
		userAgent:  ua,
		originator: originator,
		version:    verbatimVersion,
	}, true
}

// codexGridValidatedTrailer 提取 pairedUA 尾部 `(name; build)` 组：仅当
// name 与 originator 一致且 build 号为数字点分时保留，否则丢弃（不回退
// 整个请求——trailer 是可选标识组，丢弃不影响配对不变式）。
func codexGridValidatedTrailer(pairedUA, originator string) string {
	open := strings.LastIndex(pairedUA, "(")
	if open < 0 {
		return ""
	}
	inner := pairedUA[open+1:]
	closeIdx := strings.Index(inner, ")")
	if closeIdx < 0 || strings.LastIndex(inner, ")") != closeIdx {
		return ""
	}
	name, build, found := strings.Cut(inner[:closeIdx], ";")
	if !found {
		return ""
	}
	name, build = strings.TrimSpace(name), strings.TrimSpace(build)
	if name != originator || !codexGridTrailerVersionPattern.MatchString(build) {
		return ""
	}
	return " (" + name + "; " + build + ")"
}

// pickCodexGridTerminal 按 (namespace, osKind) 确定性选取终端 token：
// 同账号同平台永远同值（稳定），不同账号自然散开（贴近真实人口）。
func pickCodexGridTerminal(identityNamespace, osKind string) string {
	pool := codexGridTerminalByOS[osKind]
	if len(pool) == 0 {
		return "xterm-256color"
	}
	return pool[codexProfileHashMod(codexUAProfileSeedNamespace+"grid-terminal:"+osKind+":"+identityNamespace, len(pool))]
}

// codexGridUAEnabled 与 codexForceCodexCLIPublished 是进程级开关快照，
// 由持有配置的服务在构造时发布（enforce 收口点拿不到配置）。
var codexGridUAEnabled = func() *atomic.Bool {
	v := &atomic.Bool{}
	v.Store(true)
	return v
}()

// SetCodexGridUAEnabled 发布格子收敛开关（kill-switch）。
func SetCodexGridUAEnabled(enabled bool) {
	codexGridUAEnabled.Store(enabled)
}

var codexForceCodexCLIPublished atomic.Bool

// SetCodexForceCodexCLIPublished 发布 ForceCodexCLI 配置快照：强制规范身份
// 优先于格子收敛（管理员显式语义）。
func SetCodexForceCodexCLIPublished(enabled bool) {
	codexForceCodexCLIPublished.Store(enabled)
}
