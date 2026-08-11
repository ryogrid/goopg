package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// TestEPQSlotMovedToAnotherPartitionDetectsSentinel verifies that
// epqSlotMovedToAnotherPartition returns true exactly when the heap
// tuple at (rel, blk, slot) was stamped via
// storage.PageSetHeapTupleMovedPartition (the cross-partition UPDATE
// path).  Without this guard, EPQ retries that walk to a deleted
// tuple silently skip the row instead of raising the upstream
// `tuple to be locked was already moved to another partition due to
// concurrent update` error — which is exactly the divergence
// captured against PostgreSQL's partition-key-update-1 spec.
// M0100-0005n.
func TestEPQSlotMovedToAnotherPartitionDetectsSentinel(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 999}
	ctx := newPoolBackedContext(t, rel)

	const blk storage.BlockNumber = 0

	// Build a page in the buffer pool with one tuple, then stamp the
	// moved-partition sentinel via the storage primitive.
	pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	pin.Lock()
	tuple := storage.NewHeapTuple(storage.TransactionID(100), storage.InvalidTransactionID, []byte("v"))
	slot, err := storage.PageAddHeapTuple(pin.Page(), tuple)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	if err := storage.PageSetHeapTupleMovedPartition(pin.Page(), slot, storage.TransactionID(42)); err != nil {
		t.Fatalf("PageSetHeapTupleMovedPartition: %v", err)
	}
	pin.Unlock()
	ctx.Pool.Unpin(pin)

	if !epqSlotMovedToAnotherPartition(ctx, rel, blk, slot) {
		t.Fatal("epqSlotMovedToAnotherPartition = false, want true after sentinel stamp")
	}
}

// TestEPQSlotMovedToAnotherPartitionRejectsPlainXmax — a tuple stamped
// only with PageSetHeapTupleXmax (normal in-place / same-partition
// UPDATE) must NOT trigger the moved-partition error path.
func TestEPQSlotMovedToAnotherPartitionRejectsPlainXmax(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 998}
	ctx := newPoolBackedContext(t, rel)

	const blk storage.BlockNumber = 0
	pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	pin.Lock()
	tuple := storage.NewHeapTuple(storage.TransactionID(100), storage.InvalidTransactionID, []byte("v"))
	slot, err := storage.PageAddHeapTuple(pin.Page(), tuple)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	if err := storage.PageSetHeapTupleXmax(pin.Page(), slot, storage.TransactionID(42)); err != nil {
		t.Fatalf("PageSetHeapTupleXmax: %v", err)
	}
	pin.Unlock()
	ctx.Pool.Unpin(pin)

	if epqSlotMovedToAnotherPartition(ctx, rel, blk, slot) {
		t.Fatal("epqSlotMovedToAnotherPartition = true on plain xmax stamp; sentinel must require explicit CTID write")
	}
}

// TestErrMovedToAnotherPartitionShape pins the canonical SQLSTATE and
// MESSAGE — IsolationRunner asserts byte-identical output against
// upstream's partition-key-update-1 expected file (`ERROR:  tuple to
// be locked was already moved to another partition due to concurrent
// update`), so this string must not drift.  Code 0A000 mirrors
// upstream's `errcode_for_partition` raise in heapam.c.
func TestErrMovedToAnotherPartitionShape(t *testing.T) {
	err := errMovedToAnotherPartition(0)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("type = %T, want *ExecError", err)
	}
	if ee.Code != "0A000" {
		t.Errorf("Code = %q, want %q", ee.Code, "0A000")
	}
	want := "tuple to be locked was already moved to another partition due to concurrent update"
	if ee.Message != want {
		t.Errorf("Message = %q, want %q", ee.Message, want)
	}
}

// TestStampMovedPartitionOldTupleWritesCmax guards the cmax half of the
// cross-partition UPDATE stamp.
//
// Upstream heap_delete calls HeapTupleHeaderSetCmax (heapam.c:3065)
// unconditionally and only then adds HeapTupleHeaderSetMovedPartitions
// (heapam.c:3071) — the sentinel is an addition to the normal delete stamp, not
// a replacement. goopg's three cross-partition stamp sites (MERGE's
// mergeApplyUpdate plus the two plain-UPDATE sites in operators_storage.go)
// previously wrote only the sentinel, so the tuple's stale t_cid — the cmin of
// whichever command inserted it — was left standing in for cmax. When that
// stale value happened to be >= the deleting transaction's current command id,
// mvcc.TupleVisible's `effXmax == currentXID` arm read it as "deleted by a
// later command — pre-image visible" and the writer kept seeing its own
// moved-away row. Other sessions were unaffected (they judge xmax against the
// snapshot, never cmax), so the corruption was invisible except in the writer's
// own re-scan. Reproduced by the isolation spec merge-update, permutation
// `pa_merge1 pa_merge2a c1 pa_select2 c2` (M-NIGHTLY AI-20260809-020705-018).
//
// The stale cmin here (9) is deliberately larger than the deleting
// transaction's command id, which is the exact condition that made the old code
// leak the row; the sub-test below asserts that the bare sentinel stamp really
// does leak it, so this gate cannot pass vacuously.
func TestStampMovedPartitionOldTupleWritesCmax(t *testing.T) {
	const (
		insertingXID   = storage.TransactionID(100) // an earlier, committed txn
		deletingXID    = storage.TransactionID(250) // the txn doing the cross-partition move
		staleCmin      = storage.CommandId(9)       // inserting command id, left in t_cid
		blk            = storage.BlockNumber(0)
		nextStmtCurcid = storage.CommandId(2) // the deleting txn's NEXT statement
	)
	// A snapshot that sees insertingXID as committed, so TupleVisible reaches
	// the xmax arm rather than rejecting on xmin.
	snap := mvcc.Snapshot{Xmin: 200, Xmax: 300}

	// setup builds a one-tuple page whose t_cid carries the stale cmin, and
	// returns the context plus the tuple's slot.
	setup := func(t *testing.T, relOid uint32) (*Context, storage.RelFileNode, uint16) {
		t.Helper()
		rel := storage.RelFileNode{DBOid: 1, RelOid: relOid}
		ctx := newPoolBackedContext(t, rel)
		ctx.Tx.XID = deletingXID
		// Advance to command 1: the statement performing the move.
		advanceStmtCounter(ctx)

		pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
		tuple := storage.NewHeapTuple(insertingXID, storage.InvalidTransactionID, []byte("v"))
		pin.Lock()
		slot, err := storage.PageAddHeapTuple(pin.Page(), tuple)
		if err != nil {
			t.Fatalf("PageAddHeapTuple: %v", err)
		}
		// Simulate "inserted by command 9 of an earlier transaction".
		if err := storage.PageSetHeapTupleCmax(pin.Page(), slot, staleCmin, false); err != nil {
			t.Fatalf("seed t_cid: %v", err)
		}
		pin.Unlock()
		ctx.Pool.Unpin(pin)
		return ctx, rel, slot
	}

	readHeader := func(t *testing.T, ctx *Context, rel storage.RelFileNode, slot uint16) storage.HeapTupleHeader {
		t.Helper()
		pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
		defer ctx.Pool.Unpin(pin)
		pin.Lock()
		defer pin.Unlock()
		tup, err := storage.PageGetHeapTuple(pin.Page(), slot)
		if err != nil {
			t.Fatalf("PageGetHeapTuple: %v", err)
		}
		return tup.Header
	}

	t.Run("fixed", func(t *testing.T) {
		ctx, rel, slot := setup(t, 4101)
		moveCID := ctx.GetCurrentCommandId(false)

		pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
		pin.Lock()
		if err := stampMovedPartitionOldTuple(ctx, pin.Page(), slot); err != nil {
			t.Fatalf("stampMovedPartitionOldTuple: %v", err)
		}
		pin.Unlock()
		ctx.Pool.Unpin(pin)

		h := readHeader(t, ctx, rel, slot)
		if h.Xmax != deletingXID {
			t.Errorf("Xmax = %d, want %d", h.Xmax, deletingXID)
		}
		if got := mvcc.GetCmax(h, nil); got != moveCID {
			t.Errorf("cmax = %d, want %d (the moving command's id, not the stale cmin %d)",
				got, moveCID, staleCmin)
		}
		// The sentinel CTID must survive the cmax write — EPQ's
		// moved-partition detection depends on it.
		if !epqSlotMovedToAnotherPartition(ctx, rel, blk, slot) {
			t.Error("moved-partition sentinel lost after the cmax stamp")
		}
		// The deleting transaction's next statement must NOT see the row.
		if mvcc.TupleVisible(h, snap, deletingXID, nextStmtCurcid, nil, nil) {
			t.Error("moved-away row still visible to its own transaction at the next command")
		}
	})

	// Non-vacuity: the bare sentinel stamp (the pre-fix behaviour) leaks the row.
	t.Run("bare_sentinel_stamp_leaks_the_row", func(t *testing.T) {
		ctx, rel, slot := setup(t, 4102)

		pin, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
		pin.Lock()
		if err := storage.PageSetHeapTupleMovedPartition(pin.Page(), slot, deletingXID); err != nil {
			t.Fatalf("PageSetHeapTupleMovedPartition: %v", err)
		}
		pin.Unlock()
		ctx.Pool.Unpin(pin)

		h := readHeader(t, ctx, rel, slot)
		if got := mvcc.GetCmax(h, nil); got != staleCmin {
			t.Fatalf("precondition: cmax = %d, want the stale cmin %d", got, staleCmin)
		}
		if !mvcc.TupleVisible(h, snap, deletingXID, nextStmtCurcid, nil, nil) {
			t.Fatal("expected the pre-fix stamp to leak the moved-away row; " +
				"if this now passes, the visibility rule changed and the fixed sub-test above may be vacuous")
		}
	})
}

// newPoolBackedContext builds a minimal Context with an on-disk
// buffer pool sufficient for unit-testing storage-page-touching
// helpers (no MVCC, no WAL, no catalog).  Also extends `rel` with one
// freshly-initialised block at block 0 so subsequent Pin calls don't
// hit "short read at block".
func newPoolBackedContext(t *testing.T, rel storage.RelFileNode) *Context {
	t.Helper()
	_ = catalog.NewInMemory // keep catalog import alive for future fixtures
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() {
		_ = pool.Close()
		_ = mgr.Close()
	})
	// Extend the relation with a single zero-initialised page so the
	// caller can Pin block 0 without hitting "short read at block".
	page := make([]byte, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	ctx := NewContext()
	ctx.Ctx = context.Background()
	ctx.Pool = pool
	return ctx
}
