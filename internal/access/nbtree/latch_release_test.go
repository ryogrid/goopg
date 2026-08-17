package nbtree

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestStrandedLatchReleasedOnPanic is the regression guard for the M-NIGHTLY
// regress/suite-wedge root cause: a panic unwinding a mutation path used to
// leave the page's exclusive content latch held forever, because unpinW is an
// explicit call rather than a defer and internal/server's per-connection
// recover() throws the panicking goroutine away.
//
// The real trigger was insertItemSorted panicking ("storage: not enough free
// space in page") inside insertIntoBlock's leaf-write window. panicBeforeLeafWrite
// injects a panic at that exact point; the test then proves the tree is still
// USABLE. Without the wlatch holder the follow-up Insert blocks on the stranded
// latch forever — which is precisely how the wedge presented: a statement that
// sails past its own statement_timeout, because a mutex wait observes no
// deadline at all.
func TestStrandedLatchReleasedOnPanic(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	// insertIntoBlock is the SPLIT path — Insert only reaches it once the
	// fast no-split path reports errNeedsSplit — so fill the leaf with wide
	// keys first, exactly as the real workload did.
	wide := func(i int) []byte {
		k := make([]byte, 200)
		k[0] = byte(i / 256)
		k[1] = byte(i % 256)
		return k
	}

	// Panic once, from inside the leaf-mutation latch window.
	fired := false
	bt.panicBeforeLeafWrite = func(blk storage.BlockNumber) {
		if fired {
			return
		}
		fired = true
		panic("injected: not enough free space in page")
	}

	panicked := false
	for i := 0; i < 200 && !fired; i++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
				}
			}()
			if err := bt.Insert(wide(i), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
				t.Fatalf("seed insert %d: %v", i, err)
			}
		}()
	}
	if !fired {
		t.Fatal("fault injection never fired — the test proves nothing")
	}
	if !panicked {
		t.Fatal("expected the injected panic to propagate out of Insert")
	}
	bt.panicBeforeLeafWrite = nil

	// The latch must be free. Do the follow-up work on another goroutine so a
	// stranded latch surfaces as a timeout rather than hanging the whole suite.
	done := make(chan error, 1)
	go func() {
		if err := bt.Insert(wide(199), storage.ItemPointer{Block: 199, Offset: 3}); err != nil {
			done <- err
			return
		}
		_, _, err := bt.Search(wide(0))
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("post-panic tree use failed: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("post-panic Insert blocked: the page latch was stranded by the panic " +
			"(this is the regress/suite-wedge failure mode)")
	}
}

// TestWlatchReleaseIsIdempotent guards the property the deferred release relies
// on: insertIntoBlock calls release() on its normal exits AND defers it, so a
// second call must be a no-op rather than a double-unlock panic.
func TestWlatchReleaseIsIdempotent(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	if err := bt.Insert([]byte("k1"), storage.ItemPointer{Block: 1, Offset: 1}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	meta, err := bt.readMeta()
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	s, err := bt.pinW(meta.Root)
	if err != nil {
		t.Fatalf("pinW: %v", err)
	}
	held := wlatch{bt: bt}
	held.hold(s)
	held.release()
	held.release() // must not panic, must not unpin twice
	held.release()

	// The latch really is free: a fresh exclusive acquire must not block.
	got := make(chan struct{})
	go func() {
		s2, err := bt.pinW(meta.Root)
		if err == nil {
			bt.unpinW(s2)
		}
		close(got)
	}()
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("latch still held after release()")
	}
}
