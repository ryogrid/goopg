package optimizer

import (
	"math"
	"testing"
)

// R69 slice (a): PG's `cost_rescan` STARTUP arm for the nestloop join
// (`initial_cost_nestloop` charges `(outer−1) × rescan_startup`;
// all three nestloop call sites passed literal 0). M0146-0010 folded the
// helper into `pathRescanCost` itself — `cost_rescan` per candidate — so
// the pin now exercises the same four shapes through it:
//
//   - a parameterised probe re-pays its descent per rescan (default arm);
//   - a Memoize pays its modeled rescan startup;
//   - a BARE unparameterised inner re-executes — rescan startup is the
//     path's own startup (the arm the executor's rescanByReexec matches);
//   - a PathMaterial replays the buffer — rescan startup 0, T_Material arm.
func TestNestLoopInnerRescanStartup(t *testing.T) {
	cp := defaultCostParams()
	if st, _ := pathRescanCost(nil, cp); st != 0 {
		t.Errorf("nil inner: got %v, want 0", st)
	}
	// Parameterised index probe: the descent, repaid per rescan (PG's
	// cost_rescan default arm returns the path's own startup).
	probe := &Path{Kind: PathIndexScan, RequiredOuter: 1, Cost: Cost{Startup: 0.38, Total: 0.44}}
	if st, tot := pathRescanCost(probe, cp); st != 0.38 || tot != 0.44 {
		t.Errorf("parameterised probe: got (%v, %v), want re-exec (0.38, 0.44)", st, tot)
	}
	// Memoize inner: its modeled rescan startup (cache-lookup scale).
	memo := &Path{Kind: PathMemoize, MemoizeInfo: &memoizePathInfo{rescan: Cost{Startup: 0.05, Total: 1.2}}}
	if st, _ := pathRescanCost(memo, cp); st != 0.05 {
		t.Errorf("memoize inner: got %v, want 0.05", st)
	}
	// Bare unparameterised inner: re-executed, startup repaid — the
	// default arm, where the pre-M0146-0010 model charged 0 because every
	// inner was silently materialised.
	plain := &Path{Kind: PathSeqScan, RequiredOuter: 0, Cost: Cost{Startup: 12.5, Total: 100.0}}
	if st, tot := pathRescanCost(plain, cp); st != 12.5 || tot != 100.0 {
		t.Errorf("bare inner: got (%v, %v), want re-exec (12.5, 100.0)", st, tot)
	}
	// Materialised inner: rescan startup 0, T_Material arm — the cheap
	// replay that makes the election worth filing.
	mat := materialInnerPath(&RelOptInfo{Relids: 2}, plain, cp)
	if mat == nil || mat.Kind != PathMaterial {
		t.Fatalf("materialInnerPath returned %v", mat)
	}
	st, tot := pathRescanCost(mat, cp)
	if st != 0 {
		t.Errorf("materialised inner rescan startup: got %v, want 0", st)
	}
	if want := materialRescanCost(cp, plain.Rows, pathAvgVarBytes(plain), pathNCols(plain)); tot != want {
		t.Errorf("materialised inner rescan total: got %v, want %v", tot, want)
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
	// Zero startup (the T_Material rescan arm) is byte-identical to the
	// old plain-NL shape.
	plain := nestloopCost(cp, outer, Cost{Startup: 0.38, Total: 0.44}, 101, 1, 0, 0.06)
	wantPlain := 10.38 + 100 + 0.06 + 100*0.06 + 0.01*101
	if math.Abs(plain.Total-wantPlain) > 1e-9 {
		t.Errorf("zero-startup call: got %v, want %v", plain.Total, wantPlain)
	}
}
