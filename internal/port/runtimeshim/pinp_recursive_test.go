//go:build go1.24 && !go1.27 && !noLinkname

package runtimeshim

import "testing"

// TestPinP_StableWithinWindow verifies nested PinP/UnpinP pairs against
// the runtime's sync.runtime_procPin contract — each pin increments
// m.locks, each unpin decrements it, and the returned P index is
// identical for every nested call because the runtime cannot migrate
// the goroutine to a different P while any pin is active.
//
// This test is gated to the linkname build (matching pinp_linkname.go)
// because the fallback path's PinP locks a global sync.Mutex
// (pinp_fallback.go); calling PinP twice from the same goroutine on
// the fallback path deadlocks on the second Lock. The fallback's
// non-recursion contract is documented at the call site; goopg's
// production callers never nest PinP, so the divergence is not
// observable in service.
func TestPinP_StableWithinWindow(t *testing.T) {
	pid1 := PinP()
	pid2 := PinP()
	if pid1 != pid2 {
		UnpinP()
		UnpinP()
		t.Fatalf("nested PinP returned different P indices: outer=%d inner=%d", pid1, pid2)
	}
	UnpinP()
	UnpinP()
}
