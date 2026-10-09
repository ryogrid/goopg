package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// C-10a (docs/design/planner-c10a-grouping-sets-scope/DESIGN.md, Decision 2)
// pins the SECOND, independent gate that keeps grouping sets on the hashed
// strategy.
//
// The four planner declines named by the item
// (groupagg_hashagg.go:64, groupagg_presorted.go:47, groupagg_indexorder.go:68,
// parallel_agg.go:117) are C-15's retirement checklist. The design decision
// rests on what happens when they go away: the answer is "nothing wrong",
// because aggregateOp.Open re-tests `GroupingSets == nil` before routing to
// openSorted (operators_join_agg.go:2091), and openSorted's own invariant
// comment (:2538-2540) states it may only be reached with GroupingSets == nil
// and setIdx always 0.
//
// These tests pin that: a grouping-sets node stamped AggStrategySorted — the
// state a retired decline would leave behind — still computes every level, and
// produces byte-identical rows to the same node stamped AggStrategyHashed.
// If a future change makes Strategy reach the grouping-sets path without an
// AGG_SORTED/AGG_MIXED implementation behind it, these go red instead of
// silently dropping levels.

// rollupAggPlan builds `SELECT k1, k2, count(*), sum(v) ... GROUP BY ROLLUP(k1,k2)`
// over a Values child, with the strategy stamp under test.
func rollupAggPlan(strategy optimizer.AggStrategy, rows [][]optimizer.Expr) *optimizer.Aggregate {
	return &optimizer.Aggregate{
		Strategy: strategy,
		Child:    &optimizer.Values{Rows: rows},
		GroupExprs: []optimizer.Expr{
			&optimizer.ColumnRef{Index: 0, Name: "k1", Type: catalog.Type{Name: "int4"}},
			&optimizer.ColumnRef{Index: 1, Name: "k2", Type: catalog.Type{Name: "int4"}},
		},
		// ROLLUP(k1,k2) = (k1,k2), (k1), () — ascending indexes into GroupExprs.
		GroupingSets: [][]int{{0, 1}, {0}, {}},
		Aggs: []optimizer.AggregateCall{
			{Name: "count", Star: true, Type: catalog.Type{Name: "int8"}},
			{Name: "sum", Arg: &optimizer.ColumnRef{Index: 2, Name: "v", Type: catalog.Type{Name: "int4"}}, Type: catalog.Type{Name: "int8"}},
		},
	}
}

func rollupInput() [][]optimizer.Expr {
	return [][]optimizer.Expr{
		{&optimizer.IntegerConst{Value: 1}, &optimizer.IntegerConst{Value: 1}, &optimizer.IntegerConst{Value: 10}},
		{&optimizer.IntegerConst{Value: 1}, &optimizer.IntegerConst{Value: 1}, &optimizer.IntegerConst{Value: 20}},
		{&optimizer.IntegerConst{Value: 1}, &optimizer.IntegerConst{Value: 2}, &optimizer.IntegerConst{Value: 5}},
		{&optimizer.IntegerConst{Value: 2}, &optimizer.IntegerConst{Value: 1}, &optimizer.IntegerConst{Value: 7}},
	}
}

// rollupWant is the three-level answer, in the order the hash path emits it
// (per set index, then by group key — operators_join_agg.go:2298-2338).
// nil means the level rolled that dimension away, which is emitted NULL.
type rollupRow struct {
	k1, k2   *int64
	cnt, sum int64
}

func i64(v int64) *int64 { return &v }

func rollupWant() []rollupRow {
	return []rollupRow{
		// set 0 — (k1, k2)
		{i64(1), i64(1), 2, 30},
		{i64(1), i64(2), 1, 5},
		{i64(2), i64(1), 1, 7},
		// set 1 — (k1)
		{i64(1), nil, 3, 35},
		{i64(2), nil, 1, 7},
		// set 2 — () grand total
		{nil, nil, 4, 42},
	}
}

func checkRollup(t *testing.T, got []Row) {
	t.Helper()
	checkRollupRows(t, got, rollupWant())
}

// rollupWantSorted is the same answer in AGG_SORTED's one-pass order
// (M0146-0020a): along the rollup (k1, k2) each group closes when its prefix
// changes, so each k1's detail groups precede its (k1) group, and the grand
// total is last — the order PG 18.3 emits `GROUP BY ROLLUP(k1, k2)` in.
func rollupWantSorted() []rollupRow {
	return []rollupRow{
		{i64(1), i64(1), 2, 30},
		{i64(1), i64(2), 1, 5},
		{i64(1), nil, 3, 35},
		{i64(2), i64(1), 1, 7},
		{i64(2), nil, 1, 7},
		{nil, nil, 4, 42},
	}
}

func checkRollupRows(t *testing.T, got []Row, want []rollupRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rows=%d want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		r := got[i]
		if len(r) < 4 {
			t.Fatalf("row[%d] has %d columns, want >= 4: %+v", i, len(r), r)
		}
		checkKey := func(col int, want *int64) {
			if want == nil {
				if r[col].Kind != KindNull {
					t.Errorf("row[%d] col%d = %+v, want NULL", i, col, r[col])
				}
				return
			}
			if r[col].Kind != KindInt || r[col].Int != *want {
				t.Errorf("row[%d] col%d = %+v, want %d", i, col, r[col], *want)
			}
		}
		checkKey(0, w.k1)
		checkKey(1, w.k2)
		if r[2].Kind != KindInt || r[2].Int != w.cnt {
			t.Errorf("row[%d] count = %+v, want %d", i, r[2], w.cnt)
		}
		if r[3].Kind != KindInt || r[3].Int != w.sum {
			t.Errorf("row[%d] sum = %+v, want %d", i, r[3], w.sum)
		}
	}
}

// TestC10aGroupingSetsHashedBaseline is the control: today's production shape.
func TestC10aGroupingSetsHashedBaseline(t *testing.T) {
	checkRollup(t, runAgg(t, rollupAggPlan(optimizer.AggStrategyHashed, rollupInput())))
}

// TestC10aGroupingSetsSortedRollupComputesEveryLevel is the pin, restated
// for M0146-0020a. A grouping-sets node stamped AggStrategySorted must still
// compute every level: Open's second gate keeps it off openSorted (which
// holds one current group with setIdx 0 and would return one level). It now
// emits the levels in AGG_SORTED's rollup order rather than per set.
func TestC10aGroupingSetsSortedRollupComputesEveryLevel(t *testing.T) {
	checkRollupRows(t, runAgg(t, rollupAggPlan(optimizer.AggStrategySorted, rollupInput())), rollupWantSorted())
}

// TestC10aGroupingSetsStrategiesAgreeOnRows states the consequence
// positively: the two strategies compute the same rows; only their emission
// order differs (per set for the hashed node, rollup order for the sorted).
func TestC10aGroupingSetsStrategiesAgreeOnRows(t *testing.T) {
	hashed := runAgg(t, rollupAggPlan(optimizer.AggStrategyHashed, rollupInput()))
	sorted := runAgg(t, rollupAggPlan(optimizer.AggStrategySorted, rollupInput()))
	if len(hashed) != len(sorted) {
		t.Fatalf("hashed=%d rows, sorted=%d rows", len(hashed), len(sorted))
	}
	key := func(r Row) string { return fmt.Sprint(r) }
	seen := map[string]int{}
	for _, r := range hashed {
		seen[key(r)]++
	}
	for _, r := range sorted {
		seen[key(r)]--
	}
	for k, n := range seen {
		if n != 0 {
			t.Errorf("row %s: hashed and sorted multiplicities differ by %d", k, n)
		}
	}
}
