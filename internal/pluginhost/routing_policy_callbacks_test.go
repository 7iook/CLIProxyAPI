package pluginhost

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func newRoutingPolicyHost(t *testing.T, selector coreauth.Selector) (*Host, *coreauth.Manager, *coreauth.Auth) {
	t.Helper()
	auth := &coreauth.Auth{ID: "claude-a.json", Provider: "claude", FileName: "claude-a.json", Status: coreauth.StatusActive}
	auth.EnsureIndex()
	host := New()
	manager := coreauth.NewManager(nil, selector, nil)
	host.SetAuthManager(manager)
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return host, manager, auth
}

func callSetSessionPolicy(t *testing.T, host *Host, req pluginapi.HostRoutingSetSessionPolicyRequest) (pluginapi.HostRoutingSetSessionPolicyResponse, error) {
	t.Helper()
	payload, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	raw, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostRoutingSetSessionPolicy, payload)
	if errCall != nil {
		return pluginapi.HostRoutingSetSessionPolicyResponse{}, errCall
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostRoutingSetSessionPolicyResponse](raw)
	if errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	return resp, nil
}

func TestHostRoutingSetSessionPolicyInstallsAndClears(t *testing.T) {
	host, manager, auth := newRoutingPolicyHost(t, coreauth.NewSessionAffinitySelector(&coreauth.RoundRobinSelector{}))
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	resp, err := callSetSessionPolicy(t, host, pluginapi.HostRoutingSetSessionPolicyRequest{AuthIndex: auth.Index, ExcludeNew: true, Until: &until, Reason: "5h at line"})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !resp.Supported || !resp.Active || !resp.ExcludeNew || resp.ForceRebind || resp.Until == nil || !resp.Until.Equal(until) {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.AuthIndex != auth.Index || resp.AuthID != auth.ID {
		t.Fatalf("identity = %s/%s", resp.AuthIndex, resp.AuthID)
	}
	if p, ok := manager.SessionRoutingPolicyFor(auth.ID); !ok || !p.ExcludeNew {
		t.Fatalf("policy not stored")
	}

	resp, err = callSetSessionPolicy(t, host, pluginapi.HostRoutingSetSessionPolicyRequest{AuthID: auth.ID, ForceRebind: true})
	if err != nil || !resp.Active || !resp.ExcludeNew || !resp.ForceRebind {
		t.Fatalf("force rebind resp = %+v err=%v (force_rebind implies exclude_new)", resp, err)
	}

	resp, err = callSetSessionPolicy(t, host, pluginapi.HostRoutingSetSessionPolicyRequest{AuthIndex: auth.Index, Clear: true})
	if err != nil || !resp.Supported || resp.Active {
		t.Fatalf("clear resp = %+v err=%v", resp, err)
	}
	if _, ok := manager.SessionRoutingPolicyFor(auth.ID); ok {
		t.Fatalf("policy survived clear")
	}
}

func TestHostRoutingSetSessionPolicyUnsupportedWithoutAffinity(t *testing.T) {
	host, _, auth := newRoutingPolicyHost(t, &coreauth.RoundRobinSelector{})
	resp, err := callSetSessionPolicy(t, host, pluginapi.HostRoutingSetSessionPolicyRequest{AuthIndex: auth.Index, ExcludeNew: true})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if resp.Supported || resp.Active {
		t.Fatalf("resp = %+v, want unsupported and inactive", resp)
	}
}

func TestHostRoutingSetSessionPolicyRejectsBadIdentity(t *testing.T) {
	host, _, auth := newRoutingPolicyHost(t, coreauth.NewSessionAffinitySelector(&coreauth.RoundRobinSelector{}))
	cases := []pluginapi.HostRoutingSetSessionPolicyRequest{
		{ExcludeNew: true},
		{AuthIndex: "missing", ExcludeNew: true},
		{AuthID: "missing.json", ExcludeNew: true},
		{AuthIndex: auth.Index, AuthID: auth.ID, ExcludeNew: true},
	}
	for i, req := range cases {
		if _, err := callSetSessionPolicy(t, host, req); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
	if _, err := host.callFromPlugin(context.Background(), pluginabi.MethodHostRoutingSetSessionPolicy, []byte("{bad")); err == nil {
		t.Fatalf("invalid json accepted")
	}
}
