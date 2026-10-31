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
	// OAuth credentials get a passive snapshot (see TestClaudeOAuthQuotaSnapshot)
	// but never the setup-token usage substitute: they read /api/oauth/usage.
	oauth := &Auth{ID: "oauth", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-y", "scope": "user:profile user:inference"}, Quota: QuotaState{ObservedAt: now, Signals: signals}}
	if _, ok := ClaudeSetupTokenUsageJSON(oauth, now); ok {
		t.Fatal("unexpected setup-token usage for OAuth credential")
	}
	for _, a := range []*Auth{
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

func TestClaudeOAuthQuotaSnapshot(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	oauth := &Auth{ID: "bue", Provider: "claude", Metadata: map[string]any{"type": "claude", "access_token": "sk-ant-oat01-y", "refresh_token": "r", "scope": "user:profile user:inference"}}
	auth, err := manager.Register(context.Background(), oauth)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ClaudeQuotaSnapshot(auth); ok {
		t.Fatal("snapshot before any traffic")
	}
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.2"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"4102444800"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.05"},
	})
	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "claude", Model: "claude-sonnet-4-5", Success: true})
	updated, _ := manager.GetByID(auth.ID)
	if _, ok := updated.Metadata[ClaudeSetupTokenQuotaMetadataKey].(map[string]any); !ok {
		t.Fatalf("OAuth quota not persisted: %#v", updated.Metadata)
	}
	snap, ok := ClaudeQuotaSnapshot(updated)
	if !ok {
		t.Fatal("no snapshot")
	}
	if snap["source"] != "passive" {
		t.Fatalf("source = %v", snap["source"])
	}
	if _, errParse := time.Parse(time.RFC3339, snap["captured_at"].(string)); errParse != nil {
		t.Fatalf("captured_at = %v", snap["captured_at"])
	}
	windows := snap["windows"].(map[string]any)
	fh := windows["five_hour"].(map[string]any)
	if fh["utilization"].(float64) != 20 || fh["resets_at"] != "2100-01-01T00:00:00Z" {
		t.Fatalf("five_hour = %v", fh)
	}
	sd := windows["seven_day"].(map[string]any)
	if sd["utilization"].(float64) != 5 || sd["resets_at"] != nil {
		t.Fatalf("seven_day = %v", sd)
	}
	for _, a := range []*Auth{
		{ID: "key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-api03-z"}},
		{ID: "codex", Provider: "codex", Metadata: map[string]any{"access_token": "sk-ant-oat01-w"}},
	} {
		a.Quota = QuotaState{ObservedAt: time.Now(), Signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.2"}}
		if _, ok := ClaudeQuotaSnapshot(a); ok {
			t.Fatalf("unexpected snapshot for %s", a.ID)
		}
	}
}

func TestClaudeQuotaSnapshotPrefersNewerLiveObservation(t *testing.T) {
	old := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	a := setupTokenAuth("st")
	a.Metadata[ClaudeSetupTokenQuotaMetadataKey] = map[string]any{
		"captured_at": old.Format(time.RFC3339),
		"windows":     map[string]any{"five_hour": map[string]any{"utilization": 10.0, "resets_at": nil}},
	}
	snap, ok := ClaudeQuotaSnapshot(a)
	if !ok || snap["windows"].(map[string]any)["five_hour"].(map[string]any)["utilization"].(float64) != 10 {
		t.Fatalf("persisted snapshot = %v", snap)
	}
	a.Quota = QuotaState{ObservedAt: time.Now(), Signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.55"}}
	snap, _ = ClaudeQuotaSnapshot(a)
	if snap["windows"].(map[string]any)["five_hour"].(map[string]any)["utilization"].(float64) != 55 {
		t.Fatalf("live snapshot = %v", snap)
	}
}
