package executor

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/goopg/goopg/internal/storage"
)

// TestPaddedMutexSize pins the cache-line-padded layout used by the
// 8-stripe heap-extend-lock set so a future struct change (e.g.
// adding a field to sync.Mutex's stand-in or shrinking the pad) is
// caught instead of silently re-introducing false-sharing across
// stripes. M0107-0007a.
func TestPaddedMutexSize(t *testing.T) {
	if got := unsafe.Sizeof(paddedMutex{}); got != 64 {
		t.Fatalf("paddedMutex size = %d, want 64 (one cache line)", got)
	}
	// Cross-check: the 8-stripe set occupies exactly 8 cache lines.
	if got := unsafe.Sizeof(heapExtendLockSet{}); got != 64*heapExtendLockStripes {
		t.Fatalf("heapExtendLockSet size = %d, want %d", got, 64*heapExtendLockStripes)
	}
}

// TestLockHeapExtendStripesByProcNum confirms `lockHeapExtend(rel,
// procNum)` selects `set.locks[procNum & 0x7]`, so eight distinct
// procNums (0..7) acquire eight distinct mutexes and may proceed in
// parallel. Without striping these calls would serialise on a single
// per-relation mutex. M0107-0007a.
func TestLockHeapExtendStripesByProcNum(t *testing.T) {
	// Use an obviously test-only RelFileNode so we do not collide
	// with any leftover entry in heapExtendLocks from another test.
	rel := storage.RelFileNode{DBOid: 1, RelOid: 0xDEADBEE7}
	t.Cleanup(func() { heapExtendLocks.Delete(rel) })

	const n = heapExtendLockStripes
	var (
		wg       sync.WaitGroup
		inFlight atomic.Int32
		peak     atomic.Int32
		release  = make(chan struct{})
		acquired = make(chan struct{}, n)
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		procNum := int32(i)
		go func() {
			defer wg.Done()
			unlock := lockHeapExtend(rel, procNum)
			defer unlock()
			cur := inFlight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			acquired <- struct{}{}
			<-release
			inFlight.Add(-1)
		}()
	}
	// Wait for every goroutine to acquire its stripe. If two procNums
	// shared a stripe, this drain would hang because at most one of
	// the colliding pair can be in the locked window at any instant.
	for i := 0; i < n; i++ {
		<-acquired
	}
	if got := peak.Load(); got != n {
		t.Fatalf("peak concurrent extenders = %d, want %d", got, n)
	}
	close(release)
	wg.Wait()
}

// TestLockHeapExtendCollidesOnSameStripe confirms two backends that
// hash to the same stripe (procNum % 8 collision) are correctly
// serialised — i.e. the striping is sound, not just permissive.
// Without this assertion the previous test would still pass under a
// degenerate "no locking" implementation. M0107-0007a.
func TestLockHeapExtendCollidesOnSameStripe(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 0xDEADBEE8}
	t.Cleanup(func() { heapExtendLocks.Delete(rel) })

	// procNum 0 and procNum 8 both map to stripe 0 (8 & 7 == 0).
	first := lockHeapExtend(rel, 0)
	done := make(chan struct{})
	go func() {
		unlock := lockHeapExtend(rel, 8)
		unlock()
		close(done)
	}()
	// The blocked goroutine must remain parked while `first` is held.
	// We poll for a bounded window — long enough to rule out a missed
	// schedule, short enough to keep the test fast. If the goroutine
	// completes before we release, the stripe was not actually held.
	select {
	case <-done:
		first()
		t.Fatal("second lockHeapExtend on the same stripe acquired while the first was held")
	case <-time.After(50 * time.Millisecond):
	}
	first()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second lockHeapExtend never acquired after first released")
	}
}
