package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// sortedAggPlan builds an Aggregate over a Values child for the sorted-strategy
// tests. kCols are the 0-based child column indexes of the GROUP BY key, vCol is
// the column summed by the second aggregate, and sortedRows must arrive already
// ordered by the key columns (ascending), as the S8 contract requires.
func sortedAggPlan(strategy planner.AggStrategy, kCols []int, vCol int, rows [][]planner.Expr) *planner.Aggregate {
	groupExprs := make([]planner.Expr, 0, len(kCols))
	for _, ci := range kCols {
		groupExprs = append(groupExprs, &planner.ColumnRef{Index: ci, Name: "k", Type: catalog.Type{Name: "int4"}})
	}
	return &planner.Aggregate{
		Strategy:   strategy,
		Child:      &planner.Values{Rows: rows},
		GroupExprs: groupExprs,
		Aggs: []planner.AggregateCall{
			{Name: "count", Star: true, Type: catalog.Type{Name: "int8"}},
			{Name: "sum", Arg: &planner.ColumnRef{Index: vCol, Name: "v", Type: catalog.Type{Name: "int4"}}, Type: catalog.Type{Name: "int8"}},
		},
	}
}

func runAgg(t *testing.T, plan *planner.Aggregate) []Row {
	t.Helper()
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rows, err := Run(op, NewContext())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rows
}

// TestAggSortedGrouping pins the sorted (AGG_SORTED) path's boundary
// correctness: several groups, adjacent equal keys, and a multi-column key all
// collapse exactly as the hash path would, and the output rows appear in key
// (input) order. The hash path's M0097-0117 pre-sort orders by GroupExprs the
// same way, so this expectation is strategy-independent.
func TestAggSortedGrouping(t *testing.T) {
	rows := [][]planner.Expr{
		{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 10}},
		{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 20}}, // adjacent equal key
		{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 5}},  // same k1, new k2
		{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 7}},
		{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 3}}, // adjacent equal key
		{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 3}, &planner.IntegerConst{Value: 9}},
		{&planner.IntegerConst{Value: 3}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 4}},
	}
	got := runAgg(t, sortedAggPlan(planner.AggStrategySorted, []int{0, 1}, 2, rows))

	want := []struct {
		k1, k2, cnt, sum int64
	}{
		{1, 1, 2, 30},
		{1, 2, 1, 5},
		{2, 1, 2, 10},
		{2, 3, 1, 9},
		{3, 1, 1, 4},
	}
	if len(got) != len(want) {
		t.Fatalf("rows=%d want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		r := got[i]
		if r[0].Kind != KindInt || r[0].Int != w.k1 ||
			r[1].Kind != KindInt || r[1].Int != w.k2 ||
			r[2].Kind != KindInt || r[2].Int != w.cnt ||
			r[3].Kind != KindInt || r[3].Int != w.sum {
			t.Errorf("row[%d]=%+v want k1=%d k2=%d count=%d sum=%d", i, r, w.k1, w.k2, w.cnt, w.sum)
		}
	}
}

// TestAggSortedNullKey pins that NULL group keys group together into one group
// under the sorted strategy — datumKey renders NULL identically to the hash
// path, and element-wise parts comparison groups all NULL-keyed rows together.
func TestAggSortedNullKey(t *testing.T) {
	rows := [][]planner.Expr{
		{&planner.NullConst{}, &planner.IntegerConst{Value: 10}},
		{&planner.NullConst{}, &planner.IntegerConst{Value: 20}},
		{&planner.NullConst{}, &planner.IntegerConst{Value: 30}},
	}
	got := runAgg(t, sortedAggPlan(planner.AggStrategySorted, []int{0}, 1, rows))
	if len(got) != 1 {
		t.Fatalf("rows=%d want 1 (all NULL keys one group): %+v", len(got), got)
	}
	if !got[0][0].IsNull() {
		t.Errorf("group key=%+v want NULL", got[0][0])
	}
	if got[0][1].Int != 3 || got[0][2].Int != 60 {
		t.Errorf("row=%+v want count=3 sum=60", got[0])
	}
}

// TestAggSortedHashParity is the sibling-path discipline check: the same input,
// run once with the sorted strategy over a key-ordered child and once with the
// hash strategy, must produce byte-identical output rows in byte-identical
// order. Both strategies group identically (shared evalGroupExprs/parts) and
// the hash path's M0097-0117 pre-sort orders by GroupExprs exactly as the
// sorted path's input-order emission does.
func TestAggSortedHashParity(t *testing.T) {
	rows := [][]planner.Expr{
		{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 10}},
		{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 20}},
		{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 5}},
		{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 7}},
		{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 3}},
		{&planner.IntegerConst{Value: 3}, &planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 4}},
	}
	sorted := runAgg(t, sortedAggPlan(planner.AggStrategySorted, []int{0, 1}, 2, rows))
	hashed := runAgg(t, sortedAggPlan(planner.AggStrategyHashed, []int{0, 1}, 2, rows))

	if len(sorted) != len(hashed) {
		t.Fatalf("sorted=%d rows, hashed=%d rows", len(sorted), len(hashed))
	}
	for i := range sorted {
		if len(sorted[i]) != len(hashed[i]) {
			t.Fatalf("row[%d]: sorted=%d cols, hashed=%d cols", i, len(sorted[i]), len(hashed[i]))
		}
		for j := range sorted[i] {
			a, b := sorted[i][j], hashed[i][j]
			if a.Kind != b.Kind {
				t.Errorf("row[%d] col[%d] kind: sorted=%d hashed=%d", i, j, a.Kind, b.Kind)
				continue
			}
			if a.IsNull() != b.IsNull() {
				t.Errorf("row[%d] col[%d] null: sorted=%v hashed=%v", i, j, a.IsNull(), b.IsNull())
				continue
			}
			if !a.IsNull() {
				// The values are plain int4/int8 datums; compare the int payload.
				if a.Int != b.Int {
					t.Errorf("row[%d] col[%d]: sorted=%d hashed=%d", i, j, a.Int, b.Int)
				}
			}
		}
	}
}

// TestAggSortedEmptyInput pins the empty-input contract for both strategies: an
// empty child yields zero output rows and no panic. (An ungrouped aggregate
// would emit one row, but the sorted gate requires len(GroupExprs) > 0.)
func TestAggSortedEmptyInput(t *testing.T) {
	empty := [][]planner.Expr{}
	if got := runAgg(t, sortedAggPlan(planner.AggStrategySorted, []int{0}, 1, empty)); len(got) != 0 {
		t.Errorf("sorted empty input produced %d rows, want 0", len(got))
	}
	if got := runAgg(t, sortedAggPlan(planner.AggStrategyHashed, []int{0}, 1, empty)); len(got) != 0 {
		t.Errorf("hashed empty input produced %d rows, want 0", len(got))
	}
}

// TestExplainAggregateStrategyLabel pins the S8 EXPLAIN label plumbing: a
// Strategy=Sorted grouped aggregate renders PG's AGG_SORTED label
// "GroupAggregate", the (zero-value) Strategy=Hashed aggregate keeps
// "HashAggregate", and the ungrouped branch is unaffected ("Aggregate").
func TestExplainAggregateStrategyLabel(t *testing.T) {
	groupExprs := []planner.Expr{&planner.ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}}
	aggs := []planner.AggregateCall{{Name: "count", Star: true, Type: catalog.Type{Name: "int8"}}}

	if got := describePlan(&planner.Aggregate{Strategy: planner.AggStrategySorted, GroupExprs: groupExprs, Aggs: aggs}, nil); got != "GroupAggregate" {
		t.Errorf("sorted label=%q want GroupAggregate", got)
	}
	if got := describePlan(&planner.Aggregate{GroupExprs: groupExprs, Aggs: aggs}, nil); got != "HashAggregate" {
		t.Errorf("hashed label=%q want HashAggregate", got)
	}
	if got := describePlan(&planner.Aggregate{Aggs: aggs}, nil); got != "Aggregate" {
		t.Errorf("ungrouped label=%q want Aggregate", got)
	}
}
