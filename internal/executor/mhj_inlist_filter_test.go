package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// M0125-0046 executor half. The MHJ filter classifier walkColumnRefs
// vetoed EVERY InExpr, so a literal IN-list in mh.Filters was routed to
// leafFilters (evaluated only after all tables bind) instead of the
// step where its columns first bind, and — the planner half — the
// residual-Filter push (pushResidualQualsIntoMHJTables) now wraps a
// member scan with an IN-list Filter that the executor must evaluate on
// the build input. The end-to-end test pins the joint behaviour with a
// dimension IN-list over the RC-1b geometry (dimension created first =
// smallest OID, listed last in FROM).
func TestMHJInListDimensionPredicate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

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
	ins("dim", [][]int64{{1, 2000, 8}, {2, 2001, 8}, {3, 2001, 9}, {4, 1999, 1}})
	ins("sales", [][]int64{{10, 1, 100, 5}, {11, 2, 200, 6}, {12, 3, 300, 7}})
	ins("rets", [][]int64{{10, 1, 100, 2}, {11, 2, 200, 2}, {12, 3, 300, 3}})

	// dy IN (2001, 1999) selects dk {2,3,4}; rets r_ret values {2,2,3}
	// all land in that set and match sales on all three keys => 3 rows.
	rows := runQuery(t, ctx, `
		SELECT count(*) FROM sales, rets, dim
		WHERE s_tick = r_tick AND s_item = r_item AND s_cust = r_cust
		  AND r_ret = dk AND dy IN (2001, 1999)`)
	if len(rows) != 1 {
		t.Fatalf("want 1 result row, got %d", len(rows))
	}
	if got := rows[0][0].Int; got != 3 {
		t.Fatalf("IN-list dimension predicate: count = %d, want 3", got)
	}

	// NOT IN is the same InExpr with Negated set; dy NOT IN (2001)
	// selects dk {1,4}, matching only r_ret=... none ({2,2,3} minus
	// {2,3} = 0 rows... r_ret values are 2,2,3 and dk 1,4 remain => 0).
	rows = runQuery(t, ctx, `
		SELECT count(*) FROM sales, rets, dim
		WHERE s_tick = r_tick AND s_item = r_item AND s_cust = r_cust
		  AND r_ret = dk AND dy NOT IN (2001, 2000)`)
	if len(rows) != 1 {
		t.Fatalf("want 1 result row, got %d", len(rows))
	}
	if got := rows[0][0].Int; got != 0 {
		t.Fatalf("NOT IN dimension predicate: count = %d, want 0", got)
	}
}

// walkColumnRefs is the executor sibling of the planner's
// walkColumnRefsImpl (Hard-won Rule #2) — this pins its contract.
func TestMHJWalkColumnRefsInExprContract(t *testing.T) {
	collect := func(e planner.Expr) (idxs []int, outer bool) {
		walkColumnRefs(e, func(i int) { idxs = append(idxs, i) }, func() { outer = true })
		return
	}

	// Literal IN-list: refs in Operand AND List are visited, no veto.
	in := &planner.InExpr{
		Operand: &planner.ColumnRef{Index: 3},
		List:    []planner.Expr{&planner.ColumnRef{Index: 7}, &planner.StringConst{Value: "x"}},
	}
	idxs, outer := collect(in)
	if outer {
		t.Fatal("literal IN-list must not veto — that re-introduces the leafFilters detour")
	}
	if len(idxs) != 2 || idxs[0] != 3 || idxs[1] != 7 {
		t.Fatalf("literal IN-list refs = %v, want [3 7]", idxs)
	}

	// Subquery form (Plan != nil): vetoed, inner plan not walked.
	sub := &planner.InExpr{
		Operand: &planner.ColumnRef{Index: 3},
		Plan:    &planner.SeqScan{},
	}
	if _, outer := collect(sub); !outer {
		t.Fatal("IN (subquery) must veto — the inner plan is another coordinate space")
	}

	// Unenumerated kind: fail-closed default arm vetoes instead of
	// silently hiding the subtree's refs (tpcds-round2-fixes §0).
	if _, outer := collect(&planner.ArraySubqueryExpr{}); !outer {
		t.Fatal("unenumerated Expr kind must veto (fail-closed default arm)")
	}
}
