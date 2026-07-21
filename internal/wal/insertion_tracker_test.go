package wal

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// Verifies the zero-state contract used elsewhere in the file: a fresh
// tracker reports every stripe as idle and lowestActiveLSN returns the
// "no active" sentinel. Without this the constructor could silently
// drift to a non-zero seed (e.g., math.MaxInt64-everywhere) that breaks
// the min(upperBound, lowestActive) publication formula.
func TestInsertionTrackerNewIsAllIdle(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	for i := 0; i < appendLockStripes; i++ {
		if got := tr.insertingAt(i); got != lsnIdle {
			t.Errorf("stripe %d: got %d, want lsnIdle (%d)", i, got, lsnIdle)
		}
	}
	if got := tr.lowestActiveLSN(); got != lsnNoActive {
		t.Errorf("lowestActiveLSN on fresh tracker: got %d, want lsnNoActive (%d)", got, lsnNoActive)
	}
	if lsnNoActive != int64(math.MaxInt64) {
		t.Errorf("lsnNoActive sentinel changed: got %d, want math.MaxInt64", lsnNoActive)
	}
}

// Pins the read-after-write semantics on a single slot. A regression
// here would mean a publication walker reads stale (zero) state even
// after the stripe has marked itself active — the very bug the
// atomic-int storage was introduced to prevent.
func TestInsertionTrackerSetReadback(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	tr.setInsertingAt(3, 0xDEAD_BEEF)
	if got := tr.insertingAt(3); got != 0xDEAD_BEEF {
		t.Errorf("after Set, Get: got %d, want 0xDEAD_BEEF", got)
	}
	// Other stripes untouched.
	for i := 0; i < appendLockStripes; i++ {
		if i == 3 {
			continue
		}
		if got := tr.insertingAt(i); got != lsnIdle {
			t.Errorf("stripe %d after writing only stripe 3: got %d, want lsnIdle", i, got)
		}
	}
}

// Round-trips a set/idle cycle on every stripe so the "go-idle" path
// doesn't accidentally pin the slot at the last-written value. Mirrors
// the per-record contract setInsertingAt(start) → setInsertingAt(idle).
func TestInsertionTrackerSetThenIdleClears(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	for i := 0; i < appendLockStripes; i++ {
		tr.setInsertingAt(i, int64(100+i))
		if got := tr.insertingAt(i); got != int64(100+i) {
			t.Errorf("stripe %d after Set(%d): got %d", i, 100+i, got)
		}
		tr.setInsertingAt(i, lsnIdle)
		if got := tr.insertingAt(i); got != lsnIdle {
			t.Errorf("stripe %d after Set(idle): got %d, want lsnIdle", i, got)
		}
	}
	if got := tr.lowestActiveLSN(); got != lsnNoActive {
		t.Errorf("after all stripes idle: got %d, want lsnNoActive", got)
	}
}

// Pins the min-across-stripes behaviour. Three different stripes hold
// three different LSNs; lowestActive must equal the minimum. A
// regression that loops only the first stripe, or returns the max, or
// returns the wrong sentinel when not-all-idle, is caught here.
func TestInsertionTrackerLowestActiveLSNAcrossStripes(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	tr.setInsertingAt(0, 500)
	tr.setInsertingAt(3, 100) // lowest
	tr.setInsertingAt(7, 300)
	if got := tr.lowestActiveLSN(); got != 100 {
		t.Errorf("lowestActiveLSN: got %d, want 100", got)
	}
	// Clearing the lowest stripe shifts the answer.
	tr.setInsertingAt(3, lsnIdle)
	if got := tr.lowestActiveLSN(); got != 300 {
		t.Errorf("after clearing lowest stripe: got %d, want 300", got)
	}
	// Clearing all leaves the sentinel.
	tr.setInsertingAt(0, lsnIdle)
	tr.setInsertingAt(7, lsnIdle)
	if got := tr.lowestActiveLSN(); got != lsnNoActive {
		t.Errorf("after clearing every stripe: got %d, want lsnNoActive", got)
	}
}

// The "no active" sentinel must compose cleanly with min(upperBound,
// lowestActive). Pinning the math here documents the publication-side
// formula that the call-site rewrite will use. Without this, a future
// change to a "lowest=-1" or "lowest=0" sentinel would silently break
// the publication math at the consumer.
func TestInsertionTrackerLowestActiveSentinelComposesWithMin(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	upperBound := int64(12345)

	safeTail := upperBound
	if lowest := tr.lowestActiveLSN(); lowest < safeTail {
		safeTail = lowest
	}
	if safeTail != upperBound {
		t.Errorf("with no active stripes, safeTail should equal upperBound: got %d, want %d", safeTail, upperBound)
	}

	tr.setInsertingAt(2, 9999)
	safeTail = upperBound
	if lowest := tr.lowestActiveLSN(); lowest < safeTail {
		safeTail = lowest
	}
	if safeTail != 9999 {
		t.Errorf("with active stripe at 9999, safeTail should clamp: got %d, want 9999", safeTail)
	}
}

// Out-of-range stripe indices must panic — silent corruption of a
// neighbouring stripe slot would be a worse failure mode than a fast
// crash at the bad index. Covers both endpoints (negative and == cap)
// to pin the bounds-check exactly.
func TestInsertionTrackerSetInsertingAtPanicsOutOfRange(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	cases := []int{-1, -appendLockStripes, appendLockStripes, appendLockStripes + 1, 1024}
	for _, stripe := range cases {
		stripe := stripe
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for stripe=%d, got none", stripe)
				}
			}()
			tr.setInsertingAt(stripe, 1)
		})
	}
}

// Same bounds check on the read path. Without this, a wrong-index
// read could silently return a neighbour's state to the publication
// walker, which would then publish past pending bytes from the
// neighbour.
func TestInsertionTrackerInsertingAtPanicsOutOfRange(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	cases := []int{-1, appendLockStripes, appendLockStripes + 5}
	for _, stripe := range cases {
		stripe := stripe
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for stripe=%d, got none", stripe)
				}
			}()
			tr.insertingAt(stripe)
		})
	}
}

// Concurrent stripes writing only into their own slots is the core
// stripe-locality invariant. The test drives 8 stripes for many
// iterations and asserts (a) every Load reads a value from that
// stripe's history (not a neighbour's), (b) the race detector stays
// silent. Without per-slot atomic.Int64 storage this would tear
// under -race on weak-memory platforms.
func TestInsertionTrackerConcurrentStripeOwnership(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	const iters = 5000

	var wg sync.WaitGroup
	wg.Add(appendLockStripes)
	for s := 0; s < appendLockStripes; s++ {
		s := s
		go func() {
			defer wg.Done()
			// Each stripe writes only LSNs in a stripe-specific high range
			// so any cross-stripe leak (slot collision) would surface as
			// "stripe i read a value belonging to stripe j".
			baseLSN := int64((s + 1) * 1_000_000)
			for k := 0; k < iters; k++ {
				lsn := baseLSN + int64(k)
				tr.setInsertingAt(s, lsn)
				got := tr.insertingAt(s)
				// Could observe lsn or a later self-write from this loop iteration
				// (we are the only writer). Reject anything outside this stripe's
				// range or below the active threshold.
				if got < baseLSN || got > baseLSN+int64(iters) {
					t.Errorf("stripe %d: read %d, outside expected range [%d, %d]", s, got, baseLSN, baseLSN+int64(iters))
				}
				tr.setInsertingAt(s, lsnIdle)
			}
		}()
	}
	wg.Wait()
	// Every stripe must be idle at the end.
	if got := tr.lowestActiveLSN(); got != lsnNoActive {
		t.Errorf("after all stripes return to idle: got %d, want lsnNoActive", got)
	}
}

// Concurrent publication walker vs stripe writers. The reader observes
// lowestActiveLSN while writers oscillate between active and idle; the
// invariant is that every observed (non-sentinel) value falls inside
// the union of writer ranges. A regression that, e.g., returned 0
// when no stripe was active (instead of MaxInt64) would surface as a
// publication walker advancing tail to 0.
func TestInsertionTrackerConcurrentPublicationReader(t *testing.T) {
	t.Parallel()
	tr := newInsertionTracker()
	const iters = 5000

	var stop atomic.Bool
	var writers sync.WaitGroup
	writers.Add(appendLockStripes)
	for s := 0; s < appendLockStripes; s++ {
		s := s
		go func() {
			defer writers.Done()
			baseLSN := int64((s + 1) * 1_000_000)
			for k := 0; k < iters; k++ {
				tr.setInsertingAt(s, baseLSN+int64(k))
				tr.setInsertingAt(s, lsnIdle)
			}
		}()
	}

	// Reader goroutine: every observed lowestActive must be either
	// the sentinel or a value within some stripe's emission range.
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		const minStripeBase = int64(1_000_000)
		const maxStripeBase = int64(appendLockStripes * 1_000_000)
		for !stop.Load() {
			got := tr.lowestActiveLSN()
			if got == lsnNoActive {
				continue
			}
			if got < minStripeBase || got > maxStripeBase+int64(iters) {
				t.Errorf("publication reader observed %d, outside any stripe's range", got)
				return
			}
		}
	}()

	// Wait for writers to finish their work, then signal the reader to stop.
	// Without the two-WaitGroup split, wg.Wait would deadlock waiting on the
	// reader, which itself waits on stop.Store(true) — a circular dependency.
	writers.Wait()
	stop.Store(true)
	reader.Wait()
}

// Pins the package constants. lsnIdle == -1 is load-bearing because
// byte-LSN 0 is a legitimate active reservation (a fresh cluster /
// reset walBuffer starts its byte-addressed LSN space at 0), so the
// idle sentinel must be a value no reservation can ever take. Using 0
// would alias the first WAL record's active slot with "idle" and let
// the tail publisher race the drain against the LSN-0 stripe writer
// (M-NIGHTLY AI-20260717-010601-001; TestDrainSafetyStress). Because
// -1 is NOT the atomic.Int64 zero value, newInsertionTracker
// explicitly initialises every slot — see TestInsertionTrackerFreshIsIdle.
// lsnNoActive == math.MaxInt64 is load-bearing because the
// publication-side min() composes with it without a special branch.
func TestInsertionTrackerSentinelConstants(t *testing.T) {
	t.Parallel()
	if lsnIdle != -1 {
		t.Errorf("lsnIdle: got %d, want -1 (byte-LSN 0 must be distinguishable from idle)", lsnIdle)
	}
	if lsnNoActive != int64(math.MaxInt64) {
		t.Errorf("lsnNoActive: got %d, want math.MaxInt64", lsnNoActive)
	}
}
