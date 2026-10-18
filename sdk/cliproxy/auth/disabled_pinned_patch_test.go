package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestDisabledPinnedAuthExecutesOnlyWhenAllowed(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	exec := &refreshMockExecutor{id: "claude"}
	manager.RegisterExecutor(exec)
	disabled := &Auth{ID: "off", Provider: "claude", Disabled: true, Status: StatusDisabled}
	if _, err := manager.Register(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	pinned := map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "off"}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-haiku-4-5"}, cliproxyexecutor.Options{Metadata: pinned}); err == nil {
		t.Fatal("disabled auth must stay unroutable without the plugin flag")
	}
	allowed := map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "off", cliproxyexecutor.AllowDisabledPinnedAuthMetadataKey: true}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-haiku-4-5"}, cliproxyexecutor.Options{Metadata: allowed}); err != nil {
		t.Fatalf("allowed pinned execute: %v", err)
	}
	got, _ := manager.GetByID("off")
	if got == nil || !got.Disabled || got.Status != StatusDisabled {
		t.Fatalf("auth state changed: %+v", got)
	}
}

