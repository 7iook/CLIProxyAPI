package claude

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestRateLimitSnapshotUsageJSON(t *testing.T) {
	ResetRateLimitSnapshots()
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.2")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1791058200")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.19")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1791439200")
	h.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "1.5")
	now := time.Unix(1791000000, 0)
	if !RecordRateLimitHeaders("a1", h, now) {
		t.Fatal("expected record")
	}
	body, ok := RateLimitUsageJSON("a1")
	if !ok {
		t.Fatal("expected snapshot")
	}
	var got map[string]map[string]any
	body = stripObserved(t, body)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["five_hour"]["utilization"].(float64) != 20 || got["five_hour"]["resets_at"] != "2026-10-03T20:10:00Z" {
		t.Fatalf("five_hour = %v", got["five_hour"])
	}
	if got["seven_day"]["utilization"].(float64) != 19 {
		t.Fatalf("seven_day = %v", got["seven_day"])
	}
	if got["iguana_necktie"]["utilization"].(float64) != 100 || got["iguana_necktie"]["resets_at"] != nil {
		t.Fatalf("fable = %v", got["iguana_necktie"])
	}
}

func TestRateLimitSnapshotIgnoresHeadersWithoutWindows(t *testing.T) {
	ResetRateLimitSnapshots()
	if RecordRateLimitHeaders("a2", http.Header{"X-Other": {"1"}}, time.Now()) {
		t.Fatal("unexpected record")
	}
	if RecordRateLimitHeaders("", http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.1"}}, time.Now()) {
		t.Fatal("empty auth id must not record")
	}
	if _, ok := RateLimitUsageJSON("a2"); ok {
		t.Fatal("unexpected snapshot")
	}
}

func TestRateLimitSnapshotMergesPartialHeaders(t *testing.T) {
	ResetRateLimitSnapshots()
	RecordRateLimitHeaders("a3", http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.5"}, "Anthropic-Ratelimit-Unified-7d-Utilization": {"0.4"}}, time.Now())
	RecordRateLimitHeaders("a3", http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.6"}}, time.Now())
	body, _ := RateLimitUsageJSON("a3")
	var got map[string]map[string]any
	body = stripObserved(t, body)
	_ = json.Unmarshal(body, &got)
	if got["five_hour"]["utilization"].(float64) != 60 || got["seven_day"]["utilization"].(float64) != 40 {
		t.Fatalf("got %v", got)
	}
}

func stripObserved(t *testing.T, body []byte) []byte {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["cpa_observed_at"].(string); !ok {
		t.Fatal("missing cpa_observed_at")
	}
	delete(raw, "cpa_observed_at")
	out, _ := json.Marshal(raw)
	return out
}
