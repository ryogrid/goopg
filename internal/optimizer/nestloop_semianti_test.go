package optimizer

import (
	"math"
	"testing"
)

// TestNestloopCostSemiAntiMatchesFinalCostNestloop pins the port of
// final_cost_nestloop's SEMI/ANTI branch (costsize.c) with values worked by
// hand from PG's formulas, so a drift in any term is a named failure.
func TestNestloopCostSemiAntiMatchesFinalCostNestloop(t *testing.T) {
	cp := defaultCostParams() // cpu_tuple_cost 0.01, cpu_operator_cost 0.0025
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

	// Non-indexed inner: 1000 outer rows, 500 inner rows, one join qual,
	// outer_match_frac 0.3, match_count 3.
	//   matched = rint(300) = 300, unmatched = 700, inner_scan_frac = 0.5
	//   ntuples = 300*500*0.5 + 700*500                 = 425000
	//   run     = 100 (outer) + 50 (forced full first scan)
	//           + 300*50*0.5 (matched rescans)          = 7500
	//           + 699*50 (unmatched rescans, one used)  = 34950
	//           + (0.01 + 0.0025*1) * 425000            = 5312.5
	//   total   = 47912.5
	f := semiAntiJoinFactors{apply: true, outerMatchFrac: 0.3, matchCount: 3}
	got := nestloopCostSemiAnti(cp, Cost{0, 100}, Cost{0, 50}, 1000, 500, 0, 50, f, false, 1)
	if !near(got.Startup, 0) || !near(got.Total, 47912.5) {
		t.Errorf("non-indexed = %+v, want {0 47912.5}", got)
	}

	// Indexed inner (has_indexed_join_quals): 1 row per probe, cost 8 per
	// probe, no residual quals, match_count 1 (inner_scan_frac 1).
	//   matched = 300, unmatched = 700, ntuples = 300*1*1 = 300
	//   run = 100 + 8*1 + 299*8*1 + 700*8/1 + 0.01*300 = 8103
	f = semiAntiJoinFactors{apply: true, outerMatchFrac: 0.3, matchCount: 1}
	got = nestloopCostSemiAnti(cp, Cost{0, 100}, Cost{0, 8}, 1000, 1, 0, 8, f, true, 0)
	if !near(got.Total, 8103) {
		t.Errorf("indexed = %+v, want total 8103", got)
	}

	// No matched row at all (ANTI where nothing matches): the forced full
	// first scan is blamed on an unmatched row, never on a matched one.
	//   matched 0, unmatched 10, ntuples = 10*20 = 200
	//   run = 5 + 4 (first scan) + 9*4 (other unmatched) + 0.0125*200 = 47.5
	f = semiAntiJoinFactors{apply: true, outerMatchFrac: 0, matchCount: 1}
	got = nestloopCostSemiAnti(cp, Cost{0, 5}, Cost{0, 4}, 10, 20, 0, 4, f, false, 1)
	if !near(got.Total, 47.5) {
		t.Errorf("no-match = %+v, want total 47.5", got)
	}
}

// TestSemiAntiFactorsOnlyForSemiAnti pins compute_semi_anti_join_factors'
// caller contract: the factors exist only for SEMI and ANTI (a unique-ified
// side is demoted to INNER before this is asked), and a nil search context
// fails closed to the full-rescan model.
func TestSemiAntiFactorsOnlyForSemiAnti(t *testing.T) {
	var s *searchCtx
	if f := s.semiAntiJoinFactorsFor(&RelOptInfo{Rows: 10}, &RelOptInfo{Rows: 10}, 0, nil); f.apply {
		t.Fatal("nil searchCtx must not produce semi/anti factors")
	}
}
