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
	if ceiling := inputRows * int64(len(sets)); want > ceiling {
		want = ceiling
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

// TestEstimateAggregateGroupingSetsCeiling pins the CORRECTED ceiling.
//
// The first version of this clamped to `inputRows` and cited upstream for
// it. Upstream does not clamp the total at all — get_number_of_groups
// accumulates per-rollup with the clamp inside each per-set call — and the
// bound was wrong anyway: each of k sets can emit up to inputRows groups,
// so the sound ceiling is k*inputRows. A 4-row input under ROLLUP(a,b) can
// legitimately produce 6 output rows, which the old clamp cut back to 4 —
// under-stating, which is the same direction as the bug this function
// exists to fix.
func TestEstimateAggregateGroupingSetsCeiling(t *testing.T) {
	child := gsChild(3)
	exprs := []Expr{gsCol(0, "a"), gsCol(1, "b")}
	sets := [][]int{{0, 1}, {0}, {1}, {}}
	got := estimateAggregate(gsAgg(child, exprs, sets))
	in := EstimateRows(child)
	if ceiling := in * int64(len(sets)); got > ceiling {
		t.Fatalf("estimate %d exceeds the k*inputRows ceiling %d", got, ceiling)
	}
	if got < 1 {
		t.Fatalf("estimate %d is below the floor of 1", got)
	}
}

// TestEstimateAggregateMayExceedInputRows is the counter-pin for the
// correction: the estimate is ALLOWED to exceed the input row count,
// because k sets each emit their own groups. If someone reinstates the
// inputRows clamp, this fails.
func TestEstimateAggregateMayExceedInputRows(t *testing.T) {
	// 4 distinct rows, ROLLUP(a, b) = {a,b}, {a}, {} — the grouped output
	// is legitimately larger than the input.
	child := gsChild(4)
	exprs := []Expr{gsCol(0, "a"), gsCol(1, "b")}
	sets := [][]int{{0, 1}, {0}, {}}
	got := estimateAggregate(gsAgg(child, exprs, sets))
	in := EstimateRows(child)
	sum := estimateNumGroups([]Expr{exprs[0], exprs[1]}, child, in) +
		estimateNumGroups([]Expr{exprs[0]}, child, in) +
		estimateNumGroups(nil, child, in)
	if sum <= in {
		t.Skipf("fixture does not exercise the case: per-set sum %d <= input %d", sum, in)
	}
	if got <= in {
		t.Fatalf("estimate %d was clamped to the input row count %d; k sets may "+
			"legitimately emit more rows than the input has", got, in)
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
