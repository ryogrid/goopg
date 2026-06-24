package server

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestCancelByPID exercises the privileged, no-secret cancel path behind the
// pg_cancel_backend(pid) SQL function (M0118-0008). It must: return false for
// an unknown pid; return true for a registered-but-idle backend without firing
// anything; and return true AND fire the active query's cancel func for a busy
// backend.
func TestCancelByPID(t *testing.T) {
	r := newCancelRegistry()

	// Unknown pid → false, no panic.
	if r.cancelByPID(9999) {
		t.Fatalf("cancelByPID(unknown) = true, want false")
	}

	// Registered but idle (no active query) → true, nothing fired.
	const idlePID = 4242
	r.register(idlePID, 0xdead)
	defer r.unregister(idlePID)
	if !r.cancelByPID(idlePID) {
		t.Fatalf("cancelByPID(idle registered) = false, want true (backend exists)")
	}

	// Registered with an in-flight query → true, cancel func fired exactly once.
	const busyPID = 4243
	entry := r.register(busyPID, 0xbeef)
	defer r.unregister(busyPID)
	var fired atomic.Int32
	_, cancel := context.WithCancel(context.Background())
	entry.setQueryCancel(func() {
		fired.Add(1)
		cancel()
	})
	if !r.cancelByPID(busyPID) {
		t.Fatalf("cancelByPID(busy) = false, want true")
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("query cancel fired %d times, want 1", got)
	}

	// The secret key is irrelevant to this path: cancel succeeds without it.
	// (Contrast with cancelQuery, which requires a matching secret.)
}
