package management

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestClaudeSetupTokenUsageFallback(t *testing.T) {
	usage, _ := url.Parse("https://api.anthropic.com/api/oauth/usage")
	profile, _ := url.Parse("https://api.anthropic.com/api/oauth/profile")
	setup := &coreauth.Auth{ID: "s1", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-x", "is_setup_token": true}}
	oauth := &coreauth.Auth{ID: "o1", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-y", "scope": "user:profile user:inference"}}

	if _, ok := claudeSetupTokenUsageFallback(setup, http.MethodGet, usage, http.StatusForbidden); ok {
		t.Fatal("no snapshot yet: must keep upstream 403")
	}
	signals := map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.42", "Anthropic-Ratelimit-Unified-7d-Utilization": "0.1"}
	setup.Quota = coreauth.QuotaState{ObservedAt: time.Now(), Signals: signals}
	oauth.Quota = coreauth.QuotaState{ObservedAt: time.Now(), Signals: signals}

	got, ok := claudeSetupTokenUsageFallback(setup, http.MethodGet, usage, http.StatusForbidden)
	if !ok || got.StatusCode != http.StatusOK || got.Header[claudeUsageQuotaSourceHeader][0] != "response-headers" {
		t.Fatalf("fallback = %+v ok=%v", got, ok)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["five_hour"].(map[string]any)["utilization"].(float64) != 42 {
		t.Fatalf("body = %s", got.Body)
	}

	cases := []struct {
		name   string
		auth   *coreauth.Auth
		method string
		u      *url.URL
		status int
	}{
		{"401 invalid/expired token keeps upstream", setup, http.MethodGet, usage, http.StatusUnauthorized},
		{"success keeps upstream", setup, http.MethodGet, usage, http.StatusOK},
		{"other url", setup, http.MethodGet, profile, http.StatusForbidden},
		{"other method", setup, http.MethodPost, usage, http.StatusForbidden},
		{"full oauth credential", oauth, http.MethodGet, usage, http.StatusForbidden},
		{"nil auth", nil, http.MethodGet, usage, http.StatusForbidden},
	}
	for _, tc := range cases {
		if _, ok := claudeSetupTokenUsageFallback(tc.auth, tc.method, tc.u, tc.status); ok {
			t.Errorf("%s: unexpected fallback", tc.name)
		}
	}
}
