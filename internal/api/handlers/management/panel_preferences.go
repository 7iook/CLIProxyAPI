package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Panel preferences are presentational only. They are stored in their own file
// next to the config file so that frequent writes never touch config.yaml,
// trigger a reload, or reach auth persistence.

const (
	panelPreferencesFileName = ".cpa-panel-preferences"
	panelPreferencesMaxBytes = 64 * 1024
)

type panelPreferencesEnvelope struct {
	Revision    int64           `json:"revision"`
	UpdatedAt   string          `json:"updated_at"`
	Preferences json.RawMessage `json:"preferences"`
}

func (h *Handler) panelPreferencesPath() string {
	if strings.TrimSpace(h.configFilePath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(h.configFilePath), panelPreferencesFileName)
}

// loadPanelPreferencesLocked reads the stored envelope. A missing or corrupt
// file is treated as the empty document (revision 0). Caller holds panelPrefsMu.
func (h *Handler) loadPanelPreferencesLocked(path string) panelPreferencesEnvelope {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if !errors.Is(errRead, os.ErrNotExist) {
			log.Warnf("panel preferences: failed to read %s: %v", path, errRead)
		}
		return panelPreferencesEnvelope{}
	}
	var env panelPreferencesEnvelope
	if errParse := json.Unmarshal(data, &env); errParse != nil || env.Revision < 1 || !validPanelPreferencesObject(env.Preferences) {
		log.Warnf("panel preferences: ignoring corrupt file %s", path)
		return panelPreferencesEnvelope{}
	}
	return env
}

func panelPreferencesResponse(env panelPreferencesEnvelope) gin.H {
	var prefs any
	var updatedAt any
	if env.Revision > 0 {
		prefs = env.Preferences
		updatedAt = env.UpdatedAt
	}
	return gin.H{
		"preferences": prefs,
		"revision":    strconv.FormatInt(env.Revision, 10),
		"updated_at":  updatedAt,
	}
}

// validPanelPreferencesObject reports whether raw is a JSON object holding an
// integer "version" >= 1. The rest of the document is opaque.
func validPanelPreferencesObject(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if errParse := json.Unmarshal(raw, &obj); errParse != nil || obj == nil {
		return false
	}
	version, ok := obj["version"]
	if !ok {
		return false
	}
	n, errInt := strconv.ParseInt(string(bytes.TrimSpace(version)), 10, 64)
	return errInt == nil && n >= 1
}

// GetPanelPreferences returns the shared panel display preferences document.
func (h *Handler) GetPanelPreferences(c *gin.Context) {
	path := h.panelPreferencesPath()
	if path == "" {
		c.JSON(http.StatusOK, panelPreferencesResponse(panelPreferencesEnvelope{}))
		return
	}
	h.panelPrefsMu.Lock()
	env := h.loadPanelPreferencesLocked(path)
	h.panelPrefsMu.Unlock()
	c.JSON(http.StatusOK, panelPreferencesResponse(env))
}

// PutPanelPreferences replaces the preferences document using optimistic
// concurrency on base_revision.
func (h *Handler) PutPanelPreferences(c *gin.Context) {
	path := h.panelPreferencesPath()
	if path == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "panel preferences storage is unavailable: config file path is not set"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, panelPreferencesMaxBytes)
	raw, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(errRead, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}
	var body struct {
		Preferences  json.RawMessage `json:"preferences"`
		BaseRevision *string         `json:"base_revision"`
	}
	if errParse := json.Unmarshal(raw, &body); errParse != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if body.BaseRevision == nil || strings.TrimSpace(*body.BaseRevision) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "base_revision is required"})
		return
	}
	if !validPanelPreferencesObject(body.Preferences) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "preferences must be a JSON object with an integer version >= 1"})
		return
	}
	var compact bytes.Buffer
	if errCompact := json.Compact(&compact, body.Preferences); errCompact != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid preferences"})
		return
	}

	h.panelPrefsMu.Lock()
	defer h.panelPrefsMu.Unlock()

	current := h.loadPanelPreferencesLocked(path)
	if strconv.FormatInt(current.Revision, 10) != strings.TrimSpace(*body.BaseRevision) {
		resp := panelPreferencesResponse(current)
		resp["error"] = "revision conflict"
		c.JSON(http.StatusConflict, resp)
		return
	}

	next := panelPreferencesEnvelope{
		Revision:    current.Revision + 1,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		Preferences: json.RawMessage(compact.Bytes()),
	}
	if errWrite := writePanelPreferencesFile(path, next); errWrite != nil {
		log.Errorf("panel preferences: failed to save: %v", errWrite)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save panel preferences"})
		return
	}
	c.JSON(http.StatusOK, panelPreferencesResponse(next))
}

// writePanelPreferencesFile writes the envelope atomically with mode 0600.
func writePanelPreferencesFile(path string, env panelPreferencesEnvelope) error {
	data, errMarshal := json.Marshal(env)
	if errMarshal != nil {
		return fmt.Errorf("marshal envelope: %w", errMarshal)
	}
	tmp, errCreate := os.CreateTemp(filepath.Dir(path), panelPreferencesFileName+".tmp-*")
	if errCreate != nil {
		return fmt.Errorf("create temp file: %w", errCreate)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		if errRemove := os.Remove(tmpName); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warnf("panel preferences: failed to remove temp file %s: %v", tmpName, errRemove)
		}
	}
	if errChmod := tmp.Chmod(0o600); errChmod != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", errChmod)
	}
	if _, errWrite := tmp.Write(data); errWrite != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", errWrite)
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp file: %w", errSync)
	}
	if errClose := tmp.Close(); errClose != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", errClose)
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		cleanup()
		return fmt.Errorf("rename temp file: %w", errRename)
	}
	return nil
}
