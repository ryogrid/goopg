package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
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
	if _, err := nbtree.Create(ctx.Pool, cat.IndexRelFileNode(idx)); err != nil {
		t.Fatal(err)
	}

	insert := func(rows [][]optimizer.Expr) error {
		op, err := Build(&optimizer.Insert{Table: tbl, Source: &optimizer.Values{Rows: rows}, ColumnIndex: []int{0, 1}})
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

	if err := insert([][]optimizer.Expr{{&optimizer.IntegerConst{Value: 42}, &optimizer.StringConst{Value: "a"}}}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err = insert([][]optimizer.Expr{{&optimizer.IntegerConst{Value: 42}, &optimizer.StringConst{Value: "b"}}})
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
	if _, err := nbtree.Create(ctx.Pool, cat.IndexRelFileNode(idx)); err != nil {
		t.Fatal(err)
	}

	insert := func(rows [][]optimizer.Expr) error {
		op, err := Build(&optimizer.Insert{Table: tbl, Source: &optimizer.Values{Rows: rows}, ColumnIndex: []int{0, 1}})
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

	if err := insert([][]optimizer.Expr{{&optimizer.IntegerConst{Value: 7}, &optimizer.StringConst{Value: "x"}}}); err != nil {
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

	if err := insert([][]optimizer.Expr{{&optimizer.IntegerConst{Value: 7}, &optimizer.StringConst{Value: "y"}}}); err != nil {
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

// uniqueCheckFixture sets up the `items` table with a unique PK on `id` and a
// single live tuple (id=42, label='a') inserted by the active session, plus
// a second live tuple (id=99, label='c'). Returns the context, table, and
// columns so unique-check behaviour can be exercised directly.
func uniqueCheckFixture(t *testing.T) (*Context, *catalog.Table, []catalog.Column, func()) {
	t.Helper()
	ctx, cat, cleanup := newStorageFixture(t)
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "items_id_pkey"}, tbl, []string{"id"}, true, "btree", true)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	if _, err := nbtree.Create(ctx.Pool, cat.IndexRelFileNode(idx)); err != nil {
		cleanup()
		t.Fatal(err)
	}
	insert := func(id int64, label string) {
		t.Helper()
		op, err := Build(&optimizer.Insert{
			Table:       tbl,
			Source:      &optimizer.Values{Rows: [][]optimizer.Expr{{&optimizer.IntegerConst{Value: id}, &optimizer.StringConst{Value: label}}}},
			ColumnIndex: []int{0, 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		_, err = op.Next()
		_ = op.Close()
		if err != nil && err != EOF {
			t.Fatalf("insert id=%d: %v", id, err)
		}
	}
	insert(42, "a")
	insert(99, "c")
	return ctx, tbl, tbl.Columns, cleanup
}

// TestCheckUniqueIndexesForUpdate_NoKeyChangeSkips pins the pgbench TPC-B
// regression: an UPDATE that does NOT change the PK column must not probe the
// unique index and therefore must not raise 23505 against another MVCC version
// of the very row being updated. The seqscan/updateViaIndex non-HOT fallback
// (taken under concurrency when HOT is blocked) previously called the
// INSERT-time check unconditionally, which spuriously flagged a concurrently
// updated sibling version as a live duplicate
// (duplicate key value violates unique constraint "pgbench_tellers_pkey").
func TestCheckUniqueIndexesForUpdate_NoKeyChangeSkips(t *testing.T) {
	ctx, tbl, cols, cleanup := uniqueCheckFixture(t)
	defer cleanup()

	// id unchanged (42 -> 42), label changed 'a' -> 'b'. A live id=42 tuple
	// exists in the index, but the check must be SKIPPED because the key did
	// not change.
	oldRow := Row{NewIntDatum(42), NewStringDatum("a")}
	newRow := Row{NewIntDatum(42), NewStringDatum("b")}
	if err := checkUniqueIndexesForUpdate(ctx, tbl, cols, oldRow, newRow, false, 0); err != nil {
		t.Fatalf("no-key-change UPDATE raised %v; want nil (unique check must be skipped)", err)
	}

	// Sanity: the INSERT-time check (no key-change awareness) WOULD flag the
	// live id=42 tuple — proving the skip is what prevents the false positive.
	if err := checkUniqueIndexesForInsert(ctx, tbl, cols, newRow, 0); err == nil {
		t.Fatal("checkUniqueIndexesForInsert(id=42) returned nil; expected it to see the live duplicate (test premise invalid)")
	}
}

// TestCheckUniqueIndexesForUpdate_KeyChangeStillEnforced pins that scoping the
// check to changed keys does NOT weaken genuine enforcement: an UPDATE that
// changes the PK to a value already held by a different live row must still
// raise 23505 (the M0100-0005r behaviour the original check protected).
func TestCheckUniqueIndexesForUpdate_KeyChangeStillEnforced(t *testing.T) {
	ctx, tbl, cols, cleanup := uniqueCheckFixture(t)
	defer cleanup()

	// id changed 42 -> 99, and id=99 already exists live -> must conflict.
	oldRow := Row{NewIntDatum(42), NewStringDatum("a")}
	newRow := Row{NewIntDatum(99), NewStringDatum("b")}
	err := checkUniqueIndexesForUpdate(ctx, tbl, cols, oldRow, newRow, false, 0)
	if err == nil {
		t.Fatal("key-changing UPDATE to existing id=99 returned nil; want 23505")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23505" {
		t.Fatalf("want *ExecError 23505, got %T %v", err, err)
	}
	if !strings.Contains(ee.Message, "items_id_pkey") {
		t.Errorf("Message=%q does not name the violated constraint", ee.Message)
	}
}

// TestCheckUniqueIndexesForUpdate_ForceAllProbesUnchangedKey pins that the
// cross-partition path (forceAll=true) still probes even when the key is
// unchanged: a move into a destination relation that already holds the key
// must conflict regardless of whether this row's prior version had it.
func TestCheckUniqueIndexesForUpdate_ForceAllProbesUnchangedKey(t *testing.T) {
	ctx, tbl, cols, cleanup := uniqueCheckFixture(t)
	defer cleanup()

	oldRow := Row{NewIntDatum(42), NewStringDatum("a")}
	newRow := Row{NewIntDatum(42), NewStringDatum("b")}
	// Key unchanged, but forceAll bypasses the skip -> the live id=42 tuple is
	// a conflict.
	err := checkUniqueIndexesForUpdate(ctx, tbl, cols, oldRow, newRow, true, 0)
	if err == nil {
		t.Fatal("forceAll probe of live id=42 returned nil; want 23505")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "23505" {
		t.Fatalf("want *ExecError 23505, got %T %v", err, err)
	}
}
