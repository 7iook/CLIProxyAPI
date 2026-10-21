package management

import (
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
