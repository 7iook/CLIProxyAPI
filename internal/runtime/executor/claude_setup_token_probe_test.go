package executor

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func setupTokenTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "st", Provider: "claude", Metadata: map[string]any{
		"type": "claude", "access_token": "sk-ant-oat01-test", "refresh_token": "rt-unused", "is_setup_token": true,
	}}
}

func TestSetupTokenRefreshUsesProbeNotOAuth(t *testing.T) {
	e := NewClaudeExecutor(nil)
	calls := 0
	e.setupTokenProber = func(context.Context, *cliproxyauth.Auth) (int, http.Header, error) { calls++; return http.StatusOK, nil, nil }
	got, err := e.Refresh(context.Background(), setupTokenTestAuth())
	if err != nil || got == nil {
		t.Fatalf("refresh: %v", err)
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d", calls)
	}
	if got.Metadata["last_refresh"] == nil || got.Metadata["access_token"] != "sk-ant-oat01-test" || got.Metadata["refresh_token"] != "rt-unused" {
		t.Fatalf("unexpected metadata: %#v", got.Metadata)
	}
	// Second call within min gap is skipped.
	if _, err := e.Refresh(context.Background(), got); err != nil || calls != 1 {
		t.Fatalf("expected throttled probe, calls=%d err=%v", calls, err)
	}
}

func TestSetupTokenProbeStatuses(t *testing.T) {
	cases := []struct {
		status     int
		wantErr    bool
		wantStatus int
	}{{http.StatusTooManyRequests, false, 0}, {http.StatusUnauthorized, true, 401}, {http.StatusForbidden, true, 401}, {http.StatusInternalServerError, true, 0}, {0, true, 0}}
	for _, c := range cases {
		e := NewClaudeExecutor(nil)
		e.setupTokenProber = func(context.Context, *cliproxyauth.Auth) (int, http.Header, error) {
			if c.status == http.StatusTooManyRequests {
				return c.status, nil, errors.New("rate limited")
			}
			return c.status, nil, errors.New("boom")
		}
		_, err := e.Refresh(context.Background(), setupTokenTestAuth())
		if (err != nil) != c.wantErr {
			t.Fatalf("status %d: err=%v", c.status, err)
		}
		if c.wantStatus != 0 {
			var sc interface{ StatusCode() int }
			if !errors.As(err, &sc) || sc.StatusCode() != c.wantStatus {
				t.Fatalf("status %d: want code %d, got %v", c.status, c.wantStatus, err)
			}
		}
	}
}

func TestOAuthRefreshDoesNotProbe(t *testing.T) {
	e := NewClaudeExecutor(nil)
	e.setupTokenProber = func(context.Context, *cliproxyauth.Auth) (int, http.Header, error) {
		t.Fatal("probe must not run for normal OAuth")
		return 0, nil, nil
	}
	a := &cliproxyauth.Auth{ID: "o", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat01-x", "scope": "user:inference user:profile"}}
	if _, err := e.Refresh(context.Background(), a); err != nil {
		t.Fatalf("no refresh token should be a no-op: %v", err)
	}
	_ = time.Now
}

// productionShapeSetupToken mirrors the production files: empty refresh_token,
// no expired, last_refresh present.
func productionShapeSetupToken(lastRefresh time.Time) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "prod", Provider: "claude", Metadata: map[string]any{
		"type": "claude", "access_token": "sk-ant-oat01-test", "refresh_token": "", "id_token": "",
		"email": "x@example.com", "credential_group": "mine", "is_setup_token": true, "disabled": false,
		"last_refresh": lastRefresh.Format(time.RFC3339), "account_uuid": "u",
	}}
}

func TestSetupTokenProductionShapeRefreshProbesAndRecordsQuota(t *testing.T) {
	e := NewClaudeExecutor(nil)
	calls := 0
	e.setupTokenProber = func(context.Context, *cliproxyauth.Auth) (int, http.Header, error) {
		calls++
		h := http.Header{}
		h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.25")
		h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "4102444800")
		return http.StatusOK, h, nil
	}
	old := time.Now().Add(-13 * time.Hour)
	got, err := e.Refresh(context.Background(), productionShapeSetupToken(old))
	if err != nil || got == nil || calls != 1 {
		t.Fatalf("refresh err=%v calls=%d", err, calls)
	}
	if got.Metadata["last_refresh"] == old.Format(time.RFC3339) {
		t.Fatal("last_refresh not updated")
	}
	if _, ok := got.Metadata[cliproxyauth.ClaudeSetupTokenQuotaMetadataKey]; !ok {
		t.Fatal("probe headers not recorded as quota")
	}
	if body, ok := cliproxyauth.ClaudeSetupTokenUsageJSON(got, time.Now()); !ok || len(body) == 0 {
		t.Fatal("usage JSON should be available after probe")
	}
}
