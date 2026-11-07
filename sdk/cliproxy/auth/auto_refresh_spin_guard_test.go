package auth

import (
	"context"
	"testing"
	"time"
)

// A refresh that ends without recording any outcome leaves the credential due.
// The loop must not redispatch it within the same interval (scheduler spin).
func TestAutoRefreshSpinGuardDefersImmediateRedispatch(t *testing.T) {
	executor := &countingRefreshExecutor{id: issue6199RefreshProvider}
	manager, loop := newIssue6199RefreshLoop(executor)
	now := time.Now()
	registerIssue6199ExpiredAuth(t, manager, "spin", now)
	loop.rebuild(now)
	loop.handleDue(context.Background(), now)
	if got := len(loop.jobs); got != 1 {
		t.Fatalf("first dispatch jobs = %d, want 1", got)
	}
	job := <-loop.jobs
	manager.finishRefreshJob(job, time.Time{}, false) // no outcome: still due

	soon := now.Add(time.Millisecond)
	loop.applyDirty(soon)
	for i := 0; i < 100; i++ {
		loop.handleDue(context.Background(), soon)
	}
	if got := len(loop.jobs); got != 0 {
		t.Fatalf("redispatched %d times within interval, want 0", got)
	}
	if next, ok := loop.peek(); !ok || !next.After(soon) {
		t.Fatalf("next check = %v (ok=%v), want after %v", next, ok, soon)
	}

	later := now.Add(loop.interval)
	loop.handleDue(context.Background(), later)
	if got := len(loop.jobs); got != 1 {
		t.Fatalf("dispatch after interval jobs = %d, want 1", got)
	}
}
