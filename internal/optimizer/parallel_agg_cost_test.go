package optimizer

// Chapter 11 — the partial-aggregation split cost model.
//
// The gate exists because P9 split every decomposable aggregate. The case it
// must catch is a GROUP BY whose group count approaches its INPUT row count:
// TPC-H Q18's inner aggregate, `select l_orderkey from lineitem group by
// l_orderkey`, turns every one of 6M input rows into a partial state and
// merges each through a single contended mutex, reducing nothing.
//
// The whole decision reduces to one quantity, rho = Gw*d/R, which with the
// clamp Gw = min(ndistinct, R/d) collapses to min(1, f*d) where f is the
// distinct-to-rows FRACTION. That is why these tests set NDistinctFrac and
// never a row count: the model needs no absolute quantities at all, which is
// what lets it work on a server that has not restored TableStats.RowCount.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// aggFixture builds an aggregate over a scan whose group column has the given
// distinct-to-rows fraction.
func aggFixture(t *testing.T, frac float64, nAggs, nGroupCols int) *Aggregate {
	t.Helper()
	cat := catalog.NewInMemory()
	cols := []catalog.Column{
		{Name: "g0", Type: catalog.Type{Name: "int4"}},
		{Name: "g1", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "int4"}},
	}
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "agg_t"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	colStats := make([]catalog.ColumnStats, len(cols))
	for i := range colStats {
		colStats[i].NDistinctFrac = frac
	}
	tbl.Stats = &catalog.TableStats{Columns: colStats}

	scan := &SeqScan{Table: tbl, schema: Schema{{Name: "g0"}, {Name: "g1"}, {Name: "v"}}}
	agg := &Aggregate{Child: scan}
	for i := 0; i < nGroupCols; i++ {
		agg.GroupExprs = append(agg.GroupExprs, &ColumnRef{Index: i, Name: cols[i].Name})
	}
	for i := 0; i < nAggs; i++ {
		agg.Aggs = append(agg.Aggs, AggregateCall{Name: "sum"})
	}
	return agg
}

// TestSplitGateRefusesNearUniqueGrouping is the case the gate exists for.
//
// A near-unique group column means every input row becomes its own partial
// state, so the split ships as many states through the accumulator mutex as
// there were rows and reduces nothing.
func TestSplitGateRefusesNearUniqueGrouping(t *testing.T) {
	for _, workers := range []int{2, 4, 8} {
		agg := aggFixture(t, 1.0, 1, 1) // f = 1: every row its own group
		if splitAggregateIsProfitable(agg, workers, true) {
			t.Errorf("workers=%d: split accepted for a near-unique group key; "+
				"this is the Q18 shape, where the split reduces nothing and "+
				"serialises every input row through one mutex", workers)
		}
	}
}

// TestSplitGateAcceptsLowCardinalityGrouping is the case the split was built
// for — Q1's four groups over ~5.9M rows.
func TestSplitGateAcceptsLowCardinalityGrouping(t *testing.T) {
	for _, workers := range []int{2, 4, 8} {
		agg := aggFixture(t, 1e-4, 8, 2) // Q1: 8 aggregates, 2 group columns
		if !splitAggregateIsProfitable(agg, workers, true) {
			t.Errorf("workers=%d: split refused for a low-cardinality grouping; "+
				"this is Q1, measured at 7.15s -> 4.72s", workers)
		}
	}
}

// TestSplitGateIsMonotonicInTheRatio: the decision must not oscillate as the
// reduction gets worse. Once it refuses, it must keep refusing.
func TestSplitGateIsMonotonicInTheRatio(t *testing.T) {
	fracs := []float64{1e-6, 1e-4, 1e-2, 0.05, 0.1, 0.25, 0.5, 0.75, 1.0}
	refusedAt := -1
	for i, f := range fracs {
		ok := splitAggregateIsProfitable(aggFixture(t, f, 1, 1), 4, true)
		if !ok && refusedAt < 0 {
			refusedAt = i
		}
		if ok && refusedAt >= 0 {
			t.Fatalf("split accepted at f=%g after refusing at f=%g — the "+
				"decision is not monotonic in the reduction ratio",
				f, fracs[refusedAt])
		}
	}
	if refusedAt < 0 {
		t.Error("the gate accepted every ratio up to f=1.0, so it can never " +
			"refuse — check that the clamp confines rho to (0,1] and that the " +
			"threshold falls inside that range")
	}
}

// TestSplitGateRefusesWithoutStatistics — the conservative default. Refusal
// restores the pre-P9 shape, which is slower but never risks N group tables
// with no spill path.
func TestSplitGateRefusesWithoutStatistics(t *testing.T) {
	agg := aggFixture(t, 1e-4, 1, 1)
	agg.Child.(*SeqScan).Table.Stats = nil
	if splitAggregateIsProfitable(agg, 4, true) {
		t.Error("split accepted with no statistics at all")
	}

	agg = aggFixture(t, 0, 1, 1) // stats present, fraction unset
	if splitAggregateIsProfitable(agg, 4, true) {
		t.Error("split accepted with an unset distinct fraction")
	}
}

// TestSplitGateRefusesUnderAJoin pins the scope limit recorded as ledger row
// pq-P10. The fraction is relative to the BASE relation, but a join-fed
// aggregate reads the join's output, so applying it there would refuse splits
// that genuinely reduce — TPC-H Q13 being the worked example.
func TestSplitGateRefusesUnderAJoin(t *testing.T) {
	agg := aggFixture(t, 1e-4, 1, 1)
	scan := agg.Child
	agg.Child = &Join{Left: scan, Right: scan, Algo: JoinAlgoHash}
	if splitAggregateIsProfitable(agg, 4, true) {
		t.Error("the gate estimated through a Join; the distinct fraction is " +
			"relative to the base relation, not the join output, so it does " +
			"not mean what the model needs there")
	}
}

// TestSplitGateUngroupedAlwaysSplits: an ungrouped aggregate reduces its whole
// share to one row per worker, which is the maximum possible reduction.
func TestSplitGateUngroupedAlwaysSplits(t *testing.T) {
	agg := aggFixture(t, 0, 1, 0) // no group columns, no statistics needed
	if !splitAggregateIsProfitable(agg, 4, true) {
		t.Error("split refused for an ungrouped aggregate, which reduces an " +
			"entire worker share to a single row")
	}
}

// TestParallelDivisorMatchesUpstream pins get_parallel_divisor, including the
// positive-contribution guard an earlier draft of chapter 11 omitted.
func TestParallelDivisorMatchesUpstream(t *testing.T) {
	for _, tc := range []struct {
		workers int
		leader  bool
		want    float64
	}{
		{1, true, 1.7}, {2, true, 2.4}, {3, true, 3.1},
		{4, true, 4.0}, // 1 - 0.3*4 = -0.2, not positive: no contribution
		{8, true, 8.0},
		{2, false, 2.0}, {4, false, 4.0},
	} {
		got := parallelDivisor(tc.workers, tc.leader)
		if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("parallelDivisor(%d, leader=%v) = %g, want %g",
				tc.workers, tc.leader, got, tc.want)
		}
	}
}

// TestGroupsToRowsRatioMultipliesIndependently — several group columns
// multiply, following estimate_num_groups (selfuncs.c:3664), and the product
// is clamped at 1.
func TestGroupsToRowsRatioMultipliesIndependently(t *testing.T) {
	agg := aggFixture(t, 0.1, 1, 2) // 0.1 * 0.1 = 0.01, times d
	rho, ok := groupsToRowsRatio(agg, 4)
	if !ok {
		t.Fatal("ratio unavailable with statistics present")
	}
	if diff := rho - 0.04; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("rho = %g, want 0.04 (0.1 * 0.1 * 4)", rho)
	}

	agg = aggFixture(t, 0.9, 1, 2) // 0.81 * 4 = 3.24, must clamp
	if rho, _ = groupsToRowsRatio(agg, 4); rho != 1 {
		t.Errorf("rho = %g, want it clamped to 1", rho)
	}
}
