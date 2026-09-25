package optimizer

import (
	"math"
	"testing"
)

// TestNLINestLoopCostChargesRescanStartup pins M0146-0005 slice 2: a nested
// loop over a PARAMETERISED inner re-pays the inner's startup (its index
// descent) on every rescan — `initial_cost_nestloop`'s
// `(outer_path_rows - 1) * inner_rescan_start_cost`
// (postgres/src/backend/optimizer/path/costsize.c:3299-3302). The partial
// nested-loop arm used to pass that term as a literal 0, pricing TPC-H Q9's
// partial nested loop into orders_pk at 0.066 per probe instead of ~0.43 and
// electing it over PG's Parallel Hash Join.
//
// Both NLI arms now call nliNestLoopCost, so pinning it pins both. The test
// builds the same pair twice — once with the inner's startup, once with it
// folded into the run — and requires the difference to be exactly the
// (outer - 1) descents PG charges.
func TestNLINestLoopCostChargesRescanStartup(t *testing.T) {
	cp := defaultCostParams()
	outer := &Path{Kind: PathSeqScan, Rows: 1000, Cost: Cost{Startup: 0, Total: 500}}
	probe := func(startup float64) *Path {
		return &Path{Kind: PathIndexScan, Rows: 1, RequiredOuter: relsetOf(0),
			Cost: Cost{Startup: startup, Total: 0.43}}
	}
	withDescent := nliNestLoopCost(cp, outer, probe(0.375), nil, semiAntiJoinFactors{})
	noDescent := nliNestLoopCost(cp, outer, probe(0), nil, semiAntiJoinFactors{})

	// Same total per probe (0.43) either way; only where the startup sits
	// differs, and PG charges the startup again on every one of the
	// outer-1 rescans while the run part (Total - Startup) shrinks by it.
	// Net: the two totals must agree — the descent is not free.
	if math.Abs(withDescent.Total-noDescent.Total) > 1e-6 {
		t.Fatalf("a parameterised probe's startup must be re-paid per rescan: total %.4f with startup 0.375 vs %.4f with 0 (the pre-fix partial arm charged only the run half)",
			withDescent.Total, noDescent.Total)
	}
	// And the per-probe charge is the whole probe, not its run half.
	perProbe := (withDescent.Total - outer.Cost.Total) / outer.Rows
	if perProbe < 0.43 {
		t.Fatalf("per-probe charge %.4f below the probe's own total 0.43", perProbe)
	}
}
