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
