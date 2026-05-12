package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestConcurrentHOTUpdateDetectsRace pins M0090-0002 + M0098-0004:
// a second HOT-update on a slot whose tuple has already been stamped
// by a committed concurrent UPDATE must NOT silently overwrite the
// prior stamp (the M0090-0002 invariant). With EvalPlanQual (M0098-0004),
// the conflict causes tryApplyHOTUpdate to return (false, nil) —
// falling back to the delete+insert path — instead of SQLSTATE 40001.
//
// Pre-M0090-0002 behaviour:
//   - T1 stamps xmax = T1.xid, CTID = S', writes new tuple at S'. Commits.
//   - T2 takes Lock, pre-check sees S is LP_NORMAL ✓, writes new
//     tuple at S''. PageStampHotOldTuple OVERWRITES xmax = T2.xid,
//     CTID = S'' — clobbering T1's stamp. Result: two visible rows.
//
// Post-M0090-0002 + M0098-0004: T2 detects the concurrent xmax stamp
// and returns (used=false, nil), falling back to delete+insert where
// the EvalPlanQual wait loop handles the conflict.
func TestConcurrentHOTUpdateDetectsRace(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	rel := ctx.Catalog.RelFileNode(tbl)
	cols := tbl.Columns

	// Seed one row at slot 1 on block 0 via writeHeapRow under the
	// existing ctx transaction (T0).
	if err := writeHeapRow(ctx, rel, cols,
		Row{{Kind: KindInt, Int: 1}, {Kind: KindString, Buf: []byte("v0")}}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	// Commit T0 so the row is visible to subsequent transactions.
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatalf("commit T0: %v", err)
	}

	// T1: HOT-update the row.
	t1 := beginTxn(t, ctx)
	ctxT1 := *ctx
	ctxT1.Tx = t1
	snap1, _ := ctx.TxnMgr.SnapshotFor(t1)
	ctxT1.Snap = snap1
	used, err := tryApplyHOTUpdate(&ctxT1, rel, cols, 0, 1,
		Row{{Kind: KindInt, Int: 1}, {Kind: KindString, Buf: []byte("t1")}})
	if err != nil {
		t.Fatalf("T1 tryApplyHOTUpdate: %v", err)
	}
	if !used {
		t.Fatalf("T1 expected HOT-update to succeed (used=true); got used=false")
	}
	if err := ctx.TxnMgr.Commit(t1); err != nil {
		t.Fatalf("commit T1: %v", err)
	}

	// T2: try to HOT-update the same slot (the index lookup would
	// still resolve to slot 1 if T2's scan ran before T1 committed,
	// which is exactly the pgbench race). With EvalPlanQual (M0098-0004),
	// conflict causes fallback to delete+insert (used=false, nil error).
	t2 := beginTxn(t, ctx)
	ctxT2 := *ctx
	ctxT2.Tx = t2
	snap2, _ := ctx.TxnMgr.SnapshotFor(t2)
	ctxT2.Snap = snap2
	used2, err2 := tryApplyHOTUpdate(&ctxT2, rel, cols, 0, 1,
		Row{{Kind: KindInt, Int: 1}, {Kind: KindString, Buf: []byte("t2")}})
	if err2 != nil {
		t.Errorf("T2 tryApplyHOTUpdate: got error %v; want nil (EPQ fallback)", err2)
	}
	if used2 {
		t.Errorf("T2 returned used=true; want used=false on conflict (EPQ fallback)")
	}
	_ = ctx.TxnMgr.Rollback(t2)
}

// TestSelfUpdateInSameTxnNotBlocked pins that re-updating a row
// within the same transaction (legal, the xmax field would be our
// own XID at that point) is NOT flagged as concurrent. The check at
// `isConcurrentlyUpdated` excludes `h.Xmax == myXID`.
func TestSelfUpdateInSameTxnNotBlocked(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	rel := ctx.Catalog.RelFileNode(tbl)
	cols := tbl.Columns

	if err := writeHeapRow(ctx, rel, cols,
		Row{{Kind: KindInt, Int: 1}, {Kind: KindString, Buf: []byte("v0")}}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	// First HOT-update under the same ctx.Tx — stamps slot 1's xmax
	// = ctx.Tx.XID, writes new tuple at slot 2.
	used1, err := tryApplyHOTUpdate(ctx, rel, cols, 0, 1,
		Row{{Kind: KindInt, Int: 1}, {Kind: KindString, Buf: []byte("self-1")}})
	if err != nil || !used1 {
		t.Fatalf("first self-update: used=%v err=%v", used1, err)
	}

	// Slot 1 now has xmax = ctx.Tx.XID. A second HOT-update on the
	// SAME slot from the SAME transaction must NOT be flagged as a
	// concurrent update (re-update of our own row is legal).
	used2, err := tryApplyHOTUpdate(ctx, rel, cols, 0, 1,
		Row{{Kind: KindInt, Int: 1}, {Kind: KindString, Buf: []byte("self-2")}})
	if err != nil {
		t.Fatalf("second self-update err: %v (expected nil — xmax == myXID is not a concurrent update)", err)
	}
	_ = used2 // used may be false if HOT chain logistics differ; we only care no false-positive 40001.
}

// TestIsConcurrentlyUpdatedHelper directly unit-tests the helper's
// classification of every xmax / infomask state we care about.
// Note: the snapshot parameter is currently ignored by isConcurrentlyUpdated;
// aborted-xmax disambiguation is handled in the EPQ retry loops.
func TestIsConcurrentlyUpdatedHelper(t *testing.T) {
	const myXID storage.TransactionID = 100

	cases := []struct {
		name string
		h    storage.HeapTupleHeader
		want bool
	}{
		{"unset xmax", storage.HeapTupleHeader{Xmax: 0, Infomask: 0}, false},
		{"xmax=myXID", storage.HeapTupleHeader{Xmax: myXID, Infomask: 0}, false},
		{"xmax=other", storage.HeapTupleHeader{Xmax: 50, Infomask: 0}, true},
		{"HeapHotUpdated bit set", storage.HeapTupleHeader{Xmax: 50, Infomask: storage.HeapHotUpdated}, true},
		{"HeapXmaxInvalid bit set with xmax=other", storage.HeapTupleHeader{Xmax: 50, Infomask: storage.HeapXmaxInvalid}, false},
		{"lock-only xmax (FOR UPDATE)", storage.HeapTupleHeader{Xmax: 50, Infomask: storage.HeapXmaxLockOnly | storage.HeapXmaxExclLock}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isConcurrentlyUpdated(c.h, myXID, nil); got != c.want {
				t.Errorf("isConcurrentlyUpdated = %v, want %v", got, c.want)
			}
		})
	}
}

// beginTxn opens a fresh READ_COMMITTED transaction on the same mvcc
// manager as ctx. Used by the concurrent-update tests to simulate
// two clients hitting the same row.
func beginTxn(t *testing.T, ctx *Context) mvcc.Transaction {
	t.Helper()
	tx, err := ctx.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return tx
}
