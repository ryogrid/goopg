package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// TestInsertRuntimeUniqueViolationRaises23505 pins the M0100-0005r fix:
// once an INSERT has placed a live tuple under a unique-key, a subsequent
// INSERT in the same session that re-uses the key must surface SQLSTATE
// 23505 with the upstream "duplicate key value violates unique constraint"
// MESSAGE shape.
//
// Before this fix, the executor's INSERT path called
// `maintainUniqueIndexesForInsert` (which silently ignored
// `btree.Insert` errors) and never probed the index for live conflicts —
// callers got a successful INSERT and a duplicate row in the heap, which
// is the upstream bug `read-write-unique.spec` (M0100-0005's 21-test
// pass goal) catches at L36.
func TestInsertRuntimeUniqueViolationRaises23505(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "items_id_pkey"}, tbl, []string{"id"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := btree.Create(ctx.Pool, cat.IndexRelFileNode(idx)); err != nil {
		t.Fatal(err)
	}

	insert := func(rows [][]planner.Expr) error {
		op, err := Build(&planner.Insert{Table: tbl, Source: &planner.Values{Rows: rows}, ColumnIndex: []int{0, 1}})
		if err != nil {
			return err
		}
		if err := op.Open(ctx); err != nil {
			return err
		}
		_, err = op.Next()
		_ = op.Close()
		if err == EOF {
			return nil
		}
		return err
	}

	if err := insert([][]planner.Expr{{&planner.IntegerConst{Value: 42}, &planner.StringConst{Value: "a"}}}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err = insert([][]planner.Expr{{&planner.IntegerConst{Value: 42}, &planner.StringConst{Value: "b"}}})
	if err == nil {
		t.Fatal("second insert with duplicate id=42 should have failed; got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23505" {
		t.Errorf("Code=%q want %q", ee.Code, "23505")
	}
	if !strings.Contains(ee.Message, "duplicate key value violates unique constraint") {
		t.Errorf("Message=%q does not contain upstream prefix", ee.Message)
	}
	if !strings.Contains(ee.Message, "items_id_pkey") {
		t.Errorf("Message=%q does not name the violated constraint", ee.Message)
	}
}

// TestInsertRuntimeUniqueViolationAllowsAfterRolledBackInsert: a tuple
// inserted under xmin = X and X subsequently rolled back must NOT block
// a fresh INSERT of the same key. `isLiveForUniqueCheck` consults
// `Snap.HasAborted` for this, so the second INSERT should succeed.
func TestInsertRuntimeUniqueViolationAllowsAfterRolledBackInsert(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "items_id_pkey"}, tbl, []string{"id"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := btree.Create(ctx.Pool, cat.IndexRelFileNode(idx)); err != nil {
		t.Fatal(err)
	}

	insert := func(rows [][]planner.Expr) error {
		op, err := Build(&planner.Insert{Table: tbl, Source: &planner.Values{Rows: rows}, ColumnIndex: []int{0, 1}})
		if err != nil {
			return err
		}
		if err := op.Open(ctx); err != nil {
			return err
		}
		_, err = op.Next()
		_ = op.Close()
		if err == EOF {
			return nil
		}
		return err
	}

	if err := insert([][]planner.Expr{{&planner.IntegerConst{Value: 7}, &planner.StringConst{Value: "x"}}}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	abortedXID := ctx.Tx.XID

	// Roll back, start a new transaction with a fresh snapshot whose
	// Aborted slice contains the previous XID. The second insert must
	// succeed because the prior tuple is no longer a live conflict.
	if err := ctx.TxnMgr.Rollback(ctx.Tx); err != nil {
		t.Fatal(err)
	}
	tx, err := ctx.TxnMgr.Begin(ctx.Tx.Isolation)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ctx.TxnMgr.SnapshotFor(tx)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Tx = tx
	ctx.Snap = snap
	if !snap.HasAborted(abortedXID) {
		t.Fatalf("snapshot.Aborted missing prior xid %d (got %v)", abortedXID, snap.Aborted)
	}

	if err := insert([][]planner.Expr{{&planner.IntegerConst{Value: 7}, &planner.StringConst{Value: "y"}}}); err != nil {
		t.Fatalf("post-rollback insert should succeed; got: %v", err)
	}

	// Sanity: the index check helper independently classifies the
	// rolled-back tuple as not live.
	col := []catalog.Column{tbl.Columns[0], tbl.Columns[1]}
	if err := checkUniqueIndexesForInsert(ctx, tbl, col, Row{NewIntDatum(99), NewStringDatum("z")}, 0); err != nil {
		t.Fatalf("unrelated key check should not error: %v", err)
	}
}


// TestIsLiveForUniqueCheck_SelfXactDeleteIsDead pins the
// "DELETE then INSERT same key in the same transaction" semantics
// expected by `fk-snapshot.spec`'s `s1brr s1dfp s1ifp1 s1c s1sfn`
// permutation (and the RC analogue).  Without the self-xid xmax
// short-circuit, `isLiveForUniqueCheck` saw `xmax = ctx.Tx.XID`,
// `IsXIDActive(xmax) == true`, and returned "live" — so the follow-up
// INSERT raised SQLSTATE 23505 against the row it had just deleted.
//
// The assertion runs the helper directly with a synthesised
// `(xmin, xmax)` pair: xmin = a separate committed xact, xmax = the
// session's own active xid.  Returning `false` means "not a live
// duplicate", which is what the INSERT path wants to hear.
func TestIsLiveForUniqueCheck_SelfXactDeleteIsDead(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	// xmin = some prior committed xact (older than the snapshot we hold).
	priorTx, err := ctx.TxnMgr.Begin(ctx.Tx.Isolation)
	if err != nil {
		t.Fatal(err)
	}
	priorXID, err := ctx.TxnMgr.AssignXID(priorTx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctx.TxnMgr.Commit(priorTx); err != nil {
		t.Fatal(err)
	}

	// xmax = our own session's xid (the deleter).
	selfXID, err := ctx.TxnMgr.AssignXID(ctx.Tx)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Tx.XID = selfXID

	// Refresh snapshot so the prior xact is visible as committed.
	snap, err := ctx.TxnMgr.SnapshotFor(ctx.Tx)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Snap = snap

	if got := isLiveForUniqueCheck(ctx, priorXID, selfXID); got {
		t.Fatalf("isLiveForUniqueCheck(xmin=prior-committed, xmax=self) = true; want false (own-xact delete is not a live duplicate)")
	}

	// Sanity: a tuple deleted by a *different* live xact remains a live
	// duplicate (concurrent delete that has not yet committed).
	otherTx, err := ctx.TxnMgr.Begin(ctx.Tx.Isolation)
	if err != nil {
		t.Fatal(err)
	}
	otherXID, err := ctx.TxnMgr.AssignXID(otherTx)
	if err != nil {
		t.Fatal(err)
	}
	if got := isLiveForUniqueCheck(ctx, priorXID, otherXID); !got {
		t.Fatalf("isLiveForUniqueCheck(xmin=prior-committed, xmax=other-active) = false; want true (concurrent delete, not yet committed)")
	}
}
