package auth

import (
	"strings"
	"sync"
	"time"
)

// SessionRoutingPolicy steers session-affinity placement for one credential.
//
// ExcludeNew keeps the credential out of new session bindings while sessions already bound to it
// stay put. ForceRebind additionally moves already-bound sessions to another credential on their
// next request. Both fail open: when every candidate is excluded the normal pick is used, so a
// policy can never make a request unroutable. A zero Until never expires.
type SessionRoutingPolicy struct {
	ExcludeNew  bool
	ForceRebind bool
	Until       time.Time
	Reason      string
}

func (p SessionRoutingPolicy) empty() bool {
	return !p.ExcludeNew && !p.ForceRebind
}

func (p SessionRoutingPolicy) activeAt(now time.Time) bool {
	if p.empty() {
		return false
	}
	return p.Until.IsZero() || now.Before(p.Until)
}

type sessionRoutingPolicies struct {
	mu       sync.RWMutex
	policies map[string]SessionRoutingPolicy
}

func (s *sessionRoutingPolicies) set(authID string, policy SessionRoutingPolicy) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if policy.ForceRebind {
		policy.ExcludeNew = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if policy.empty() {
		delete(s.policies, authID)
		return
	}
	if s.policies == nil {
		s.policies = make(map[string]SessionRoutingPolicy)
	}
	s.policies[authID] = policy
}

func (s *sessionRoutingPolicies) clear(authID string) {
	s.mu.Lock()
	delete(s.policies, strings.TrimSpace(authID))
	s.mu.Unlock()
}

func (s *sessionRoutingPolicies) get(authID string, now time.Time) (SessionRoutingPolicy, bool) {
	s.mu.RLock()
	policy, ok := s.policies[authID]
	s.mu.RUnlock()
	if !ok {
		return SessionRoutingPolicy{}, false
	}
	if !policy.activeAt(now) {
		s.mu.Lock()
		if current, still := s.policies[authID]; still && !current.activeAt(now) {
			delete(s.policies, authID)
		}
		s.mu.Unlock()
		return SessionRoutingPolicy{}, false
	}
	return policy, true
}

func (s *sessionRoutingPolicies) hasAny() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.policies) > 0
}

// SetSessionRoutingPolicy installs or replaces the policy for one credential. An empty policy clears it.
func (s *SessionAffinitySelector) SetSessionRoutingPolicy(authID string, policy SessionRoutingPolicy) {
	if s == nil {
		return
	}
	s.routingPolicies.set(authID, policy)
}

// ClearSessionRoutingPolicy removes the policy for one credential.
func (s *SessionAffinitySelector) ClearSessionRoutingPolicy(authID string) {
	if s == nil {
		return
	}
	s.routingPolicies.clear(authID)
}

// SessionRoutingPolicyFor reports the active policy for one credential.
func (s *SessionAffinitySelector) SessionRoutingPolicyFor(authID string, now time.Time) (SessionRoutingPolicy, bool) {
	if s == nil {
		return SessionRoutingPolicy{}, false
	}
	return s.routingPolicies.get(authID, now)
}

func (s *SessionAffinitySelector) forceRebindActive(authID string, now time.Time) bool {
	if s == nil || !s.routingPolicies.hasAny() {
		return false
	}
	policy, ok := s.routingPolicies.get(authID, now)
	return ok && policy.ForceRebind
}

// newBindingCandidates drops credentials excluded from new sessions, falling back to the full
// list when nothing would remain.
func (s *SessionAffinitySelector) newBindingCandidates(auths []*Auth, now time.Time) []*Auth {
	if s == nil || len(auths) == 0 || !s.routingPolicies.hasAny() {
		return auths
	}
	filtered := make([]*Auth, 0, len(auths))
	for _, candidate := range auths {
		if candidate == nil {
			continue
		}
		if policy, ok := s.routingPolicies.get(candidate.ID, now); ok && policy.ExcludeNew {
			continue
		}
		filtered = append(filtered, candidate)
	}
	if len(filtered) == 0 {
		return auths
	}
	return filtered
}

// rebindCandidates is newBindingCandidates minus the credential being moved away from. It returns
// nil when no alternative exists, so the caller keeps the current binding.
func (s *SessionAffinitySelector) rebindCandidates(auths []*Auth, fromAuthID string, now time.Time) []*Auth {
	candidates := s.newBindingCandidates(auths, now)
	out := make([]*Auth, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil && candidate.ID != fromAuthID {
			out = append(out, candidate)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Manager) sessionAffinitySelector() *SessionAffinitySelector {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	scheduler := m.pluginScheduler
	selector := m.selector
	m.mu.RUnlock()
	// The plugin host is always wired in as the scheduler; it only takes over
	// selection when a plugin actually registered one.
	if scheduler != nil {
		if state, ok := scheduler.(pluginSchedulerState); !ok || state.HasScheduler() {
			return nil
		}
	}
	sel, _ := selector.(*SessionAffinitySelector)
	return sel
}

// SetSessionRoutingPolicy installs a policy on the session-affinity selector. It reports false when
// session affinity is not the active selector, in which case nothing is stored.
func (m *Manager) SetSessionRoutingPolicy(authID string, policy SessionRoutingPolicy) bool {
	sel := m.sessionAffinitySelector()
	if sel == nil {
		return false
	}
	sel.SetSessionRoutingPolicy(authID, policy)
	return true
}

// ClearSessionRoutingPolicy removes a policy. It reports false when session affinity is inactive.
func (m *Manager) ClearSessionRoutingPolicy(authID string) bool {
	sel := m.sessionAffinitySelector()
	if sel == nil {
		return false
	}
	sel.ClearSessionRoutingPolicy(authID)
	return true
}

// SessionRoutingPolicyFor reports the active policy for one credential.
func (m *Manager) SessionRoutingPolicyFor(authID string) (SessionRoutingPolicy, bool) {
	sel := m.sessionAffinitySelector()
	if sel == nil {
		return SessionRoutingPolicy{}, false
	}
	return sel.SessionRoutingPolicyFor(authID, time.Now())
}
