//go:build !go1.24 || go1.27 || noLinkname

package runtimeshim

import "sync"

// fallbackPinMu serialises all PinP/UnpinP pairs when the linkname
// target is not available. The fallback collapses the per-P sharding
// to a single virtual P (index 0): correctness is preserved (mutual
// exclusion holds for every operation that would have relied on the
// no-preemption invariant), but contention degrades to that of a
// global mutex. The fallback is intentionally slow — its purpose is
// to keep goopg correct on Go minors outside the tested window, not
// to match linkname-path performance.
var fallbackPinMu sync.Mutex

// PinP acquires the fallback pin mutex and returns 0. Per-P arrays
// indexed with the returned value should therefore size their backing
// storage to accommodate index 0 (i.e., length >= 1).
//
// Non-recursion contract: the fallback's mutex is non-reentrant.
// Calling PinP a second time from the same goroutine before UnpinP
// deadlocks. The linkname path supports nested PinP via the runtime's
// m.locks counter, but goopg's production callers do not nest, so the
// fallback's stricter contract is acceptable. The recursion-shape
// regression test for the linkname path lives in
// pinp_recursive_test.go (linkname-tag-gated).
func PinP() int {
	fallbackPinMu.Lock()
	return 0
}

// UnpinP releases the fallback pin mutex.
func UnpinP() {
	fallbackPinMu.Unlock()
}
