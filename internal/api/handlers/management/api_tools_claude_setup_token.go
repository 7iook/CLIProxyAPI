package management

import (
	"context"
	"errors"
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
// without calling the endpoint (it always returns 403 for setup tokens).
// It first runs the setup-token probe (a 1-token request, throttled by the
// executor) so the panel never reports a cached quota as a successful refresh
// for a token that no longer works: when Anthropic rejects the probe, the raw
// upstream status and error body are returned unchanged, as the upstream
// endpoint would for a dead credential. Only a working token gets the quota
// rebuilt from observed rate-limit headers. ok=false keeps the legacy path.
func (h *Handler) claudeSetupTokenUsageWithProbe(ctx context.Context, authIndex string, auth *coreauth.Auth, method string, target *url.URL) (apiCallResponse, bool) {
	if auth == nil || !coreauth.IsClaudeSetupTokenAuth(auth) {
		return apiCallResponse{}, false
	}
	if method != http.MethodGet || target == nil || !strings.EqualFold(target.Hostname(), "api.anthropic.com") || strings.TrimRight(target.Path, "/") != "/api/oauth/usage" {
		return apiCallResponse{}, false
	}
	if h == nil || h.authManager == nil {
		return claudeSetupTokenUsageFallback(auth, method, target, http.StatusForbidden)
	}
	refreshed, err := h.authManager.ForceRefreshAuth(ctx, auth.ID)
	if err != nil {
		if resp, ok := claudeSetupTokenProbeRejection(err); ok {
			return resp, true
		}
		return apiCallResponse{}, false
	}
	if refreshed == nil {
		return apiCallResponse{}, false
	}
	return claudeSetupTokenUsageFallback(refreshed, method, target, http.StatusForbidden)
}

// claudeSetupTokenProbeRejection turns a rejected setup-token probe into the
// upstream answer: Anthropic's own status code and raw error body.
func claudeSetupTokenProbeRejection(err error) (apiCallResponse, bool) {
	var rejected interface {
		UpstreamStatus() int
		UpstreamBody() string
	}
	if !errors.As(err, &rejected) || rejected == nil {
		return apiCallResponse{}, false
	}
	status := rejected.UpstreamStatus()
	if status < 400 {
		return apiCallResponse{}, false
	}
	body := strings.TrimSpace(rejected.UpstreamBody())
	header := map[string][]string{claudeUsageQuotaSourceHeader: {"setup-token-probe"}}
	if strings.HasPrefix(body, "{") {
		header["Content-Type"] = []string{"application/json"}
	}
	return apiCallResponse{StatusCode: status, Header: header, Body: body}, true
}
