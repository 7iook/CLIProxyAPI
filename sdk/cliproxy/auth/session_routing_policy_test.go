package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func routingPolicyOpts(sessionID string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{sessionID}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
}

func mustPick(t *testing.T, s *SessionAffinitySelector, sessionID string, auths []*Auth) string {
	t.Helper()
	got, err := s.Pick(context.Background(), "claude", "claude-opus", routingPolicyOpts(sessionID), auths)
	if err != nil {
		t.Fatalf("Pick(%s) error = %v", sessionID, err)
	}
	if got == nil {
		t.Fatalf("Pick(%s) returned nil", sessionID)
	}
	return got.ID
}

func TestSessionRoutingPolicy_ExcludeNewKeepsExistingBinding(t *testing.T) {
	t.Parallel()
	s := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer s.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	bound := mustPick(t, s, "sess-existing", auths)
	s.SetSessionRoutingPolicy(bound, SessionRoutingPolicy{ExcludeNew: true})

	for i := 0; i < 5; i++ {
		if got := mustPick(t, s, "sess-existing", auths); got != bound {
			t.Fatalf("existing session moved to %s, want %s (exclude_new must not rebind)", got, bound)
		}
	}
}

func TestSessionRoutingPolicy_ExcludeNewSkipsAccountForNewSessions(t *testing.T) {
	t.Parallel()
	s := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer s.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}, {ID: "auth-c"}}

	s.SetSessionRoutingPolicy("auth-a", SessionRoutingPolicy{ExcludeNew: true})
	for i := 0; i < 12; i++ {
		if got := mustPick(t, s, "sess-new-"+string(rune('a'+i)), auths); got == "auth-a" {
			t.Fatalf("new session %d bound to excluded auth-a", i)
		}
	}
}

func TestSessionRoutingPolicy_AllExcludedFailsOpen(t *testing.T) {
	t.Parallel()
	s := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer s.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	s.SetSessionRoutingPolicy("auth-a", SessionRoutingPolicy{ExcludeNew: true})
	s.SetSessionRoutingPolicy("auth-b", SessionRoutingPolicy{ExcludeNew: true})

	if got := mustPick(t, s, "sess-all-excluded", auths); got != "auth-a" && got != "auth-b" {
		t.Fatalf("unexpected auth %s", got)
	}
}

func TestSessionRoutingPolicy_ForceRebindMovesExistingSession(t *testing.T) {
	t.Parallel()
	s := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer s.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}

	bound := mustPick(t, s, "sess-migrate", auths)
	s.SetSessionRoutingPolicy(bound, SessionRoutingPolicy{ExcludeNew: true, ForceRebind: true})

	moved := mustPick(t, s, "sess-migrate", auths)
	if moved == bound {
		t.Fatalf("force_rebind kept session on %s", bound)
	}
	// The new binding sticks even after the policy is cleared.
	s.ClearSessionRoutingPolicy(bound)
	if got := mustPick(t, s, "sess-migrate", auths); got != moved {
		t.Fatalf("rebound session drifted to %s, want %s", got, moved)
	}
}

func TestSessionRoutingPolicy_ForceRebindWithNoAlternativeStays(t *testing.T) {
	t.Parallel()
	s := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer s.Stop()
	auths := []*Auth{{ID: "auth-a"}}

	mustPick(t, s, "sess-solo", auths)
	s.SetSessionRoutingPolicy("auth-a", SessionRoutingPolicy{ExcludeNew: true, ForceRebind: true})
	if got := mustPick(t, s, "sess-solo", auths); got != "auth-a" {
		t.Fatalf("got %s, want auth-a (fail open when no alternative)", got)
	}
}

func TestSessionRoutingPolicy_ExpiresAndClears(t *testing.T) {
	t.Parallel()
	s := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer s.Stop()

	s.SetSessionRoutingPolicy("auth-a", SessionRoutingPolicy{ExcludeNew: true, Until: time.Now().Add(-time.Second)})
	if _, ok := s.SessionRoutingPolicyFor("auth-a", time.Now()); ok {
		t.Fatalf("expired policy still active")
	}
	s.SetSessionRoutingPolicy("auth-b", SessionRoutingPolicy{ForceRebind: true, Until: time.Now().Add(time.Hour)})
	if p, ok := s.SessionRoutingPolicyFor("auth-b", time.Now()); !ok || !p.ForceRebind {
		t.Fatalf("active policy missing: %+v ok=%v", p, ok)
	}
	s.ClearSessionRoutingPolicy("auth-b")
	if _, ok := s.SessionRoutingPolicyFor("auth-b", time.Now()); ok {
		t.Fatalf("cleared policy still active")
	}
	// A policy with no flags behaves as a clear.
	s.SetSessionRoutingPolicy("auth-c", SessionRoutingPolicy{})
	if _, ok := s.SessionRoutingPolicyFor("auth-c", time.Now()); ok {
		t.Fatalf("empty policy should not be stored")
	}
}

func TestSessionRoutingPolicy_NoPolicyUnchangedBehaviour(t *testing.T) {
	t.Parallel()
	s := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer s.Stop()
	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	first := mustPick(t, s, "sess-plain", auths)
	for i := 0; i < 5; i++ {
		if got := mustPick(t, s, "sess-plain", auths); got != first {
			t.Fatalf("binding changed without policy")
		}
	}
}

func TestManagerSetSessionRoutingPolicy(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, NewSessionAffinitySelector(&RoundRobinSelector{}), nil)
	if !m.SetSessionRoutingPolicy("auth-a", SessionRoutingPolicy{ExcludeNew: true}) {
		t.Fatalf("manager with affinity selector should support policies")
	}
	if p, ok := m.SessionRoutingPolicyFor("auth-a"); !ok || !p.ExcludeNew {
		t.Fatalf("policy not visible through manager")
	}
	plain := NewManager(nil, &RoundRobinSelector{}, nil)
	if plain.SetSessionRoutingPolicy("auth-a", SessionRoutingPolicy{ExcludeNew: true}) {
		t.Fatalf("manager without affinity selector must report unsupported")
	}
}
