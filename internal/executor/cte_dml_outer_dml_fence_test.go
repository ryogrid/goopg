package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// M0125-0052 — an outer INSERT/UPDATE/DELETE must not see the rows its own
// data-modifying CTE wrote.
//
// PostgreSQL runs every sub-statement of a data-modifying WITH on the SAME
// statement snapshot and under the SAME command id, so "the sub-statements …
// can't see one another's effects on the target tables" (the WITH
// documentation; the mechanism is `estate->es_snapshot` +
// `estate->es_output_cid` shared by every ModifyTable node of the query, see
// `postgres/src/backend/executor/execMain.c:InitPlan` and the TM_SelfModified
// arms of `ExecUpdate`/`ExecDelete` in `nodeModifyTable.c`).
//
// goopg already implements the read half of that rule with ctx.CTEWriteFence,
// but before this fix only the outer *SELECT* consulted it: an outer DML's
// scan ran after cteDMLPrefixOp restored the snapshot with no fence in hand,
// so `WITH x AS (INSERT INTO dm VALUES (15) RETURNING a) DELETE FROM dm WHERE
// a = 15` deleted the row the CTE had just inserted — goopg answered 1 row and
// emptied the table where PG answers 0 rows and keeps it.
//
// Oracle values below were captured on live PG 18.3 (port 65432) on
// 2026-08-06 before the fix was written.

// runDMLRows runs one statement through parser -> planner -> executor on the
// fixture context and returns its RETURNING rows. It mirrors the per-statement
// reset the server's dispatch loop performs (dispatch.go: "Per-statement
// reset"), because the fixture reuses a single Context across statements while
// a real session gets the fence cleared between them.
func runDMLRows(t *testing.T, ctx *Context, sql string) []Row {
	t.Helper()
	ctx.CTEWriteFence = nil
	ctx.CTEXmaxReveal = nil
	ctx.InDMLCTE = false
	ctx.CmdID = 0
	ctx.CTERowCache = nil
	ctx.MaterializedCTEs = nil

	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(%q): %v", sql, err)
	}
	defer op.Close()
	var rows []Row
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next(%q): %v", sql, err)
		}
		rows = append(rows, append(Row(nil), slot.Row()...))
	}
	return rows
}

// tableInts returns column `a` of every live row of the fixture table, sorted.
func tableInts(t *testing.T, ctx *Context, table string) []int64 {
	t.Helper()
	rows := runDMLRows(t, ctx, "SELECT a FROM "+table+" ORDER BY a")
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if len(r) == 0 || r[0].Kind == KindNull {
			continue
		}
		out = append(out, r[0].Int)
	}
	return out
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCTEDMLOuterDeleteCannotSeeCTEInsert: PG 18.3 answers 0 rows and keeps
// the row (verified live). Pre-fix goopg answered 1 row and emptied the table.
func TestCTEDMLOuterDeleteCannotSeeCTEInsert(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dm15 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO dm15 VALUES (1)")

	got := runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO dm15 VALUES (15) RETURNING a) DELETE FROM dm15 WHERE a = 15 RETURNING a")
	if len(got) != 0 {
		t.Errorf("outer DELETE returned %d row(s), want 0 — it saw the CTE's insert", len(got))
	}
	if after := tableInts(t, ctx, "dm15"); !equalInts(after, []int64{1, 15}) {
		t.Errorf("table after = %v, want [1 15]", after)
	}
}

// TestCTEDMLOuterUpdateCannotSeeCTEInsert: PG 18.3 answers UPDATE 0 and leaves
// the CTE-inserted row unmodified.
func TestCTEDMLOuterUpdateCannotSeeCTEInsert(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dm16 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO dm16 VALUES (1)")

	got := runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO dm16 VALUES (16) RETURNING a) UPDATE dm16 SET a = a + 100 WHERE a = 16 RETURNING a")
	if len(got) != 0 {
		t.Errorf("outer UPDATE returned %d row(s), want 0 — it saw the CTE's insert", len(got))
	}
	if after := tableInts(t, ctx, "dm16"); !equalInts(after, []int64{1, 16}) {
		t.Errorf("table after = %v, want [1 16]", after)
	}
}

// TestCTEDMLOuterInsertSelectCannotSeeCTEInsert: the outer INSERT's *read* of
// the target table is fenced too. PG 18.3: the CTE inserts 17, the outer
// INSERT … SELECT reads only the pre-statement rows {1,15,16} and so copies
// 1015,1016 — never 1017.
func TestCTEDMLOuterInsertSelectCannotSeeCTEInsert(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dm17 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO dm17 VALUES (1), (15), (16)")

	got := runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO dm17 VALUES (17) RETURNING a) "+
			"INSERT INTO dm17 SELECT a + 1000 FROM dm17 WHERE a >= 15 RETURNING a")
	if len(got) != 2 {
		t.Fatalf("outer INSERT returned %d row(s), want 2 — it read the CTE's insert", len(got))
	}
	if after := tableInts(t, ctx, "dm17"); !equalInts(after, []int64{1, 15, 16, 17, 1015, 1016}) {
		t.Errorf("table after = %v, want [1 15 16 17 1015 1016]", after)
	}
}

// TestCTEDMLOuterSelectCannotSeeCTEInsert: the outer SELECT half of the same
// rule. It was broken for the same reason — plain INSERT never registered its
// rows in the fence, only ON CONFLICT and the UPDATE paths did. PG 18.3:
// count(*) is 1 (the pre-statement row) while the CTE's own RETURNING still
// yields the inserted row, and both rows are in the table afterwards.
func TestCTEDMLOuterSelectCannotSeeCTEInsert(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE fs (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO fs VALUES (1)")

	got := runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO fs VALUES (15) RETURNING a) SELECT count(*) FROM fs")
	if len(got) != 1 || got[0][0].Int != 1 {
		t.Errorf("outer SELECT count = %v, want 1 — it saw the CTE's insert", got)
	}
	// The CTE's own RETURNING output is not fenced: it is replayed from
	// ctx.MaterializedCTEs, never re-read from the heap.
	got = runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO fs VALUES (16) RETURNING a) SELECT a FROM x")
	if len(got) != 1 || got[0][0].Int != 16 {
		t.Errorf("CTE RETURNING = %v, want one row [16]", got)
	}
	if after := tableInts(t, ctx, "fs"); !equalInts(after, []int64{1, 15, 16}) {
		t.Errorf("table after = %v, want [1 15 16]", after)
	}
}

// TestCTEDMLFenceHoldsUnderIndexScan: the fence must not be plan-shape
// dependent. With a primary key on the target, the outer query reaches the
// heap through an index-only scan rather than seqScanOp; PG 18.3 still answers
// 0 rows and the CTE's row still lands in the table.
func TestCTEDMLFenceHoldsUnderIndexScan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE fc (a int PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO fc VALUES (1)")

	got := runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO fc VALUES (15) RETURNING a) SELECT a FROM fc WHERE a = 15")
	if len(got) != 0 {
		t.Errorf("outer indexed SELECT returned %v, want no rows", got)
	}
	got = runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO fc VALUES (16) RETURNING a) UPDATE fc SET a = a + 100 WHERE a = 16 RETURNING a")
	if len(got) != 0 {
		t.Errorf("outer indexed UPDATE returned %v, want no rows", got)
	}
	if after := tableInts(t, ctx, "fc"); !equalInts(after, []int64{1, 15, 16}) {
		t.Errorf("table after = %v, want [1 15 16]", after)
	}
}

// TestCTEDMLFenceIsRelationQualified: the fence must not hide an unrelated
// table's row. An ItemPointer alone is not unique across relations — {block 0,
// offset 1} exists in every table — so a relation-blind fence would make the
// outer DELETE on `fb` skip the row that happens to sit where the CTE's INSERT
// into `fa` landed. PG 18.3 deletes fb's row (DELETE 1, table left empty).
func TestCTEDMLFenceIsRelationQualified(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE fa (a int)"); err != nil {
		t.Fatalf("CREATE TABLE fa: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE fb (a int)"); err != nil {
		t.Fatalf("CREATE TABLE fb: %v", err)
	}
	// fb's only row lands at the same {block, offset} the CTE's insert into
	// fa will occupy, which is exactly the collision a relation-blind key hits.
	runDMLRows(t, ctx, "INSERT INTO fb VALUES (7)")

	got := runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO fa VALUES (1) RETURNING a) DELETE FROM fb WHERE a = 7 RETURNING a")
	if len(got) != 1 {
		t.Errorf("outer DELETE on fb returned %d row(s), want 1 — the fence hid another table's row", len(got))
	}
	if after := tableInts(t, ctx, "fb"); len(after) != 0 {
		t.Errorf("fb after = %v, want empty", after)
	}
	if after := tableInts(t, ctx, "fa"); !equalInts(after, []int64{1}) {
		t.Errorf("fa after = %v, want [1]", after)
	}
}

// TestCTEDMLSiblingCTEsCannotSeeEachOther: the sibling case. PG 18.3 returns
// only x's row (30) — y's DELETE cannot see the row x inserted — and the row
// survives in the table.
func TestCTEDMLSiblingCTEsCannotSeeEachOther(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dm30 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runDMLRows(t, ctx, "INSERT INTO dm30 VALUES (1), (2)")

	got := runDMLRows(t, ctx,
		"WITH x AS (INSERT INTO dm30 VALUES (30) RETURNING a), "+
			"y AS (DELETE FROM dm30 WHERE a = 30 RETURNING a) "+
			"SELECT a FROM x UNION ALL SELECT a FROM y")
	if len(got) != 1 || got[0][0].Int != 30 {
		t.Errorf("sibling CTEs returned %v, want exactly one row [30]", got)
	}
	if after := tableInts(t, ctx, "dm30"); !equalInts(after, []int64{1, 2, 30}) {
		t.Errorf("table after = %v, want [1 2 30]", after)
	}
}
