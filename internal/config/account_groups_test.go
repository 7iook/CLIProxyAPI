package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeAccountGroups(t *testing.T) {
	long := strings.Repeat("a", 65)
	many := make([]AccountGroup, MaxAccountGroups+1)
	for i := range many {
		many[i] = AccountGroup{Name: strings.Repeat("n", 1) + string(rune('A'+i%26)) + strings.Repeat("x", i/26)}
	}
	tests := []struct {
		name    string
		in      []AccountGroup
		wantErr string
	}{
		{"empty ok", nil, ""},
		{"valid", []AccountGroup{{ID: "g-1", Name: " Pro ", Color: "#7c3aed"}}, ""},
		{"empty name", []AccountGroup{{Name: "  "}}, "name"},
		{"long name", []AccountGroup{{Name: long}}, "name"},
		{"dup name case-insensitive", []AccountGroup{{Name: "Pro"}, {Name: "pRO"}}, "duplicate group name"},
		{"dup id", []AccountGroup{{ID: "a", Name: "A"}, {ID: "a", Name: "B"}}, "duplicate group id"},
		{"bad id", []AccountGroup{{ID: "Bad_ID", Name: "A"}}, "invalid group id"},
		{"long id", []AccountGroup{{ID: strings.Repeat("a", 33), Name: "A"}}, "invalid group id"},
		{"bad color", []AccountGroup{{Name: "A", Color: "red"}}, "invalid group color"},
		{"too many", many, "too many groups"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeAccountGroups(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestNormalizeAccountGroupsGeneratesIDsAndNormalizes(t *testing.T) {
	out, err := NormalizeAccountGroups([]AccountGroup{{Name: " Pro ", Color: "#7c3aed"}, {Name: "Free"}})
	if err != nil {
		t.Fatal(err)
	}
	idPattern := regexp.MustCompile(`^g-[0-9a-f]{8}$`)
	if !idPattern.MatchString(out[0].ID) || !idPattern.MatchString(out[1].ID) || out[0].ID == out[1].ID {
		t.Fatalf("unexpected ids: %+v", out)
	}
	if out[0].Name != "Pro" || out[0].Color != "#7C3AED" || out[1].Color != "" {
		t.Fatalf("unexpected normalization: %+v", out)
	}
}

func TestAccountGroupsConfigRoundTrip(t *testing.T) {
	for _, header := range []string{"config-version: 8\n", ""} {
		t.Run("header="+strings.TrimSpace(header), func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			raw := header + "port: 8317\naccount-groups:\n  - id: g-b\n    name: Beta\n  - id: g-a\n    name: Alpha\n    color: \"#112233\"\n"
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.AccountGroups) != 2 || cfg.AccountGroups[0].ID != "g-b" || cfg.AccountGroups[1].Color != "#112233" {
				t.Fatalf("unexpected groups: %+v", cfg.AccountGroups)
			}
			cfg.AccountGroups = []AccountGroup{cfg.AccountGroups[1], {ID: "g-c", Name: "Gamma"}}
			if err = SaveConfigPreserveComments(file, cfg, true); err != nil {
				t.Fatal(err)
			}
			cfg2, err := LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg2.AccountGroups) != 2 || cfg2.AccountGroups[0].ID != "g-a" || cfg2.AccountGroups[1].ID != "g-c" {
				t.Fatalf("reordered groups lost: %+v", cfg2.AccountGroups)
			}
			cfg2.AccountGroups = nil
			if err = SaveConfigPreserveComments(file, cfg2, true); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "account-groups") {
				t.Fatalf("account-groups not removed:\n%s", data)
			}
			cfg3, err := LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg3.AccountGroups) != 0 {
				t.Fatalf("groups remain: %+v", cfg3.AccountGroups)
			}
		})
	}
}
