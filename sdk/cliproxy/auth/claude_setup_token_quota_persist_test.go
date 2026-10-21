package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The last-known setup-token quota must survive a restart: it is written to
// the auth file through the normal persist path and read back after Load.
func TestSetupTokenQuotaSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := cliproxyauth.NewManager(store, nil, nil)
	auth, err := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: "st.json", FileName: "st.json", Provider: "claude",
		Metadata: map[string]any{"type": "claude", "access_token": "sk-ant-oat01-x", "is_setup_token": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.42"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"4102444800"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.1"},
	})
	manager.MarkResult(ctx, cliproxyauth.Result{AuthID: auth.ID, Provider: "claude", Model: "claude-haiku-4-5", Success: true})

	// "Restart": a fresh store + manager loading from the same directory.
	store2 := sdkauth.NewFileTokenStore()
	store2.SetBaseDir(dir)
	manager2 := cliproxyauth.NewManager(store2, nil, nil)
	if err := manager2.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	var reloaded *cliproxyauth.Auth
	for _, a := range manager2.List() {
		if a.Provider == "claude" {
			reloaded = a
		}
	}
	if reloaded == nil {
		t.Fatal("auth not reloaded")
	}
	if !reloaded.Quota.ObservedAt.IsZero() {
		t.Fatal("live observation must not survive restart; data must come from metadata")
	}
	body, ok := cliproxyauth.ClaudeSetupTokenUsageJSON(reloaded, time.Now())
	if !ok {
		t.Fatalf("no usage after restart; metadata=%v", reloaded.Metadata)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	fh := got["five_hour"].(map[string]any)
	if fh["utilization"].(float64) != 42 || fh["resets_at"] != "2100-01-01T00:00:00Z" || got["captured_at"] == nil {
		t.Fatalf("usage after restart = %s", body)
	}
	if got["seven_day"].(map[string]any)["utilization"].(float64) != 10 {
		t.Fatalf("usage after restart = %s", body)
	}
}
