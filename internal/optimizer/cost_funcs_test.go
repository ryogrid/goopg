package optimizer

import (
	"math"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/executor/hashsize"
)

// Phase C3.1 — per-node cost functions, checked against hand-computed oracle
// values. Pure functions; no plan changes.

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestGetParallelDivisor(t *testing.T) {
	cases := []struct {
		workers int
		leader  bool
		want    float64
	}{
		{1, true, 1.7}, {2, true, 2.4}, {3, true, 3.1}, {4, true, 4.0},
		{5, true, 5.0}, // leader contribution 1-1.5 < 0, so just workers
		{2, false, 2.0},
	}
	for _, c := range cases {
		if got := getParallelDivisor(c.workers, c.leader); !approx(got, c.want) {
			t.Errorf("getParallelDivisor(%d, leader=%v) = %v, want %v", c.workers, c.leader, got, c.want)
		}
	}
}

func TestCostSeqscan_HandComputed(t *testing.T) {
	cp := defaultCostParams()
	// 1000 pages, 100000 tuples, 1 qual op:
	// run = 1.0*1000 + (0.01 + 0.0025*1)*100000 = 1000 + 0.0125*100000 = 1000 + 1250 = 2250
	got := costSeqscan(cp, 1000, 100000, 1)
	if !approx(got.Total, 2250) || got.Startup != 0 {
		t.Fatalf("costSeqscan = %+v, want {0, 2250}", got)
	}
}

func TestCostSortRun_StartupHeavy(t *testing.T) {
	cp := defaultCostParams()
	// `cost_tuplesort` clamps `tuples` to 2 rather than returning zero
	// ("mustn't do log(0)"), so a degenerate input still costs something.
	if tiny := costSortRun(cp, 1, 0); !(tiny.Total > 0) {
		t.Fatalf("a 1-row sort must be clamped to PG's 2-tuple floor, got %+v", tiny)
	}
	c := costSortRun(cp, 1000, 0)
	// startup = 2*0.0025 * 1000 * log2(1000)
	wantStartup := 2 * 0.0025 * 1000 * math.Log2(1000)
	if !approx(c.Startup, wantStartup) {
		t.Fatalf("costSortRun startup = %v, want %v", c.Startup, wantStartup)
	}
	if c.Total <= c.Startup {
		t.Fatalf("sort total must exceed startup by the per-row emit")
	}
}

func TestGatherCost_AddsSetupAndTransfer(t *testing.T) {
	cp := defaultCostParams()
	sub := Cost{Startup: 10, Total: 100}
	g := gatherCost(cp, sub, 5000)
	// startup += 1000; total += 1000 + 0.1*5000 = 1000 + 500
	if !approx(g.Startup, 1010) {
		t.Fatalf("gather startup = %v, want 1010", g.Startup)
	}
	if !approx(g.Total, 100+1000+500) {
		t.Fatalf("gather total = %v, want 1600", g.Total)
	}
}

func TestHashJoinCost_BuildIsStartup(t *testing.T) {
	cp := defaultCostParams()
	outer := Cost{Startup: 0, Total: 500}
	inner := Cost{Startup: 0, Total: 50}
	c := hashJoinCost(cp, hashJoinInputs{
		outer: outer, inner: inner,
		outerRows: 10000, innerRows: 1000,
		outputRows:     8000,
		numHashClauses: 1,
		outerCols:      4, innerCols: 4,
		outerAvgVarBytes: 0, innerAvgVarBytes: 0,
	})
	// A 1000-row, 4-column build is 216 kB against the 512 MB default budget,
	// so NBatch is 1 and no spill I/O is charged (M0127-P5.7-a; before it, an
	// unconditional seqPageCost*(1000/100) = 10 was added here).
	// build = (0.0025+0.01)*1000 + 50 = 12.5 + 50 = 62.5
	if !approx(c.Startup, 62.5) {
		t.Fatalf("hashjoin startup = %v, want 62.5 (build is startup, no spill at this size)", c.Startup)
	}
	if c.Total <= c.Startup {
		t.Fatalf("hashjoin total must exceed the build-only startup")
	}
}

// TestHashJoinCost_SpillTermFiresExactlyWhenTheExecutorSpills is the
// sibling-path assertion for M0127-P5.7-a. The batch I/O charge is only honest
// if it appears for precisely the builds `joinOp.buildGeometry` will batch —
// so the test does not hard-code a size threshold, it asks `hashsize.Choose`
// (the executor's own function, with the executor's own arguments) and
// requires the cost to move iff it answered NBatch > 1.
func TestHashJoinCost_SpillTermFiresExactlyWhenTheExecutorSpills(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 8
	var fit, spilled int
	// 8 columns is 8*48+24 = 408 bytes per entry, so the 512 MB default budget
	// holds roughly 1.3 M rows. Straddle it by three orders of magnitude.
	for _, rows := range []float64{1e3, 1e5, 1e6, 2e6, 1e7, 6e7} {
		in := hashJoinInputs{
			outer: Cost{Total: 500}, inner: Cost{Total: 50},
			outerRows: rows * 3, innerRows: rows,
			outputRows:     rows,
			numHashClauses: 1,
			outerCols:      ncols, innerCols: ncols,
			outerAvgVarBytes: 0, innerAvgVarBytes: 0,
		}
		got := hashJoinCost(cp, in)

		// The CPU half, hand-computed — the whole cost when nothing spills.
		// (A "no-spill baseline" cannot be obtained by zeroing the column
		// counts: a zero-column build still allocates a bucket array, and at
		// ten million rows that array alone overflows the budget.)
		wantStartup := (cp.cpuOperatorCost*1+cp.cpuTupleCost)*in.innerRows + in.inner.Total
		wantRun := in.outer.Total + cp.cpuOperatorCost*in.outerRows + cp.cpuTupleCost*in.outputRows

		if hashsize.Choose(rows, ncols, 0, cp.workMem).NBatch > 1 {
			// PG's charge verbatim (costsize.c:4239-4248): the inner is
			// written during the build (startup), then read back while the
			// outer is written and read (run).
			innerPages := spillPages(in.innerRows, ncols, 0)
			outerPages := spillPages(in.outerRows, ncols, 0)
			wantStartup += cp.seqPageCost * innerPages
			wantRun += cp.seqPageCost * (innerPages + 2*outerPages)
			spilled++
		} else {
			fit++
		}

		if !approx(got.Startup, wantStartup) {
			t.Fatalf("rows=%g: startup = %v, want %v", rows, got.Startup, wantStartup)
		}
		if !approx(got.Total, wantStartup+wantRun) {
			t.Fatalf("rows=%g: total = %v, want %v", rows, got.Total, wantStartup+wantRun)
		}
	}
	// The sweep is only a test of the BRANCH if it visits both sides of it.
	if fit == 0 || spilled == 0 {
		t.Fatalf("sweep degenerated: %d sizes fit, %d spilled — it must straddle the budget", fit, spilled)
	}
}

// TestHashJoinCost_SpillDependsOnFitNotOnSize pins the distinction the deleted
// M0126-0013 stand-in could not draw. That term was `seq_page_cost *
// innerRows/100` unconditionally, so it charged in proportion to the build's
// size whether or not the build fit. The real term charges nothing up to the
// budget and a large amount past it, which is what makes it decide a plan.
func TestHashJoinCost_SpillDependsOnFitNotOnSize(t *testing.T) {
	cp := defaultCostParams()
	mk := func(rows float64, ncols int) Cost {
		return hashJoinCost(cp, hashJoinInputs{
			outerRows: rows, innerRows: rows, outputRows: rows,
			numHashClauses: 1,
			outerCols:      ncols, innerCols: ncols,
			outerAvgVarBytes: 0, innerAvgVarBytes: 0,
		})
	}
	// Same row count, different widths: narrow fits, wide does not.
	narrow := mk(2e6, 1)
	wide := mk(2e6, 40)
	if !(wide.Total > narrow.Total*2) {
		t.Fatalf("a build that spills (%v) must cost far more than the same row count that fits (%v)",
			wide.Total, narrow.Total)
	}
	if hashsize.Choose(2e6, 1, 0, cp.workMem).NBatch != 1 {
		t.Fatal("premise broken: the narrow build was expected to fit work_mem")
	}
	if hashsize.Choose(2e6, 40, 0, cp.workMem).NBatch <= 1 {
		t.Fatal("premise broken: the wide build was expected to spill")
	}
}

// TestCostParamsWorkMemMatchesExecutorFallback is the other half of the
// sibling-path rule: the budget the planner solves the geometry for must be
// the one the executor falls back to when a session sets no work_mem. If these
// two drift, every hash join is priced for a table size the executor will not
// build.
func TestCostParamsWorkMemMatchesExecutorFallback(t *testing.T) {
	if got, want := defaultCostParams().workMem, hashsize.EffectiveMemLimit(0); got != want {
		t.Fatalf("planner work_mem = %d, executor fallback = %d", got, want)
	}
}

func TestAggCost_Hashed(t *testing.T) {
	cp := defaultCostParams()
	child := Cost{Startup: 0, Total: 100}
	// perInput = 0.0025*2 + 0.0025*1 = 0.0075; startup = 100 + 0.0075*1000 = 107.5
	c := aggCost(cp, child, 1000, 10, 2, 1)
	if !approx(c.Startup, 107.5) {
		t.Fatalf("agg startup = %v, want 107.5", c.Startup)
	}
	// perGroup = 0.0025*2 + 0.01 = 0.015; total = 107.5 + 0.015*10 = 107.65
	if !approx(c.Total, 107.65) {
		t.Fatalf("agg total = %v, want 107.65", c.Total)
	}
}

func TestIndexProbeCost(t *testing.T) {
	cp := defaultCostParams()
	want := 2*4.0 + 0.005 + 0.01 + 0.0025 // 8.0175
	if !approx(indexProbeCost(cp), want) {
		t.Fatalf("indexProbeCost = %v, want %v", indexProbeCost(cp), want)
	}
}

// TestCostParamsMatchConfigDefaults is the drift guard: the PG constants baked
// into defaultCostParams must equal the boot values config/defaults.go registers,
// so the cost model and SHOW cannot silently diverge (memory: GUC defaults must
// match PG).
func TestCostParamsMatchConfigDefaults(t *testing.T) {
	reg := misc.BuildDefaultRegistry()
	cp := defaultCostParams()
	cases := []struct {
		name string
		val  float64
	}{
		{"seq_page_cost", cp.seqPageCost},
		{"random_page_cost", cp.randomPageCost},
		{"cpu_tuple_cost", cp.cpuTupleCost},
		{"cpu_index_tuple_cost", cp.cpuIndexTupleCost},
		{"cpu_operator_cost", cp.cpuOperatorCost},
		{"parallel_setup_cost", cp.parallelSetupCost},
		{"parallel_tuple_cost", cp.parallelTupleCost},
	}
	for _, c := range cases {
		v, ok := reg.Get(c.name)
		if !ok {
			t.Errorf("GUC %q not registered", c.name)
			continue
		}
		boot, err := strconv.ParseFloat(v.BootVal, 64)
		if err != nil {
			t.Errorf("GUC %q BootVal %q not a float: %v", c.name, v.BootVal, err)
			continue
		}
		if !approx(boot, c.val) {
			t.Errorf("GUC %q: costParams has %v but config BootVal is %v — drift", c.name, c.val, boot)
		}
	}
}

// dpEntryOfWidth and the two TestCostJoinCandidate* pins lived here until
// M0127-P6.3 deleted costJoinCandidate with the old subset-bitmask DP it priced
// (08 §4). The property they guarded — the DP's hash cost is exactly
// hashJoinCost, with spill priced by the real batch geometry — is now pinned
// at the cost-function level by the hashsize.Choose tests above.
