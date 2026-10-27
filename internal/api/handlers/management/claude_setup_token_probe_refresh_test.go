package management

import (
	"context"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// fakeSetupTokenProbeExecutor stands in for the Claude executor's probe refresh.
type fakeSetupTokenProbeExecutor struct {
	refreshRecordExecutor
	probes atomic.Int32
}

func (e *fakeSetupTokenProbeExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.probes.Add(1)
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.3")
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	coreauth.RecordClaudeSetupTokenProbeHeaders(auth, h, time.Now())
	return auth, nil
}

func newSetupTokenProbeHandler(t *testing.T) (*Handler, *fakeSetupTokenProbeExecutor, *coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	exec := &fakeSetupTokenProbeExecutor{refreshRecordExecutor: refreshRecordExecutor{provider: "claude"}}
	manager.RegisterExecutor(exec)
	a := &coreauth.Auth{ID: "claude-st.json", Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{
		"type": "claude", "access_token": "sk-ant-oat01-x", "refresh_token": "", "is_setup_token": true,
		"last_refresh": time.Now().Add(-time.Hour).Format(time.RFC3339),
	}}
	if _, err := manager.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager), exec, a
}

func TestForceRefreshAllIncludesSetupTokenWithoutRefreshToken(t *testing.T) {
	h, exec, _ := newSetupTokenProbeHandler(t)
	results := h.authManager.ForceRefreshAll(context.Background())
	if len(results) != 1 || !results[0].Success || exec.probes.Load() != 1 {
		t.Fatalf("results=%+v probes=%d", results, exec.probes.Load())
	}
}

func TestSetupTokenUsageQueryProbesWhenNoSnapshot(t *testing.T) {
	h, exec, a := newSetupTokenProbeHandler(t)
	usage, _ := url.Parse("https://api.anthropic.com/api/oauth/usage")
	got, ok := h.claudeSetupTokenUsageWithProbe(context.Background(), "", a, http.MethodGet, usage)
	if !ok || got.StatusCode != http.StatusOK || exec.probes.Load() != 1 {
		t.Fatalf("ok=%v resp=%+v probes=%d", ok, got, exec.probes.Load())
	}
	other, _ := url.Parse("https://api.anthropic.com/api/oauth/profile")
	if _, ok := h.claudeSetupTokenUsageWithProbe(context.Background(), "", a, http.MethodGet, other); ok {
		t.Fatal("non-usage URL must keep legacy path")
	}
}
