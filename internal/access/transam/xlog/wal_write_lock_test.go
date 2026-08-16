package xlog

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWALWriteLockTriState verifies the three acquire outcomes.
func TestWALWriteLockTriState(t *testing.T) {
	l := newWALWriteLock()
	done := make(chan struct{})

	held, err := l.acquireOrWait(done)
	if !held || err != nil {
		t.Fatalf("uncontended acquire: held=%v err=%v, want true,nil", held, err)
	}

	// A second acquirer parks because the lock is held; when we release it
	// must wake without holding (false,nil).
	res := make(chan struct {
		held bool
		err  error
	}, 1)
	go func() {
		h, e := l.acquireOrWait(done)
		if h {
			l.release()
		}
		res <- struct {
			held bool
			err  error
		}{h, e}
	}()
	time.Sleep(50 * time.Millisecond) // let it park
	l.release()                       // wakes the waiter

	select {
	case r := <-res:
		if r.err != nil {
			t.Errorf("woken waiter err=%v, want nil", r.err)
		}
		// r.held may be true (it re-raced and won) or false (woken without
		// holding); both are valid tri-state outcomes.
	case <-time.After(5 * time.Second):
		t.Fatal("waiter not woken by release (missed wakeup)")
	}
}

// TestWALWriteLockCloseWakesWaiter verifies the shutdown arm: a parked waiter
// wakes with ErrClosed when the done channel closes.
func TestWALWriteLockCloseWakesWaiter(t *testing.T) {
	l := newWALWriteLock()
	done := make(chan struct{})

	held, err := l.acquireOrWait(done)
	if !held || err != nil {
		t.Fatalf("initial acquire: held=%v err=%v", held, err)
	}

	res := make(chan error, 1)
	go func() {
		h, e := l.acquireOrWait(done)
		if h {
			l.release()
		}
		res <- e
	}()
	time.Sleep(50 * time.Millisecond)
	close(done)

	select {
	case e := <-res:
		if e != ErrClosed {
			t.Errorf("waiter err=%v, want ErrClosed", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter not woken by close")
	}
	l.release()
}

// TestWALWriteLockReleasedOnPanic pins the M4 mitigation: flushAsHolder uses a
// deferred release so a panic in xlogWrite (recovered per-connection by the
// server) does not leak the lock and wedge every future commit. This exercises
// the same defer-release-then-repanic pattern at the primitive level.
func TestWALWriteLockReleasedOnPanic(t *testing.T) {
	l := newWALWriteLock()
	done := make(chan struct{})

	func() {
		defer func() { _ = recover() }()
		held, err := l.acquireOrWait(done)
		if !held || err != nil {
			t.Fatalf("acquire: held=%v err=%v", held, err)
		}
		defer l.release() // the flushAsHolder pattern
		panic("boom in xlogWrite")
	}()

	// After the recovered panic the lock must be free for the next committer.
	held, err := l.acquireOrWait(done)
	if !held || err != nil {
		t.Fatalf("lock not released after panic: held=%v err=%v", held, err)
	}
	l.release()
}

// TestWALWriteLockGroupCommitModel stresses the missed-wakeup / coverage
// properties by modeling emergent group commit: N workers each want a distinct
// "flush target"; a holder advances a shared durable counter to at least its
// own target; losers, woken without holding, re-check the counter and usually
// exit with zero work. The whole set must finish (no waiter blocks forever) and
// the counter must reach the max target (the largest-target worker must itself
// hold at least once, since nobody else covers it).
func TestWALWriteLockGroupCommitModel(t *testing.T) {
	const N = 64
	l := newWALWriteLock()
	done := make(chan struct{})
	var flushed atomic.Int64 // written only by the (serialized) holder

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		tgt := int64(i + 1)
		go func() {
			defer wg.Done()
			for {
				if flushed.Load() >= tgt {
					return // covered by another holder's flush
				}
				held, err := l.acquireOrWait(done)
				if err != nil {
					return
				}
				if !held {
					continue // woken without holding → recheck
				}
				// holder: only one at a time (mutex), so a plain max-store is safe.
				if cur := flushed.Load(); tgt > cur {
					flushed.Store(tgt)
				}
				l.release()
				return
			}
		}()
	}

	fin := make(chan struct{})
	go func() { wg.Wait(); close(fin) }()
	select {
	case <-fin:
	case <-time.After(15 * time.Second):
		t.Fatalf("workers did not finish; flushed=%d (missed wakeup / deadlock)", flushed.Load())
	}
	if got := flushed.Load(); got < N {
		t.Errorf("flushed=%d, want >= %d", got, N)
	}
}
