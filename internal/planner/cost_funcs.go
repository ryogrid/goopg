package planner

import (
	"math"
	"os"
	"strconv"
)

// envFloatDefault reads a float from the environment, returning def when unset
// or unparseable. Used for measurement-time cost-calibration overrides.
func envFloatDefault(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// Per-node cost functions, reproduced from PostgreSQL's costsize.c in PG's units
// (seq_page_cost = 1.0). See docs/design/cost-model/ chapter 02. Phase C3.1:
// pure functions plus the cost constants; nothing selects on them yet (path
// generation is C3.2, selection is C4), so they cannot change a plan. Each is
// unit-tested against a hand-computed oracle value (cost_funcs_test.go).
//
// All costs are absolute and in one unit, so a scan, a sort, a join, and a
// Gather are directly comparable — invariant #1 of the design README. "Relative
// is enough for join order" is a trap: the parallelize decision compares a plan's
// total against the absolute parallel_setup_cost, so every term is charged.

// costParams holds PG's cost GUCs. defaultCostParams returns the PG 18 boot
// values (cost.h:24-30), which config/defaults.go also registers; a drift-guard
// test asserts the two agree. Threaded from the session at path-generation time
// (C3.2/C4); until then the defaults are used.
type costParams struct {
	seqPageCost       float64
	randomPageCost    float64
	cpuTupleCost      float64
	cpuIndexTupleCost float64
	cpuOperatorCost   float64
	parallelSetupCost float64
	parallelTupleCost float64
}

func defaultCostParams() costParams {
	return costParams{
		seqPageCost:       1.0,
		randomPageCost:    4.0,
		cpuTupleCost:      0.01,
		cpuIndexTupleCost: 0.005,
		cpuOperatorCost:   0.0025,
		parallelSetupCost: 1000.0,
		parallelTupleCost: 0.1,
	}
}

// getParallelDivisor reproduces get_parallel_divisor (costsize.c:6474): the
// worker count plus the leader's fractional contribution when
// parallel_leader_participation is on and positive. Yields d(1)=1.7, d(2)=2.4,
// d(3)=3.1, d(w>=4)=w.
func getParallelDivisor(workers int, leaderParticipates bool) float64 {
	d := float64(workers)
	if leaderParticipates {
		leader := 1.0 - 0.3*float64(workers)
		if leader > 0 {
			d += leader
		}
	}
	if d < 1 {
		d = 1
	}
	return d
}

// costSeqscan reproduces cost_seqscan (costsize.c:295): sequential page reads
// plus per-tuple CPU. numQualOps is the number of operator evaluations per tuple
// from the scan's restriction qual. The parallel case divides the run cost by the
// divisor (the caller passes a per-worker tuple/page count, or divides after).
func costSeqscan(cp costParams, relPages int64, relTuples float64, numQualOps int) Cost {
	run := cp.seqPageCost*float64(relPages) +
		(cp.cpuTupleCost+cp.cpuOperatorCost*float64(numQualOps))*relTuples
	return Cost{Startup: 0, Total: run}
}

// costSortRun reproduces the comparison term of cost_sort (costsize.c:2144) for
// an in-memory sort of n rows: an n*log2(n) comparison cost, charged as startup
// (the sort must complete before the first row), plus a per-row emit at run. The
// caller adds the input path's cost. width participates in the external-merge
// arm (not modelled at the milestone; TPC-H sorts are small dimension outputs).
func costSortRun(cp costParams, inputRows float64) Cost {
	if inputRows < 2 {
		return Cost{}
	}
	comparisonCost := 2.0 * cp.cpuOperatorCost
	startup := comparisonCost * inputRows * math.Log2(inputRows)
	return Cost{Startup: startup, Total: startup + cp.cpuOperatorCost*inputRows}
}

// hashJoinCost reproduces initial_cost_hashjoin + final_cost_hashjoin
// (costsize.c:4160/:4275) at milestone fidelity: build the inner side (charged as
// startup — the hash table must be complete before probing) and probe the outer.
// numHashClauses is the number of hash conditions. outputRows is the join's
// result cardinality. Batching/spill I/O is omitted (design ch. 06 §5).
func hashJoinCost(cp costParams, outer, inner Cost, outerRows, innerRows, outputRows float64, numHashClauses int) Cost {
	// Build: read + hash every inner row, all before the first probe.
	build := (cp.cpuOperatorCost*float64(numHashClauses)+cp.cpuTupleCost)*innerRows + inner.Total
	startup := outer.Startup + build
	// Probe: hash each outer key and walk its bucket; emit each match.
	probe := cp.cpuOperatorCost*float64(numHashClauses)*outerRows + cp.cpuTupleCost*outputRows
	total := startup + (outer.Total - outer.Startup) + probe
	return Cost{Startup: startup, Total: total}
}

// nestloopCost reproduces final_cost_nestloop (costsize.c:3349): for each outer
// row, pay the inner path's per-rescan cost. innerRescanTotal is the cost of one
// inner scan (one parameterised index probe for an NLI). Cheap for a selective
// outer side.
func nestloopCost(cp costParams, outer, inner Cost, outerRows, outputRows, innerRescanTotal float64) Cost {
	startup := outer.Startup + inner.Startup
	run := (outer.Total - outer.Startup) + outerRows*innerRescanTotal + cp.cpuTupleCost*outputRows
	return Cost{Startup: startup, Total: startup + run}
}

// mergeJoinCost reproduces final_cost_mergejoin (costsize.c:3837) at milestone
// fidelity: merge two already-costed (and sorted) inputs, charging a per-row
// merge. A cost_sort on an unsorted input is added by the caller when the input's
// pathkeys do not satisfy the merge clause (design ch. 06 §3.2) — the whole
// reason pathkeys exist.
func mergeJoinCost(cp costParams, outer, inner Cost, outerRows, innerRows, outputRows float64) Cost {
	startup := outer.Startup + inner.Startup
	run := (outer.Total - outer.Startup) + (inner.Total - inner.Startup) +
		cp.cpuOperatorCost*(outerRows+innerRows) + cp.cpuTupleCost*outputRows
	return Cost{Startup: startup, Total: startup + run}
}

// gatherCost reproduces cost_gather (costsize.c:446): a flat parallel_setup_cost
// at startup plus parallel_tuple_cost per row emerging from the Gather. `sub` is
// the (already divisor-divided) partial subpath's cost, and outputRows is the
// rows emerging (partial rows * divisor, compute_gather_rows costsize.c:6625).
// The honest parallelize comparison (design ch. 08): this competes in add_path
// against the serial path, and only wins because `sub` already has the bulk of
// the plan's cost divided by the divisor.
func gatherCost(cp costParams, sub Cost, outputRows float64) Cost {
	startup := sub.Startup + cp.parallelSetupCost
	total := sub.Total + cp.parallelSetupCost + cp.parallelTupleCost*outputRows
	return Cost{Startup: startup, Total: total}
}

// indexProbeCost is the cost of one equality probe of a selective/unique index
// returning ~1 row: an index page and a heap page, both random, plus per-tuple
// CPU. This is the per-outer-row rescan cost a nested-loop-index join pays
// (nestloopCost's innerRescanTotal). It is what makes NL-index cheap for a
// selective outer side and ruinous for a large one — the Q9 lesson (ch. 07 §4.5).
func indexProbeCost(cp costParams) float64 {
	return indexProbeCostMultiplier * (2*cp.randomPageCost + cp.cpuIndexTupleCost + cp.cpuTupleCost + cp.cpuOperatorCost)
}

// indexProbeCostMultiplier scales indexProbeCost. PG's constants (multiplier 1)
// under-cost goopg's NL-index probe — goopg materialises the whole TID list
// eagerly per probe (ch. 06 §5), so an NL-probe of a large relation runs far
// slower than PG's random_page_cost model predicts, and the cost-driven DP would
// pick ruinous PG-shaped NL plans (measured: Q5/Q9 20-200x). This multiplier
// recalibrates the probe cost toward goopg's in-memory reality so the DP prefers
// a hash join over NL-probing a large outer. Overridable via
// GOOPG_INDEX_PROBE_MULT for measurement; the calibrated default is set once a
// value is validated on SF1.
var indexProbeCostMultiplier = envFloatDefault("GOOPG_INDEX_PROBE_MULT", 1.0)

// multiHashJoinCost costs goopg's N-way MultiHashJoin under the comparability
// invariant (design ch. 06 §4.1): build each dimension hash table (charged as
// startup, since all builds complete before probing) and make ONE probe pass over
// the driving table, hashing each driving row against every dimension. dims/dimRows
// are the build-side rels; probe/probeRows the driving rel; outputRows the join
// result. Costed in the same PG units as the equivalent hash cascade, so add_path
// can rank the MHJ against it — the MHJ wins exactly when eliminating the cascade's
// intermediate materialisations pays.
func multiHashJoinCost(cp costParams, probe Cost, probeRows float64, dims []Cost, dimRows []float64, outputRows float64) Cost {
	build := 0.0
	for i := range dims {
		r := 0.0
		if i < len(dimRows) {
			r = dimRows[i]
		}
		build += (cp.cpuOperatorCost+cp.cpuTupleCost)*r + dims[i].Total
	}
	startup := build + probe.Startup
	probeCost := cp.cpuOperatorCost*float64(len(dims))*probeRows + cp.cpuTupleCost*outputRows
	total := startup + (probe.Total - probe.Startup) + probeCost
	return Cost{Startup: startup, Total: total}
}

// aggCost reproduces the AGG_HASHED arm of cost_agg (costsize.c:2751): per input
// tuple, a transition call plus a hash of each group column; per output group, a
// finalize plus a tuple emit. numAggs transition calls are charged per input row.
// The hash aggregate builds fully before emitting, so the input work is startup.
func aggCost(cp costParams, child Cost, inputRows, numGroups float64, numAggs, numGroupCols int) Cost {
	perInput := cp.cpuOperatorCost*float64(numAggs) + cp.cpuOperatorCost*float64(numGroupCols)
	startup := child.Total + perInput*inputRows
	perGroup := cp.cpuOperatorCost*float64(numAggs) + cp.cpuTupleCost
	total := startup + perGroup*numGroups
	return Cost{Startup: startup, Total: total}
}
