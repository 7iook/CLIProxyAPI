package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestV8AccessAPIKeyCredentialGroupsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	src := "config-version: 8\naccess:\n    api-keys:\n        - \"k1\"\n        - \"k2\"\n    api-key-credential-groups:\n        k2:\n            - solo\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"k2": {"solo"}}
	if !reflect.DeepEqual(cfg.APIKeyCredentialGroups, want) {
		t.Fatalf("loaded groups = %#v", cfg.APIKeyCredentialGroups)
	}
	cfg.APIKeyCredentialGroups["k1"] = []string{"mine"}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "\napi-key-credential-groups:") || !strings.Contains(string(raw), "    api-key-credential-groups:") {
		t.Fatalf("groups not saved under access:\n%s", raw)
	}
	again, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.APIKeyCredentialGroups, map[string][]string{"k1": {"mine"}, "k2": {"solo"}}) {
		t.Fatalf("reloaded groups = %#v\n%s", again.APIKeyCredentialGroups, raw)
	}
}
