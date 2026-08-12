package executor

import "testing"

// TestUpdateOverRangeIndexScanDoesNotPanic locks the M0131-S27 discovery.
//
// updateOp.Next takes a fast path through updateViaIndex whenever the child
// plan carries an *planner.IndexScan. That helper probes exactly ONE equality
// key — `evalExpr(ix.Key, nil, o.ctx)` — but the planner also emits an
// IndexScan for a RANGE predicate, where `Key` is nil and the bounds live in
// LowKey/HighKey. Dereferencing the nil Key panicked the backend goroutine with
// `invalid memory address or nil pointer dereference` inside evalExprSlot, so
// an ordinary `UPDATE … WHERE id BETWEEN a AND b` on an indexed column killed
// the connection (`driver: bad connection`) instead of updating rows.
//
// Found by the forward crash E2E (internal/testport/
// e2e_pg_crashstart_on_goopgdata_test.go), whose post-checkpoint workload runs
// exactly that statement. The fix makes the fast path require an equality key
// and lets every other predicate shape fall through to the SeqScan path, which
// handles all of them.
func TestUpdateOverRangeIndexScanDoesNotPanic(t *testing.T) {
	ctx, _, cleanup := newHOTFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		"CREATE TABLE ranged (id int, label text, qty int)",
		"INSERT INTO ranged VALUES (1, 'a', 10), (2, 'b', 20), (3, 'c', 30), (4, 'd', 40), (5, 'e', 50)",
		// Index built after the inserts so the backfill captures every row.
		"CREATE INDEX ranged_id_idx ON ranged (id)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// The statement that used to panic. A range predicate over the indexed
	// column is what makes the planner emit an IndexScan with Key == nil.
	if err := runDDL(t, ctx, "UPDATE ranged SET qty = qty + 7 WHERE id BETWEEN 2 AND 4"); err != nil {
		t.Fatalf("ranged UPDATE: %v", err)
	}

	// Correctness, not just survival: exactly the three in-range rows move.
	rows := runQuery(t, ctx, "SELECT id, qty FROM ranged ORDER BY id")
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}
	want := []int64{10, 27, 37, 47, 50}
	for i, r := range rows {
		if got := r[1].Int; got != want[i] {
			t.Errorf("row id=%d qty=%d, want %d", i+1, got, want[i])
		}
	}

	// The equality fast path must still work — the guard narrows it, and a
	// regression that disabled it entirely would be invisible above.
	if err := runDDL(t, ctx, "UPDATE ranged SET qty = 999 WHERE id = 1"); err != nil {
		t.Fatalf("equality UPDATE: %v", err)
	}
	eq := runQuery(t, ctx, "SELECT qty FROM ranged WHERE id = 1")
	if len(eq) != 1 || eq[0][0].Int != 999 {
		t.Fatalf("equality UPDATE result = %v, want a single 999", eq)
	}
}
