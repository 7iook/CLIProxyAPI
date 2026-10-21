package claude

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Claude setup tokens (`claude setup-token`) only carry the user:inference
// scope, so /api/oauth/usage rejects them. Anthropic still reports the
// account's unified rate-limit windows on every /v1/messages response, as
// Anthropic-Ratelimit-Unified-{5h,7d,7d_oi}-{Utilization,Reset} headers
// (utilization is a 0-1 fraction, reset is unix seconds). The snapshot below
// keeps the latest values per auth so the management quota view can show them
// in the oauth/usage response shape.

type rateLimitWindow struct {
	Utilization float64
	ResetsAt    time.Time
}

type rateLimitSnapshot struct {
	Windows    map[string]rateLimitWindow
	ObservedAt time.Time
}

// rateLimitHeaderWindows maps header window names to oauth/usage keys.
var rateLimitHeaderWindows = []struct{ header, key string }{
	{"5h", "five_hour"},
	{"7d", "seven_day"},
	{"7d_oi", "iguana_necktie"},
}

var (
	rateLimitMu        sync.RWMutex
	rateLimitSnapshots = map[string]*rateLimitSnapshot{}
)

// RecordRateLimitHeaders stores the unified rate-limit windows found in header
// for authID. Windows absent from header keep their previous value. It reports
// whether any window was found.
func RecordRateLimitHeaders(authID string, header http.Header, now time.Time) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" || len(header) == 0 {
		return false
	}
	found := map[string]rateLimitWindow{}
	for _, spec := range rateLimitHeaderWindows {
		prefix := "Anthropic-Ratelimit-Unified-" + spec.header + "-"
		raw := strings.TrimSpace(header.Get(prefix + "Utilization"))
		if raw == "" {
			continue
		}
		used, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(used) || math.IsInf(used, 0) {
			continue
		}
		w := rateLimitWindow{Utilization: math.Max(0, math.Min(100, math.Round(used*10000)/100))}
		if reset, errReset := strconv.ParseFloat(strings.TrimSpace(header.Get(prefix+"Reset")), 64); errReset == nil && reset > 0 {
			w.ResetsAt = time.Unix(int64(reset), 0).UTC()
		}
		found[spec.key] = w
	}
	if len(found) == 0 {
		return false
	}
	rateLimitMu.Lock()
	defer rateLimitMu.Unlock()
	snap := rateLimitSnapshots[authID]
	if snap == nil {
		snap = &rateLimitSnapshot{Windows: map[string]rateLimitWindow{}}
		rateLimitSnapshots[authID] = snap
	}
	for key, w := range found {
		snap.Windows[key] = w
	}
	snap.ObservedAt = now.UTC()
	return true
}

// RateLimitUsageJSON renders the snapshot of authID like an /api/oauth/usage
// response body. ok is false when nothing was recorded.
func RateLimitUsageJSON(authID string) ([]byte, bool) {
	rateLimitMu.RLock()
	snap := rateLimitSnapshots[strings.TrimSpace(authID)]
	out := map[string]any{}
	if snap != nil {
		for key, w := range snap.Windows {
			item := map[string]any{"utilization": w.Utilization, "resets_at": nil}
			if !w.ResetsAt.IsZero() {
				item["resets_at"] = w.ResetsAt.Format(time.RFC3339)
			}
			out[key] = item
		}
		out["cpa_observed_at"] = snap.ObservedAt.Format(time.RFC3339)
	}
	rateLimitMu.RUnlock()
	if snap == nil {
		return nil, false
	}
	body, err := json.Marshal(out)
	return body, err == nil
}

// ResetRateLimitSnapshots clears all snapshots (tests).
func ResetRateLimitSnapshots() {
	rateLimitMu.Lock()
	rateLimitSnapshots = map[string]*rateLimitSnapshot{}
	rateLimitMu.Unlock()
}
