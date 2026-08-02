package planner

import (
	"math"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/config"
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
	if (costSortRun(cp, 1) != Cost{}) {
		t.Fatalf("a 1-row sort should cost nothing")
	}
	c := costSortRun(cp, 1000)
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
	c := hashJoinCost(cp, outer, inner, 10000, 1000, 8000, 1)
	// build = (0.0025+0.01)*1000 + 50 + seqPageCost*(1000/100) = 12.5 + 50 + 10 = 72.5
	if !approx(c.Startup, 72.5) {
		t.Fatalf("hashjoin startup = %v, want 72.5 (build is startup-heavy, +I/O pages)", c.Startup)
	}
	if c.Total <= c.Startup {
		t.Fatalf("hashjoin total must exceed the build-only startup")
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

func TestMultiHashJoinCost_BuildIsStartup(t *testing.T) {
	cp := defaultCostParams()
	probe := Cost{Startup: 0, Total: 1000}
	dims := []Cost{{Total: 10}, {Total: 5}}
	dimRows := []float64{100, 50}
	c := multiHashJoinCost(cp, probe, 100000, dims, dimRows, 100000)
	// build = (0.0025+0.01)*100 + 10 + (0.0025+0.01)*50 + 5 = 1.25+10 + 0.625+5 = 16.875
	if !approx(c.Startup, 16.875) {
		t.Fatalf("MHJ startup = %v, want 16.875 (all dim builds)", c.Startup)
	}
	if c.Total <= c.Startup {
		t.Fatalf("MHJ total must exceed the build-only startup by the probe pass")
	}
}

// TestCostParamsMatchConfigDefaults is the drift guard: the PG constants baked
// into defaultCostParams must equal the boot values config/defaults.go registers,
// so the cost model and SHOW cannot silently diverge (memory: GUC defaults must
// match PG).
func TestCostParamsMatchConfigDefaults(t *testing.T) {
	reg := config.BuildDefaultRegistry()
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

// TestCostJoinCandidateLargeBuildPressure pins M0126-0013: building on more
// than largeBuildThreshold rows adds a quadratic penalty that makes the DP
// avoid enormous intermediate hash tables.
func TestCostJoinCandidateLargeBuildPressure(t *testing.T) {
	cp := defaultCostParams()
	outer := dpEntry{rows: 6_000_000, pgCost: Cost{Startup: 10, Total: 100}}
	inner := dpEntry{rows: 6_000_000, pgCost: Cost{Startup: 5, Total: 50}}

	// 6M build: overshoot = (6M-2M)/2M = 2, penalty = 4 * 0.01 * 6M = 240K
	penalty := costJoinCandidate(cp, nil, outer, inner, 6_000_000, nil)

	// Small build (below threshold): no penalty.
	innerSmall := dpEntry{rows: 500_000, pgCost: Cost{Startup: 5, Total: 50}}
	noPenalty := costJoinCandidate(cp, nil, outer, innerSmall, 500_000, nil)

	if penalty.Total <= noPenalty.Total {
		t.Errorf("6M build Total=%v must exceed 500K build Total=%v",
			penalty.Total, noPenalty.Total)
	}

	// 10M build: overshoot = (10M-2M)/2M = 4, penalty = 16 * 0.01 * 10M = 1.6M
	innerHuge := dpEntry{rows: 10_000_000, pgCost: Cost{Startup: 5, Total: 50}}
	huge := costJoinCandidate(cp, nil, outer, innerHuge, 10_000_000, nil)
	if huge.Total <= penalty.Total {
		t.Errorf("10M build Total=%v must exceed 6M build Total=%v",
			huge.Total, penalty.Total)
	}
}
