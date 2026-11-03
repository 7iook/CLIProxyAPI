package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func postSetupTokenCallback(t *testing.T, authDir, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.POST("/v8/management/oauth/callback", h.PostOAuthCallbackV8)
	req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func readAnthropicCallbackPayload(t *testing.T, authDir, state string) oauthCallbackFilePayload {
	t.Helper()
	data, errRead := os.ReadFile(filepath.Join(authDir, ".oauth-anthropic-"+state+".oauth"))
	if errRead != nil {
		t.Fatalf("expected anthropic callback file: %v", errRead)
	}
	var payload oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		t.Fatalf("decode callback payload: %v", errUnmarshal)
	}
	return payload
}

func TestOAuthCallbackSetupTokenProviderMatchesAnthropicSession(t *testing.T) {
	authDir := t.TempDir()
	state := "setup-token-state-url"
	RegisterOAuthSession(state, "anthropic")
	defer CompleteOAuthSession(state)

	body := `{"provider":"claude-setup-token","redirect_url":"http://localhost:54545/callback?code=the-code&state=` + state + `"}`
	w := postSetupTokenCallback(t, authDir, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if p := readAnthropicCallbackPayload(t, authDir, state); p.State != state || p.Code != "the-code" {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestOAuthCallbackAcceptsPastedCodeHashState(t *testing.T) {
	cases := []struct {
		name string
		body func(state string) string
	}{
		{"redirect_url code#state", func(s string) string {
			return `{"provider":"claude-setup-token","redirect_url":"pasted-code#` + s + `"}`
		}},
		{"redirect_url bare code with body state", func(s string) string {
			return `{"provider":"claude","state":"` + s + `","redirect_url":"pasted-code"}`
		}},
		{"code field code#state", func(s string) string {
			return `{"provider":"anthropic","code":"pasted-code#` + s + `"}`
		}},
		{"url with code#state in code param", func(s string) string {
			return `{"provider":"claude-setup-token","redirect_url":"http://localhost:54545/callback?code=pasted-code%23` + s + `&state=` + s + `"}`
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authDir := t.TempDir()
			state := "setup-token-state-paste-" + string(rune('a'+i))
			RegisterOAuthSession(state, "anthropic")
			defer CompleteOAuthSession(state)
			w := postSetupTokenCallback(t, authDir, tc.body(state))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if p := readAnthropicCallbackPayload(t, authDir, state); p.State != state || p.Code != "pasted-code" {
				t.Fatalf("unexpected payload: %+v", p)
			}
		})
	}
}

func TestOAuthCallbackRejectsCodeStateMismatch(t *testing.T) {
	authDir := t.TempDir()
	state := "setup-token-state-mismatch"
	RegisterOAuthSession(state, "anthropic")
	defer CompleteOAuthSession(state)

	body := `{"provider":"claude-setup-token","state":"` + state + `","redirect_url":"pasted-code#other-state"}`
	w := postSetupTokenCallback(t, authDir, body)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "state does not match code") {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(authDir, ".oauth-anthropic-"+state+".oauth")); !os.IsNotExist(err) {
		t.Fatalf("callback file must not be written on mismatch, stat err = %v", err)
	}
}

func TestNormalizeOAuthCallbackProviderSetupTokenAlias(t *testing.T) {
	got, err := NormalizeOAuthCallbackProvider("Claude-Setup-Token")
	if err != nil || got != "anthropic" {
		t.Fatalf("NormalizeOAuthCallbackProvider = %q, %v", got, err)
	}
}
