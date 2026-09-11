package optimizer

import (
	"math"
	"testing"
)

// R69 slice (a): PG's `cost_rescan` STARTUP arm for the nestloop join
// (`initial_cost_nestloop` charges `(outer−1) × rescan_startup`;
// all three nestloop call sites passed literal 0). Only the
// parameterised-index case changes behaviour (descent repaid per
// rescan); Memoize and materialised/plain cases return exactly what
// the literal did.

func TestNestLoopInnerRescanStartup(t *testing.T) {
	if got := nestLoopInnerRescanStartup(nil); got != 0 {
		t.Errorf("nil inner: got %v, want 0", got)
	}
	// Parameterised index probe: the descent, repaid per rescan (PG's
	// cost_rescan default arm returns the path's own startup).
	probe := &Path{Kind: PathNestLoop, RequiredOuter: 1, Cost: Cost{Startup: 0.38, Total: 0.44}}
	if got := nestLoopInnerRescanStartup(probe); got != 0.38 {
		t.Errorf("parameterised probe: got %v, want descent 0.38", got)
	}
	// Memoize inner: its modeled rescan startup (cache-lookup scale).
	memo := &Path{Kind: PathMemoize, MemoizeInfo: &memoizePathInfo{rescan: Cost{Startup: 0.05, Total: 1.2}}}
	if got := nestLoopInnerRescanStartup(memo); got != 0.05 {
		t.Errorf("memoize inner: got %v, want 0.05", got)
	}
	// Plain (materialised-model) inner: 0, PG's T_Material arm.
	plain := &Path{Kind: PathNestLoop, RequiredOuter: 0, Cost: Cost{Startup: 12.5, Total: 100.0}}
	if got := nestLoopInnerRescanStartup(plain); got != 0 {
		t.Errorf("plain inner: got %v, want 0", got)
	}
}

// TestNestloopCostRescanStartupTerm pins PG's initial_cost_nestloop shape
// inside nestloopCost: the rescan STARTUP is charged per rescan after the
// first, SEPARATELY from the rescan run (R69 caught a first cut folding
// it into the run part, double-subtracting the descent).
func TestNestloopCostRescanStartupTerm(t *testing.T) {
	cp := defaultCostParams()
	outer := Cost{Startup: 10, Total: 110} // run 100
	// A rescan (Total, Startup) pair must be consistent (Total ≥ Startup);
	// passing a run-only Total with a nonzero Startup double-subtracts
	// the descent — the R69 first-cut error, pinned here by construction.
	got := nestloopCost(cp, outer, Cost{Startup: 0.38, Total: 0.44}, 101, 1, 0.38, 0.44)
	// startup 10.38 + outer run 100 + inner run 0.06 + 100×0.38 startup
	// + 100×0.06 run + cpuTuple 0.01×101×1.
	want := 10.38 + 100 + 0.06 + 100*0.38 + 100*0.06 + 0.01*101
	if math.Abs(got.Total-want) > 1e-9 {
		t.Errorf("got total %v, want %v", got.Total, want)
	}
	if got.Startup != 10.38 {
		t.Errorf("got startup %v, want 10.38", got.Startup)
	}
	// Zero startup (plain-NL callers) is byte-identical to the old shape.
	plain := nestloopCost(cp, outer, Cost{Startup: 0.38, Total: 0.44}, 101, 1, 0, 0.06)
	wantPlain := 10.38 + 100 + 0.06 + 100*0.06 + 0.01*101
	if math.Abs(plain.Total-wantPlain) > 1e-9 {
		t.Errorf("zero-startup call: got %v, want %v", plain.Total, wantPlain)
	}
}
