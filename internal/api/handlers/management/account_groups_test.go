package management

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newAccountGroupsHandler(t *testing.T, groups []config.AccountGroup) (*Handler, *coreauth.Manager) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("config-version: 8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	cfg := &config.Config{AuthDir: dir, AccountGroups: groups}
	return NewHandler(cfg, cfgPath, manager), manager
}

func registerGroupTestAuth(t *testing.T, manager *coreauth.Manager, id string, attrs map[string]string, meta map[string]any) {
	t.Helper()
	if attrs == nil {
		attrs = map[string]string{}
	}
	if _, ok := attrs["path"]; !ok {
		attrs["path"] = "/tmp/" + id
	}
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: id, FileName: id, Provider: "claude", Status: coreauth.StatusActive,
		Attributes: attrs, Metadata: meta,
	})
}

func callGroupsHandler(t *testing.T, fn gin.HandlerFunc, method, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v8/management/account-groups", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	fn(ctx)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, out
}

func TestAccountGroupsGetEmptyAndPut(t *testing.T) {
	h, _ := newAccountGroupsHandler(t, nil)
	code, out := callGroupsHandler(t, h.GetAccountGroups, http.MethodGet, "")
	if code != http.StatusOK || string(mustJSON(t, out["groups"])) != "[]" {
		t.Fatalf("get empty: %d %v", code, out)
	}

	code, out = callGroupsHandler(t, h.PutAccountGroups, http.MethodPut,
		`{"groups":[{"name":" Pro ","color":"#7c3aed"},{"id":"free","name":"Free"}]}`)
	if code != http.StatusOK {
		t.Fatalf("put: %d %v", code, out)
	}
	groups := out["groups"].([]any)
	first := groups[0].(map[string]any)
	if !strings.HasPrefix(first["id"].(string), "g-") || first["name"] != "Pro" || first["color"] != "#7C3AED" {
		t.Fatalf("unexpected first group: %v", first)
	}
	if _, hasColor := groups[1].(map[string]any)["color"]; hasColor {
		t.Fatalf("empty color must be omitted: %v", groups[1])
	}
	if out["cleared"].(float64) != 0 {
		t.Fatalf("cleared = %v", out["cleared"])
	}
	// Persisted to the config file and visible via GET.
	reloaded, err := config.LoadConfig(h.configFilePath)
	if err != nil || len(reloaded.AccountGroups) != 2 || reloaded.AccountGroups[1].ID != "free" {
		t.Fatalf("persisted groups: %+v err=%v", reloaded, err)
	}
	_, out = callGroupsHandler(t, h.GetAccountGroups, http.MethodGet, "")
	if len(out["groups"].([]any)) != 2 {
		t.Fatalf("get after put: %v", out)
	}
}

func TestAccountGroupsPutValidation(t *testing.T) {
	many := make([]string, config.MaxAccountGroups+1)
	for i := range many {
		many[i] = `{"name":"n` + strings.Repeat("x", i) + `"}`
	}
	tests := []struct{ name, body string }{
		{"missing groups", `{}`},
		{"malformed", `{`},
		{"empty name", `{"groups":[{"name":" "}]}`},
		{"duplicate name", `{"groups":[{"name":"A"},{"name":"a"}]}`},
		{"duplicate id", `{"groups":[{"id":"x","name":"A"},{"id":"x","name":"B"}]}`},
		{"bad id", `{"groups":[{"id":"BAD","name":"A"}]}`},
		{"bad color", `{"groups":[{"name":"A","color":"#12"}]}`},
		{"too many", `{"groups":[` + strings.Join(many, ",") + `]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newAccountGroupsHandler(t, []config.AccountGroup{{ID: "keep", Name: "Keep"}})
			code, out := callGroupsHandler(t, h.PutAccountGroups, http.MethodPut, tc.body)
			if code != http.StatusBadRequest || out["error"] == "" {
				t.Fatalf("got %d %v, want 400 with error", code, out)
			}
			if len(h.cfg.AccountGroups) != 1 || h.cfg.AccountGroups[0].ID != "keep" {
				t.Fatalf("config mutated on error: %+v", h.cfg.AccountGroups)
			}
		})
	}
}

func TestAccountGroupsAssignAndDeleteClears(t *testing.T) {
	h, manager := newAccountGroupsHandler(t, []config.AccountGroup{{ID: "pro", Name: "Pro"}, {ID: "free", Name: "Free"}})
	registerGroupTestAuth(t, manager, "a.json", map[string]string{"priority": "5", "credential_group": "x"}, map[string]any{"credential_group": "x", "priority": float64(5)})
	registerGroupTestAuth(t, manager, "b.json", nil, map[string]any{"ui": map[string]any{"order": float64(3)}})
	registerGroupTestAuth(t, manager, "cfg.json", map[string]string{"source": "config:claude[abc]"}, nil)
	registerGroupTestAuth(t, manager, "virt.json", map[string]string{coreauth.AttributePluginVirtual: "true"}, nil)

	code, out := callGroupsHandler(t, h.AssignAccountGroup, http.MethodPost,
		`{"group_id":"pro","names":["a.json","b.json","a.json","missing.json","cfg.json","virt.json"]}`)
	if code != http.StatusOK {
		t.Fatalf("assign: %d %v", code, out)
	}
	if got := string(mustJSON(t, out["updated"])); got != `["a.json","b.json"]` {
		t.Fatalf("updated = %s", got)
	}
	if failed := out["failed"].([]any); len(failed) != 3 {
		t.Fatalf("failed = %v", failed)
	}

	a, _ := manager.GetByID("a.json")
	if group, _ := uiMetadataGroup(a.Metadata); group != "pro" {
		t.Fatalf("ui.group = %v", a.Metadata["ui"])
	}
	// Display-only: routing fields untouched.
	if a.Attributes["priority"] != "5" || a.Attributes["credential_group"] != "x" ||
		a.Metadata["credential_group"] != "x" || a.Metadata["priority"] != float64(5) {
		t.Fatalf("routing fields changed: attrs=%v meta=%v", a.Attributes, a.Metadata)
	}
	b, _ := manager.GetByID("b.json")
	if b.Metadata["ui"].(map[string]any)["order"] != float64(3) {
		t.Fatalf("order lost: %v", b.Metadata)
	}

	// Unknown group is a 400.
	code, _ = callGroupsHandler(t, h.AssignAccountGroup, http.MethodPost, `{"group_id":"nope","names":["a.json"]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown group status = %d", code)
	}

	// Un-assign with empty and null group ids; ui map removed when emptied.
	for _, body := range []string{`{"group_id":"","names":["a.json"]}`, `{"group_id":null,"names":["a.json"]}`} {
		callGroupsHandler(t, h.AssignAccountGroup, http.MethodPost, `{"group_id":"pro","names":["a.json"]}`)
		code, out = callGroupsHandler(t, h.AssignAccountGroup, http.MethodPost, body)
		if code != http.StatusOK || len(out["updated"].([]any)) != 1 {
			t.Fatalf("unassign: %d %v", code, out)
		}
		a, _ = manager.GetByID("a.json")
		if _, has := a.Metadata["ui"]; has {
			t.Fatalf("empty ui not removed: %v", a.Metadata)
		}
	}

	// Deleting a group clears members; b keeps its order.
	callGroupsHandler(t, h.AssignAccountGroup, http.MethodPost, `{"group_id":"pro","names":["a.json","b.json"]}`)
	callGroupsHandler(t, h.AssignAccountGroup, http.MethodPost, `{"group_id":"free","names":[]}`)
	code, out = callGroupsHandler(t, h.PutAccountGroups, http.MethodPut, `{"groups":[{"id":"free","name":"Free"}]}`)
	if code != http.StatusOK || out["cleared"].(float64) != 2 {
		t.Fatalf("put delete: %d %v", code, out)
	}
	a, _ = manager.GetByID("a.json")
	b, _ = manager.GetByID("b.json")
	if _, has := a.Metadata["ui"]; has {
		t.Fatalf("a ui not removed: %v", a.Metadata)
	}
	if ui := b.Metadata["ui"].(map[string]any); ui["order"] != float64(3) || ui["group"] != nil {
		t.Fatalf("b ui = %v", ui)
	}
}

func TestUIEchoPayloadWhitelist(t *testing.T) {
	long := strings.Repeat("g", 65)
	tests := []struct {
		name string
		meta map[string]any
		want string
	}{
		{"no ui", map[string]any{}, ""},
		{"ui not a map", map[string]any{"ui": "x"}, ""},
		{"group and order", map[string]any{"ui": map[string]any{"group": " g-1 ", "order": float64(-1.5), "junk": "x"}}, `{"group":"g-1","order":-1.5}`},
		{"json number and int", map[string]any{"ui": map[string]any{"order": json.Number("7")}}, `{"order":7}`},
		{"int order", map[string]any{"ui": map[string]any{"order": 4}}, `{"order":4}`},
		{"only junk", map[string]any{"ui": map[string]any{"junk": 1, "group": 5}}, ""},
		{"nan order dropped", map[string]any{"ui": map[string]any{"order": math.NaN(), "group": "g"}}, `{"group":"g"}`},
		{"inf order dropped", map[string]any{"ui": map[string]any{"order": math.Inf(1)}}, ""},
		{"long group dropped", map[string]any{"ui": map[string]any{"group": long}}, ""},
		{"blank group dropped", map[string]any{"ui": map[string]any{"group": "  "}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := uiEchoPayload(tc.meta)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if string(mustJSON(t, got)) != tc.want {
				t.Fatalf("got %s, want %s", mustJSON(t, got), tc.want)
			}
		})
	}
}

func TestListAuthFilesEchoesUIAndPatchUIOrder(t *testing.T) {
	h, manager := newAccountGroupsHandler(t, nil)
	dir := h.cfg.AuthDir
	path := filepath.Join(dir, "a.json")
	if err := os.WriteFile(path, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registerGroupTestAuth(t, manager, "a.json", map[string]string{"path": path}, map[string]any{"type": "claude", "ui": map[string]any{"group": "g-1", "evil": "x"}})

	patch := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.PatchAuthFileFields(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	listUI := func() any {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
		h.ListAuthFiles(ctx)
		var payload struct {
			Files []map[string]any `json:"files"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || len(payload.Files) != 1 {
			t.Fatalf("list: %v %s", err, rec.Body.String())
		}
		return payload.Files[0]["ui"]
	}

	if got := string(mustJSON(t, listUI())); got != `{"group":"g-1"}` {
		t.Fatalf("ui = %s", got)
	}
	patch(`{"name":"a.json","ui.order":1024}`)
	if got := string(mustJSON(t, listUI())); got != `{"group":"g-1","order":1024}` {
		t.Fatalf("ui after set = %s", got)
	}
	patch(`{"name":"a.json","ui.order":null}`)
	if got := string(mustJSON(t, listUI())); got != `{"group":"g-1"}` {
		t.Fatalf("ui after delete = %s", got)
	}
	a, _ := manager.GetByID("a.json")
	if _, has := a.Metadata["ui"].(map[string]any)["order"]; has {
		t.Fatalf("order not deleted: %v", a.Metadata)
	}
	patch(`{"name":"a.json","ui.order":-0.5}`)
	if got := string(mustJSON(t, listUI())); got != `{"group":"g-1","order":-0.5}` {
		t.Fatalf("ui after fractional set = %s", got)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(`{"name":"a.json","ui.order":"high"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchAuthFileFields(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric order status = %d", rec.Code)
	}
}

func TestUIMetadataSurvivesRelogin(t *testing.T) {
	target := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{"type": "claude"}}
	coreauth.MergeExistingAuthMetadata(target, map[string]any{"ui": map[string]any{"group": "g-1", "order": float64(2)}})
	ui, ok := target.Metadata["ui"].(map[string]any)
	if !ok || ui["group"] != "g-1" || ui["order"] != float64(2) {
		t.Fatalf("ui not preserved: %v", target.Metadata)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
