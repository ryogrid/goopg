package runtimeshim

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSema_PreReleasedAcquireReturns confirms the no-block path: when
// the cell already has a positive count, SemaAcquire decrements and
// returns immediately. This anchors design-doc §5: "Acquire decrements
// the semaphore *s, blocking until it's > 0." A positive *s on entry
// means the call must not block.
func TestSema_PreReleasedAcquireReturns(t *testing.T) {
	var s uint32
	SemaRelease(&s)
	SemaRelease(&s)
	SemaRelease(&s)

	done := make(chan struct{})
	go func() {
		SemaAcquire(&s)
		SemaAcquire(&s)
		SemaAcquire(&s)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("three Acquires after three Releases blocked unexpectedly")
	}
}

// TestSema_BlocksUntilRelease confirms the block-then-wake path: an
// Acquire on a zero-valued cell must park the goroutine, and a
// subsequent Release on the same cell must wake exactly one waiter.
// This anchors design-doc §5: the per-slot wait pattern used by the
// lock-free bufpool depends on a Release issued by the I/O-completion
// goroutine unblocking a waiting Pin caller.
func TestSema_BlocksUntilRelease(t *testing.T) {
	var s uint32

	var acquired atomic.Bool
	go func() {
		SemaAcquire(&s)
		acquired.Store(true)
	}()

	// Give the acquirer time to park on the cell. The acquirer must
	// not progress while *s is zero.
	time.Sleep(50 * time.Millisecond)
	if acquired.Load() {
		t.Fatalf("SemaAcquire returned before any SemaRelease was issued")
	}

	SemaRelease(&s)

	deadline := time.Now().Add(2 * time.Second)
	for !acquired.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("SemaAcquire did not return after SemaRelease")
		}
		time.Sleep(1 * time.Millisecond)
	}
}

// TestSema_BalancedManyProducersConsumers exercises the canonical
// bufpool-style caller pattern: N producer goroutines issue Releases,
// M consumer goroutines issue Acquires, and every Acquire must
// eventually pair with exactly one Release. The test runs producers
// and consumers in approximately equal volume and validates that all
// consumers return (no consumer is left parked on the cell after the
// expected number of Releases has been issued).
//
// The race detector is the primary correctness signal: any data race
// inside SemaAcquire / SemaRelease would surface here, since the
// goroutines also bump a separate atomic counter that the test reads
// at the end.
func TestSema_BalancedManyProducersConsumers(t *testing.T) {
	const producers = 8
	const consumers = 8
	const opsPerWorker = 1 << 12
	const totalOps = producers * opsPerWorker

	var s uint32
	var consumed atomic.Int64

	var pwg sync.WaitGroup
	pwg.Add(producers)
	for p := 0; p < producers; p++ {
		go func() {
			defer pwg.Done()
			for i := 0; i < opsPerWorker; i++ {
				SemaRelease(&s)
			}
		}()
	}

	var cwg sync.WaitGroup
	cwg.Add(consumers)
	perConsumer := totalOps / consumers
	for c := 0; c < consumers; c++ {
		go func() {
			defer cwg.Done()
			for i := 0; i < perConsumer; i++ {
				SemaAcquire(&s)
				consumed.Add(1)
			}
		}()
	}

	pwg.Wait()
	cwg.Wait()

	if got := consumed.Load(); got != int64(totalOps) {
		t.Fatalf("consumed = %d, want %d (Acquire/Release pairing lost)", got, totalOps)
	}
	// After all consumers return, the cell must have drained to zero:
	// producers issued exactly totalOps Releases and consumers issued
	// exactly totalOps Acquires.
	if atomic.LoadUint32(&s) != 0 {
		t.Fatalf("final *s = %d, want 0 (residual Releases on cell)", atomic.LoadUint32(&s))
	}
}

// TestSema_DistinctCellsIndependent confirms that two distinct *uint32
// cells route to independent wait queues: an Acquire on cell A must
// not be woken by a Release on cell B. This is critical for the
// bufpool's per-slot wait model, where every buffer slot owns its own
// uint32 and waiters on one slot must not be spuriously woken by I/O
// completion on a different slot.
func TestSema_DistinctCellsIndependent(t *testing.T) {
	var a, b uint32

	var aDone atomic.Bool
	go func() {
		SemaAcquire(&a)
		aDone.Store(true)
	}()

	time.Sleep(50 * time.Millisecond)
	// Release on cell B must NOT unblock the waiter on cell A.
	SemaRelease(&b)
	time.Sleep(50 * time.Millisecond)
	if aDone.Load() {
		t.Fatalf("SemaAcquire on cell A returned after a Release on unrelated cell B")
	}

	// Drain the spurious Release on B so the test leaves no residual
	// state, then unblock A by releasing on its own cell.
	SemaAcquire(&b)
	SemaRelease(&a)

	deadline := time.Now().Add(2 * time.Second)
	for !aDone.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("SemaAcquire on A did not return after a Release on A")
		}
		time.Sleep(1 * time.Millisecond)
	}
}

// BenchmarkSemaAcquireRelease measures the uncontended pair cost. The
// linkname path on Linux/amd64 should land in the tens of nanoseconds
// (no goroutine park: the cell is positive when Acquire arrives); the
// fallback path is dominated by sync.Mutex + map lookup and lands
// closer to the hundreds. A regression past either band signals a
// build-tag or fallback issue worth investigating.
func BenchmarkSemaAcquireRelease(b *testing.B) {
	var s uint32
	for i := 0; i < b.N; i++ {
		SemaRelease(&s)
		SemaAcquire(&s)
	}
}
