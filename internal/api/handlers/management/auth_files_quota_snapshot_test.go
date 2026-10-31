package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type quotaSnapshotWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    *string `json:"resets_at"`
}

type quotaSnapshotPayload struct {
	CapturedAt string                         `json:"captured_at"`
	Source     string                         `json:"source"`
	Windows    map[string]quotaSnapshotWindow `json:"windows"`
}

type quotaSnapshotFile struct {
	Name          string                `json:"name"`
	ID            string                `json:"id"`
	QuotaSnapshot *quotaSnapshotPayload `json:"quota_snapshot"`
}

// listAuthFilesRaw calls ListAuthFiles as GET /v0/management/auth-files, or as
// GET /v8/management/credentials (same handler, v8 context) when v8 is true.
func listAuthFilesRaw(t *testing.T, h *Handler, v8 bool) (string, []quotaSnapshotFile) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	if v8 {
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
		ctx.Set(ConfigV8ContextKey, true)
	}
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []quotaSnapshotFile `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return rec.Body.String(), payload.Files
}

func TestListAuthFilesQuotaSnapshot(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	now := time.Now().UTC()
	captured := now.Add(-5 * time.Minute).Truncate(time.Second)
	runtime := map[string]string{"runtime_only": "true"}
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "setup", Provider: "claude", Attributes: runtime,
		Metadata: map[string]any{"access_token": "sk-ant-oat01-SECRETSETUP", "is_setup_token": true,
			coreauth.ClaudeSetupTokenQuotaMetadataKey: map[string]any{
				"captured_at": captured.Format(time.RFC3339),
				"windows": map[string]any{
					"five_hour": map[string]any{"utilization": 20.0, "resets_at": "2100-01-01T00:00:00Z"},
					"seven_day": map[string]any{"utilization": 3.0, "resets_at": nil},
				},
			}},
	})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "oauth", Provider: "claude", Attributes: runtime,
		Metadata: map[string]any{"access_token": "sk-ant-oat01-SECRETOAUTH", "refresh_token": "SECRETREFRESH"},
		Quota:    coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.8", "Anthropic-Ratelimit-Unified-5h-Reset": "4102444800"}},
	})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "empty", Provider: "claude", Attributes: runtime,
		Metadata: map[string]any{"access_token": "sk-ant-oat01-SECRETEMPTY"},
	})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID: "codex", Provider: "codex", Attributes: runtime,
		Quota: coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{"x-codex-primary-used-percent": "90"}},
	})
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	for _, v8 := range []bool{false, true} {
		body, files := listAuthFilesRaw(t, h, v8)
		if strings.Contains(body, "SECRET") {
			t.Fatal("list response leaks credential material")
		}
		byID := map[string]quotaSnapshotFile{}
		for _, f := range files {
			byID[f.ID] = f
		}
		setup := byID["setup"].QuotaSnapshot
		if setup == nil || setup.Source != "passive" || setup.CapturedAt != captured.Format(time.RFC3339) {
			t.Fatalf("setup snapshot = %+v", setup)
		}
		if fh := setup.Windows["five_hour"]; fh.Utilization != 20 || fh.ResetsAt == nil || *fh.ResetsAt != "2100-01-01T00:00:00Z" {
			t.Fatalf("setup five_hour = %+v", fh)
		}
		if sd := setup.Windows["seven_day"]; sd.Utilization != 3 || sd.ResetsAt != nil {
			t.Fatalf("setup seven_day = %+v", sd)
		}
		oauth := byID["oauth"].QuotaSnapshot
		if oauth == nil || oauth.Source != "passive" || oauth.Windows["five_hour"].Utilization != 80 {
			t.Fatalf("oauth snapshot = %+v", oauth)
		}
		if byID["empty"].QuotaSnapshot != nil || byID["codex"].QuotaSnapshot != nil {
			t.Fatal("quota_snapshot must be omitted without data")
		}
		if strings.Count(body, `"quota_snapshot"`) != 2 {
			t.Fatalf("quota_snapshot should appear exactly twice: %s", body)
		}
	}
}

func TestListAuthFilesFromDiskQuotaSnapshot(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	file := `{"type":"claude","email":"a@b.c","access_token":"sk-ant-oat01-SECRET","setup_token_quota":{"captured_at":"2026-10-30T10:00:00Z","windows":{"five_hour":{"utilization":42,"resets_at":"2026-10-30T12:00:00Z"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "claude-a.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude-b.json"), []byte(`{"type":"claude","access_token":"sk-ant-oat01-SECRET"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, nil)
	body, files := listAuthFilesRaw(t, h, false)
	if strings.Contains(body, "SECRET") {
		t.Fatal("list response leaks credential material")
	}
	got := map[string]*quotaSnapshotPayload{}
	for _, f := range files {
		got[f.Name] = f.QuotaSnapshot
	}
	a := got["claude-a.json"]
	if a == nil || a.Source != "passive" || a.CapturedAt != "2026-10-30T10:00:00Z" || a.Windows["five_hour"].Utilization != 42 {
		t.Fatalf("disk snapshot = %+v", a)
	}
	if got["claude-b.json"] != nil {
		t.Fatal("quota_snapshot must be omitted without data")
	}
}
