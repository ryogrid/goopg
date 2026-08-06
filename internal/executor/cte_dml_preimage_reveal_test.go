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

// TestCTEWriteWriteRunsOuterStatementFirst is the flipped form of what
// M0125-0053 filed as TestCTEPreImageWriteWriteDivergesFromPG. It was written
// to pin a residue and to be inverted once the order was fixed; M0125-0054
// inverted it.
//
// When the outer statement WRITES a row a CTE also writes, the answer is
// decided by ORDER, not visibility: whichever sub-statement runs SECOND finds
// the row already stamped by this same command and declines it. PG runs the
// main plan first and only afterwards the data-modifying CTEs nothing pulled
// from, in ExecPostprocessPlan (postgres/src/backend/executor/execMain.c), so
// the OUTER statement is the one that gets there first. goopg ran every DML
// CTE before it even built the outer body, so it answered the mirror image of
// each case below.
//
// Every expectation here was captured on live PG 18.3 (port 65432) on
// 2026-08-06, before the fix was written.
//
// The PG documentation does call two sub-statements modifying the same row
// unpredictable, so the old answers were defensible — but "unpredictable" is
// not licence to be predictably opposite, and the ordering these witnesses
// expose is the same one that decides the ctid case in
// TestCTEDMLRunsAfterOuterStatement.
func TestCTEWriteWriteRunsOuterStatementFirst(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Witness 1 — outer DELETE of a row the CTE UPDATEs. The outer DELETE
	// runs first and removes row 5; the CTE's UPDATE then finds nothing.
	if err := runDDL(t, ctx, "CREATE TABLE pz6 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE pz6: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz6 VALUES (5), (2)")
	got := runDMLRows(t, ctx,
		"WITH x AS (UPDATE pz6 SET a = 6 WHERE a = 5 RETURNING a) DELETE FROM pz6 WHERE a = 5 RETURNING a")
	if !equalInts(rowsFirstInts(got), []int64{5}) {
		t.Errorf("outer DELETE = %v, want [5]", rowsFirstInts(got))
	}
	if after := tableInts(t, ctx, "pz6"); !equalInts(after, []int64{2}) {
		t.Errorf("pz6 after = %v, want [2]", after)
	}

	// Witness 2 — outer UPDATE of a row the CTE DELETEs.
	if err := runDDL(t, ctx, "CREATE TABLE pz7 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE pz7: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz7 VALUES (1), (2)")
	got = runDMLRows(t, ctx,
		"WITH x AS (DELETE FROM pz7 WHERE a = 1 RETURNING a) UPDATE pz7 SET a = a + 100 WHERE a = 1 RETURNING a")
	if !equalInts(rowsFirstInts(got), []int64{101}) {
		t.Errorf("outer UPDATE = %v, want [101]", rowsFirstInts(got))
	}
	if after := tableInts(t, ctx, "pz7"); !equalInts(after, []int64{2, 101}) {
		t.Errorf("pz7 after = %v, want [2 101]", after)
	}

	// Witness 3 — outer DELETE of a row the CTE also DELETEs. PG: DELETE 1.
	if err := runDDL(t, ctx, "CREATE TABLE pz8 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE pz8: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz8 VALUES (1), (2)")
	got = runDMLRows(t, ctx,
		"WITH x AS (DELETE FROM pz8 WHERE a = 1 RETURNING a) DELETE FROM pz8 WHERE a = 1 RETURNING a")
	if !equalInts(rowsFirstInts(got), []int64{1}) {
		t.Errorf("outer DELETE = %v, want [1]", rowsFirstInts(got))
	}
	if after := tableInts(t, ctx, "pz8"); !equalInts(after, []int64{2}) {
		t.Errorf("pz8 after = %v, want [2]", after)
	}

	// Witness 4 — outer UPDATE of a row the CTE also UPDATEs. PG: UPDATE 1,
	// table [2 7] — the outer's value wins, the CTE's 6 is never written.
	if err := runDDL(t, ctx, "CREATE TABLE pz9 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE pz9: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO pz9 VALUES (5), (2)")
	got = runDMLRows(t, ctx,
		"WITH x AS (UPDATE pz9 SET a = 6 WHERE a = 5 RETURNING a) UPDATE pz9 SET a = 7 WHERE a = 5 RETURNING a")
	if !equalInts(rowsFirstInts(got), []int64{7}) {
		t.Errorf("outer UPDATE = %v, want [7]", rowsFirstInts(got))
	}
	if after := tableInts(t, ctx, "pz9"); !equalInts(after, []int64{2, 7}) {
		t.Errorf("pz9 after = %v, want [2 7]", after)
	}
}

// TestCTEDMLRunsAfterOuterStatement is the ordering witness itself, stripped of
// any write-write conflict: two INSERTs into the same empty table, so the only
// thing the result records is which ran first. On live PG 18.3 (2026-08-06)
// 'outer' lands at ctid (0,1) and 'cte' at (0,2) — heap order, which an
// unordered SELECT reproduces.
func TestCTEDMLRunsAfterOuterStatement(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE ord_log (tag text)"); err != nil {
		t.Fatalf("CREATE TABLE ord_log: %v", err)
	}
	got := runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO ord_log VALUES ('cte') RETURNING tag) "+
			"INSERT INTO ord_log SELECT 'outer' RETURNING tag")
	if len(got) != 1 || string(got[0][0].Buf) != "outer" {
		t.Fatalf("outer INSERT RETURNING = %v, want one row 'outer'", got)
	}
	rows := runDMLRows(t, ctx, "SELECT tag FROM ord_log")
	tags := make([]string, len(rows))
	for i, r := range rows {
		tags[i] = string(r[0].Buf)
	}
	if len(tags) != 2 || tags[0] != "outer" || tags[1] != "cte" {
		t.Errorf("heap order = %v, want [outer cte] — PG puts 'outer' at ctid (0,1)", tags)
	}
}

// TestCTEDMLReferencedByOuterRunsFirst is the other half of PG's model: a CTE
// the main plan READS cannot be deferred, because the CteScan that pulls from
// it drives it. Both orders must therefore coexist in one statement — x is
// read and runs first, y is unread and runs last.
func TestCTEDMLReferencedByOuterRunsFirst(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE ord_src (a int)"); err != nil {
		t.Fatalf("CREATE TABLE ord_src: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE ord_dst (tag text)"); err != nil {
		t.Fatalf("CREATE TABLE ord_dst: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO ord_src VALUES (1)")

	got := runDMLRows(t, ctx,
		"WITH x AS (DELETE FROM ord_src RETURNING a), "+
			"y AS (INSERT INTO ord_dst VALUES ('deferred') RETURNING tag) "+
			"INSERT INTO ord_dst SELECT 'from-x-' || x.a FROM x RETURNING tag")
	if len(got) != 1 || string(got[0][0].Buf) != "from-x-1" {
		t.Fatalf("outer INSERT RETURNING = %v, want one row 'from-x-1' — "+
			"the referenced CTE x must run before the outer body reads it", got)
	}
	rows := runDMLRows(t, ctx, "SELECT tag FROM ord_dst")
	tags := make([]string, len(rows))
	for i, r := range rows {
		tags[i] = string(r[0].Buf)
	}
	if len(tags) != 2 || tags[0] != "from-x-1" || tags[1] != "deferred" {
		t.Errorf("heap order = %v, want [from-x-1 deferred]: x is pulled from so it "+
			"runs on demand, y is not so it runs in the post-body phase", tags)
	}
}

// TestCTEDeferredDMLRunsInReverseDeclarationOrder pins the order of the
// post-body sweep itself. ExecInitModifyTable files each aux ModifyTable with
// lcons (postgres/src/backend/executor/nodeModifyTable.c), so
// es_auxmodifytables is reverse initialization order and ExecPostprocessPlan
// walks it head-first — the LAST-declared unreferenced CTE runs first.
//
// Captured on live PG 18.3 (2026-08-06): with an outer body that writes
// nothing, three unreferenced INSERT CTEs a, b, c land at ctid (0,1)=c,
// (0,2)=b, (0,3)=a; and when b alone is read by the outer body, b runs on
// demand and the sweep then takes c before a → (0,1)=b, (0,2)=c, (0,3)=a.
func TestCTEDeferredDMLRunsInReverseDeclarationOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	heapTags := func(table string) []string {
		rows := runDMLRows(t, ctx, "SELECT tag FROM "+table)
		tags := make([]string, len(rows))
		for i, r := range rows {
			tags[i] = string(r[0].Buf)
		}
		return tags
	}

	if err := runDDL(t, ctx, "CREATE TABLE ordq (tag text)"); err != nil {
		t.Fatalf("CREATE TABLE ordq: %v", err)
	}
	runDMLRows(t, ctx,
		"WITH a AS (INSERT INTO ordq VALUES ('a') RETURNING tag), "+
			"b AS (INSERT INTO ordq VALUES ('b') RETURNING tag), "+
			"c AS (INSERT INTO ordq VALUES ('c') RETURNING tag) SELECT 1")
	if got := heapTags("ordq"); len(got) != 3 || got[0] != "c" || got[1] != "b" || got[2] != "a" {
		t.Errorf("ordq heap order = %v, want [c b a]", got)
	}

	// b is referenced, so it runs on demand — before the sweep takes c and a.
	if err := runDDL(t, ctx, "CREATE TABLE ordr (tag text)"); err != nil {
		t.Fatalf("CREATE TABLE ordr: %v", err)
	}
	runDMLRows(t, ctx,
		"WITH a AS (INSERT INTO ordr VALUES ('a') RETURNING tag), "+
			"b AS (INSERT INTO ordr VALUES ('b') RETURNING tag), "+
			"c AS (INSERT INTO ordr VALUES ('c') RETURNING tag) SELECT count(*) FROM b")
	if got := heapTags("ordr"); len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "a" {
		t.Errorf("ordr heap order = %v, want [b c a]", got)
	}
}

// TestCTEDeferredCTECannotSeeOuterInserts is the fence obligation the reorder
// created. Once the outer statement runs first, its own writes have to enter
// CTEWriteFence, or a deferred CTE would see rows PG's cmin test hides from
// it. PG 18.3 (2026-08-06) leaves fs_dst with exactly one row: the CTE's
// SELECT over fs_src finds only the pre-existing row 1, never the outer's 2.
func TestCTEDeferredCTECannotSeeOuterInserts(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE fs_src (a int)"); err != nil {
		t.Fatalf("CREATE TABLE fs_src: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE fs_dst (a int)"); err != nil {
		t.Fatalf("CREATE TABLE fs_dst: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO fs_src VALUES (1)")

	runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO fs_dst SELECT a FROM fs_src RETURNING a) "+
			"INSERT INTO fs_src VALUES (2)")
	if after := tableInts(t, ctx, "fs_dst"); !equalInts(after, []int64{1}) {
		t.Errorf("fs_dst = %v, want [1] — the deferred CTE must not see the outer INSERT's row", after)
	}
	if after := tableInts(t, ctx, "fs_src"); !equalInts(after, []int64{1, 2}) {
		t.Errorf("fs_src = %v, want [1 2]", after)
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
