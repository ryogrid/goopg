package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestMHJSingleSourceFilterCoordinateSpace pins the RC-1b fix
// (docs/design/tpcds-round2-fixes/README.md §3): a single-table
// predicate in a 3-way comma join must be evaluated against ITS OWN
// table after the MultiHashJoin rewrite, not against whichever table
// happens to cover its stale index range.
//
// The bug: rewriteMultiWayChain sorts MultiHashJoin.Tables by table
// OID, while mh.Filters still carried FROM-cumulative ColumnRef
// indices. pushSingleSourceFiltersIntoMHJTables then attributed
// conjuncts by index range computed from the OID-sorted tables — so
// whenever FROM order differed from creation (OID) order, a
// dimension's predicate could be pushed onto a different table's scan
// and silently zero the result (TPC-DS Q47: 0 vs 100; Q50: 0 vs 6).
//
// This test reproduces the geometry deliberately: the dimension is
// created FIRST (smallest OID) but listed LAST in FROM, exactly the
// store_sales/store_returns/date_dim shape. Before the fix the count
// came back 0; after (push runs post-remap + positional name
// validation) it returns the true match count.
func TestMHJSingleSourceFilterCoordinateSpace(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Creation order = OID order: dim first, facts after — the
	// reverse of the FROM order used below.
	for _, ddl := range []string{
		"CREATE TABLE dim (dk int, dy int, dm int)",
		"CREATE TABLE sales (s_tick int, s_item int, s_cust int, s_sold int)",
		"CREATE TABLE rets (r_tick int, r_item int, r_cust int, r_ret int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	ins := func(table string, rows [][]int64) {
		tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: table})
		rel := ctx.Catalog.RelFileNode(tbl)
		for _, r := range rows {
			row := make(Row, len(r))
			for i, v := range r {
				row[i] = Datum{Kind: KindInt, Int: v}
			}
			if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
				t.Fatal(err)
			}
		}
	}
	// dim: dk 1..4; only dk=2 has (dy=2001, dm=8).
	ins("dim", [][]int64{{1, 2000, 8}, {2, 2001, 8}, {3, 2001, 9}, {4, 1999, 1}})
	// sales/rets join on (tick, item, cust); rets reference dim via r_ret.
	ins("sales", [][]int64{{10, 1, 100, 5}, {11, 2, 200, 6}, {12, 3, 300, 7}})
	ins("rets", [][]int64{{10, 1, 100, 2}, {11, 2, 200, 2}, {12, 3, 300, 3}})

	// FROM order: sales, rets, dim — dim LAST in FROM but FIRST by OID.
	// True answer: rets rows with r_ret -> dim(dy=2001, dm=8) = dk 2,
	// matched by sales on all three keys => 2 rows.
	rows := runQuery(t, ctx, `
		SELECT count(*) FROM sales, rets, dim
		WHERE s_tick = r_tick AND s_item = r_item AND s_cust = r_cust
		  AND r_ret = dk AND dy = 2001 AND dm = 8`)
	if len(rows) != 1 {
		t.Fatalf("want 1 result row, got %d", len(rows))
	}
	if got := rows[0][0].Int; got != 2 {
		t.Fatalf("3-way join with dimension predicate: count = %d, want 2 (0 is the RC-1b wrong-scan signature)", got)
	}

	// The Q47 variant: an OR-of-ANDs dimension predicate (the shape that
	// pushed date_dim's OR filter onto store's scan).
	rows = runQuery(t, ctx, `
		SELECT count(*) FROM sales, rets, dim
		WHERE s_tick = r_tick AND s_item = r_item AND s_cust = r_cust
		  AND r_ret = dk
		  AND (dy = 2001 OR (dy = 2000 AND dm = 8) OR (dy = 1999 AND dm = 1))`)
	if len(rows) != 1 {
		t.Fatalf("want 1 result row, got %d", len(rows))
	}
	// dk=1 (2000,8) matches branch 2; dk=2 (2001,8) and dk=3 (2001,9)
	// match branch 1; dk=4 (1999,1) matches branch 3. rets r_ret values
	// {2,2,3} all match => 3 rows.
	if got := rows[0][0].Int; got != 3 {
		t.Fatalf("OR-of-ANDs dimension predicate: count = %d, want 3", got)
	}
}
