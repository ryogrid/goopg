package wal

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// TestPaddedMutexSize pins the cache-line layout. The whole point of
// paddedMutex is that adjacent entries in an array occupy distinct
// cache lines, so the 64 B size — and the 64*N size of the appendLockSet
// — are load-bearing invariants of M0107-0007 slice B's contention
// reduction story.
func TestPaddedMutexSize(t *testing.T) {
	if got := unsafe.Sizeof(paddedMutex{}); got != 64 {
		t.Fatalf("paddedMutex size = %d, want 64 (one cache line)", got)
	}
	if got := unsafe.Sizeof(appendLockSet{}); got != 64*appendLockStripes {
		t.Fatalf("appendLockSet size = %d, want %d", got, 64*appendLockStripes)
	}
}

// TestStripeForProcNumMaskedByStripes verifies the procNum→stripe
// mapping matches the design's `procNum & 0x7` formula across the
// full int32 range (including the wraparound point and negative
// values that a wraparound counter might produce after overflow).
func TestStripeForProcNumMaskedByStripes(t *testing.T) {
	cases := []struct {
		procNum int32
		want    int
	}{
		{0, 0},
		{1, 1},
		{7, 7},
		{8, 0},
		{15, 7},
		{16, 0},
		{-1, 7},          // 0xFFFFFFFF & 0x7
		{-8, 0},          // 0xFFFFFFF8 & 0x7
		{1<<31 - 1, 7},   // INT32_MAX = 0x7FFFFFFF
		{-(1 << 31), 0},  // INT32_MIN = 0x80000000
	}
	for _, c := range cases {
		if got := stripeForProcNum(c.procNum); got != c.want {
			t.Fatalf("stripeForProcNum(%d) = %d, want %d", c.procNum, got, c.want)
		}
	}
}

// TestAppendLockSetStripesByProcNum drives 8 goroutines on procNums
// 0..7 and observes peak concurrency of 8 — i.e. all stripes are
// genuinely independent. A single shared mutex (the pre-slice-B state)
// would cap peak at 1.
func TestAppendLockSetStripesByProcNum(t *testing.T) {
	var s appendLockSet
	const N = appendLockStripes

	var inCS atomic.Int32
	var peak atomic.Int32
	var ready sync.WaitGroup
	var done sync.WaitGroup
	start := make(chan struct{})

	ready.Add(N)
	done.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			ready.Done()
			<-start
			unlock := s.lockByProcNum(int32(i))
			cur := inCS.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			// Hold long enough that peers definitely have a chance to
			// enter their own stripe. 5 ms is comfortably larger than
			// scheduling jitter on a healthy machine without making
			// the test long.
			time.Sleep(5 * time.Millisecond)
			inCS.Add(-1)
			unlock()
			done.Done()
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	if got := peak.Load(); got != N {
		t.Fatalf("peak concurrency = %d, want %d (all stripes independent)", got, N)
	}
}

// TestAppendLockSetCollidesOnSameStripe verifies stripe-mates (e.g.
// procNum 3 and procNum 11, both `& 0x7 == 3`) serialise. Without the
// modulo mapping a backend per "stripe" could trivially pass; the
// modulo is what makes the 8-stripe cap real, not a nominal one.
func TestAppendLockSetCollidesOnSameStripe(t *testing.T) {
	var s appendLockSet

	var inCS atomic.Int32
	var peak atomic.Int32
	var ready sync.WaitGroup
	var done sync.WaitGroup
	start := make(chan struct{})

	// procNum 3 and procNum 11 both hash to stripe 3.
	peers := []int32{3, 11}
	ready.Add(len(peers))
	done.Add(len(peers))
	for _, pn := range peers {
		pn := pn
		go func() {
			ready.Done()
			<-start
			unlock := s.lockByProcNum(pn)
			cur := inCS.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inCS.Add(-1)
			unlock()
			done.Done()
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrency on same stripe = %d, want 1 (mutual exclusion)", got)
	}
}

// TestAppendLockSetUnlockClosureIdempotent is a negative-shape
// regression: lockByProcNum returns the bare sync.Mutex.Unlock method
// value, which would panic on double-unlock. The test does NOT exercise
// the panic (the runtime would tear down the test process); it pins
// the contract that the returned closure is a single-shot unlock so
// callers cannot accidentally rely on idempotent unlock semantics.
// Encoded as a behavioural assertion: after one unlock, the same
// stripe is immediately re-acquirable by a peer.
func TestAppendLockSetUnlockClosureReleasesStripe(t *testing.T) {
	var s appendLockSet

	unlock := s.lockByProcNum(0)
	unlock()

	// If unlock() did not release stripe 0, the goroutine below would
	// block forever; a 500 ms watchdog catches that case.
	acquired := make(chan struct{})
	go func() {
		u2 := s.lockByProcNum(0)
		u2()
		close(acquired)
	}()
	select {
	case <-acquired:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("stripe 0 not released by unlock closure within 500 ms")
	}
}
