package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// TestCrossPageCatalogUpdateStampsOldPageLSN is the M0131-S26 regression for
// the cross-page branch of updateHeapRowCanonicalPG.
//
// That branch mutates TWO pages — the new version lands on a fresh page, the
// old version's page gets the xmax + forward-t_ctid stamp — and describes both
// with ONE xl_heap_update record. Only the new page went through
// MarkDirtyLogicalChange; the old page got a plain MarkDirty, which stamps
// pd_lsn only as a side effect of a first-touch FPI. Once the old page had
// already been imaged in the current checkpoint epoch (the common case for a
// hot catalog page) its pd_lsn stayed BEHIND the record that mutated it:
//
//   - flushSlots flushes WAL only up to max(pd_lsn) over the batch, so the
//     xmax stamp could reach disk before its record was durable, and
//   - PG's redo skips a record only when `lsn <= PageGetLSN(page)`, so a real
//     PG replaying goopg's stream (the M0131-S27 lane) re-applies it.
//
// The test forces the epoch state that hid the bug: the old page is imaged
// first, so the update's MarkDirty on it emits no image and therefore stamps
// nothing.
func TestCrossPageCatalogUpdateStampsOldPageLSN(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	var nextLSN storage.LSN = 1000
	var updateLSN storage.LSN
	logFPI := func(_ storage.RelFileNode, _ storage.BlockNumber, _ storage.Page) (storage.LSN, error) {
		nextLSN++
		return nextLSN, nil
	}
	logUpdate := func(_ storage.RelFileNode, _ storage.BlockNumber, _ uint16,
		_ storage.BlockNumber, _ uint16, _ storage.TransactionID, _ []byte) (storage.LSN, error) {
		nextLSN++
		updateLSN = nextLSN
		return nextLSN, nil
	}
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:          8,
		LogPageImage:   logFPI,
		LogHeapUpdate:  logUpdate,
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 3626, Fork: storage.MainFork}
	cols := []catalog.Column{{Name: "payload", Type: catalog.Type{Name: "text"}}}
	// Half a page each: the old page holds exactly one and cannot accept the
	// new version, which is what selects the cross-page branch.
	oldRow := Row{NewStringDatum(strings.Repeat("o", 5000))}
	newRow := Row{NewStringDatum(strings.Repeat("n", 5000))}

	ctx := &Context{Pool: pool, Tx: mvcc.Transaction{XID: 42}}

	seed, _, err := buildCatalogPGHeapTuple(ctx, cols, oldRow)
	if err != nil {
		t.Fatal(err)
	}
	slot, blk, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	if blk != 0 {
		t.Fatalf("seed block = %d, want 0", blk)
	}
	slot.Lock()
	if err := storage.InitPage(slot.Page()); err != nil {
		t.Fatal(err)
	}
	oldOff, err := storage.PageAddHeapTuple(slot.Page(), seed)
	if err != nil {
		t.Fatal(err)
	}
	// First touch of the epoch — emits the page's FPI and stamps pd_lsn with
	// it. From here on plain MarkDirty on this page is a no-op for pd_lsn.
	pool.MarkDirty(slot)
	slot.Unlock()
	imagedLSN := storage.MustHeader(slot.Page()).LSN()
	pool.Unpin(slot)
	if imagedLSN == 0 {
		t.Fatal("seed MarkDirty did not stamp pd_lsn (no first-touch image emitted)")
	}

	oldTID := storage.ItemPointer{Block: 0, Offset: oldOff}
	newTID, err := updateHeapRowCanonicalPG(ctx, rel, cols, oldTID, newRow)
	if err != nil {
		t.Fatalf("updateHeapRowCanonicalPG: %v", err)
	}
	if newTID.Block == oldTID.Block {
		t.Fatalf("new version landed on block %d — the same-page branch ran, "+
			"this test must exercise the cross-page branch", newTID.Block)
	}
	if updateLSN == 0 {
		t.Fatal("no xl_heap_update record was emitted")
	}

	oldSlot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: oldTID.Block})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(oldSlot)
	gotLSN := storage.MustHeader(oldSlot.Page()).LSN()
	if gotLSN < updateLSN {
		t.Fatalf("old page pd_lsn = %d, still behind the covering xl_heap_update at %d "+
			"(page was imaged at %d): the xmax stamp can reach disk ahead of its WAL record, "+
			"and a replaying PG re-applies the record over this page", gotLSN, updateLSN, imagedLSN)
	}

	// Sanity: the old tuple really was stamped on that page, so the LSN above
	// is describing a mutation that happened rather than a no-op.
	ht, err := storage.PageGetHeapTuple(oldSlot.Page(), oldTID.Offset)
	if err != nil {
		t.Fatal(err)
	}
	if ht.Header.Xmax != ctx.Tx.XID {
		t.Fatalf("old tuple xmax = %d, want %d", ht.Header.Xmax, ctx.Tx.XID)
	}
}
