package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type siblingUsageStub struct {
	calls  atomic.Int32
	status int
	body   string
	header http.Header
	gotURL string
	gotTok string
}

func (s *siblingUsageStub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	s.gotURL = r.URL.String()
	s.gotTok = r.Header.Get("Authorization")
	h := s.header
	if h == nil {
		h = http.Header{"Content-Type": {"application/json"}}
	}
	return &http.Response{StatusCode: s.status, Header: h.Clone(), Body: io.NopCloser(strings.NewReader(s.body)), Request: r}, nil
}

func withSiblingStub(t *testing.T, stub *siblingUsageStub) {
	t.Helper()
	prev := claudeSiblingUsageTransport
	claudeSiblingUsageTransport = func(*Handler, *coreauth.Auth, string) http.RoundTripper { return stub }
	claudeSiblingUsageMu.Lock()
	claudeSiblingUsageCache = map[string]claudeSiblingUsageEntry{}
	claudeSiblingUsageMu.Unlock()
	t.Cleanup(func() { claudeSiblingUsageTransport = prev })
}

func oauthSibling(id, email string, disabled bool) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive, Disabled: disabled, Metadata: map[string]any{
		"type": "claude", "access_token": "sk-ant-oat01-oauth-" + id, "refresh_token": "rt", "email": email,
		"scope": "user:profile user:inference",
	}}
}

func TestClaudeOAuthSiblingMatching(t *testing.T) {
	setup := &coreauth.Auth{ID: "st", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-x", "is_setup_token": true, "email": "A@x.com", "account_uuid": "u1"}}
	byEmail := oauthSibling("o-email", "a@x.com", true)
	byUUID := oauthSibling("o-uuid", "other@x.com", true)
	byUUID.Metadata["account_uuid"] = "u1"
	other := oauthSibling("o-other", "b@x.com", false)
	otherSetup := &coreauth.Auth{ID: "st2", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-z", "is_setup_token": true, "email": "a@x.com"}}
	apiKey := &coreauth.Auth{ID: "k", Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-api03-x"}, Metadata: map[string]any{"email": "a@x.com"}}

	if got := claudeOAuthSibling([]*coreauth.Auth{setup, other, otherSetup, apiKey}, setup); got != nil {
		t.Fatalf("unexpected sibling %s", got.ID)
	}
	if got := claudeOAuthSibling([]*coreauth.Auth{setup, byEmail, other}, setup); got == nil || got.ID != "o-email" {
		t.Fatalf("email match (disabled allowed) = %v", got)
	}
	if got := claudeOAuthSibling([]*coreauth.Auth{byEmail, byUUID}, setup); got == nil || got.ID != "o-uuid" {
		t.Fatalf("account_uuid must win = %v", got)
	}
	enabled := oauthSibling("o-enabled", "a@x.com", false)
	if got := claudeOAuthSibling([]*coreauth.Auth{byEmail, enabled}, setup); got == nil || got.ID != "o-enabled" {
		t.Fatalf("enabled must win over disabled = %v", got)
	}
}

func newSiblingHandler(t *testing.T) (*Handler, *coreauth.Auth) {
	t.Helper()
	h, _, setup := newSetupTokenProbeHandler(t)
	setup.Metadata["email"] = "a@x.com"
	if _, err := h.authManager.Update(context.Background(), setup); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authManager.Register(context.Background(), oauthSibling("claude-oauth.json", "a@x.com", true)); err != nil {
		t.Fatal(err)
	}
	return h, setup
}

func TestSetupTokenUsagePrefersSiblingOfficialBody(t *testing.T) {
	official := `{"five_hour":{"utilization":12,"resets_at":"2100-01-01T00:00:00Z"},"iguana_necktie":{"utilization":7,"resets_at":"2100-01-02T00:00:00Z"},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":7}],"cedar_ember":{"available":1}}`
	stub := &siblingUsageStub{status: http.StatusOK, body: official}
	withSiblingStub(t, stub)
	h, setup := newSiblingHandler(t)
	raw := "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1"
	target, _ := url.Parse(raw)
	req := claudeUsageRequest{URL: raw, Header: map[string]string{"Authorization": "Bearer $TOKEN$", "anthropic-beta": "oauth-2025-04-20"}}

	got, ok := h.claudeSetupTokenUsage(context.Background(), setup, http.MethodGet, target, req)
	if !ok || got.StatusCode != http.StatusOK || got.Body != official {
		t.Fatalf("ok=%v resp=%+v", ok, got)
	}
	if stub.gotURL != raw || stub.gotTok != "Bearer sk-ant-oat01-oauth-claude-oauth.json" {
		t.Fatalf("replayed url=%q auth=%q", stub.gotURL, stub.gotTok)
	}
	if got.Header[claudeUsageQuotaSourceHeader][0] != claudeSiblingUsageSourceHeader {
		t.Fatalf("header = %v", got.Header)
	}
	// Cached: a second panel refresh within the TTL does not hit upstream.
	if _, ok := h.claudeSetupTokenUsage(context.Background(), setup, http.MethodGet, target, req); !ok || stub.calls.Load() != 1 {
		t.Fatalf("calls = %d", stub.calls.Load())
	}
}

func TestSetupTokenUsageFallsBackWhenSiblingFails(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		stub := &siblingUsageStub{status: status, body: `{"error":"x"}`, header: http.Header{"Retry-After": {"120"}}}
		withSiblingStub(t, stub)
		h, setup := newSiblingHandler(t)
		raw := "https://api.anthropic.com/api/oauth/usage"
		target, _ := url.Parse(raw)
		req := claudeUsageRequest{URL: raw, Header: map[string]string{"Authorization": "Bearer $TOKEN$"}}
		got, ok := h.claudeSetupTokenUsage(context.Background(), setup, http.MethodGet, target, req)
		if !ok || got.StatusCode != http.StatusOK || got.Header[claudeUsageQuotaSourceHeader][0] != "response-headers" {
			t.Fatalf("status %d: ok=%v resp=%+v", status, ok, got)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(got.Body), &body); err != nil || body["five_hour"] == nil {
			t.Fatalf("status %d: passive body = %s", status, got.Body)
		}
		if _, has := body["cedar_ember"]; has {
			t.Fatalf("passive body must not carry cedar_ember")
		}
		// Failure is remembered, so the next refresh does not retry immediately.
		_, _ = h.claudeSetupTokenUsage(context.Background(), setup, http.MethodGet, target, req)
		if stub.calls.Load() != 1 {
			t.Fatalf("status %d: calls = %d", status, stub.calls.Load())
		}
		claudeSiblingUsageMu.Lock()
		for _, e := range claudeSiblingUsageCache {
			if status == http.StatusTooManyRequests && time.Until(e.until) < 100*time.Second {
				t.Fatalf("429 must honour Retry-After, until in %s", time.Until(e.until))
			}
		}
		claudeSiblingUsageMu.Unlock()
	}
}

func TestSetupTokenUsageWithoutSiblingKeepsPassive(t *testing.T) {
	stub := &siblingUsageStub{status: http.StatusOK, body: `{}`}
	withSiblingStub(t, stub)
	h, _, setup := newSetupTokenProbeHandler(t)
	raw := "https://api.anthropic.com/api/oauth/usage"
	target, _ := url.Parse(raw)
	got, ok := h.claudeSetupTokenUsage(context.Background(), setup, http.MethodGet, target, claudeUsageRequest{URL: raw, Header: map[string]string{"Authorization": "Bearer $TOKEN$"}})
	if !ok || got.Header[claudeUsageQuotaSourceHeader][0] != "response-headers" || stub.calls.Load() != 0 {
		t.Fatalf("ok=%v resp=%+v calls=%d", ok, got, stub.calls.Load())
	}
}

func TestSetupTokenAccountEndpointsRouteToSibling(t *testing.T) {
	h, setup := newSiblingHandler(t)
	parse := func(s string) *url.URL { u, _ := url.Parse(s); return u }
	cases := []struct {
		method, url string
		want        bool
	}{
		{http.MethodGet, "https://api.anthropic.com/api/oauth/profile", true},
		{http.MethodPost, "https://api.anthropic.com/api/organizations/1d99405c-0000/reset_rate_limits", true},
		{http.MethodGet, "https://api.anthropic.com/api/organizations/1d99405c-0000/reset_rate_limits", false},
		{http.MethodPost, "https://api.anthropic.com/api/organizations//reset_rate_limits", false},
		{http.MethodGet, "https://api.anthropic.com/api/oauth/usage", false},
		{http.MethodPost, "https://api.anthropic.com/v1/messages", false},
		{http.MethodGet, "https://example.com/api/oauth/profile", false},
	}
	for _, tc := range cases {
		got := h.claudeSetupTokenAccountSibling(setup, tc.method, parse(tc.url))
		if (got != nil) != tc.want || (got != nil && got.ID != "claude-oauth.json") {
			t.Errorf("%s %s: sibling=%v want=%v", tc.method, tc.url, got, tc.want)
		}
	}
	// Full OAuth credentials and setup-tokens without a sibling are untouched.
	if got := h.claudeSetupTokenAccountSibling(oauthSibling("x", "a@x.com", false), http.MethodGet, parse("https://api.anthropic.com/api/oauth/profile")); got != nil {
		t.Fatal("OAuth credential must keep itself")
	}
	lone, _, loneSetup := newSetupTokenProbeHandler(t)
	if got := lone.claudeSetupTokenAccountSibling(loneSetup, http.MethodGet, parse("https://api.anthropic.com/api/oauth/profile")); got != nil {
		t.Fatal("no sibling: keep setup-token")
	}
}
