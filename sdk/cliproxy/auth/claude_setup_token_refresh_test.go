package auth

import (
	"testing"
	"time"
)

func TestShouldRefreshSetupTokenUsesProbeInterval(t *testing.T) {
	m := &Manager{}
	now := time.Now()
	a := &Auth{ID: "s", Provider: "claude", Metadata: map[string]any{
		"access_token": "sk-ant-oat01-x", "is_setup_token": true,
		"expired": now.Add(-time.Hour).Format(time.RFC3339),
	}}
	if !m.shouldRefresh(a, now) {
		t.Fatal("never-probed setup token should be due")
	}
	a.Metadata["last_refresh"] = now.Add(-time.Hour).Format(time.RFC3339)
	if m.shouldRefresh(a, now) {
		t.Fatal("recently probed setup token must not be due even if expired")
	}
	a.Metadata["last_refresh"] = now.Add(-ClaudeSetupTokenProbeInterval - time.Minute).Format(time.RFC3339)
	if !m.shouldRefresh(a, now) {
		t.Fatal("stale setup token should be due")
	}
}
