package service

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func codexProfileTestAccount(accountID string) *Account {
	return &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_user_id":    "user-" + accountID,
		},
	}
}

func setCodexPanelUAConfiguredForTest(t *testing.T, configured bool) {
	t.Helper()
	codexCanonicalPanelConfiguredMu.RLock()
	prev := codexCanonicalPanelUAConfiguredResolv
	codexCanonicalPanelConfiguredMu.RUnlock()
	SetCodexCanonicalPanelUAConfiguredResolver(func() bool { return configured })
	t.Cleanup(func() {
		SetCodexCanonicalPanelUAConfiguredResolver(prev)
	})
}

func setCodexAccountUAProfileEnabledForTest(t *testing.T, enabled bool) {
	t.Helper()
	prev := codexAccountUAProfileEnabled.Load()
	SetCodexAccountUAProfileEnabled(enabled)
	t.Cleanup(func() { SetCodexAccountUAProfileEnabled(prev) })
}

func TestDeriveCodexUAProfileSuffixDeterministic(t *testing.T) {
	suffix := deriveCodexUAProfileSuffix("chatgpt:acc-1:user:user-1")
	if suffix == "" {
		t.Fatal("expected non-empty suffix")
	}
	for i := 0; i < 10; i++ {
		if got := deriveCodexUAProfileSuffix("chatgpt:acc-1:user:user-1"); got != suffix {
			t.Fatalf("derivation not deterministic: %q != %q", got, suffix)
		}
	}
	format := regexp.MustCompile(`^ \([^()]+; (x86_64|arm64)\) \S+$`)
	if !format.MatchString(suffix) {
		t.Fatalf("suffix %q does not match codex UA profile format", suffix)
	}
	// 空 seed 回退编译期常量后缀（无账号上下文的路径不派生）。
	if got := deriveCodexUAProfileSuffix(""); got != codexCLIUserAgentSuffix {
		t.Fatalf("empty seed should fall back to built-in suffix, got %q", got)
	}
}

func TestDeriveCodexUAProfileSuffixStaysWithinTable(t *testing.T) {
	valid := make(map[string]bool, len(codexUAProfileTable))
	for _, entry := range codexUAProfileTable {
		valid[" ("+entry.os+"; "+entry.arch+") "+entry.terminal] = true
	}
	distinct := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		suffix := deriveCodexUAProfileSuffix("seed-account-" + strconv.Itoa(i) + "-" + strings.Repeat("x", i%7))
		if !valid[suffix] {
			t.Fatalf("derived suffix %q is not a table entry", suffix)
		}
		distinct[suffix] = true
	}
	if len(distinct) < 10 {
		t.Fatalf("expected distribution over >=10 profiles, got %d", len(distinct))
	}
}

func TestBuildCodexProfileUserAgent(t *testing.T) {
	ua := buildCodexProfileUserAgent("chatgpt:acc-1", "0.146.0")
	originator, pairedUA, ok := openai.PairCodexClientIdentity(ua)
	if !ok {
		t.Fatalf("profile UA %q does not pair to an official identity", ua)
	}
	if originator != openai.CodexDefaultOriginator {
		t.Fatalf("profile UA originator = %q, want %q", originator, openai.CodexDefaultOriginator)
	}
	if pairedUA != ua {
		t.Fatalf("profile UA should be preserved verbatim by pairing, got %q", pairedUA)
	}
	if v := openai.CodexUserAgentVersion(ua); v != "0.146.0" {
		t.Fatalf("profile UA version = %q, want 0.146.0", v)
	}
	if v := openai.CodexUserAgentVersion(buildCodexProfileUserAgent("s", "")); v != codexCLIVersion {
		t.Fatalf("empty version should fall back to %s", codexCLIVersion)
	}
}

func TestResolveCodexOutboundIdentityForAccountDerivesProfile(t *testing.T) {
	setCodexPanelUAConfiguredForTest(t, false)
	setCodexAccountUAProfileEnabledForTest(t, true)

	// nil 账号显式安全：退回全局 canonical，不派生。
	if got := resolveCodexOutboundIdentityForAccount("", nil); got.userAgent != codexCLIUserAgent {
		t.Fatalf("nil account should use global canonical, got %q", got.userAgent)
	}
	if ua, originator := CodexAuthIdentityForAccount(nil); ua != codexCLIUserAgent || originator != openai.CodexDefaultOriginator {
		t.Fatalf("nil account auth identity mismatch: %q / %q", ua, originator)
	}

	account := codexProfileTestAccount("acc-1")
	first := resolveCodexOutboundIdentityForAccount("", account)
	if first.userAgent == codexCLIUserAgent {
		t.Fatal("expected per-account profile UA, got built-in constant")
	}
	second := resolveCodexOutboundIdentityForAccount("", account)
	if first.userAgent != second.userAgent {
		t.Fatalf("profile UA not stable per account: %q != %q", first.userAgent, second.userAgent)
	}
	// 版本段与 canonical 当前生效版本同源（测试环境未注入 resolver → 内置常量）。
	if v := openai.CodexUserAgentVersion(first.userAgent); v != codexCLIVersion {
		t.Fatalf("profile UA version = %q, want %q", v, codexCLIVersion)
	}
	if first.version != codexCLIVersion || first.originator != openai.CodexDefaultOriginator {
		t.Fatalf("profile identity mismatch: %+v", first)
	}
}

func TestResolveCodexOutboundIdentityForAccountPrecedence(t *testing.T) {
	account := codexProfileTestAccount("acc-1")
	global := resolveCodexOutboundIdentity("")

	t.Run("panel ua configured wins over derivation", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, true)
		setCodexAccountUAProfileEnabledForTest(t, true)
		got := resolveCodexOutboundIdentityForAccount("", account)
		if got.userAgent != global.userAgent {
			t.Fatalf("configured panel UA should win: %q != %q", got.userAgent, global.userAgent)
		}
	})

	t.Run("kill switch disables derivation", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexAccountUAProfileEnabledForTest(t, false)
		got := resolveCodexOutboundIdentityForAccount("", account)
		if got.userAgent != global.userAgent {
			t.Fatalf("disabled derivation should fall back to canonical: %q != %q", got.userAgent, global.userAgent)
		}
	})

	t.Run("account custom UA wins over profile", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexAccountUAProfileEnabledForTest(t, true)
		custom := "codex_cli_rs/0.146.0 (Ubuntu 24.4.0; x86_64) tmux-256color"
		got := resolveCodexOutboundIdentityForAccount(custom, account)
		if !strings.HasPrefix(got.userAgent, "codex_cli_rs/") || !strings.Contains(got.userAgent, "tmux-256color") {
			t.Fatalf("account custom UA should win, got %q", got.userAgent)
		}
	})

	t.Run("non oauth account falls back to canonical", func(t *testing.T) {
		setCodexPanelUAConfiguredForTest(t, false)
		setCodexAccountUAProfileEnabledForTest(t, true)
		apiKeyAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
		got := resolveCodexOutboundIdentityForAccount("", apiKeyAccount)
		if got.userAgent != global.userAgent {
			t.Fatalf("non-OAuth account should use global canonical: %q != %q", got.userAgent, global.userAgent)
		}
	})
}

func TestEnforceCodexIdentityHeadersForAccountUsesProfile(t *testing.T) {
	setCodexPanelUAConfiguredForTest(t, false)
	setCodexAccountUAProfileEnabledForTest(t, true)

	header := http.Header{}
	header.Set("originator", "codex-tui")
	enforceCodexIdentityHeadersForAccount(header, codexProfileTestAccount("acc-9"), "")
	if header.Get("user-agent") == codexCLIUserAgent {
		t.Fatal("expected per-account profile UA on the wire")
	}
	if header.Get("user-agent") == "" || header.Get("version") == "" || header.Get("originator") == "" {
		t.Fatalf("identity headers incomplete: %+v", header)
	}
}

func TestValidateOpenAICodexCanonicalUserAgent(t *testing.T) {
	valid := []string{
		"codex-tui/0.146.0 (Mac OS 26.6.0; arm64) Apple_Terminal/455.1.1",
		"codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color",
		"codex_exec/0.146.0 (Ubuntu 24.4.0; x86_64) xterm-256color",
	}
	for _, ua := range valid {
		if err := ValidateOpenAICodexCanonicalUserAgent(ua); err != nil {
			t.Fatalf("expected %q to be valid, got %v", ua, err)
		}
	}
	invalid := map[string]string{
		"desktop family":  "Codex Desktop/0.154.0 (Mac OS 26.6.2; arm64) unknown (Codex Desktop; 26.908.40834)",
		"vscode family":   "codex_vscode/1.103.0 (Mac OS 26.6.0; arm64) vscode/1.103.0",
		"not a codex ua":  "Mozilla/5.0 (Macintosh) Chrome/120.0",
		"missing version": "codex-tui",
	}
	for name, ua := range invalid {
		if err := ValidateOpenAICodexCanonicalUserAgent(ua); err == nil {
			t.Fatalf("expected %q (%s) to be rejected", ua, name)
		}
	}
}
