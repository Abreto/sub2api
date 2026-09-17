package service

import (
	"crypto/sha256"
	"encoding/binary"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// codexUAProfileEntry 是出站 canonical UA 的「机器 profile」：OS 段 + arch + 终端
// token。表内条目全部取自自然界高人口组合（含 WSL 的 Ubuntu×WindowsTerminal），
// 权重 ≈ 真实流量占比；OS 版本串已按 os_info 语义化三段归一化（15.5→15.5.0、
// 22.04→22.4.0），随 sub2api 发版缓慢更新，不做动态跟随（身份值必须长期稳定，
// AA26-251A：previously consistent metadata 的突变本身是检测信号）。
type codexUAProfileEntry struct {
	os       string
	arch     string
	terminal string
	weight   int
}

// codexUAProfileTable 2026-09 基线。维护规则：只收录真实存在的组合；每条都应是
// 高人口形态；改表等于全网关账号集体换机器，仅随发版进行。
var codexUAProfileTable = []codexUAProfileEntry{
	// macOS（Apple Silicon 为主，Intel 残留一条）
	{os: "Mac OS 26.6.0", arch: "arm64", terminal: "Apple_Terminal/455.1.1", weight: 8},
	{os: "Mac OS 26.6.0", arch: "arm64", terminal: "iTerm.app/3.7.1", weight: 7},
	{os: "Mac OS 26.4.0", arch: "arm64", terminal: "vscode/1.103.0", weight: 5},
	{os: "Mac OS 26.6.0", arch: "arm64", terminal: "Ghostty", weight: 3},
	{os: "Mac OS 26.6.0", arch: "arm64", terminal: "xterm-256color", weight: 2},
	{os: "Mac OS 26.6.0", arch: "x86_64", terminal: "iTerm.app/3.7.1", weight: 1},

	// Linux x86_64（xterm 为主，WSL 的 WindowsTerminal 是真实组合）
	{os: "Ubuntu 24.4.0", arch: "x86_64", terminal: "xterm-256color", weight: 10},
	{os: "Ubuntu 22.4.0", arch: "x86_64", terminal: "xterm-256color", weight: 5},
	{os: "Ubuntu 24.4.0", arch: "x86_64", terminal: "vscode/1.103.0", weight: 4},
	{os: "Ubuntu 24.4.0", arch: "x86_64", terminal: "tmux-256color", weight: 3},
	{os: "Ubuntu 24.4.0", arch: "x86_64", terminal: "WindowsTerminal", weight: 3},
	{os: "Ubuntu 22.4.0", arch: "x86_64", terminal: "WindowsTerminal", weight: 2},

	// Linux arm64（ARM 云主机 / 开发板）
	{os: "Ubuntu 24.4.0", arch: "arm64", terminal: "xterm-256color", weight: 3},
	{os: "Ubuntu 22.4.0", arch: "arm64", terminal: "xterm-256color", weight: 1},

	// Windows
	{os: "Windows 10.0.26100", arch: "x86_64", terminal: "WindowsTerminal", weight: 6},
	{os: "Windows 10.0.26100", arch: "x86_64", terminal: "vscode/1.103.0", weight: 2},
}

const codexUAProfileSeedNamespace = "sub2api:codex-ua-profile:v1:"

// codexUAProfileTotalWeight 是表的权重总和（包级常量语义，随表一起维护）。
var codexUAProfileTotalWeight = func() int {
	total := 0
	for _, entry := range codexUAProfileTable {
		total += entry.weight
	}
	return total
}()

// codexProfileHashMod 返回 sha256(input) 的稳定桶下标（mod 个桶），供
// profile 加权抽签与格子 terminal 选取等确定性派生共用。
func codexProfileHashMod(input string, mod int) int {
	if mod <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(input))
	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(mod))
}

// deriveCodexUAProfileSuffix 由身份 seed 确定性派生 profile 后缀
// （" (os; arch) terminal"）。同一 seed 永远返回同一结果：纯函数、零状态、
// 重启不漂移。目标不是账号间唯一（碰撞合法且符合真实 UA 人口分布），而是
// 消灭「全网关 / 全部署共享同一精确 UA 串」的单点指纹。
func deriveCodexUAProfileSuffix(identitySeed string) string {
	if identitySeed == "" || codexUAProfileTotalWeight <= 0 {
		return codexCLIUserAgentSuffix
	}
	sum := sha256.Sum256([]byte(codexUAProfileSeedNamespace + identitySeed))
	cursor := binary.BigEndian.Uint64(sum[:8]) % uint64(codexUAProfileTotalWeight)
	for _, entry := range codexUAProfileTable {
		if cursor < uint64(entry.weight) {
			return " (" + entry.os + "; " + entry.arch + ") " + entry.terminal
		}
		cursor -= uint64(entry.weight)
	}
	return codexCLIUserAgentSuffix
}

// buildCodexProfileUserAgent 按 profile 后缀拼出完整 canonical UA。版本段永远
// 由调用方传入的当前生效版本（面板 → 自动同步 → 内置）填充，profile 不含版本
// 语义——陈旧版本正是上游优先降载的那一侧（2eb24814f 事故结论）。
func buildCodexProfileUserAgent(identitySeed, version string) string {
	if version == "" {
		version = codexCLIVersion
	}
	return openai.CodexDefaultOriginator + "/" + version + deriveCodexUAProfileSuffix(identitySeed)
}

// codexAccountUAProfileEnabled 控制账号级 profile 派生（kill-switch），由持有
// 配置的服务在构造时发布进程级快照，与 codexIdentityEnforcement 同模式。
var codexAccountUAProfileEnabled = func() *atomic.Bool {
	v := &atomic.Bool{}
	v.Store(true)
	return v
}()

// SetCodexAccountUAProfileEnabled 发布账号级 canonical UA profile 开关。
func SetCodexAccountUAProfileEnabled(enabled bool) {
	codexAccountUAProfileEnabled.Store(enabled)
}

// canonicalCodexUserAgentForSeed 解析本请求应使用的 canonical UA：
//
//	面板已显式配置 → 面板值（管理员显式意图优先，行为与历史一致；清除面板值
//	即可启用每账号派生）；
//	面板未配置 + 开关开 + seed 非空 → 账号级 profile（版本段跟随 canonical 的
//	当前生效版本，由下游 SetCodexUserAgentVersion 统一重建）。
func canonicalCodexUserAgentForSeed(identitySeed string) string {
	canonical := codexCanonicalUserAgent()
	if codexCanonicalPanelUAConfigured() || !codexAccountUAProfileEnabled.Load() || identitySeed == "" {
		return canonical
	}
	return buildCodexProfileUserAgent(identitySeed, codexClientVersionFromUA(canonical))
}
