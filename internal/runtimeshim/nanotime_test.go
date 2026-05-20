package runtimeshim

import (
	"testing"
	"time"
)

// TestNanotime_Monotonic verifies that successive Nanotime() reads
// never decrease. Under the linkname build the underlying source is
// the runtime's monotonic clock; under the fallback build it is
// time.Now() which is also monotonic per the Go time package contract.
func TestNanotime_Monotonic(t *testing.T) {
	const iters = 1 << 16
	prev := Nanotime()
	for i := 0; i < iters; i++ {
		now := Nanotime()
		if now < prev {
			t.Fatalf("Nanotime went backwards: prev=%d now=%d (iter=%d)", prev, now, i)
		}
		prev = now
	}
}

// TestNanotime_ApproximatesWallElapsed verifies that a Nanotime delta
// is within an order of magnitude of a measured time.Sleep elapsed.
// The linkname source is a process-monotonic counter, NOT a wall
// clock, so the two numbers do not have to share an epoch — only the
// elapsed delta is comparable. A sleep of 50 ms should produce an
// elapsed delta between ~30 ms and ~500 ms even on a contended CI
// runner.
func TestNanotime_ApproximatesWallElapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping wall-elapsed comparison in -short mode")
	}
	const sleep = 50 * time.Millisecond
	start := Nanotime()
	time.Sleep(sleep)
	elapsed := time.Duration(Nanotime() - start)
	lower := sleep / 2  // generous to absorb CI jitter
	upper := sleep * 10 // generous to absorb scheduler pauses
	if elapsed < lower || elapsed > upper {
		t.Fatalf("Nanotime elapsed=%s outside [%s, %s] for time.Sleep(%s)",
			elapsed, lower, upper, sleep)
	}
}

// TestNanotime_NonZero is a smoke test against an accidentally-stubbed
// shim that returns 0 (e.g., a build-tag misalignment where the
// linkname site failed to bind).
func TestNanotime_NonZero(t *testing.T) {
	if got := Nanotime(); got == 0 {
		t.Fatalf("Nanotime() == 0; build-tag misalignment?")
	}
}

// BenchmarkNanotime measures the call cost so future regressions in
// the build-tag window are visible. On Linux/amd64 the linkname path
// should be in the single-digit-nanosecond range; the fallback path
// is dominated by time.Now() at ~50 ns.
func BenchmarkNanotime(b *testing.B) {
	var sink int64
	for i := 0; i < b.N; i++ {
		sink = Nanotime()
	}
	_ = sink
}
