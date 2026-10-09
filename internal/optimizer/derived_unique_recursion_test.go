package optimizer

import "testing"

// TestDerivedLeafUniqueThroughWindowLevel pins M0146-0120 against
// examine_simple_variable's RTE_SUBQUERY recursion (selfuncs.c): TPC-DS Q44's
// `asceding.item_sk` reads v11, a window over v1, which groups by
// `ss_item_sk` alone, so the column is isunique and get_variable_numdistinct
// gives it the leaf's tuples. goopg stopped at the top level (no GROUP BY
// there) — and the stripped leaf reached the search as a bare Project, which
// derivedLeafUniqueCols did not accept — so Memoize priced its cache with the
// 200-distinct default and the item probes lost their Memoize.
func TestDerivedLeafUniqueThroughWindowLevel(t *testing.T) {
	two := Schema{{Name: "item_sk"}, {Name: "rank_col"}}
	scan := &SeqScan{schema: Schema{{Name: "ss_item_sk"}, {Name: "ss_net_profit"}}}
	// v1: SELECT ss_item_sk item_sk, avg(...) rank_col ... GROUP BY ss_item_sk HAVING ...
	agg := &Aggregate{Child: scan, GroupExprs: []Expr{&ColumnRef{Index: 0}}, schema: two}
	v1 := &Project{Child: &Filter{Child: agg}, Targets: []Expr{&ColumnRef{Index: 0}, &ColumnRef{Index: 1}}, schema: two}
	// v11: SELECT item_sk, rank() OVER (...) rnk FROM v1
	sub := &SubqueryScan{Child: v1, schema: two}
	win := &WindowAgg{Child: &Sort{Child: sub}, schema: Schema{{Name: "item_sk"}, {Name: "rank_col"}, {Name: "rank"}}}
	v11 := &Project{Child: win, Targets: []Expr{&ColumnRef{Index: 0}, &ColumnRef{Index: 2}}, schema: Schema{{Name: "item_sk"}, {Name: "rnk"}}}

	got := derivedLeafUniqueCols(v11)
	if !got["item_sk"] || got["rnk"] {
		t.Fatalf("unique outputs = %v, want item_sk only (rnk is a window output)", got)
	}
	// A base relation below the walk is never isunique.
	plain := &Project{Child: scan, Targets: []Expr{&ColumnRef{Index: 0}}, schema: Schema{{Name: "ss_item_sk"}}}
	if got := derivedLeafUniqueCols(plain); len(got) != 0 {
		t.Fatalf("a projected base scan reported unique outputs %v", got)
	}
	// A two-key GROUP BY stops the recursion without isunique.
	agg2 := &Aggregate{Child: scan, GroupExprs: []Expr{&ColumnRef{Index: 0}, &ColumnRef{Index: 1}}, schema: two}
	top := &Project{Child: &WindowAgg{Child: agg2, schema: two}, Targets: []Expr{&ColumnRef{Index: 0}}, schema: Schema{{Name: "item_sk"}}}
	if got := derivedLeafUniqueCols(top); len(got) != 0 {
		t.Fatalf("a multi-key GROUP BY reported unique outputs %v", got)
	}
}
