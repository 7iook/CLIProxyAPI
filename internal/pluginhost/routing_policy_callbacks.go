package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// callHostRoutingSetSessionPolicy installs or clears a session-affinity routing policy for one
// credential. It never touches token files or persisted auth state.
func (h *Host) callHostRoutingSetSessionPolicy(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostRoutingSetSessionPolicyRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host routing set session policy request: %w", errUnmarshal)
	}
	auth, errResolve := h.resolveRoutingPolicyAuth(req.AuthIndex, req.AuthID)
	if errResolve != nil {
		return nil, errResolve
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return nil, fmt.Errorf("core auth manager unavailable")
	}
	auth.EnsureIndex()

	policy := coreauth.SessionRoutingPolicy{
		ExcludeNew:  req.ExcludeNew,
		ForceRebind: req.ForceRebind,
		Reason:      strings.TrimSpace(req.Reason),
	}
	if req.Until != nil {
		policy.Until = req.Until.UTC()
	}
	var supported bool
	if req.Clear {
		supported = manager.ClearSessionRoutingPolicy(auth.ID)
	} else {
		supported = manager.SetSessionRoutingPolicy(auth.ID, policy)
	}

	resp := pluginapi.HostRoutingSetSessionPolicyResponse{
		Supported: supported,
		AuthIndex: auth.Index,
		AuthID:    auth.ID,
	}
	if supported {
		if active, ok := manager.SessionRoutingPolicyFor(auth.ID); ok {
			resp.Active = true
			resp.ExcludeNew = active.ExcludeNew
			resp.ForceRebind = active.ForceRebind
			if !active.Until.IsZero() {
				until := active.Until.UTC()
				resp.Until = &until
			}
		}
	}
	log.WithFields(log.Fields{
		"plugin_id":    hostCallbackPluginIDFromContext(ctx),
		"auth_index":   auth.Index,
		"supported":    supported,
		"active":       resp.Active,
		"exclude_new":  resp.ExcludeNew,
		"force_rebind": resp.ForceRebind,
		"reason":       policy.Reason,
	}).Info("pluginhost: plugin set session routing policy")
	return marshalRPCResult(resp)
}

func (h *Host) resolveRoutingPolicyAuth(authIndex, authID string) (*coreauth.Auth, error) {
	authIndex = strings.TrimSpace(authIndex)
	authID = strings.TrimSpace(authID)
	switch {
	case authIndex != "" && authID != "":
		return nil, fmt.Errorf("auth_index and auth_id are mutually exclusive")
	case authIndex != "":
		return h.authByIndex(authIndex)
	case authID != "":
		manager := h.currentAuthManager()
		if manager == nil {
			return nil, fmt.Errorf("core auth manager unavailable")
		}
		auth, ok := manager.GetByID(authID)
		if !ok || auth == nil {
			return nil, fmt.Errorf("auth not found for auth_id")
		}
		return auth, nil
	default:
		return nil, fmt.Errorf("auth_index or auth_id is required")
	}
}
