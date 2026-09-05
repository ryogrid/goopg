package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// gsChild builds a Values node of n single-column rows. Values is used
// rather than a table scan so the estimate depends only on the row count
// and the group expressions, with no ANALYZE statistics in play — these
// tests are about the SUM over sets, not about estimate_num_groups itself.
func gsChild(n int) Node {
	rows := make([][]Expr, n)
	for i := range rows {
		rows[i] = []Expr{&IntegerConst{Value: int64(i)}}
	}
	return &Values{Rows: rows}
}

func gsCol(idx int, name string) Expr {
	return &ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: "int4"}}
}

// C-10a: estimateAggregate must SUM estimate_num_groups over the grouping
// sets, as PG accumulates dNumGroups in create_grouping_paths. Before the
// fix it estimated a.GroupExprs alone — the deduplicated union of every set
// — so an N-set query was priced as one set, under-stating by up to N×.
//
// These tests use a child whose row count is large enough that the
// input-row clamp does not mask the arithmetic, and they compare the
// grouping-sets answer against the single-set answers it is built from,
// rather than against hand-computed constants that would pin whatever
// estimate_num_groups happens to return today.

func gsAgg(child Node, groupExprs []Expr, sets [][]int) *Aggregate {
	return &Aggregate{Child: child, GroupExprs: groupExprs, GroupingSets: sets}
}

func TestEstimateAggregateSumsOverGroupingSets(t *testing.T) {
	child := gsChild(10000)
	a := gsCol(0, "a")
	b := gsCol(1, "b")
	exprs := []Expr{a, b}

	// ROLLUP(a, b) = {a,b}, {a}, {}
	sets := [][]int{{0, 1}, {0}, {}}
	got := estimateAggregate(gsAgg(child, exprs, sets))

	inputRows := EstimateRows(child)
	want := estimateNumGroups([]Expr{a, b}, child, inputRows) +
		estimateNumGroups([]Expr{a}, child, inputRows) +
		estimateNumGroups(nil, child, inputRows)
	if want > inputRows {
		want = inputRows
	}
	if got != want {
		t.Fatalf("estimateAggregate over 3 sets = %d, want the sum %d", got, want)
	}

	// And it must exceed the single-set answer it replaced, or the fix does
	// nothing on the shape it exists for.
	single := estimateNumGroups(exprs, child, inputRows)
	if got <= single && single < inputRows {
		t.Fatalf("grouping-sets estimate %d did not exceed the union-only "+
			"estimate %d; the under-count this fixes is still present", got, single)
	}
}

// TestEstimateAggregatePlainUnchanged pins that the ordinary single-set
// aggregate — the overwhelming majority — takes exactly the old path. A
// regression here would move estimates on every grouped query in both
// suites, not just the 12 TPC-DS grouping-sets ones.
func TestEstimateAggregatePlainUnchanged(t *testing.T) {
	child := gsChild(10000)
	exprs := []Expr{gsCol(0, "a"), gsCol(1, "b")}
	plain := &Aggregate{Child: child, GroupExprs: exprs}
	if got, want := estimateAggregate(plain),
		estimateNumGroups(exprs, child, EstimateRows(child)); got != want {
		t.Fatalf("plain aggregate = %d, want %d (must take the old path)", got, want)
	}
}

// TestEstimateAggregateGroupingSetsClamped pins the input-row clamp: N sets
// must not multiply a small input into a larger output than the input has
// rows, which is unreachable for a grouping aggregate.
func TestEstimateAggregateGroupingSetsClamped(t *testing.T) {
	child := gsChild(3)
	exprs := []Expr{gsCol(0, "a"), gsCol(1, "b")}
	sets := [][]int{{0, 1}, {0}, {1}, {}}
	got := estimateAggregate(gsAgg(child, exprs, sets))
	if in := EstimateRows(child); got > in {
		t.Fatalf("estimate %d exceeds the input row count %d", got, in)
	}
	if got < 1 {
		t.Fatalf("estimate %d is below the clamp floor of 1", got)
	}
}

// TestEstimateAggregateOutOfRangeSetFailsSafe pins the fail-safe: a set
// index that does not address GroupExprs means the two disagree, and the
// estimator must fall back to the whole-union answer rather than silently
// dropping a dimension — dropping one would under-state further, in the
// same direction as the bug being fixed.
func TestEstimateAggregateOutOfRangeSetFailsSafe(t *testing.T) {
	child := gsChild(10000)
	exprs := []Expr{gsCol(0, "a"), gsCol(1, "b")}
	bad := gsAgg(child, exprs, [][]int{{0, 1}, {7}})
	if got, want := estimateAggregate(bad),
		estimateNumGroups(exprs, child, EstimateRows(child)); got != want {
		t.Fatalf("out-of-range set gave %d, want the union fallback %d", got, want)
	}
}
