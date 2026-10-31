package management

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Account groups are display-only: the registry lives in config and membership
// lives in credential metadata under "ui". Nothing here feeds credential
// selection, priority, weight or credential_group routing.

const (
	authMetadataUIKey      = "ui"
	authMetadataUIGroupKey = "group"
	authMetadataUIOrderKey = "order"
	maxUIGroupEchoRunes    = 64
)

func accountGroupsPayload(groups []config.AccountGroup) []config.AccountGroup {
	out := make([]config.AccountGroup, len(groups))
	copy(out, groups)
	return out
}

// GetAccountGroups returns the ordered account group registry.
func (h *Handler) GetAccountGroups(c *gin.Context) {
	h.mu.Lock()
	groups := accountGroupsPayload(h.cfg.AccountGroups)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"groups": groups})
}

// PutAccountGroups replaces the whole ordered group registry and un-assigns
// credentials that reference groups which no longer exist.
func (h *Handler) PutAccountGroups(c *gin.Context) {
	var body struct {
		Groups *[]config.AccountGroup `json:"groups"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.Groups == nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errBind, http.StatusBadRequest), gin.H{"error": "invalid request body: groups is required"})
		return
	}
	groups, errNormalize := config.NormalizeAccountGroups(*body.Groups)
	if errNormalize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNormalize.Error()})
		return
	}

	h.mu.Lock()
	previous := h.cfg.AccountGroups
	h.cfg.AccountGroups = groups
	cfgSnapshot, okSnapshot := h.saveConfigAndSnapshotLocked(c)
	if !okSnapshot {
		h.cfg.AccountGroups = previous
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), cfgSnapshot)

	kept := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		kept[g.ID] = struct{}{}
	}
	cleared := h.clearUnknownAccountGroups(c, kept)
	c.JSON(http.StatusOK, gin.H{"groups": accountGroupsPayload(groups), "cleared": cleared})
}

func (h *Handler) clearUnknownAccountGroups(c *gin.Context, kept map[string]struct{}) int {
	if h.authManager == nil {
		return 0
	}
	ctx := c.Request.Context()
	cleared := 0
	for _, auth := range h.authManager.List() {
		if auth == nil || auth.AuthSourceKind() == coreauth.AuthSourceConfig || coreauth.IsPluginVirtualAuth(auth) {
			continue
		}
		id, _ := uiMetadataGroup(auth.Metadata)
		if id == "" {
			continue
		}
		if _, ok := kept[id]; ok {
			continue
		}
		updated, committed, errModify := h.authManager.Modify(ctx, auth.ID, auth.RegistrationEpoch, func(current *coreauth.Auth) error {
			setUIGroup(current, "")
			return nil
		})
		if errModify != nil || !committed || updated == nil {
			log.Warnf("account groups: failed to clear group on credential %s: %v", auth.ID, errModify)
			continue
		}
		if errHook := h.invokePostAuthPersistHooks(ctx, []*coreauth.Auth{updated}); errHook != nil {
			log.Warnf("account groups: post-auth persist hook failed for %s: %v", auth.ID, errHook)
		}
		cleared++
	}
	return cleared
}

type accountGroupAssignFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// AssignAccountGroup sets or clears the display group of the named credentials.
func (h *Handler) AssignAccountGroup(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var body struct {
		GroupID *string  `json:"group_id"`
		Names   []string `json:"names"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errBind, http.StatusBadRequest), gin.H{"error": "invalid request body"})
		return
	}
	groupID := ""
	if body.GroupID != nil {
		groupID = strings.TrimSpace(*body.GroupID)
	}
	if groupID != "" {
		h.mu.Lock()
		found := false
		for _, g := range h.cfg.AccountGroups {
			if g.ID == groupID {
				found = true
				break
			}
		}
		h.mu.Unlock()
		if !found {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown group id %q", groupID)})
			return
		}
	}

	ctx := c.Request.Context()
	updated := make([]string, 0, len(body.Names))
	failed := make([]accountGroupAssignFailure, 0)
	seen := make(map[string]struct{}, len(body.Names))
	for _, rawName := range body.Names {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		target, _ := h.lookupAuthFile(name, "")
		if target == nil || target.AuthSourceKind() == coreauth.AuthSourceConfig {
			failed = append(failed, accountGroupAssignFailure{Name: name, Error: "auth file not found"})
			continue
		}
		if coreauth.IsPluginVirtualAuth(target) {
			failed = append(failed, accountGroupAssignFailure{Name: name, Error: errPluginVirtualAuth.Error()})
			continue
		}
		saved, committed, errModify := h.authManager.Modify(ctx, target.ID, target.RegistrationEpoch, func(current *coreauth.Auth) error {
			setUIGroup(current, groupID)
			return nil
		})
		if errModify != nil {
			failed = append(failed, accountGroupAssignFailure{Name: name, Error: fmt.Sprintf("failed to update auth: %v", errModify)})
			continue
		}
		if !committed || saved == nil {
			failed = append(failed, accountGroupAssignFailure{Name: name, Error: "auth file not found"})
			continue
		}
		if errHook := h.invokePostAuthPersistHooks(ctx, []*coreauth.Auth{saved}); errHook != nil {
			failed = append(failed, accountGroupAssignFailure{Name: name, Error: fmt.Sprintf("post-auth persist hook failed: %v", errHook)})
			continue
		}
		updated = append(updated, name)
	}
	c.JSON(http.StatusOK, gin.H{"updated": updated, "failed": failed})
}

// uiMetadataGroup returns the trimmed ui.group string of a credential's metadata.
func uiMetadataGroup(metadata map[string]any) (string, bool) {
	ui, ok := metadata[authMetadataUIKey].(map[string]any)
	if !ok {
		return "", false
	}
	group, ok := ui[authMetadataUIGroupKey].(string)
	if !ok {
		return "", false
	}
	group = strings.TrimSpace(group)
	return group, group != ""
}

// setUIGroup writes or removes metadata ui.group, dropping an emptied ui map.
func setUIGroup(auth *coreauth.Auth, groupID string) {
	if auth.Metadata == nil {
		if groupID == "" {
			return
		}
		auth.Metadata = make(map[string]any)
	}
	ui, _ := auth.Metadata[authMetadataUIKey].(map[string]any)
	if groupID == "" {
		if ui == nil {
			return
		}
		delete(ui, authMetadataUIGroupKey)
		if len(ui) == 0 {
			delete(auth.Metadata, authMetadataUIKey)
		}
	} else {
		if ui == nil {
			ui = make(map[string]any)
		}
		ui[authMetadataUIGroupKey] = groupID
		auth.Metadata[authMetadataUIKey] = ui
	}
	auth.UpdatedAt = time.Now()
}

// uiEchoPayload returns the whitelisted ui object for list entries, or nil.
func uiEchoPayload(metadata map[string]any) map[string]any {
	ui, ok := metadata[authMetadataUIKey].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, 2)
	if group, ok := ui[authMetadataUIGroupKey].(string); ok {
		group = strings.TrimSpace(group)
		if group != "" && utf8.RuneCountInString(group) <= maxUIGroupEchoRunes {
			out[authMetadataUIGroupKey] = group
		}
	}
	if order, ok := finiteNumber(ui[authMetadataUIOrderKey]); ok {
		out[authMetadataUIOrderKey] = order
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func finiteNumber(value any) (float64, bool) {
	var f float64
	switch v := value.(type) {
	case float64:
		f = v
	case float32:
		f = float64(v)
	case int:
		f = float64(v)
	case int64:
		f = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// patchUIOrder sets metadata ui.order to a finite number, or deletes it for nil.
func patchUIOrder(metadata map[string]any, value any) error {
	ui, _ := metadata[authMetadataUIKey].(map[string]any)
	if value == nil {
		if ui != nil {
			delete(ui, authMetadataUIOrderKey)
			if len(ui) == 0 {
				delete(metadata, authMetadataUIKey)
			}
		}
		return nil
	}
	order, ok := finiteNumber(value)
	if !ok {
		return fmt.Errorf("ui.order must be a finite number or null")
	}
	if ui == nil {
		ui = make(map[string]any)
		metadata[authMetadataUIKey] = ui
	}
	ui[authMetadataUIOrderKey] = order
	return nil
}
