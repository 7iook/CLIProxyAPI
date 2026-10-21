package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func setupTokenAuth(id string) *Auth {
	return &Auth{ID: id, Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-x", "is_setup_token": true}}
}

func decodeUsage(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestClaudeSetupTokenUsageFromSignals(t *testing.T) {
	a := setupTokenAuth("a")
	now := time.Unix(1791000000, 0)
	a.Quota = QuotaState{ObservedAt: now, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization":    "0.2",
		"Anthropic-Ratelimit-Unified-5h-Reset":          "1791010000",
		"Anthropic-Ratelimit-Unified-7d-Utilization":    "0.19",
		"Anthropic-Ratelimit-Unified-7d-Reset":          "1791400000",
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization": "1.5",
		"Anthropic-Ratelimit-Unified-Status":            "allowed",
	}}
	body, ok := ClaudeSetupTokenUsageJSON(a, now)
	if !ok {
		t.Fatal("expected usage")
	}
	got := decodeUsage(t, body)
	fh := got["five_hour"].(map[string]any)
	if fh["utilization"].(float64) != 20 || fh["resets_at"] != time.Unix(1791010000, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("five_hour = %v", fh)
	}
	if got["seven_day"].(map[string]any)["utilization"].(float64) != 19 {
		t.Fatalf("seven_day = %v", got["seven_day"])
	}
	if oi := got["iguana_necktie"].(map[string]any); oi["utilization"].(float64) != 100 || oi["resets_at"] != nil {
		t.Fatalf("7d_oi = %v", oi)
	}
	if got["captured_at"] != now.UTC().Format(time.RFC3339) {
		t.Fatalf("captured_at = %v", got["captured_at"])
	}
}

func TestClaudeSetupTokenUsageExpiredWindowIsReset(t *testing.T) {
	a := setupTokenAuth("a")
	obs := time.Unix(1791000000, 0)
	a.Quota = QuotaState{ObservedAt: obs, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.9",
		"Anthropic-Ratelimit-Unified-5h-Reset":       "1791001000",
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.5",
		"Anthropic-Ratelimit-Unified-7d-Reset":       "1791400000",
	}}
	body, _ := ClaudeSetupTokenUsageJSON(a, time.Unix(1791002000, 0))
	got := decodeUsage(t, body)
	if fh := got["five_hour"].(map[string]any); fh["utilization"].(float64) != 0 || fh["resets_at"] != nil {
		t.Fatalf("expired five_hour = %v", fh)
	}
	if sd := got["seven_day"].(map[string]any); sd["utilization"].(float64) != 50 {
		t.Fatalf("seven_day = %v", sd)
	}
}

func TestClaudeSetupTokenUsageNonSetupTokenUntouched(t *testing.T) {
	signals := map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.2"}
	now := time.Now()
	for _, a := range []*Auth{
		{ID: "oauth", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-y", "scope": "user:profile user:inference"}},
		{ID: "key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-api03-z"}},
		{ID: "codex", Provider: "codex", Metadata: map[string]any{"access_token": "sk-ant-oat01-w", "is_setup_token": true}},
		nil,
	} {
		if a != nil {
			a.Quota = QuotaState{ObservedAt: now, Signals: signals}
		}
		if _, ok := ClaudeSetupTokenUsageJSON(a, now); ok {
			t.Fatalf("unexpected usage for %+v", a)
		}
		if a != nil && syncClaudeSetupTokenQuotaMetadata(a) {
			t.Fatalf("unexpected metadata write for %s", a.ID)
		}
	}
}

func TestSetupTokenQuotaWriteThrottle(t *testing.T) {
	t0 := time.Unix(1791000000, 0)
	reset := time.Unix(1791010000, 0)
	q := func(at time.Time, util float64, r time.Time) setupTokenQuota {
		return setupTokenQuota{CapturedAt: at, Windows: map[string]setupTokenWindow{"five_hour": {Utilization: util, ResetsAt: r}}}
	}
	prev := q(t0, 20, reset)
	cases := []struct {
		name string
		next setupTokenQuota
		want bool
	}{
		{"same values within heartbeat", q(t0.Add(5*time.Minute), 20, reset), false},
		{"small change", q(t0.Add(5*time.Minute), 20.5, reset), false},
		{"1pp change too soon", q(t0.Add(30*time.Second), 21, reset), false},
		{"1pp change after min interval", q(t0.Add(61*time.Second), 21, reset), true},
		{"reset changed immediately", q(t0.Add(time.Second), 20, reset.Add(time.Hour)), true},
		{"heartbeat", q(t0.Add(10*time.Minute), 20, reset), true},
		{"older observation", q(t0.Add(-time.Minute), 50, reset), false},
	}
	for _, tc := range cases {
		if got := setupTokenQuotaWorthWriting(prev, tc.next, true); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if !setupTokenQuotaWorthWriting(setupTokenQuota{}, prev, false) {
		t.Error("first observation must be written")
	}
}

func TestMarkResultSyncsSetupTokenQuotaMetadata(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth, err := manager.Register(context.Background(), setupTokenAuth("st"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.3"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"4102444800"},
	})
	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "claude", Model: "claude-haiku-4-5", Success: true})
	updated, _ := manager.GetByID(auth.ID)
	raw, ok := updated.Metadata[ClaudeSetupTokenQuotaMetadataKey].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v", updated.Metadata)
	}
	w := raw["windows"].(map[string]any)["five_hour"].(map[string]any)
	if w["utilization"].(float64) != 30 || w["resets_at"] != "2100-01-01T00:00:00Z" {
		t.Fatalf("window = %v", w)
	}
}
