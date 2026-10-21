package auth

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Claude setup-token credentials (`claude setup-token`, inference-only scope)
// cannot read /api/oauth/usage. Their quota is still reported on every
// /v1/messages response as Anthropic-Ratelimit-Unified-* headers, which the
// passive quota observation (QuotaState.Signals) already captures. This file
// maps those signals to the oauth/usage response shape and keeps a last-known
// copy in the credential metadata so it survives a restart.

// ClaudeSetupTokenQuotaMetadataKey is the auth metadata key holding the
// last-known setup-token quota: {"captured_at": RFC3339, "windows":
// {"five_hour": {"utilization": 0-100, "resets_at": RFC3339|null}, ...}}.
const ClaudeSetupTokenQuotaMetadataKey = "setup_token_quota"

const (
	// setupTokenQuotaMinWriteInterval bounds metadata writes caused only by a
	// utilization change. Reset/window-set changes are written immediately.
	setupTokenQuotaMinWriteInterval = 60 * time.Second
	// setupTokenQuotaHeartbeat refreshes captured_at for unchanged values.
	setupTokenQuotaHeartbeat = 10 * time.Minute
	// setupTokenQuotaMinDelta is the utilization change (percentage points)
	// worth persisting; headers are reported at 1% granularity.
	setupTokenQuotaMinDelta = 1.0
)

// claudeUnifiedWindows maps unified header window names to oauth/usage keys.
var claudeUnifiedWindows = []struct{ header, key string }{
	{"5h", "five_hour"},
	{"7d", "seven_day"},
	{"7d_oi", "iguana_necktie"},
}

type setupTokenWindow struct {
	Utilization float64
	ResetsAt    time.Time
}

type setupTokenQuota struct {
	CapturedAt time.Time
	Windows    map[string]setupTokenWindow
}

// IsClaudeSetupTokenCredential reports whether token (the credential's bearer
// value) on auth is a Claude setup token.
func IsClaudeSetupTokenCredential(auth *Auth, token string) bool {
	if auth == nil || !strings.Contains(token, "sk-ant-oat") {
		return false
	}
	md := auth.Metadata
	if v, _ := md["skip_account_profile"].(bool); v {
		return true
	}
	if v, _ := md["is_setup_token"].(bool); v {
		return true
	}
	if v, _ := md["setup_token"].(bool); v {
		return true
	}
	if kind := strings.ToLower(auth.Attributes["auth_kind"]); kind == "setup_token" || kind == "setup-token" {
		return true
	}
	scopes, _ := md["scopes"].(string)
	if scopes == "" {
		scopes, _ = md["scope"].(string)
	}
	scopes = strings.ToLower(scopes)
	return scopes != "" && !strings.Contains(scopes, "user:profile") && !strings.Contains(scopes, "user:office")
}

// IsClaudeSetupTokenAuth reports whether auth is a Claude setup-token credential.
func IsClaudeSetupTokenAuth(auth *Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return false
	}
	token := ""
	if auth.Attributes != nil {
		token = auth.Attributes["api_key"]
	}
	if token == "" {
		token, _ = auth.Metadata["access_token"].(string)
	}
	return IsClaudeSetupTokenCredential(auth, token)
}

func quotaFromSignals(signals map[string]string, observedAt time.Time) (setupTokenQuota, bool) {
	q := setupTokenQuota{CapturedAt: observedAt.UTC(), Windows: map[string]setupTokenWindow{}}
	lookup := func(name string) string {
		for k, v := range signals {
			if strings.EqualFold(k, name) {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	for _, spec := range claudeUnifiedWindows {
		prefix := "Anthropic-Ratelimit-Unified-" + spec.header + "-"
		used, err := strconv.ParseFloat(lookup(prefix+"Utilization"), 64)
		if err != nil || math.IsNaN(used) || math.IsInf(used, 0) {
			continue
		}
		w := setupTokenWindow{Utilization: math.Max(0, math.Min(100, math.Round(used*10000)/100))}
		if reset, errReset := strconv.ParseFloat(lookup(prefix+"Reset"), 64); errReset == nil && reset > 0 {
			w.ResetsAt = time.Unix(int64(reset), 0).UTC()
		}
		q.Windows[spec.key] = w
	}
	return q, len(q.Windows) > 0
}

func (q setupTokenQuota) metadataValue() map[string]any {
	windows := make(map[string]any, len(q.Windows))
	for key, w := range q.Windows {
		item := map[string]any{"utilization": w.Utilization, "resets_at": nil}
		if !w.ResetsAt.IsZero() {
			item["resets_at"] = w.ResetsAt.Format(time.RFC3339)
		}
		windows[key] = item
	}
	return map[string]any{"captured_at": q.CapturedAt.Format(time.RFC3339), "windows": windows}
}

func quotaFromMetadata(md map[string]any) (setupTokenQuota, bool) {
	raw, _ := md[ClaudeSetupTokenQuotaMetadataKey].(map[string]any)
	if raw == nil {
		return setupTokenQuota{}, false
	}
	q := setupTokenQuota{Windows: map[string]setupTokenWindow{}}
	if s, _ := raw["captured_at"].(string); s != "" {
		q.CapturedAt, _ = time.Parse(time.RFC3339, s)
	}
	windows, _ := raw["windows"].(map[string]any)
	for key, v := range windows {
		item, _ := v.(map[string]any)
		used, ok := toFloat(item["utilization"])
		if !ok {
			continue
		}
		w := setupTokenWindow{Utilization: used}
		if s, _ := item["resets_at"].(string); s != "" {
			w.ResetsAt, _ = time.Parse(time.RFC3339, s)
		}
		q.Windows[key] = w
	}
	return q, len(q.Windows) > 0 && !q.CapturedAt.IsZero()
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// setupTokenQuotaWorthWriting decides whether next replaces prev on disk.
func setupTokenQuotaWorthWriting(prev, next setupTokenQuota, havePrev bool) bool {
	if !havePrev {
		return true
	}
	elapsed := next.CapturedAt.Sub(prev.CapturedAt)
	if elapsed < 0 {
		return false
	}
	if elapsed >= setupTokenQuotaHeartbeat {
		return true
	}
	if len(prev.Windows) != len(next.Windows) {
		return true
	}
	utilChanged := false
	for key, nw := range next.Windows {
		pw, ok := prev.Windows[key]
		if !ok || !pw.ResetsAt.Equal(nw.ResetsAt) {
			return true
		}
		if math.Abs(pw.Utilization-nw.Utilization) >= setupTokenQuotaMinDelta {
			utilChanged = true
		}
	}
	return utilChanged && elapsed >= setupTokenQuotaMinWriteInterval
}

// syncClaudeSetupTokenQuotaMetadata copies the live quota observation of a
// setup-token auth into its metadata when worth persisting. The caller holds
// the manager lock and persists the auth afterwards. The metadata map is
// replaced, not mutated, because clones may share readers.
func syncClaudeSetupTokenQuotaMetadata(auth *Auth) bool {
	if !IsClaudeSetupTokenAuth(auth) || auth.Quota.ObservedAt.IsZero() {
		return false
	}
	next, ok := quotaFromSignals(auth.Quota.Signals, auth.Quota.ObservedAt)
	if !ok {
		return false
	}
	prev, havePrev := quotaFromMetadata(auth.Metadata)
	if !setupTokenQuotaWorthWriting(prev, next, havePrev) {
		return false
	}
	md := cloneMetadataMap(auth.Metadata)
	if md == nil {
		md = map[string]any{}
	}
	md[ClaudeSetupTokenQuotaMetadataKey] = next.metadataValue()
	auth.Metadata = md
	return true
}

// ClaudeSetupTokenUsageJSON renders the latest known quota of a setup-token
// auth as an /api/oauth/usage body, plus "captured_at". The live observation
// wins over the persisted copy when newer. Windows whose reset time has passed
// are reported as reset (utilization 0, resets_at null).
func ClaudeSetupTokenUsageJSON(auth *Auth, now time.Time) ([]byte, bool) {
	if !IsClaudeSetupTokenAuth(auth) {
		return nil, false
	}
	q, ok := quotaFromMetadata(auth.Metadata)
	if live, okLive := quotaFromSignals(auth.Quota.Signals, auth.Quota.ObservedAt); okLive && !auth.Quota.ObservedAt.IsZero() && (!ok || live.CapturedAt.After(q.CapturedAt)) {
		q, ok = live, true
	}
	if !ok {
		return nil, false
	}
	out := map[string]any{"captured_at": q.CapturedAt.Format(time.RFC3339)}
	for key, w := range q.Windows {
		item := map[string]any{"utilization": w.Utilization, "resets_at": nil}
		if !w.ResetsAt.IsZero() {
			if w.ResetsAt.After(now) {
				item["resets_at"] = w.ResetsAt.Format(time.RFC3339)
			} else {
				item["utilization"] = 0.0
			}
		}
		out[key] = item
	}
	body, err := json.Marshal(out)
	return body, err == nil
}
