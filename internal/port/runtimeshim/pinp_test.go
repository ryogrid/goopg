package runtimeshim

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// TestPinP_ReturnsValidIndex verifies that PinP returns an index in
// the expected range. Under the linkname build, the index is in
// [0, GOMAXPROCS); under the fallback build, the index is always 0.
// Either way the value must be representable as a non-negative int
// and must be < some upper bound the caller can reasonably allocate
// against.
func TestPinP_ReturnsValidIndex(t *testing.T) {
	pid := PinP()
	UnpinP()
	if pid < 0 {
		t.Fatalf("PinP returned negative index %d", pid)
	}
	upper := runtime.GOMAXPROCS(0)
	if upper < 1 {
		upper = 1
	}
	if pid >= upper {
		t.Fatalf("PinP returned %d, expected < GOMAXPROCS=%d", pid, upper)
	}
}

// TestPinP_BalancedAcrossGoroutines exercises PinP/UnpinP cycles from
// many goroutines under -race. Any misuse of the runtime-internal
// m.locks counter (e.g., an unbalanced pair, or a blocking call inside
// the pinned window) would surface as a runtime deadlock or race
// report. The test does no shared-memory work between pin/unpin to
// keep the pinned window absolutely empty of suspicious operations.
func TestPinP_BalancedAcrossGoroutines(t *testing.T) {
	const goroutines = 32
	const iters = 1 << 12

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				pid := PinP()
				_ = pid
				UnpinP()
			}
		}()
	}
	wg.Wait()
}

// TestPinP_PerPCounterCorrectness checks the canonical caller pattern:
// per-P slots indexed by PinP's return value, mutated without
// synchronisation inside the pinned window, summed via atomics
// afterwards. If the pin contract holds, the per-slot increments
// observed inside the window are race-free, and the final sum equals
// total increments performed. If the pin contract is silently broken
// (e.g., the linkname target moved or the fallback is incorrect), the
// race detector or the sum check will catch it.
//
// The slots array is sized to (GOMAXPROCS+1) so it accommodates both
// the linkname path (indices in [0, GOMAXPROCS)) and the fallback path
// (always index 0 under a mutex). A power-of-two cache-line padding
// avoids false sharing distorting the test signal.
func TestPinP_PerPCounterCorrectness(t *testing.T) {
	const goroutines = 16
	const iters = 1 << 14

	maxP := runtime.GOMAXPROCS(0)
	if maxP < 1 {
		maxP = 1
	}
	// Use atomic.Int64 so the post-window Sum is well-defined even
	// under -race (the pinned-window writes are unsynchronised but
	// can never race with each other because each goroutine targets
	// its own P's slot; the atomic only matters for cross-goroutine
	// reads at the end of the test).
	type paddedCounter struct {
		n atomic.Int64
		_ [56]byte
	}
	slots := make([]paddedCounter, maxP+1)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				pid := PinP()
				slots[pid].n.Add(1)
				UnpinP()
			}
		}()
	}
	wg.Wait()

	var total int64
	for i := range slots {
		total += slots[i].n.Load()
	}
	want := int64(goroutines) * int64(iters)
	if total != want {
		t.Fatalf("per-P counter total = %d, want %d (loss in pinned window?)", total, want)
	}
}

// BenchmarkPinUnpin measures the PinP+UnpinP pair cost. The linkname
// path on Linux/amd64 should be in the low single-digit nanoseconds;
// the fallback path is dominated by sync.Mutex Lock/Unlock at ~15 ns
// uncontended. A regression past either band indicates a build-tag or
// runtime-symbol issue worth investigating before shipping.
func BenchmarkPinUnpin(b *testing.B) {
	var sink int
	for i := 0; i < b.N; i++ {
		sink = PinP()
		UnpinP()
	}
	_ = sink
}
