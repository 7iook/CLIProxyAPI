package management

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// claudeUsageQuotaSourceHeader tells callers the usage body was rebuilt from
// response headers of earlier requests instead of /api/oauth/usage.
const claudeUsageQuotaSourceHeader = "X-Cpa-Quota-Source"

// claudeSetupTokenUsageFallback answers the management quota query of a Claude
// setup-token credential. Anthropic rejects such tokens on /api/oauth/usage
// with 403 (missing user:profile scope); the account's windows are then served
// from the unified rate-limit headers observed on real requests (live
// QuotaState signals, or the copy persisted in metadata across restarts), in the same
// response shape, so the existing quota UI renders them unchanged. Any other
// status (including 401 for an invalid or expired token), credential or URL
// keeps the upstream answer.
func claudeSetupTokenUsageFallback(auth *coreauth.Auth, method string, target *url.URL, upstreamStatus int) (apiCallResponse, bool) {
	if upstreamStatus != http.StatusForbidden || method != http.MethodGet || target == nil {
		return apiCallResponse{}, false
	}
	if !strings.EqualFold(target.Hostname(), "api.anthropic.com") || strings.TrimRight(target.Path, "/") != "/api/oauth/usage" {
		return apiCallResponse{}, false
	}
	body, ok := coreauth.ClaudeSetupTokenUsageJSON(auth, time.Now())
	if !ok {
		return apiCallResponse{}, false
	}
	return apiCallResponse{
		StatusCode: http.StatusOK,
		Header: map[string][]string{
			"Content-Type":               {"application/json"},
			claudeUsageQuotaSourceHeader: {"response-headers"},
		},
		Body: string(body),
	}, true
}

// claudeSetupTokenUsageWithProbe answers a setup-token /api/oauth/usage query
// without calling the endpoint (it always returns 403 for setup tokens). When
// no quota observation exists yet, it forces one setup-token refresh probe
// (throttled by the executor) and retries. ok=false keeps the legacy path.
func (h *Handler) claudeSetupTokenUsageWithProbe(ctx context.Context, authIndex string, auth *coreauth.Auth, method string, target *url.URL) (apiCallResponse, bool) {
	if auth == nil || !coreauth.IsClaudeSetupTokenAuth(auth) {
		return apiCallResponse{}, false
	}
	if resp, ok := claudeSetupTokenUsageFallback(auth, method, target, http.StatusForbidden); ok {
		return resp, true
	}
	if method != http.MethodGet || target == nil || !strings.EqualFold(target.Hostname(), "api.anthropic.com") || strings.TrimRight(target.Path, "/") != "/api/oauth/usage" {
		return apiCallResponse{}, false
	}
	if h == nil || h.authManager == nil {
		return apiCallResponse{}, false
	}
	refreshed, err := h.authManager.ForceRefreshAuth(ctx, auth.ID)
	if err != nil || refreshed == nil {
		return apiCallResponse{}, false
	}
	return claudeSetupTokenUsageFallback(refreshed, method, target, http.StatusForbidden)
}
