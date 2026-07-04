package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// beginTx starts a new transaction and installs it in ctx.
func beginTx(t *testing.T, ctx *Context) {
	t.Helper()
	tx, err := ctx.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := ctx.TxnMgr.SnapshotFor(tx)
	if err != nil {
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx.Tx = tx
	ctx.Snap = snap
}

// commitTx commits the current transaction.
func commitTx(t *testing.T, ctx *Context) {
	t.Helper()
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// TestOpportunisticPruneReclaims verifies the DoD for M0046-0002.
//
// Scenario:
//  1. T1: INSERT 1 keyed row + fill page 0 with many filler rows; DELETE fillers; COMMIT.
//     After T1 commits, page 0 has dead filler tuples (xmax=T1.XID) and 1 live row.
//  2. T2 begins (OldestXmin advances past T1.XID).
//  3. T2: HOT-UPDATE the keyed row. Page 0 is full with dead tuples.
//     Opportunistic pruning reclaims the dead fillers, making space for the HOT insert.
//  4. No new heap page is allocated.
func TestOpportunisticPruneReclaims(t *testing.T) {
	ctx, cat, cleanup := newHOTFixture(t)
	defer cleanup()
	ctx.EnableOpportunisticPrune = true

	if err := runDDL(t, ctx, "CREATE TABLE t (id int, v text)"); err != nil {
		t.Fatal(err)
	}

	// T1: INSERT target row and then fill the page with filler rows.
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "t"})
	heapRel := ctx.Catalog.RelFileNode(tbl)

	// Insert the target row.
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1, 'target')"); err != nil {
		t.Fatal(err)
	}
	// Create index AFTER insert so it picks up the row.
	if err := runDDL(t, ctx, "CREATE INDEX t_id ON t (id)"); err != nil {
		t.Fatal(err)
	}

	// Insert filler rows until page 0 fills up (NBlocks grows to 2).
	var fillerCount int
	for i := 2; ; i++ {
		sql := fmt.Sprintf("INSERT INTO t VALUES (%d, 'filler')", i)
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("filler insert %d: %v", i, err)
		}
		n, _ := ctx.Pool.NBlocks(heapRel)
		if n > 1 {
			// Stop: just extended to page 1. The last filler went to page 1.
			// All fillers on page 0 are what we want to prune.
			fillerCount = i - 2 // rows on page 0 minus the target row
			break
		}
		if i > 1000 {
			t.Fatal("could not fill page in 1000 inserts")
		}
	}
	if fillerCount == 0 {
		t.Skip("no fillers fit on page 0 alongside target row")
	}

	// Delete all filler rows from page 0 (id 2..fillerCount+1) so their
	// slots have committed xmax and can be pruned by T2.
	for i := 2; i <= fillerCount+1; i++ {
		sql := fmt.Sprintf("DELETE FROM t WHERE id = %d", i)
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("delete filler id=%d: %v", i, err)
		}
	}
	// Also delete the stray filler that landed on page 1.
	if err := runDDL(t, ctx, fmt.Sprintf("DELETE FROM t WHERE id = %d", fillerCount+2)); err != nil {
		t.Logf("extra filler delete: %v (ok if not found)", err)
	}

	// COMMIT T1. All filler deletions are now committed.
	commitTx(t, ctx)

	// T2 begins. OldestXmin now exceeds T1's XID, so the filler slots
	// (xmax=T1.XID) are universally dead and eligible for pruning.
	beginTx(t, ctx)

	nBefore, _ := ctx.Pool.NBlocks(heapRel)

	// T2: HOT-update the target row (non-indexed column 'v').
	// Page 0 should be full of dead fillers. Opportunistic pruning must reclaim
	// those slots so the HOT insert succeeds without extending the relation.
	if err := runDDL(t, ctx, "UPDATE t SET v = 'pruned' WHERE id = 1"); err != nil {
		t.Fatalf("T2 HOT update: %v", err)
	}

	nAfter, _ := ctx.Pool.NBlocks(heapRel)
	if nAfter > nBefore {
		t.Errorf("heap grew from %d to %d pages — pruning should have reclaimed space",
			nBefore, nAfter)
	}

	// Verify the row value is correct via a SeqScan (safe: SeqScan skips
	// unused/redirect line pointers). Index-only reads need redirect support
	// which is also implemented, but SeqScan is the simplest verification.
	rows := runQuery(t, ctx, "SELECT v FROM t WHERE id = 1")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after T2 update, got %d", len(rows))
	}
	if rows[0][0].Kind != KindString || rows[0][0].StringValue() != "pruned" {
		t.Errorf("expected v='pruned', got %+v", rows[0][0])
	}
}

// TestOpportunisticPruneEmitsCanonicalWAL guards M0119-0005's fix: before
// this, the opportunistic page-full prune fallback inside a HOT update
// emitted only goopg's native RecordKindHeapPruneOpt record, so a page whose
// only WAL activity was pruning was invisible to pg_waldump/a real PG18
// standby. Reuses TestOpportunisticPruneReclaims's exact fixture/repro
// (fill page 0, delete the fillers, commit, then a T2 HOT update that must
// opportunistically prune to make room) but wires ctx.LogCanonical to assert
// a PG-canonical XLOG_HEAP2_PRUNE_ON_ACCESS record was emitted for the prune,
// distinct from the HOT update's own XLOG_HEAP_INPLACE record.
func TestOpportunisticPruneEmitsCanonicalWAL(t *testing.T) {
	ctx, cat, cleanup := newHOTFixture(t)
	defer cleanup()
	ctx.EnableOpportunisticPrune = true

	if err := runDDL(t, ctx, "CREATE TABLE t (id int, v text)"); err != nil {
		t.Fatal(err)
	}

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "t"})
	heapRel := ctx.Catalog.RelFileNode(tbl)

	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1, 'target')"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_id ON t (id)"); err != nil {
		t.Fatal(err)
	}

	var fillerCount int
	for i := 2; ; i++ {
		sql := fmt.Sprintf("INSERT INTO t VALUES (%d, 'filler')", i)
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("filler insert %d: %v", i, err)
		}
		n, _ := ctx.Pool.NBlocks(heapRel)
		if n > 1 {
			fillerCount = i - 2
			break
		}
		if i > 1000 {
			t.Fatal("could not fill page in 1000 inserts")
		}
	}
	if fillerCount == 0 {
		t.Skip("no fillers fit on page 0 alongside target row")
	}

	for i := 2; i <= fillerCount+1; i++ {
		sql := fmt.Sprintf("DELETE FROM t WHERE id = %d", i)
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("delete filler id=%d: %v", i, err)
		}
	}
	if err := runDDL(t, ctx, fmt.Sprintf("DELETE FROM t WHERE id = %d", fillerCount+2)); err != nil {
		t.Logf("extra filler delete: %v (ok if not found)", err)
	}

	commitTx(t, ctx)
	beginTx(t, ctx)

	nBefore, _ := ctx.Pool.NBlocks(heapRel)

	var payloads [][]byte
	ctx.LogCanonical = func(payload []byte) (uint64, error) {
		payloads = append(payloads, append([]byte(nil), payload...))
		return uint64(len(payloads)) * 1000, nil
	}

	if err := runDDL(t, ctx, "UPDATE t SET v = 'pruned' WHERE id = 1"); err != nil {
		t.Fatalf("T2 HOT update: %v", err)
	}

	nAfter, _ := ctx.Pool.NBlocks(heapRel)
	if nAfter > nBefore {
		t.Fatalf("heap grew from %d to %d pages — pruning should have reclaimed space",
			nBefore, nAfter)
	}

	const rmHeap2ID = 9        // RM_HEAP2_ID
	const pruneOnAccess = 0x10 // XLOG_HEAP2_PRUNE_ON_ACCESS
	found := false
	for _, p := range payloads {
		if len(p) < 3 || p[0] != catalog.RecordKindCanonical {
			t.Fatalf("unexpected non-canonical payload: %x", p)
		}
		if p[1] == rmHeap2ID && p[2] == pruneOnAccess {
			found = true
		}
	}
	if !found {
		t.Fatalf("no XLOG_HEAP2_PRUNE_ON_ACCESS canonical record emitted among %d LogCanonical calls", len(payloads))
	}
}

// TestPageSetXmaxTracksPruneXID verifies that the pd_prune_xid bookkeeping
// in PageSetHeapTupleXmax correctly tracks the max xmax seen.
func TestPageSetXmaxTracksPruneXID(t *testing.T) {
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		tup := storage.NewHeapTuple(1, 0, []byte("x"))
		if _, err := storage.PageAddHeapTuple(page, tup); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.PageSetHeapTupleXmax(page, 1, 5); err != nil {
		t.Fatal(err)
	}
	if got := storage.TransactionID(storage.MustHeader(page).PruneXID()); got != 5 {
		t.Errorf("pd_prune_xid after slot1 xmax=5: want 5, got %d", got)
	}
	if err := storage.PageSetHeapTupleXmax(page, 2, 3); err != nil {
		t.Fatal(err)
	}
	// xmax=3 < current 5 → pd_prune_xid must remain 5.
	if got := storage.TransactionID(storage.MustHeader(page).PruneXID()); got != 5 {
		t.Errorf("pd_prune_xid should stay at 5 (not decrease to 3), got %d", got)
	}
}
