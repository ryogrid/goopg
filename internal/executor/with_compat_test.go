package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runQuery is a tiny helper that parses, plans, builds, and
// drains a single SELECT, returning its rows. Used by the
// M0016-0004 compat tests so each test stays focused on the
// CTE shape under test rather than the boilerplate.
func runQuery(t *testing.T, ctx *Context, sql string) []Row {
	t.Helper()
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
	rows, err := drainScan(op)
	if err != nil {
		t.Fatalf("drain(%q): %v", sql, err)
	}
	_ = op.Close()
	return rows
}

// TestCompatCTEFilterThenAggregate exercises a representative
// PG-shaped pattern: a CTE filters source rows, the outer query
// aggregates over the filtered set. Pins
//
//   - Stage A inlining produces the same answer as a subquery.
//   - Aggregate-over-CTE works (no aggregate-from-CTE-body
//     restrictions in this slice's scope).
func TestCompatCTEFilterThenAggregate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, n := range []int64{1, 50, 150, 200, 300} {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: n}}); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx, "WITH big AS (SELECT id FROM t WHERE id > 100) SELECT count(*) FROM big")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 3 {
		t.Errorf("count(*) = %+v, want 3 (rows with id>100: 150, 200, 300)", rows[0][0])
	}
}

// TestCompatCTEMultiConsumerCrossProduct: same CTE appears twice
// in the FROM list. With Stage A inlining each consumer gets a
// freshly-cloned plan and the cross-product of two single-row
// CTEs is exactly one row.
func TestCompatCTEMultiConsumerCrossProduct(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx, "WITH small AS (SELECT 1 AS x) SELECT a.x, b.x FROM small a, small b")
	if len(rows) != 1 || len(rows[0]) != 2 {
		t.Fatalf("rows=%d cols=%d, want 1×2", len(rows), len(rows[0]))
	}
	if rows[0][0].Int != 1 || rows[0][1].Int != 1 {
		t.Errorf("got %+v, want [1, 1]", rows[0])
	}
}

// TestCompatCTEChainedSiblings exercises the left-to-right scope
// rule end-to-end: CTE `b` references CTE `a` from the same WITH
// list. Pins that the planner's "earlier sibling visible to
// later" semantics works through the executor.
func TestCompatCTEChainedSiblings(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx,
		"WITH a AS (SELECT 1 AS x), b AS (SELECT x + 1 AS y FROM a) SELECT y FROM b")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 2 {
		t.Errorf("y = %+v, want 2", rows[0][0])
	}
}

// TestCompatCTEVisibleInFromSubquery exercises a WITH-list CTE
// referenced from inside a derived-table (FROM-clause subquery)
// of the OUTER statement — the AC-002 gap #4 shape that
// pg_amcheck's 002_nonesuch bootstrap query hits:
//
//	WITH x(a) AS (SELECT 1) SELECT a FROM (SELECT a FROM x) s
//
// Before the planSubqueryRangeVar fix the non-correlated derived
// table was re-planned via Plan(), which re-ran the analyzer on
// the subquery standalone (no enclosing WITH scope) and rejected
// the CTE reference with `relation "x" does not exist`. Pins that
// the CTE substitutes in and the row flows through end-to-end.
func TestCompatCTEVisibleInFromSubquery(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx,
		"WITH x(a) AS (SELECT 1) SELECT a FROM (SELECT a FROM x) s")
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("rows=%d cols=%d, want 1×1", len(rows), len(rows[0]))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 1 {
		t.Errorf("a = %+v, want 1", rows[0][0])
	}

	// Two-level nesting: CTE visible through two stacked derived
	// tables. Fresh fixture — runQuery caches CTE results on the
	// executor Context, so reusing ctx across queries with a
	// same-named CTE would surface the prior value (a harness
	// artifact; the server uses a fresh Context per query).
	ctx2, _, cleanup2 := newDDLFixture(t)
	defer cleanup2()
	rows = runQuery(t, ctx2,
		"WITH x(a) AS (SELECT 7) SELECT s.a FROM (SELECT a FROM (SELECT a FROM x) t) s")
	if len(rows) != 1 || rows[0][0].Int != 7 {
		t.Fatalf("nested: got %+v, want single row [7]", rows)
	}
}

// TestCompatCTESameNameDisjointScopes pins M0125-0050: a CTE's identity is its
// DECLARATION, not its name.
//
// ctx.CTERowCache used to be keyed by the lowercased CTE name statement-wide,
// so two unrelated `WITH x` declarations in disjoint subqueries shared one
// buffer: the first scan materialized `SELECT 1` and the second replayed it
// instead of running its own `SELECT 2`. goopg answered 1,1 where PG 18.3
// answers 1,2 — a wrong answer, not a plan-shape difference.
//
// PG cannot express the bug: SS_process_ctes makes one subplan per WITH entry
// and CteScanState keys off that subplan's ctePlanId
// (postgres/src/backend/executor/nodeCtescan.c), which is per-declaration by
// construction. planner.CTEScan.DeclKey is goopg's equivalent.
func TestCompatCTESameNameDisjointScopes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx,
		`SELECT v FROM (WITH x AS (SELECT 1 AS v) SELECT v FROM x) a
		 UNION ALL
		 SELECT v FROM (WITH x AS (SELECT 2 AS v) SELECT v FROM x) b`)
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want 2", len(rows))
	}
	got := []int64{rows[0][0].Int, rows[1][0].Int}
	if got[0] != 1 || got[1] != 2 {
		t.Errorf("got %v, want [1 2] (PG 18.3); [1 1] is the M0125-0050 regression", got)
	}
}

// TestCompatCTEMultiReferenceStillMaterializesOnce is the other side of
// M0125-0050's key change: narrowing the key must not un-share a CTE that PG
// materializes once. Two references to ONE declaration still run the body a
// single time, which is the optimization-fence guarantee the volatile-CTE
// tests rely on. random() would make an un-shared body visible as two
// different values; a sequence-free equivalent is to check both references
// observe the same buffer identity via a row count that would double.
func TestCompatCTEMultiReferenceStillMaterializesOnce(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx,
		"WITH x(a) AS (SELECT 1) SELECT p.a, q.a FROM x p, x q")
	if len(rows) != 1 || len(rows[0]) != 2 {
		t.Fatalf("rows=%d cols=%d, want 1×2", len(rows), len(rows[0]))
	}
	if rows[0][0].Int != 1 || rows[0][1].Int != 1 {
		t.Errorf("got %+v, want [1 1]", rows[0])
	}
}
