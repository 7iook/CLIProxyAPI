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

func prodShape(last time.Time) *Auth {
	return &Auth{ID: "p", Provider: "claude", Metadata: map[string]any{
		"type": "claude", "access_token": "sk-ant-oat01-x", "refresh_token": "", "is_setup_token": true,
		"last_refresh": last.Format(time.RFC3339),
	}}
}

func TestSetupTokenProductionShapeSchedule(t *testing.T) {
	now := time.Now()
	m := &Manager{}
	a := prodShape(now.Add(-time.Hour))
	next, ok := nextRefreshCheckAt(now, a, time.Minute)
	if !ok {
		t.Fatal("setup token without refresh_token/expired must still be scheduled")
	}
	if d := next.Sub(now); d < 10*time.Hour || d > 12*time.Hour {
		t.Fatalf("next probe in %v, want ~11h", d)
	}
	if m.shouldRefresh(a, now) {
		t.Fatal("must not be due 1h after last_refresh")
	}
	a = prodShape(now.Add(-13 * time.Hour))
	if next, ok := nextRefreshCheckAt(now, a, time.Minute); !ok || next.After(now) {
		t.Fatal("13h-old setup token should be due now")
	}
	if !m.shouldRefresh(a, now) {
		t.Fatal("13h-old setup token should refresh")
	}
	a.Disabled = true
	if _, ok := nextRefreshCheckAt(now, a, time.Minute); ok {
		t.Fatal("disabled setup token must not be scheduled")
	}
}
