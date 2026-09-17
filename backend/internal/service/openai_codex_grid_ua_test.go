package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func setCodexGridUAEnabledForTest(t *testing.T, enabled bool) {
	t.Helper()
	prev := codexGridUAEnabled.Load()
	SetCodexGridUAEnabled(enabled)
	t.Cleanup(func() { SetCodexGridUAEnabled(prev) })
}

func setCodexForceCodexCLIPublishedForTest(t *testing.T, enabled bool) {
	t.Helper()
	prev := codexForceCodexCLIPublished.Load()
	SetCodexForceCodexCLIPublished(enabled)
	t.Cleanup(func() { SetCodexForceCodexCLIPublished(prev) })
}

func setCodexCanonicalUAForTest(t *testing.T, ua string) {
	t.Helper()
	codexCanonicalUAMu.RLock()
	prev := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	SetCodexCanonicalUserAgentResolver(func() string { return ua })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(prev) })
}

func codexGridEnforceHeaders(clientUA string, account *Account) http.Header {
	h := http.Header{}
	h.Set("originator", "codex-tui")
	if clientUA != "" {
		h.Set("user-agent", clientUA)
	}
	enforceCodexIdentityHeadersForAccountWithClientUA(h, account, "", clientUA)
	return h
}

func TestBuildCodexGridIdentityCLI(t *testing.T) {
	ns := "chatgpt:acc-grid:user:u1"

	terminal := pickCodexGridTerminal(ns, codexGridOSMac)
	want := "codex-tui/0.147.0 (Mac OS 26.6.0; arm64) " + terminal
	got, ok := buildCodexGridIdentity("codex-tui/0.147.0 (Mac OS 26.6.2; arm64) iTerm.app/3.7.1", ns)
	if !ok || got.userAgent != want || got.originator != "codex-tui" || got.version != "0.147.0" {
		t.Fatalf("mac cli grid mismatch:\n got %+v\nwant %q", got, want)
	}

	// Mac Intel 用户被钉到 arm64；Windows 用户钉到 x86_64 且 OS 归一化。
	got, ok = buildCodexGridIdentity("codex-tui/0.147.0 (Mac OS 26.6.2; x86_64) iTerm.app/3.7.1", ns)
	if !ok || !strings.Contains(got.userAgent, "; arm64)") {
		t.Fatalf("mac should pin arm64, got %q", got.userAgent)
	}
	got, ok = buildCodexGridIdentity("codex-tui/0.148.1 (Windows 10.0.19045; arm64) WindowsTerminal", ns)
	if !ok || !strings.Contains(got.userAgent, "(Windows 10.0.26100; x86_64)") {
		t.Fatalf("windows grid mismatch: %q", got.userAgent)
	}

	// Linux 保留真实 arch，发行版归一到 Ubuntu。
	got, ok = buildCodexGridIdentity("codex_cli_rs/0.147.0 (Debian GNU/Linux 12.7; arm64) xterm-256color", ns)
	if !ok || !strings.Contains(got.userAgent, "(Ubuntu 24.4.0; arm64)") || !strings.HasPrefix(got.userAgent, "codex_cli_rs/0.147.0") {
		t.Fatalf("linux arm64 grid mismatch: %q", got.userAgent)
	}
	got, ok = buildCodexGridIdentity("codex_cli_rs/0.147.0 (Ubuntu 22.4.0; x86_64) xterm-256color", ns)
	if !ok || !strings.Contains(got.userAgent, "(Ubuntu 24.4.0; x86_64)") {
		t.Fatalf("linux x86_64 grid mismatch: %q", got.userAgent)
	}

	// 预发布版本：窗口判定用基版本，出站逐字保留。
	got, ok = buildCodexGridIdentity("codex-tui/0.147.0-alpha.6.2 (Mac OS 26.6.2; arm64) unknown", ns)
	if !ok || !strings.HasPrefix(got.userAgent, "codex-tui/0.147.0-alpha.6.2 ") || got.version != "0.147.0-alpha.6.2" {
		t.Fatalf("prerelease grid mismatch: %q", got.userAgent)
	}

	// 覆盖形态：trailer 恢复真实客户端并以其为 family（重造丢弃覆盖首段与旧 trailer）。
	got, ok = buildCodexGridIdentity("cccc/0.147.0 (Mac OS 26.6.2; arm64) xterm-256color (codex-tui; 0.147.0)", ns)
	if !ok || !strings.HasPrefix(got.userAgent, "codex-tui/0.147.0 ") || got.originator != "codex-tui" ||
		strings.Contains(got.userAgent, "cccc") || strings.Contains(got.userAgent, "(codex-tui;") {
		t.Fatalf("override trailer grid mismatch: %q / %q", got.userAgent, got.originator)
	}
}

func TestBuildCodexGridIdentityVersionWindow(t *testing.T) {
	ns := "chatgpt:acc-grid:user:u1"
	// 测试环境 canonical 版本 = codexCLIVersion（0.146.0）→ 窗口 [0.144.0, 0.148.x]。
	floor, ceiling := codexGridVersionBounds()
	if floor != "0.144.0" || ceiling != "0.148.999999" {
		t.Fatalf("bounds = %q..%q, want 0.144.0..0.148.999999", floor, ceiling)
	}
	accept := []string{"0.144.0", "0.146.0", "0.148.3"}
	reject := []string{"0.143.9", "0.149.0", "0.999.0"}
	for _, v := range accept {
		if _, ok := buildCodexGridIdentity("codex-tui/"+v+" (Ubuntu 22.4.0; x86_64) xterm-256color", ns); !ok {
			t.Fatalf("version %s should be accepted", v)
		}
	}
	for _, v := range reject {
		if _, ok := buildCodexGridIdentity("codex-tui/"+v+" (Ubuntu 22.4.0; x86_64) xterm-256color", ns); ok {
			t.Fatalf("version %s should fall back", v)
		}
	}

	// 注入更高 canonical：窗口整体跟随（0.160.0 → [0.158.0, 0.162.x]）。
	setCodexCanonicalUAForTest(t, "codex-tui/0.160.0 (Ubuntu 24.4.0; x86_64) xterm-256color")
	floor, _ = codexGridVersionBounds()
	if floor != "0.158.0" {
		t.Fatalf("injected canonical floor = %q, want 0.158.0", floor)
	}
	if _, ok := buildCodexGridIdentity("codex-tui/0.157.9 (Ubuntu 22.4.0; x86_64) xterm-256color", ns); ok {
		t.Fatal("0.157.9 should be below injected floor")
	}
}

func TestBuildCodexGridIdentityDesktop(t *testing.T) {
	ns := "chatgpt:acc-grid:user:u1"

	// 实测样本：OS 归一化、版本与合法 trailer 逐字保留、terminal 钉死 unknown。
	in := "Codex Desktop/0.147.0-alpha.6.2 (Mac OS 26.6.2; arm64) unknown (Codex Desktop; 26.908.40834)"
	want := "Codex Desktop/0.147.0-alpha.6.2 (Mac OS 26.6.0; arm64) unknown (Codex Desktop; 26.908.40834)"
	got, ok := buildCodexGridIdentity(in, ns)
	if !ok || got.userAgent != want || got.originator != "Codex Desktop" || got.version != "0.147.0-alpha.6.2" {
		t.Fatalf("desktop grid mismatch:\n got %q / %q / %q\nwant %q", got.userAgent, got.originator, got.version, want)
	}

	// kimi P0：override 包裹的 Desktop（原始首段是第三方名）不得透传原始首段。
	got, ok = buildCodexGridIdentity("cccc/0.147.0 (Mac OS 26.6.2; arm64) xterm-256color (Codex Desktop; 26.9.1)", ns)
	if !ok {
		t.Fatal("override desktop should grid")
	}
	if !strings.HasPrefix(got.userAgent, "Codex Desktop/0.147.0 ") || strings.Contains(got.userAgent, "cccc") {
		t.Fatalf("override desktop must rebuild on pairedUA, got %q", got.userAgent)
	}
	if !strings.HasSuffix(got.userAgent, "unknown (Codex Desktop; 26.9.1)") {
		t.Fatalf("valid trailer should be kept, got %q", got.userAgent)
	}

	// kimi P1：freeform 槽防铸造——terminal 强制 unknown，非法 trailer 丢弃。
	got, ok = buildCodexGridIdentity("Codex Desktop/0.147.0 (Mac OS 26.6.2; arm64) track-abc123 (Codex Desktop; not-a-build)", ns)
	if !ok || !strings.Contains(got.userAgent, ") unknown") || strings.Contains(got.userAgent, "track-abc123") ||
		strings.Contains(got.userAgent, "not-a-build") {
		t.Fatalf("desktop minting must be neutralized, got %q", got.userAgent)
	}

	// 未实测家族名 / 小写变体：回退，不格子化。
	for _, ua := range []string{
		"Codex Foo/0.147.0 (Mac OS 26.6.2; arm64) unknown (Codex Foo; 1.0)",
		"codex desktop/0.147.0 (Mac OS 26.6.2; arm64) unknown",
	} {
		if _, ok := buildCodexGridIdentity(ua, ns); ok {
			t.Fatalf("unwitnessed/变体家族不应格子化: %q", ua)
		}
	}
}

func TestBuildCodexGridIdentityRejects(t *testing.T) {
	ns := "chatgpt:acc-grid:user:u1"
	reject := map[string]string{
		"non official":  "Mozilla/5.0 (Macintosh) Chrome/120.0",
		"vscode family": "codex_vscode/1.103.0 (Mac OS 26.6.0; arm64) vscode/1.103.0",
		"atlas family":  "codex_atlas/0.147.0 (Mac OS 26.6.0; arm64) unknown",
		"unknown os":    "codex-tui/0.147.0 (SunOS 5.11; x86_64) xterm-256color",
		"missing group": "codex-tui/0.147.0",
		"bad version":   "codex-tui/unknown (Ubuntu 24.4.0; x86_64) xterm-256color",
		"empty ua":      "",
	}
	for name, ua := range reject {
		if _, ok := buildCodexGridIdentity(ua, ns); ok {
			t.Fatalf("%s (%q) should not grid", name, ua)
		}
	}
}

func TestPickCodexGridTerminalDeterministic(t *testing.T) {
	first := pickCodexGridTerminal("ns-1", codexGridOSMac)
	for i := 0; i < 5; i++ {
		if got := pickCodexGridTerminal("ns-1", codexGridOSMac); got != first {
			t.Fatalf("terminal not deterministic: %q != %q", got, first)
		}
	}
	pool := codexGridTerminalByOS[codexGridOSMac]
	if !containsCodexGridTerminal(pool, first) {
		t.Fatalf("terminal %q not in pool", first)
	}
	distinct := map[string]bool{}
	for i := 0; i < 200; i++ {
		distinct[pickCodexGridTerminal("ns-"+strconvItoa(i), codexGridOSMac)] = true
	}
	if len(distinct) < 3 {
		t.Fatalf("expected spread across terminals, got %d", len(distinct))
	}
}

func containsCodexGridTerminal(pool []string, value string) bool {
	for _, item := range pool {
		if item == value {
			return true
		}
	}
	return false
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for ; v > 0; v /= 10 {
		digits = string(rune('0'+v%10)) + digits
	}
	return digits
}

func TestEnforceCodexIdentityHeadersForAccountGrid(t *testing.T) {
	account := codexProfileTestAccount("acc-grid")
	clientMac := "codex-tui/0.147.0 (Mac OS 26.6.2; arm64) iTerm.app/3.7.1"

	t.Run("grid applies to classifiable client ua", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexAccountUAProfileEnabledForTest(t, true)
		setCodexGridUAEnabledForTest(t, true)
		h := codexGridEnforceHeaders(clientMac, account)
		if !strings.Contains(h.Get("user-agent"), "(Mac OS 26.6.0; arm64)") {
			t.Fatalf("expected grid UA, got %q", h.Get("user-agent"))
		}
		if h.Get("version") != "0.147.0" || h.Get("originator") != "codex-tui" {
			t.Fatalf("grid identity mismatch: %q / %q", h.Get("version"), h.Get("originator"))
		}
	})

	t.Run("synthetic path without client ua never grids", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexAccountUAProfileEnabledForTest(t, true)
		setCodexGridUAEnabledForTest(t, true)
		h := http.Header{}
		h.Set("originator", "codex-tui")
		h.Set("user-agent", clientMac) // 头里即使是 canonical/profile 形态也不参与
		enforceCodexIdentityHeadersForAccount(h, account, "")
		if strings.Contains(h.Get("user-agent"), "Mac OS 26.6.0") {
			t.Fatalf("synthetic path must not grid, got %q", h.Get("user-agent"))
		}
	})

	t.Run("out of window falls back to account profile", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexAccountUAProfileEnabledForTest(t, true)
		setCodexGridUAEnabledForTest(t, true)
		h := codexGridEnforceHeaders("codex-tui/0.140.0 (Mac OS 26.6.2; arm64) iTerm.app/3.7.1", account)
		if h.Get("user-agent") == codexCLIUserAgent {
			t.Fatal("fallback should use account profile, not built-in constant")
		}
		if !strings.Contains(h.Get("user-agent"), "codex-tui/") || h.Get("version") != codexCLIVersion {
			t.Fatalf("fallback identity mismatch: %q / %q", h.Get("user-agent"), h.Get("version"))
		}
	})

	t.Run("kill switch disables grid", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexAccountUAProfileEnabledForTest(t, true)
		setCodexGridUAEnabledForTest(t, false)
		h := codexGridEnforceHeaders(clientMac, account)
		if strings.Contains(h.Get("user-agent"), "Mac OS 26.6.0") {
			t.Fatalf("grid should be off, got %q", h.Get("user-agent"))
		}
	})

	t.Run("force codex cli wins over grid", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexGridUAEnabledForTest(t, true)
		setCodexForceCodexCLIPublishedForTest(t, true)
		h := codexGridEnforceHeaders(clientMac, account)
		if strings.Contains(h.Get("user-agent"), "Mac OS 26.6.0") {
			t.Fatalf("ForceCodexCLI should win over grid, got %q", h.Get("user-agent"))
		}
	})

	t.Run("panel ua configured disables grid", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, true)
		setCodexGridUAEnabledForTest(t, true)
		h := codexGridEnforceHeaders(clientMac, account)
		if strings.Contains(h.Get("user-agent"), "Mac OS 26.6.0") {
			t.Fatalf("panel UA should win over grid, got %q", h.Get("user-agent"))
		}
	})

	t.Run("account custom ua wins over grid", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexGridUAEnabledForTest(t, true)
		h := http.Header{}
		h.Set("originator", "codex-tui")
		h.Set("user-agent", clientMac)
		enforceCodexIdentityHeadersForAccountWithClientUA(h, account, "codex_cli_rs/0.146.0 (Ubuntu 24.4.0; x86_64) tmux-256color", clientMac)
		if !strings.HasPrefix(h.Get("user-agent"), "codex_cli_rs/") || !strings.Contains(h.Get("user-agent"), "tmux-256color") {
			t.Fatalf("account custom UA should win, got %q", h.Get("user-agent"))
		}
	})

	t.Run("grid ua pairs to official identity", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexGridUAEnabledForTest(t, true)
		h := codexGridEnforceHeaders(clientMac, account)
		originator, _, ok := openai.PairCodexClientIdentity(h.Get("user-agent"))
		if !ok || originator != h.Get("originator") {
			t.Fatalf("grid UA must pair with originator: %q vs %q", originator, h.Get("originator"))
		}
	})
}
