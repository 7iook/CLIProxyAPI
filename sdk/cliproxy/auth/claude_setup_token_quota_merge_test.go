package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func fableHeaders(util, reset string) http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization":    {"0.1"},
		"Anthropic-Ratelimit-Unified-5h-Reset":          {"4102444800"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":    {"0.2"},
		"Anthropic-Ratelimit-Unified-7d-Reset":          {"4102444800"},
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {util},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":       {reset},
	}
}

func haikuHeaders(util string) http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {util},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"4102444800"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.25"},
		"Anthropic-Ratelimit-Unified-7d-Reset":       {"4102444800"},
	}
}

func markClaude(t *testing.T, m *Manager, id, model string, h http.Header) {
	t.Helper()
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, h)
	m.MarkResult(ctx, Result{AuthID: id, Provider: "claude", Model: model, Success: true})
}

func TestMergeClaudeQuotaWindows(t *testing.T) {
	now := time.Unix(1791000000, 0).UTC()
	prev := setupTokenQuota{CapturedAt: now.Add(-time.Hour), Windows: map[string]setupTokenWindow{
		"five_hour":      {Utilization: 50, ResetsAt: now.Add(time.Hour)},
		"iguana_necktie": {Utilization: 30, ResetsAt: now.Add(48 * time.Hour)},
		"stale":          {Utilization: 10, ResetsAt: now.Add(-time.Minute)},
		"unbounded":      {Utilization: 10},
	}}
	next := setupTokenQuota{CapturedAt: now, Windows: map[string]setupTokenWindow{
		"five_hour": {Utilization: 60, ResetsAt: now.Add(time.Hour)},
	}}
	got := mergeClaudeQuotaWindows(prev, next, true)
	if got.Windows["five_hour"].Utilization != 60 {
		t.Fatalf("observed window must replace: %+v", got.Windows["five_hour"])
	}
	if w, ok := got.Windows["iguana_necktie"]; !ok || w.Utilization != 30 {
		t.Fatalf("unexpired absent window must be carried: %+v", got.Windows)
	}
	if _, ok := got.Windows["stale"]; ok {
		t.Fatal("expired absent window must be dropped")
	}
	if _, ok := got.Windows["unbounded"]; ok {
		t.Fatal("absent window without reset must be dropped")
	}
	if !got.CapturedAt.Equal(now) {
		t.Fatalf("captured_at = %v", got.CapturedAt)
	}
	if len(next.Windows) != 1 {
		t.Fatal("merge must not mutate next")
	}
}

// A Fable response's 7d_oi window must survive later non-Fable responses
// (which lack the header) and be served as iguana_necktie until it resets.
func TestSetupTokenFableWindowSurvivesNonFableResponses(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, err := m.Register(context.Background(), setupTokenAuth("st"))
	if err != nil {
		t.Fatal(err)
	}
	fableReset := time.Now().Add(72 * time.Hour).Unix()
	markClaude(t, m, a.ID, "claude-fable-5-2", fableHeaders("0.34", itoa(fableReset)))
	markClaude(t, m, a.ID, "claude-haiku-4-5", haikuHeaders("0.15"))

	updated, _ := m.GetByID(a.ID)
	body, ok := ClaudeSetupTokenUsageJSON(updated, time.Now())
	if !ok {
		t.Fatal("expected usage body")
	}
	got := decodeUsage(t, body)
	oi, ok := got["iguana_necktie"].(map[string]any)
	if !ok || oi["utilization"].(float64) != 34 || oi["resets_at"] != time.Unix(fableReset, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("iguana_necktie = %v (body %s)", got["iguana_necktie"], body)
	}
	if got["five_hour"].(map[string]any)["utilization"].(float64) != 15 {
		t.Fatalf("five_hour must follow the latest response: %v", got["five_hour"])
	}
	// The live signals themselves stay a plain replace (cooldown logic untouched).
	if _, has := updated.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Utilization"]; has {
		t.Fatal("live signals unexpectedly kept 7d_oi")
	}
	windows := updated.Metadata[ClaudeSetupTokenQuotaMetadataKey].(map[string]any)["windows"].(map[string]any)
	if _, ok := windows["iguana_necktie"]; !ok {
		t.Fatalf("persisted windows lost iguana_necktie: %v", windows)
	}
}

func TestSetupTokenFableWindowDroppedAfterReset(t *testing.T) {
	a := setupTokenAuth("st")
	obs := time.Now().Add(-2 * time.Hour)
	a.Metadata[ClaudeSetupTokenQuotaMetadataKey] = setupTokenQuota{CapturedAt: obs, Windows: map[string]setupTokenWindow{
		"five_hour":      {Utilization: 40, ResetsAt: obs.Add(time.Hour)},
		"iguana_necktie": {Utilization: 80, ResetsAt: obs.Add(time.Hour)},
	}}.metadataValue()
	body, ok := ClaudeSetupTokenUsageJSON(a, time.Now())
	if !ok {
		t.Fatal("expected usage body")
	}
	got := decodeUsage(t, body)
	if _, has := got["iguana_necktie"]; has {
		t.Fatalf("expired Fable window must be omitted, got %s", body)
	}
	if fh := got["five_hour"].(map[string]any); fh["utilization"].(float64) != 0 || fh["resets_at"] != nil {
		t.Fatalf("expired five_hour keeps reset-as-0 behaviour: %v", fh)
	}

	// A new non-Fable observation after the reset drops it from metadata too.
	q, _ := quotaFromMetadata(a.Metadata)
	next, _ := quotaFromSignals(map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.05"}, time.Now())
	if merged := mergeClaudeQuotaWindows(q, next, true); len(merged.Windows) != 1 {
		t.Fatalf("merged = %+v", merged.Windows)
	}
}

func TestSetupTokenNoFableDataEmitsNoWindow(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, _ := m.Register(context.Background(), setupTokenAuth("st"))
	markClaude(t, m, a.ID, "claude-haiku-4-5", haikuHeaders("0.15"))
	updated, _ := m.GetByID(a.ID)
	body, _ := ClaudeSetupTokenUsageJSON(updated, time.Now())
	if _, has := decodeUsage(t, body)["iguana_necktie"]; has {
		t.Fatalf("no Fable traffic must not fabricate a window: %s", body)
	}
}

func TestSetupTokenProbeHeadersKeepFableWindow(t *testing.T) {
	a := setupTokenAuth("st")
	now := time.Now()
	reset := now.Add(24 * time.Hour).Unix()
	if !RecordClaudeSetupTokenProbeHeaders(a, fableHeaders("0.5", itoa(reset)), now) {
		t.Fatal("record fable")
	}
	if !RecordClaudeSetupTokenProbeHeaders(a, haikuHeaders("0.2"), now.Add(time.Minute)) {
		t.Fatal("record haiku probe")
	}
	q, _ := quotaFromMetadata(a.Metadata)
	if w, ok := q.Windows["iguana_necktie"]; !ok || w.Utilization != 50 {
		t.Fatalf("probe overwrote Fable window: %+v", q.Windows)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
