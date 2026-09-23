package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage/lmgr"
)

// TestForUpdateOnPartitionedTableThroughBatchFilter is the unit-level pin of
// isolation spec insert-conflict-do-update-4 (M-NIGHTLY, 2026-09-23): a
// `SELECT … WHERE i = 1 FOR UPDATE` over a partitioned table plans a filterOp
// whose single column-vs-constant comparison is batch-eligible (M0122-0012).
// The batch path pulled up to 64 child rows before emitting the first, so
// LockRows' currentTID read the position of a LATER row — in another
// partition — and failed with "short read at block". With
// disableFilterReadAhead the filter stays per-row under a currentTID consumer.
func TestForUpdateOnPartitionedTableThroughBatchFilter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.LockMgr = lmgr.New()
	ctx.BackendID = 1
	for _, s := range []string{
		"CREATE TABLE upsert (i int PRIMARY KEY, j int, k int) PARTITION BY RANGE (i)",
		"CREATE TABLE upsert_1 PARTITION OF upsert FOR VALUES FROM (1) TO (100)",
		"CREATE TABLE upsert_2 PARTITION OF upsert FOR VALUES FROM (100) TO (200)",
		"INSERT INTO upsert VALUES (1, 10, 100)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	rows, err := runForUpdate(t, ctx, "SELECT * FROM upsert WHERE i = 1 FOR UPDATE")
	if err != nil {
		t.Fatalf("FOR UPDATE over a partitioned table: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
}

// TestDisableFilterReadAheadCoversEveryWalkerBranch pins the walker's descent
// against findScanLeaf's: a filterOp on the single-child spine, under either
// setOp branch and under either join side must all be forced per-row — any
// filter those walkers can see through sits between a currentTID consumer and
// the leaf it reads.
func TestDisableFilterReadAheadCoversEveryWalkerBranch(t *testing.T) {
	mk := func() *filterOp { return &filterOp{child: &seqScanOp{}, batchEnabled: true} }
	setL, joinL, joinR, spine := mk(), mk(), mk(), mk()
	top := &filterOp{
		child:        &setOp{left: setL, right: &joinOp{left: joinL, right: joinR}},
		batchEnabled: true,
	}
	disableFilterReadAhead(&projectOp{child: top})
	disableFilterReadAhead(&sortOp{child: spine})
	for name, f := range map[string]*filterOp{"top": top, "setOp-left": setL, "join-left": joinL, "join-right": joinR, "sort-spine": spine} {
		if f.batchEnabled || !f.noReadAhead {
			t.Errorf("%s: batchEnabled=%v noReadAhead=%v, want read-ahead disabled", name, f.batchEnabled, f.noReadAhead)
		}
	}
}
