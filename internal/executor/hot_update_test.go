package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// newHOTFixture creates a fresh executor context backed by a real
// storage manager and an in-memory catalog. It exposes the
// storage.Manager so callers can inspect heap pages directly after
// calling ctx.Pool.FlushAll().
func newHOTFixture(t *testing.T) (*Context, catalog.Catalog, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	mgrMVCC := transam.NewManager()
	tx, err := mgrMVCC.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := mgrMVCC.SnapshotFor(tx)
	if err != nil {
		t.Fatal(err)
	}
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mgrMVCC
	ctx.Tx = tx
	ctx.Snap = snap
	cleanup := func() {
		_ = mgrMVCC.Rollback(tx)
		_ = pool.Close()
		_ = mgr.Close()
	}
	return ctx, cat, cleanup
}

// readPageViaPool pins page blk from the pool and copies its bytes
// for direct inspection. The returned Page is a detached copy — the
// page lock is released before returning.
func readPageViaPool(t *testing.T, pool *storage.Pool, rel storage.RelFileNode, blk storage.BlockNumber) storage.Page {
	t.Helper()
	slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		t.Fatalf("Pin(blk=%d): %v", blk, err)
	}
	slot.RLock()
	out := make(storage.Page, storage.BlockSize)
	copy(out, slot.Page())
	slot.RUnlock()
	pool.Unpin(slot)
	return out
}

// TestHOTUpdateSamePage verifies the core HOT invariant: when only a
// non-indexed column is changed, the new tuple version lands on the
// same heap page as the old one (no page extension), the old slot carries
// HEAP_HOT_UPDATED + the CTID chain link, and the new slot carries
// HEAP_ONLY_TUPLE.
func TestHOTUpdateSamePage(t *testing.T) {
	ctx, cat, cleanup := newHOTFixture(t)
	defer cleanup()

	// Create table, insert data first, THEN create index so backfill
	// captures the row (regular INSERT doesn't maintain secondary indexes).
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO items VALUES (1, 'before')"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX items_id_idx ON items (id)"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	heapRel := ctx.Catalog.RelFileNode(tbl)

	nBefore, _ := ctx.Pool.NBlocks(heapRel)
	if nBefore != 1 {
		t.Fatalf("expected 1 heap page before update, got %d", nBefore)
	}

	// HOT-eligible update: only 'label' (non-indexed) changes.
	if err := runDDL(t, ctx, "UPDATE items SET label = 'after' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	// Heap page count must not have grown — HOT keeps the new version
	// on the same page.
	nAfter, _ := ctx.Pool.NBlocks(heapRel)
	if nAfter != nBefore {
		t.Fatalf("HOT update should not grow heap pages: before=%d after=%d", nBefore, nAfter)
	}

	// Read page 0 via the pool (no flush needed — buffer pool is the truth).
	page := readPageViaPool(t, ctx.Pool, heapRel, 0)
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatalf("PageLinePointerCount: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 line pointers (old+new HOT tuple), got %d", count)
	}

	oldTuple, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatalf("old tuple slot 1: %v", err)
	}
	if !oldTuple.Header.IsHotUpdated() {
		t.Errorf("old tuple slot 1 missing HeapHotUpdated flag (infomask=0x%04x)", oldTuple.Header.Infomask)
	}
	if oldTuple.Header.CTID.Offset != 2 {
		t.Errorf("old tuple CTID.Offset=%d, want 2", oldTuple.Header.CTID.Offset)
	}
	if oldTuple.Header.Xmax == storage.InvalidTransactionID {
		t.Errorf("old tuple xmax should be set (deleted by HOT update)")
	}

	newTuple, err := storage.PageGetHeapTuple(page, 2)
	if err != nil {
		t.Fatalf("new HOT-only tuple slot 2: %v", err)
	}
	if !newTuple.Header.IsHeapOnly() {
		t.Errorf("new HOT-only tuple slot 2 missing HeapOnlyTuple flag (infomask=0x%04x)", newTuple.Header.Infomask)
	}
	if newTuple.Header.Xmax != storage.InvalidTransactionID {
		t.Errorf("new HOT-only tuple should not be deleted (xmax=%d)", newTuple.Header.Xmax)
	}
}

// TestHOTUpdateIndexScanFindsNewVersion verifies that after a HOT update,
// an index scan on the indexed column still returns the current (HOT-only)
// row — i.e. followHOTChain navigates the chain correctly.
func TestHOTUpdateIndexScanFindsNewVersion(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE emp (id int, name text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO emp VALUES (42, 'Alice')"); err != nil {
		t.Fatal(err)
	}
	// Create index after insert so the entry exists in the index.
	if err := runDDL(t, ctx, "CREATE INDEX emp_id_idx ON emp (id)"); err != nil {
		t.Fatal(err)
	}

	// HOT-eligible update (non-indexed column only).
	if err := runDDL(t, ctx, "UPDATE emp SET name = 'Bob' WHERE id = 42"); err != nil {
		t.Fatal(err)
	}

	// The index scan should find the HOT-updated row (name='Bob'),
	// not the stale original (name='Alice').
	rows := runQuery(t, ctx, "SELECT id, name FROM emp WHERE id = 42")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0][1].Kind != KindString || rows[0][1].StringValue() != "Bob" {
		t.Errorf("expected name='Bob', got %+v", rows[0][1])
	}
}

// TestHOTUpdateIndexedColumnFallback verifies that updating an indexed
// column disables HOT: the old tuple is deleted normally (no
// HEAP_HOT_UPDATED flag) and the new tuple does NOT carry HEAP_ONLY_TUPLE.
func TestHOTUpdateIndexedColumnFallback(t *testing.T) {
	ctx, cat, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int, val text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1, 'x')"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_id_idx ON t (id)"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "t"})
	heapRel := ctx.Catalog.RelFileNode(tbl)

	// Non-HOT update: the indexed column 'id' changes.
	if err := runDDL(t, ctx, "UPDATE t SET id = 2 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	// The old tuple should NOT have HEAP_HOT_UPDATED (normal delete).
	page := readPageViaPool(t, ctx.Pool, heapRel, 0)
	oldTuple, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatalf("old tuple: %v", err)
	}
	if oldTuple.Header.IsHotUpdated() {
		t.Errorf("non-HOT update must not set HeapHotUpdated (infomask=0x%04x)", oldTuple.Header.Infomask)
	}
}

// TestHOTUpdateChainDepthTwo verifies a two-step HOT chain: two
// consecutive HOT updates of the same non-indexed column produce a
// depth-2 chain, and a query returns the final version.
func TestHOTUpdateChainDepthTwo(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int, v text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1, 'v1')"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_idx ON t (id)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "UPDATE t SET v = 'v2' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "UPDATE t SET v = 'v3' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	heapRel := ctx.Catalog.RelFileNode(tbl)
	nPages, _ := ctx.Pool.NBlocks(heapRel)
	if nPages != 1 {
		t.Fatalf("two HOT updates should stay on 1 heap page, got %d", nPages)
	}

	page := readPageViaPool(t, ctx.Pool, heapRel, 0)
	count, _ := storage.PageLinePointerCount(page)
	if count != 3 {
		t.Fatalf("expected 3 line pointers (3 versions), got %d", count)
	}

	// Index scan must return the final version v3.
	rows := runQuery(t, ctx, "SELECT v FROM t WHERE id = 1")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0][0].Kind != KindString || rows[0][0].StringValue() != "v3" {
		t.Errorf("expected v='v3', got %+v", rows[0][0])
	}
}

// TestFollowHOTChainDirect unit-tests followHOTChain at the page level:
// manually construct a two-slot HOT chain (slot 1 → slot 2) and verify
// the helper navigates it correctly.
func TestFollowHOTChainDirect(t *testing.T) {
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}

	// xid=5 is the current transaction.
	// Snapshot sees xids 1..4 as committed, 5..∞ as future/in-progress.
	xid := storage.TransactionID(5)
	snap := transam.Snapshot{Xmin: 5, Xmax: 10}

	// Slot 1: old tuple — created in xid=4 (visible to snap), deleted by
	// xid=5 (our transaction → invisible to us), with HEAP_HOT_UPDATED.
	old := storage.NewHeapTuple(4, xid, []byte("old"))
	old.Header.SetHotUpdated()
	old.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
	s1, err := storage.PageAddHeapTuple(page, old)
	if err != nil || s1 != 1 {
		t.Fatalf("add old tuple: slot=%d err=%v", s1, err)
	}

	// Slot 2: new HOT-only tuple created by xid=5 (visible to self).
	newT := storage.NewHeapTuple(xid, storage.InvalidTransactionID, []byte("new"))
	newT.Header.SetHeapOnly()
	s2, err := storage.PageAddHeapTuple(page, newT)
	if err != nil || s2 != 2 {
		t.Fatalf("add new tuple: slot=%d err=%v", s2, err)
	}

	// followHOTChain from slot 1 must return (newT, slot 2, true).
	got, gotSlot, found := followHOTChain(page, 1, snap, xid, nil, storage.InvalidCommandId, nil)
	if !found {
		t.Fatal("followHOTChain: not found, expected the HOT-only successor")
	}
	if gotSlot != 2 {
		t.Errorf("gotSlot=%d, want 2", gotSlot)
	}
	if !got.Header.IsHeapOnly() {
		t.Errorf("returned tuple missing HeapOnlyTuple (infomask=0x%04x)", got.Header.Infomask)
	}

	// Starting from slot 2 directly should also return it (already visible).
	got2, slot2, found2 := followHOTChain(page, 2, snap, xid, nil, storage.InvalidCommandId, nil)
	if !found2 {
		t.Fatal("followHOTChain from slot 2: not found")
	}
	if slot2 != 2 || !got2.Header.IsHeapOnly() {
		t.Errorf("direct hit on slot 2 failed: slot=%d infomask=0x%04x", slot2, got2.Header.Infomask)
	}
}

// TestHOTUpdateChainBeyond64Versions is the M0131-S32 regression guard: a HOT
// chain longer than 64 versions must stay reachable through the index.
//
// Every chain walker used an arbitrary `maxChain = 64`, so version 65 of a
// repeatedly-HOT-updated row became invisible to the index scan while a seq
// scan still returned version 64. Because the UPDATE's row count comes from the
// planned row set, each subsequent statement still reported `UPDATE 1` and
// committed — one client, one row, no crash, no error, and a silently wrong
// value (docs/design/0131-0025). The correct bound is
// storage.MaxHeapTuplesPerPage: a chain visits distinct slots of ONE page.
//
// 120 updates is comfortably past 64 and below the 291-slot page limit, so the
// whole chain stays on one page and no non-HOT fallback masks the defect.
func TestHOTUpdateChainBeyond64Versions(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int, v bigint)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1, 0)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_idx ON t (id)"); err != nil {
		t.Fatal(err)
	}

	const updates = 120
	for i := 1; i <= updates; i++ {
		if err := runDDL(t, ctx, "UPDATE t SET v = v + 1 WHERE id = 1"); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
		// Read back through the index after every update: the defect froze the
		// value silently, so only a per-step check identifies the exact
		// version at which the chain became unreachable.
		rows := runQuery(t, ctx, "SELECT v FROM t WHERE id = 1")
		if len(rows) != 1 {
			t.Fatalf("after update %d: index scan returned %d rows, want 1"+
				" (chain walk truncated — see docs/design/0131-0025)", i, len(rows))
		}
		if got := rows[0][0].Int; got != int64(i) {
			t.Fatalf("after update %d: v=%d, want %d (committed update silently not applied)", i, got, i)
		}
	}

	// A seq scan must agree with the index scan — the original symptom was the
	// two disagreeing (index returned nothing, seq returned the stale row).
	seq := runQuery(t, ctx, "SELECT v FROM t WHERE id + 0 = 1")
	if len(seq) != 1 || seq[0][0].Int != int64(updates) {
		t.Fatalf("seq scan disagrees with index scan: %+v, want v=%d", seq, updates)
	}
}
