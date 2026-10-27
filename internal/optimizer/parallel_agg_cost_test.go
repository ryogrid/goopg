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
//
// The gate itself (splitAggregateIsProfitable) and its tests were deleted at
// M0147-0047 with the GOOPG_PARTIAL_AGG_PATHS=off arm that was its only
// caller, and its private parallelDivisor duplicate went too
// (getParallelDivisor, cost_funcs.go, is the one copy). What stays pinned here
// is the input the priced tournament (partialaggpaths.go) still reads:
// groupsToRowsRatio.

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
