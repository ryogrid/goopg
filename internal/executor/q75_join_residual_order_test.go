package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestQ75ShapeSingleSideQualsPrecedeJoinResidual is the end-to-end pin
// for M0125-0004 on the reduced TPC-DS Q75 shape.
//
// Q75's final block self-joins the `all_sales` CTE, restricting each
// side to its own year and comparing the two sides with a division:
//
//	FROM all_sales curr_yr, all_sales prev_yr
//	WHERE … AND curr_yr.d_year=2002 AND prev_yr.d_year=2002-1
//	  AND CAST(curr_yr.sales_cnt AS DECIMAL(17,2))
//	      /CAST(prev_yr.sales_cnt AS DECIMAL(17,2))<0.9
//
// The CTE carries no year filter of its own, so with correct data a
// later year holds a group whose `sales_cnt` is 0 — PG has it too
// (`zerogroups = 1` at d_year=2003 on both engines). goopg evaluated the
// side-mixed division as the hash-join residual on every matched pair,
// BEFORE the outer Filter's year equalities could exclude the pair, and
// raised `division by zero`; PG attaches each single-relation qual to
// that relation's baserestrictinfo
// (distribute_restrictinfo_to_rels, initsplan.c), so its division never
// meets the zero.
//
// This fixture reproduces that exactly: group 9 exists only in year
// 2003 and has cnt = 0, so it can never pair with a 2001 row under the
// year restrictions — but it IS a hash-join match on `g` if the
// restrictions have not yet run.
//
// The test asserts two things, and the second is the one that matters:
// no error, AND the surviving row is the one PG returns. A fix that
// suppressed the error by dropping rows would pass the first check only.
func TestQ75ShapeSingleSideQualsPrecedeJoinResidual(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE sales (y int, g int, cnt int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "sales"})
	rel := ctx.Catalog.RelFileNode(tbl)
	rows := [][3]int64{
		// g=1: 2001 → 100, 2002 → 50. 50/100 = 0.5 < 0.9, so it is the
		// one row the query must return.
		{2001, 1, 100},
		{2002, 1, 50},
		// g=2: 2001 → 100, 2002 → 100. Ratio 1.0, filtered out by <0.9.
		{2001, 2, 100},
		{2002, 2, 100},
		// g=9: the zero-denominator group. Present ONLY in 2003, so
		// under PG's qual placement it is excluded at scan level and the
		// division never sees it. Under the pre-fix residual-first order
		// it reached `… / 0` and aborted the whole query.
		{2003, 9, 0},
		{2002, 9, 7},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: r[0]}, {Kind: KindInt, Int: r[1]}, {Kind: KindInt, Int: r[2]},
		}); err != nil {
			t.Fatal(err)
		}
	}

	const sql = `WITH all_sales AS (
	                 SELECT y, g, sum(cnt) AS cnt FROM sales GROUP BY y, g)
	             SELECT curr_yr.g, curr_yr.cnt, prev_yr.cnt
	             FROM all_sales curr_yr, all_sales prev_yr
	             WHERE curr_yr.g = prev_yr.g
	               AND curr_yr.y = 2002
	               AND prev_yr.y = 2001
	               AND CAST(curr_yr.cnt AS DECIMAL(17,2))
	                   / CAST(prev_yr.cnt AS DECIMAL(17,2)) < 0.9`

	got, err := runQueryWithErr(ctx, sql)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "division by zero") {
			t.Fatalf("division by zero: the join residual ran before the single-side "+
				"year restrictions reached the join inputs (M0125-0004): %v", err)
		}
		t.Fatalf("query failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 (g=1); rows=%v", len(got), got)
	}
	if got[0][0].Int != 1 || got[0][1].Int != 50 || got[0][2].Int != 100 {
		t.Errorf("row = (g=%d, curr=%d, prev=%d), want (1, 50, 100)",
			got[0][0].Int, got[0][1].Int, got[0][2].Int)
	}
}
