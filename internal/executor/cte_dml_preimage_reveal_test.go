package executor

import "testing"

// M0125-0053 — the rest of a statement still sees the PRE-IMAGE of a row its
// own data-modifying CTE removed.
//
// M0125-0052 gave the DML-CTE write fence its missing half: rows a CTE ADDED
// are hidden from every later scan of the statement. But the fence can only
// ever SKIP rows, and PostgreSQL's rule is symmetric — "the sub-statements …
// can't see one another's effects on the target tables" applies to deletions
// too. A CTE DELETE stamps xmax with our own XID and a CTE UPDATE does the
// same to the old version (whose new version is then fenced), so in goopg the
// row vanished from the rest of the statement where PG still shows it.
//
// PG needs no second mechanism for this: the same es_output_cid that hides a
// sibling's insert through cmin reveals the pre-image through cmax
// (postgres/src/backend/access/heap/heapam_visibility.c,
// HeapTupleSatisfiesMVCC). goopg's heap has no per-tuple command id, so
// ctx.CTEXmaxReveal stands in for the cmax test — the mirror image of
// ctx.CTEWriteFence.
//
// Only READ scans consult it. PG's ExecDelete/ExecUpdate take the
// TM_SelfModified arm for such a tuple and, when cmax equals es_output_cid,
// return NULL without touching the row (nodeModifyTable.c), so a DML target
// scan that simply does not find it produces the same row count and heap
// state. See TestCTEPreImageWriteWriteDivergesFromPG for the residue of that
// choice.
//
// Every oracle value below was captured on live PG 18.3 (port 65432) on
// 2026-08-06 before the fix was written.

// TestCTEPreImageVisibleToOuterSelect is the filed witness #1: PG answers 2
// (the pre-CTE row count), goopg answered 1.
func TestCTEPreImageVisibleToOuterSelect(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE pz1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz1 VALUES (1), (2)")

	got := runDMLRows(t, ctx,
		"WITH x AS (DELETE FROM pz1 WHERE a = 1 RETURNING a) SELECT count(*) FROM pz1")
	if len(got) != 1 || got[0][0].Int != 2 {
		t.Errorf("outer SELECT count = %v, want 2 — the CTE's DELETE hid the pre-image", got)
	}
	// The delete itself still takes effect once the statement ends.
	if after := tableInts(t, ctx, "pz1"); !equalInts(after, []int64{2}) {
		t.Errorf("table after = %v, want [2]", after)
	}
}

// TestCTEPreImageVisibleAfterCTEUpdate is the filed witness #2, extended with
// the content check: PG shows the row's OLD column values, not just its old
// key. The pre-image is the old heap version itself, so this holds by
// construction — the assertion guards a future fix that tried to reconstruct
// the row from the new version instead.
func TestCTEPreImageVisibleAfterCTEUpdate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE pz2 (a int, t text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz2 VALUES (2, 'two'), (5, 'five')")

	got := runDMLRows(t, ctx,
		"WITH x AS (UPDATE pz2 SET a = 6, t = 'six' WHERE a = 5 RETURNING a) "+
			"SELECT a, t FROM pz2 ORDER BY a")
	if len(got) != 2 {
		t.Fatalf("outer SELECT returned %d row(s), want 2 (PG: [2 two], [5 five])", len(got))
	}
	if got[0][0].Int != 2 || string(got[0][1].Buf) != "two" {
		t.Errorf("row 0 = (%d, %q), want (2, \"two\")", got[0][0].Int, got[0][1].Buf)
	}
	if got[1][0].Int != 5 || string(got[1][1].Buf) != "five" {
		t.Errorf("row 1 = (%d, %q), want (5, \"five\") — the pre-image must carry the OLD values",
			got[1][0].Int, got[1][1].Buf)
	}
	if after := tableInts(t, ctx, "pz2"); !equalInts(after, []int64{2, 6}) {
		t.Errorf("table after = %v, want [2 6]", after)
	}
}

// TestCTEPreImageHoldsUnderIndexScan: the reveal must not be plan-shape
// dependent, the same requirement M0125-0052 discovered for the fence. With a
// primary key the outer query reaches the heap through an index / index-only
// scan whose HOT-chain walk does its own visibility test, so the reveal has to
// live INSIDE that walk. PG 18.3 answers 1.
func TestCTEPreImageHoldsUnderIndexScan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE pz3 (a int PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz3 VALUES (1), (2)")

	got := runDMLRows(t, ctx,
		"WITH x AS (DELETE FROM pz3 WHERE a = 1 RETURNING a) SELECT a FROM pz3 WHERE a = 1")
	if len(got) != 1 || got[0][0].Int != 1 {
		t.Errorf("outer indexed SELECT = %v, want one row [1]", got)
	}
	if after := tableInts(t, ctx, "pz3"); !equalInts(after, []int64{2}) {
		t.Errorf("table after = %v, want [2]", after)
	}
}

// TestCTEPreImageVisibleToOuterInsertSelect: the read behind an outer INSERT …
// SELECT is a read like any other. PG copies both pre-CTE rows (1001, 1002)
// even though the CTE deleted one of them.
func TestCTEPreImageVisibleToOuterInsertSelect(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE pz4 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz4 VALUES (1), (2)")

	got := runDMLRows(t, ctx,
		"WITH x AS (DELETE FROM pz4 WHERE a = 1 RETURNING a) "+
			"INSERT INTO pz4 SELECT a + 1000 FROM pz4 RETURNING a")
	if !equalInts(rowsFirstInts(got), []int64{1001, 1002}) {
		t.Errorf("outer INSERT RETURNING = %v, want [1001 1002]", rowsFirstInts(got))
	}
	if after := tableInts(t, ctx, "pz4"); !equalInts(after, []int64{2, 1001, 1002}) {
		t.Errorf("table after = %v, want [2 1001 1002]", after)
	}
}

// TestCTEPreImageSiblingDeleteIsNoOp: two sibling CTEs deleting the same row.
// The second one's target scan must NOT consult the reveal set — if it did it
// would find the pre-image and delete it a second time, double-counting the
// row. PG 18.3: x deletes 1 row, y deletes 0, one row leaves the table.
func TestCTEPreImageSiblingDeleteIsNoOp(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE pz5 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz5 VALUES (1), (2)")

	got := runDMLRows(t, ctx,
		"WITH x AS (DELETE FROM pz5 WHERE a = 1 RETURNING a), "+
			"y AS (DELETE FROM pz5 WHERE a = 1 RETURNING a) "+
			"SELECT (SELECT count(*) FROM x), (SELECT count(*) FROM y)")
	if len(got) != 1 || got[0][0].Int != 1 || got[0][1].Int != 0 {
		t.Errorf("sibling deletes = %v, want x=1 y=0", got)
	}
	if after := tableInts(t, ctx, "pz5"); !equalInts(after, []int64{2}) {
		t.Errorf("table after = %v, want [2]", after)
	}
}

// TestCTEPreImageWriteWriteDivergesFromPG pins the residue this fix does NOT
// close, so a later loop sees it flip rather than discovering it again.
//
// When the outer statement WRITES the same row a CTE already wrote, goopg and
// PG disagree — not about visibility but about ORDER. PG runs the main plan
// first and only then runs the not-yet-completed data-modifying CTEs, in
// ExecPostprocessPlan (postgres/src/backend/executor/execMain.c); proved on
// live PG 18.3 by `WITH x AS (INSERT INTO log VALUES ('cte') RETURNING tag)
// INSERT INTO log SELECT 'outer' RETURNING tag`, where 'outer' lands at ctid
// (0,1) and 'cte' at (0,2). goopg's cteDMLPrefixOp runs every DML CTE before
// building the outer body, so the outer write finds the row already stamped
// and declines it, where PG's outer write got there first.
//
// The reads above are unaffected — that is exactly why they are the defined
// half. The PG documentation calls the write-write case unpredictable ("the
// sub-statements … see the same snapshot, so … the effects of such statements
// on the target tables are not visible"; two sub-statements modifying the same
// row is explicitly not guaranteed), so goopg's answers here are defensible,
// but they are NOT PG's. Filed as the successor item; deferral ledger
// 2026-08-06.
func TestCTEPreImageWriteWriteDivergesFromPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Outer DELETE of a row the CTE UPDATEd. PG: DELETE 1, table [2].
	if err := runDDL(t, ctx, "CREATE TABLE pz6 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE pz6: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz6 VALUES (5), (2)")
	got := runDMLRows(t, ctx,
		"WITH x AS (UPDATE pz6 SET a = 6 WHERE a = 5 RETURNING a) DELETE FROM pz6 WHERE a = 5 RETURNING a")
	if len(got) != 0 {
		t.Errorf("outer DELETE = %v, want 0 rows (goopg order; PG answers [5])", rowsFirstInts(got))
	}
	if after := tableInts(t, ctx, "pz6"); !equalInts(after, []int64{2, 6}) {
		t.Errorf("pz6 after = %v, want [2 6] (goopg order; PG answers [2])", after)
	}

	// Outer UPDATE of a row the CTE DELETEd. PG: UPDATE 1, table [2 101].
	if err := runDDL(t, ctx, "CREATE TABLE pz7 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE pz7: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz7 VALUES (1), (2)")
	got = runDMLRows(t, ctx,
		"WITH x AS (DELETE FROM pz7 WHERE a = 1 RETURNING a) UPDATE pz7 SET a = a + 100 WHERE a = 1 RETURNING a")
	if len(got) != 0 {
		t.Errorf("outer UPDATE = %v, want 0 rows (goopg order; PG answers [101])", rowsFirstInts(got))
	}
	if after := tableInts(t, ctx, "pz7"); !equalInts(after, []int64{2}) {
		t.Errorf("pz7 after = %v, want [2] (goopg order; PG answers [2 101])", after)
	}
}

// rowsFirstInts projects column 0 of each row, skipping NULLs.
func rowsFirstInts(rs []Row) []int64 {
	out := make([]int64, 0, len(rs))
	for _, r := range rs {
		if len(r) > 0 && r[0].Kind != KindNull {
			out = append(out, r[0].Int)
		}
	}
	return out
}
