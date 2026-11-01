package claude

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestClaudeGenerateAuthURLWithInferenceOnlyScope(t *testing.T) {
	auth := &ClaudeAuth{}
	authURL, _, err := auth.GenerateAuthURLWithOptions("state-value", &PKCECodes{CodeChallenge: "challenge"}, ClaudeAuthOptions{Scope: SetupTokenScope})
	if err != nil {
		t.Fatalf("GenerateAuthURLWithOptions() error = %v", err)
	}
	parsed, errParse := url.Parse(authURL)
	if errParse != nil {
		t.Fatalf("parse auth url: %v", errParse)
	}
	scope := parsed.Query().Get("scope")
	if scope != SetupTokenScope {
		t.Fatalf("scope = %q, want %q", scope, SetupTokenScope)
	}
	if strings.Contains(scope, "user:profile") {
		t.Fatalf("setup-token scope must not include user:profile, got %q", scope)
	}
	if got := parsed.Query().Get("code"); got != "true" {
		t.Fatalf("code = %q, want true", got)
	}
}

func TestClaudeGenerateAuthURLDefaultsUnchanged(t *testing.T) {
	auth := &ClaudeAuth{}
	legacyURL, _, errLegacy := auth.GenerateAuthURL("state-value", &PKCECodes{CodeChallenge: "challenge"})
	if errLegacy != nil {
		t.Fatalf("GenerateAuthURL() error = %v", errLegacy)
	}
	optsURL, _, errOpts := auth.GenerateAuthURLWithOptions("state-value", &PKCECodes{CodeChallenge: "challenge"}, ClaudeAuthOptions{})
	if errOpts != nil {
		t.Fatalf("GenerateAuthURLWithOptions() error = %v", errOpts)
	}
	if legacyURL != optsURL {
		t.Fatalf("zero-opts URL = %q, want byte-identical to legacy %q", optsURL, legacyURL)
	}
}

func TestClaudeExchangeCarriesExpiresInWhenSetupToken(t *testing.T) {
	var tokenBody string
	var profileCalled bool
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case TokenURL:
					body, _ := io.ReadAll(req.Body)
					tokenBody = string(body)
					return jsonResponse(req, `{"access_token":"sk-ant-oat01-x","refresh_token":"","expires_in":31536000,"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email_address":"user@example.com"}}`), nil
				case ProfileURL, RolesURL:
					profileCalled = true
					return jsonResponse(req, `{}`), nil
				default:
					t.Fatalf("unexpected URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}
	_, err := auth.ExchangeCodeForTokensWithOptions(context.Background(), "code", "state", &PKCECodes{CodeVerifier: "verifier"}, ClaudeAuthOptions{ExpiresIn: SetupTokenExpiresInSeconds, SkipAccountProfile: true})
	if err != nil {
		t.Fatalf("ExchangeCodeForTokensWithOptions() error = %v", err)
	}
	if !strings.Contains(tokenBody, `"expires_in":31536000`) {
		t.Fatalf("exchange body = %q, want it to carry expires_in=31536000", tokenBody)
	}
	if profileCalled {
		t.Fatal("setup-token exchange must not call profile/roles (inference-only token 403s)")
	}
}

func TestClaudeExchangeOmitsExpiresInForNormalOAuth(t *testing.T) {
	var tokenBody string
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case TokenURL:
					body, _ := io.ReadAll(req.Body)
					tokenBody = string(body)
					return jsonResponse(req, `{"access_token":"access","refresh_token":"refresh","expires_in":28800,"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email_address":"user@example.com"}}`), nil
				case ProfileURL, RolesURL:
					return jsonResponse(req, `{"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"}}`), nil
				default:
					t.Fatalf("unexpected URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}
	_, err := auth.ExchangeCodeForTokens(context.Background(), "code", "state", &PKCECodes{CodeVerifier: "verifier"})
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", err)
	}
	if strings.Contains(tokenBody, "expires_in") {
		t.Fatalf("normal OAuth exchange body = %q, must not carry expires_in", tokenBody)
	}
}
