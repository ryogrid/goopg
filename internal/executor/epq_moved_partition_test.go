package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
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
