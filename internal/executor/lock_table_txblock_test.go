package executor

import "testing"

// LOCK TABLE at top level outside a transaction block raises 25P01 before the
// relation is even resolved, as PostgreSQL's RequireTransactionBlock does
// (utility.c:936); inside a block, and inside a routine (RoutineDepth > 0, PG's
// !isTopLevel), it runs. Found live against PG 18.3 on 2026-09-23.
func TestLockTableRequiresTransactionBlock(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"LOCK TABLE t", "LOCK TABLE nosuch", "LOCK t IN SHARE MODE"} {
		err := runDDL(t, ctx, sql)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "25P01" || ee.Message != "LOCK TABLE can only be used in transaction blocks" {
			t.Errorf("%s outside a block: err = %v, want 25P01", sql, err)
		}
	}

	ctx.RoutineDepth = 1 // not top-level: a PL/pgSQL or SQL-function body
	if err := runDDL(t, ctx, "LOCK TABLE t"); err != nil {
		t.Errorf("LOCK TABLE inside a routine: %v", err)
	}
	ctx.RoutineDepth = 0

	sess.BeginExplicitTransaction(ctx.Tx, ctx.Snap)
	if err := runDDL(t, ctx, "LOCK TABLE t"); err != nil {
		t.Errorf("LOCK TABLE inside a block: %v", err)
	}
}
