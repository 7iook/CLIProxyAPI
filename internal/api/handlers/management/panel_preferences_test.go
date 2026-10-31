package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func newPanelPrefsHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("config-version: 8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewHandler(&config.Config{AuthDir: dir}, cfgPath, nil), cfgPath
}

func callPanelPrefs(t *testing.T, fn gin.HandlerFunc, method, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v8/management/panel-preferences", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	fn(ctx)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, out
}

const panelPrefsDoc = `{"version":1,"display":{"compact":true}}`

func putPrefs(t *testing.T, h *Handler, base, prefs string) (int, map[string]any) {
	t.Helper()
	return callPanelPrefs(t, h.PutPanelPreferences, http.MethodPut, `{"base_revision":"`+base+`","preferences":`+prefs+`}`)
}

func TestPanelPreferencesEmptyGet(t *testing.T) {
	h, _ := newPanelPrefsHandler(t)
	code, out := callPanelPrefs(t, h.GetPanelPreferences, http.MethodGet, "")
	if code != http.StatusOK || out["revision"] != "0" || out["preferences"] != nil || out["updated_at"] != nil {
		t.Fatalf("unexpected: %d %v", code, out)
	}
}

func TestPanelPreferencesPutAndGet(t *testing.T) {
	h, _ := newPanelPrefsHandler(t)
	code, out := putPrefs(t, h, "0", panelPrefsDoc)
	if code != http.StatusOK || out["revision"] != "1" || out["updated_at"] == nil {
		t.Fatalf("put: %d %v", code, out)
	}
	code, got := callPanelPrefs(t, h.GetPanelPreferences, http.MethodGet, "")
	if code != http.StatusOK || got["revision"] != "1" {
		t.Fatalf("get: %d %v", code, got)
	}
	want, _ := json.Marshal(map[string]any{"version": 1, "display": map[string]any{"compact": true}})
	have, _ := json.Marshal(got["preferences"])
	if !bytes.Equal(want, have) {
		t.Fatalf("preferences mismatch: %s vs %s", have, want)
	}
	code, out = putPrefs(t, h, "1", `{"version":1}`)
	if code != http.StatusOK || out["revision"] != "2" {
		t.Fatalf("second put: %d %v", code, out)
	}
}

func TestPanelPreferencesConflictAndMissingBase(t *testing.T) {
	h, _ := newPanelPrefsHandler(t)
	if code, _ := putPrefs(t, h, "0", panelPrefsDoc); code != http.StatusOK {
		t.Fatalf("seed: %d", code)
	}
	code, out := putPrefs(t, h, "0", `{"version":1}`)
	if code != http.StatusConflict || out["error"] != "revision conflict" || out["revision"] != "1" || out["preferences"] == nil {
		t.Fatalf("conflict: %d %v", code, out)
	}
	for _, body := range []string{
		`{"preferences":` + panelPrefsDoc + `}`,
		`{"base_revision":"","preferences":` + panelPrefsDoc + `}`,
	} {
		if code, _ := callPanelPrefs(t, h.PutPanelPreferences, http.MethodPut, body); code != http.StatusBadRequest {
			t.Fatalf("missing base %s: %d", body, code)
		}
	}
}

func TestPanelPreferencesRejectsInvalidDocuments(t *testing.T) {
	h, _ := newPanelPrefsHandler(t)
	for _, prefs := range []string{`[1]`, `"x"`, `null`, `{}`, `{"version":0}`, `{"version":"1"}`, `{"version":1.5}`, `{"version":-1}`} {
		if code, _ := putPrefs(t, h, "0", prefs); code != http.StatusBadRequest {
			t.Fatalf("%s: got %d", prefs, code)
		}
	}
	if code, _ := callPanelPrefs(t, h.PutPanelPreferences, http.MethodPut, `not json`); code != http.StatusBadRequest {
		t.Fatalf("garbage body: %d", code)
	}
	big := `{"version":1,"pad":"` + strings.Repeat("a", 70*1024) + `"}`
	if code, _ := putPrefs(t, h, "0", big); code != http.StatusRequestEntityTooLarge && code != http.StatusBadRequest {
		t.Fatalf("oversize: %d", code)
	}
}

func TestPanelPreferencesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on windows")
	}
	h, cfgPath := newPanelPrefsHandler(t)
	if code, _ := putPrefs(t, h, "0", panelPrefsDoc); code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(cfgPath), panelPreferencesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestPanelPreferencesCorruptFileTreatedAsEmpty(t *testing.T) {
	h, cfgPath := newPanelPrefsHandler(t)
	path := filepath.Join(filepath.Dir(cfgPath), panelPreferencesFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := callPanelPrefs(t, h.GetPanelPreferences, http.MethodGet, "")
	if code != http.StatusOK || out["revision"] != "0" || out["preferences"] != nil {
		t.Fatalf("get corrupt: %d %v", code, out)
	}
	code, out = putPrefs(t, h, "0", panelPrefsDoc)
	if code != http.StatusOK || out["revision"] != "1" {
		t.Fatalf("put over corrupt: %d %v", code, out)
	}
}

func TestPanelPreferencesLeavesConfigUntouched(t *testing.T) {
	h, cfgPath := newPanelPrefsHandler(t)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := putPrefs(t, h, "0", panelPrefsDoc); code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("config.yaml changed")
	}
}

func TestPanelPreferencesNoConfigPath(t *testing.T) {
	h := NewHandler(&config.Config{}, "", nil)
	code, out := callPanelPrefs(t, h.GetPanelPreferences, http.MethodGet, "")
	if code != http.StatusOK || out["revision"] != "0" {
		t.Fatalf("get: %d %v", code, out)
	}
	code, out = putPrefs(t, h, "0", panelPrefsDoc)
	if code != http.StatusServiceUnavailable || out["error"] == nil {
		t.Fatalf("put: %d %v", code, out)
	}
}
