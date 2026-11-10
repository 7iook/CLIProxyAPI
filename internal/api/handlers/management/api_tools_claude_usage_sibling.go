package management

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// claudeUsageRequest carries the caller's original usage request (URL with
// its query, headers with the $TOKEN$ placeholder, proxy override) so it can be
// replayed with a sibling credential.
type claudeUsageRequest struct {
	URL      string
	Header   map[string]string
	ProxyURL string
}

const (
	// claudeSiblingUsageTTL dedupes panel refreshes of the setup-token row and
	// its OAuth sibling row; Anthropic allows only a few usage reads per hour.
	claudeSiblingUsageTTL = 60 * time.Second
	// claudeSiblingUsageFailTTL pauses sibling reads after a failed answer.
	claudeSiblingUsageFailTTL = 60 * time.Second
	// claudeSiblingUsageRateLimitTTL pauses sibling reads after a 429 without Retry-After.
	claudeSiblingUsageRateLimitTTL = 5 * time.Minute
	claudeSiblingUsageSourceHeader = "oauth-sibling"
)

type claudeSiblingUsageEntry struct {
	resp  apiCallResponse
	ok    bool
	until time.Time
}

var (
	claudeSiblingUsageMu    sync.Mutex
	claudeSiblingUsageCache = map[string]claudeSiblingUsageEntry{}
	// claudeSiblingUsageTransport is replaced in tests.
	claudeSiblingUsageTransport = func(h *Handler, auth *coreauth.Auth, proxyURL string) http.RoundTripper {
		return h.apiCallTransport(auth, proxyURL)
	}
)

// claudeOAuthSibling returns the full-OAuth Claude credential of the same
// account as the setup-token credential setup: same account_uuid, otherwise
// same email. Enabled credentials win over disabled ones, then the most
// recently refreshed. Disabled siblings stay eligible: a usage read does not
// route traffic, and their tokens keep being refreshed.
func claudeOAuthSibling(auths []*coreauth.Auth, setup *coreauth.Auth) *coreauth.Auth {
	if setup == nil {
		return nil
	}
	setupUUID := claudeMetadataString(setup, "account_uuid")
	setupEmail := strings.ToLower(claudeMetadataString(setup, "email"))
	var best *coreauth.Auth
	bestRank := 0
	for _, candidate := range auths {
		if candidate == nil || candidate.ID == setup.ID || !coreauth.IsClaudeSubscriptionAuth(candidate) || coreauth.IsClaudeSetupTokenAuth(candidate) {
			continue
		}
		rank := 0
		switch {
		case setupUUID != "" && claudeMetadataString(candidate, "account_uuid") == setupUUID:
			rank = 4
		case setupEmail != "" && strings.ToLower(claudeMetadataString(candidate, "email")) == setupEmail:
			rank = 2
		default:
			continue
		}
		if !candidate.Disabled && candidate.Status != coreauth.StatusDisabled {
			rank++
		}
		if best == nil || rank > bestRank || (rank == bestRank && candidate.LastRefreshedAt.After(best.LastRefreshedAt)) {
			best, bestRank = candidate, rank
		}
	}
	return best
}

func claudeMetadataString(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	v, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(v)
}

// claudeSiblingOfficialUsage replays the usage request with the OAuth sibling
// of setup and returns the official answer unchanged when it is a 200. Any
// failure (no sibling, transport error, 401/403/429/5xx) reports ok=false so
// the caller serves the passive body.
func (h *Handler) claudeSiblingOfficialUsage(ctx context.Context, setup *coreauth.Auth, req claudeUsageRequest) (apiCallResponse, bool) {
	if h == nil || h.authManager == nil || setup == nil || strings.TrimSpace(req.URL) == "" {
		return apiCallResponse{}, false
	}
	usesToken := false
	for _, value := range req.Header {
		if strings.Contains(value, "$TOKEN$") {
			usesToken = true
			break
		}
	}
	if !usesToken {
		return apiCallResponse{}, false
	}
	sibling := claudeOAuthSibling(h.authManager.List(), setup)
	if sibling == nil {
		return apiCallResponse{}, false
	}
	token := tokenValueForAuth(sibling)
	if token == "" {
		return apiCallResponse{}, false
	}

	cacheKey := sibling.ID + "\n" + req.URL
	now := time.Now()
	claudeSiblingUsageMu.Lock()
	if entry, ok := claudeSiblingUsageCache[cacheKey]; ok && now.Before(entry.until) {
		claudeSiblingUsageMu.Unlock()
		return entry.resp, entry.ok
	}
	claudeSiblingUsageMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if errReq != nil {
		return apiCallResponse{}, false
	}
	for key, value := range req.Header {
		if strings.EqualFold(key, "host") {
			continue
		}
		httpReq.Header.Set(key, strings.ReplaceAll(value, "$TOKEN$", token))
	}
	client := &http.Client{Timeout: defaultAPICallTimeout, Transport: claudeSiblingUsageTransport(h, sibling, req.ProxyURL)}
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		log.WithError(errDo).Debugf("claude setup-token usage: sibling %s request failed", sibling.ID)
		h.storeClaudeSiblingUsage(cacheKey, apiCallResponse{}, false, now.Add(claudeSiblingUsageFailTTL))
		return apiCallResponse{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil || resp.StatusCode != http.StatusOK {
		until := now.Add(claudeSiblingUsageFailTTL)
		if resp.StatusCode == http.StatusTooManyRequests {
			until = now.Add(claudeSiblingUsageRateLimitTTL)
			if secs, errAtoi := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); errAtoi == nil && secs > 0 {
				until = now.Add(time.Duration(secs) * time.Second)
			}
		}
		log.Debugf("claude setup-token usage: sibling %s answered %d, serving passive quota", sibling.ID, resp.StatusCode)
		h.storeClaudeSiblingUsage(cacheKey, apiCallResponse{}, false, until)
		return apiCallResponse{}, false
	}
	header := resp.Header.Clone()
	header.Set(claudeUsageQuotaSourceHeader, claudeSiblingUsageSourceHeader)
	out := apiCallResponse{StatusCode: resp.StatusCode, Header: header, Body: string(body)}
	h.storeClaudeSiblingUsage(cacheKey, out, true, now.Add(claudeSiblingUsageTTL))
	return out, true
}

func (h *Handler) storeClaudeSiblingUsage(key string, resp apiCallResponse, ok bool, until time.Time) {
	claudeSiblingUsageMu.Lock()
	defer claudeSiblingUsageMu.Unlock()
	for k, entry := range claudeSiblingUsageCache {
		if !time.Now().Before(entry.until) {
			delete(claudeSiblingUsageCache, k)
		}
	}
	claudeSiblingUsageCache[key] = claudeSiblingUsageEntry{resp: resp, ok: ok, until: until}
}

// claudeSiblingAccountTarget reports whether target is an account endpoint a
// setup-token cannot call but its full-OAuth sibling can: GET
// /api/oauth/profile and POST /api/organizations/{uuid}/reset_rate_limits.
// /api/oauth/usage is handled by claudeSetupTokenUsage (passive fallback).
func claudeSiblingAccountTarget(method string, target *url.URL) bool {
	if target == nil || !strings.EqualFold(target.Hostname(), "api.anthropic.com") {
		return false
	}
	path := strings.TrimRight(target.Path, "/")
	switch method {
	case http.MethodGet:
		return path == "/api/oauth/profile"
	case http.MethodPost:
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		return len(parts) == 4 && parts[0] == "api" && parts[1] == "organizations" && parts[2] != "" && parts[3] == "reset_rate_limits"
	}
	return false
}

// claudeSetupTokenAccountSibling returns the OAuth sibling that should serve
// an account-endpoint call made with a setup-token credential, or nil.
func (h *Handler) claudeSetupTokenAccountSibling(auth *coreauth.Auth, method string, target *url.URL) *coreauth.Auth {
	if h == nil || h.authManager == nil || !coreauth.IsClaudeSetupTokenAuth(auth) || !claudeSiblingAccountTarget(method, target) {
		return nil
	}
	return claudeOAuthSibling(h.authManager.List(), auth)
}
