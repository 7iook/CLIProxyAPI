package executor

import (
	"net/http"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRecordClaudeSetupTokenRateLimitOnlyForSetupTokens(t *testing.T) {
	claudeauth.ResetRateLimitSnapshots()
	header := http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.3"}}
	setup := &cliproxyauth.Auth{ID: "setup", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-x", "is_setup_token": true}}
	oauth := &cliproxyauth.Auth{ID: "oauth", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-y", "scope": "user:profile user:inference"}}
	apiKey := &cliproxyauth.Auth{ID: "key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-api03-z"}}
	other := &cliproxyauth.Auth{ID: "codex", Provider: "codex", Metadata: map[string]any{"access_token": "sk-ant-oat01-w", "is_setup_token": true}}
	for _, a := range []*cliproxyauth.Auth{setup, oauth, apiKey, other, nil} {
		recordClaudeSetupTokenRateLimit(a, header)
	}
	if _, ok := claudeauth.RateLimitUsageJSON("setup"); !ok {
		t.Fatal("setup token snapshot missing")
	}
	for _, id := range []string{"oauth", "key", "codex"} {
		if _, ok := claudeauth.RateLimitUsageJSON(id); ok {
			t.Fatalf("unexpected snapshot for %s", id)
		}
	}
}
