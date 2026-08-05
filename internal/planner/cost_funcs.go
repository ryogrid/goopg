package planner

import (
	"math"
	"os"
	"strconv"

	"github.com/goopg/goopg/internal/hashsize"
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

	// effectiveCacheSize is the `effective_cache_size` GUC in PAGES, which is
	// the unit PG's own variable carries (`effective_cache_size` is declared
	// `int` and set from a GUC with GUC_UNIT_BLOCKS, cost.h:33). It is not a
	// per-tuple or per-page price like the fields above — it is the cache
	// budget the Mackert-Lohman formula pro-rates between the relations of a
	// query (`index_pages_fetched`, costsize.c:906), so it belongs to the same
	// struct only because it is a planner GUC the index cost model reads.
	// M0127-P5.4c-ii-b.
	effectiveCacheSize float64

	// workMem is the in-memory budget one hash build may occupy, in BYTES —
	// the `work_mem` GUC, and the fourth argument `hashJoinCost` hands
	// `hashsize.Choose`. It is a budget rather than a price, so like
	// effectiveCacheSize it belongs to this struct only because a cost
	// function reads it.
	//
	// The default is `hashsize.DefaultMemLimitBytes`, which is the SAME
	// fallback the executor applies when a session has no work_mem set
	// (`hashsize.EffectiveMemLimit`, called from `joinOp.buildGeometry`). The
	// two must agree or the planner prices a geometry the executor will not
	// build — the whole reason the sizing lives in a shared leaf package.
	// The per-session value does not reach the planner yet: cost time has no
	// session in scope (the same gap `ParallelSettings` exists to bridge for
	// the parallel post-pass). Deferral ledger 2026-08-05 M0127-P5.7-a.
	workMem int64
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
		// PG 18's boot value is "4GB" (guc_tables.c), which in 8 kB blocks is
		// 524288. config/defaults.go registers the string form; the unit
		// conversion is pinned by TestEffectiveCacheSizeMatchesConfigDefault.
		effectiveCacheSize: 4 * 1024 * 1024 * 1024 / blockSizeBytes,
		workMem:            hashsize.DefaultMemLimitBytes,
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

// qualEvalCost is `cost_qual_eval`'s contribution to a join (costsize.c:4700):
// the per-tuple operator cost of a qual list, times the number of tuples the
// operator actually evaluates it on. PG folds this into `cpu_per_tuple` inside
// each final_cost_* function; goopg adds it at the path-generation site
// (pathgen.go) so the same term serves hash, nested loop and — from P5.4c —
// merge without three signature changes.
//
// numQuals counts CONJUNCTS, not operator nodes: one restrictInfo is one
// charge. That matches how `numHashClauses` is already counted by
// hashJoinCost, so the two terms of a hash path's cost use one currency; the
// refinement to a real operator walk belongs with the estimate-audit tooling
// (leftdeep-joins 09 §5), not here.
//
// The tuple count is the CALLER's choice and it is the whole point of the
// function: a nested loop evaluates its quals on the full cross product, a
// hash join only on the tuples that survived the key match. Charging both on
// the join's OUTPUT rows would make a cartesian nested loop look free.
func qualEvalCost(cp costParams, numQuals int, tuples float64) float64 {
	if numQuals <= 0 || !(tuples > 0) {
		return 0
	}
	return cp.cpuOperatorCost * float64(numQuals) * tuples
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

// hashJoinInputs is everything hashJoinCost needs that is not a GUC — the
// argument set of `initial_cost_hashjoin`, named for the upstream fields it
// stands in for. It is a struct rather than eight positional parameters because
// three of them are counts (`numHashClauses`, `outerCols`, `innerCols`) that no
// compiler check would keep in order.
type hashJoinInputs struct {
	// outer / inner are the two input paths' costs; outerRows / innerRows are
	// those paths' OWN row counts (`outer_path->rows` / `inner_path->rows`,
	// costsize.c:4170-4171), which for a parameterised inner is the per-probe
	// count and not the rel's total. See Path.Rows.
	outer, inner         Cost
	outerRows, innerRows float64

	// outputRows is the join's result cardinality (PG's `hashjointuples`).
	outputRows float64

	// numHashClauses is `list_length(hashclauses)`: one cpu_operator_cost per
	// clause is charged per input row on both sides.
	numHashClauses int

	// outerCols / innerCols are the COLUMN COUNTS of the two sides' rows.
	//
	// PG passes `pathtarget->width` in bytes here, because a PG hash entry is a
	// packed MinimalTuple whose size follows the byte width. goopg's is a
	// `[]Datum` of 48-byte structs, so its size follows the COLUMN COUNT — and
	// the executor's own call (`joinOp.buildGeometry`) passes exactly
	// `len(schema)`. Passing anything else here (a byte width, say) would size
	// the same build differently on the two sides of the sibling-path rule and
	// reintroduce the divergence `internal/hashsize` exists to prevent: a cost
	// model that believes a build fits and an executor that spills.
	//
	// Zero means "unknown", and prices the join as never spilling — the
	// pre-P5.7 behaviour. `relNCols` is the production source and never
	// returns zero for a rel the search built.
	outerCols, innerCols int
}

// hashJoinCost reproduces initial_cost_hashjoin + final_cost_hashjoin
// (costsize.c:4160/:4275) at milestone fidelity: build the inner side (charged
// as startup — the hash table must be complete before probing) and probe the
// outer, plus the batch I/O of the spill the executor will actually perform.
//
// M0127-P5.7-a: the spill term. `hashsize.Choose` is the SAME function
// `joinOp.buildGeometry` calls, so `NBatch > 1` here means the executor will
// really write batch files, and PG's charge for them applies verbatim
// (costsize.c:4239-4248): the inner is written once (startup — it happens
// during the build) and then the inner is read back and the outer is written
// and read, all at seq_page_cost.
//
// It REPLACES an unconditional `seq_page_cost * innerRows/100` charge added by
// M0126-0013 as a stand-in deterrent against building on huge intermediates.
// That term cited costsize.c:4166 for a page charge PG does not make there:
// upstream charges pages only under `numbatches > 1`, and charges them for the
// SPILL, not for the resident table. Its stand-in was also monotone in
// innerRows, so it penalised a 6 M-row build that fits work_mem exactly as much
// as one that does not — which is the distinction that decides the plan.
// leftdeep-joins 04 §4, 06 §5.
func hashJoinCost(cp costParams, in hashJoinInputs) Cost {
	// Build: read + hash every inner row, all before the first probe.
	build := (cp.cpuOperatorCost*float64(in.numHashClauses)+cp.cpuTupleCost)*in.innerRows + in.inner.Total

	startup := in.outer.Startup + build
	// Probe: hash each outer key and walk its bucket; emit each match.
	run := (in.outer.Total - in.outer.Startup) +
		cp.cpuOperatorCost*float64(in.numHashClauses)*in.outerRows +
		cp.cpuTupleCost*in.outputRows

	// The geometry the executor will pick for this build. Skew buckets and the
	// parallel combined budget are absent on both sides alike (06 §6).
	sizing := hashsize.Choose(in.innerRows, in.innerCols, 0, cp.workMem)
	if sizing.NBatch > 1 {
		innerPages := spillPages(in.innerRows, in.innerCols)
		outerPages := spillPages(in.outerRows, in.outerCols)
		startup += cp.seqPageCost * innerPages
		run += cp.seqPageCost * (innerPages + 2*outerPages)
	}

	return Cost{Startup: startup, Total: startup + run}
}

// spillPages is `page_size` (costsize.c:6464) in goopg's width model: how many
// BLCKSZ pages `rows` rows of `ncols` columns occupy when written to a batch
// file. `relation_byte_size`'s MinimalTuple math is replaced by
// `hashsize.EntryBytes`, the one place goopg's per-row footprint is defined, so
// the pages this charge prices and the bytes the geometry above solved for come
// from the same model.
//
// The batch FILE encoding is narrower than the in-memory footprint
// (`spillWriter.WriteRow` frames datums with uvarint lengths rather than
// storing 48-byte structs), so this over-states the I/O of a wide build. That
// is deliberate for now and it is the safe direction — it deters spilling — but
// it is a real approximation and is recorded as such (deferral ledger
// 2026-08-05 M0127-P5.7-a).
func spillPages(rows float64, ncols int) float64 {
	if !(rows > 0) {
		return 0
	}
	return math.Ceil(rows * hashsize.EntryBytes(ncols, 0) / blockSizeBytes)
}

// nestloopCost reproduces final_cost_nestloop (costsize.c:3349): for each outer
// row, pay the inner path's per-rescan cost. innerRescanTotal is the cost of one
// inner scan (one parameterised index probe for an NLI). Cheap for a selective
// outer side.
//
// M0127-P5.9-j — the per-tuple CPU charge rides `ntuples`, the number of pairs
// the loop PROCESSES, not the number it emits. PG is explicit about the
// distinction at the assignment ("Compute number of tuples processed (not
// number emitted!)", costsize.c) and then charges the combined
// `cpu_per_tuple = cpu_tuple_cost + restrict_qual_cost.per_tuple` on it:
//
//	ntuples = outer_path_rows * inner_path_rows;
//	cpu_per_tuple = cpu_tuple_cost + restrict_qual_cost.per_tuple;
//	run_cost += cpu_per_tuple * ntuples;
//
// goopg splits that sum across two sites — the qual half is the caller's
// `qualEvalCost(cp, len(quals), outerRows*innerRows)` — and the
// `cpu_tuple_cost` half was landing on the join's OUTPUT rows instead. That is
// the one direction the error cannot be tolerated in: a nested loop is chosen
// precisely when its output is small, so charging the tuple cost on the output
// makes a cartesian loop over a collapsed estimate look free. Q47's
// `{v1,v1_lag} ⋈ v1_lead` (three CTE self-scans, four stats-less equalities ⇒
// the outer sizes to 1 row) priced NL at 968.53 against a hash at 968.55 and
// won by 0.02, then rescanned 7 193 rows per outer row at runtime: 8 m 40 s
// against 11–13 s. Charged on `ntuples` the same NL is 1040.45 and the hash
// wins. The hash and merge siblings are NOT affected — PG charges those on
// `hashjointuples` / `mergejointuples`, which really are output counts.
//
// innerRows is the INNER PATH's own row count (`inner_path->rows`), so for a
// parameterised NLI inner it is the per-probe count (`ppi_rows`), not the
// relation's total — the same number the caller's `qualEvalCost` uses.
func nestloopCost(cp costParams, outer, inner Cost, outerRows, innerRows, innerRescanTotal float64) Cost {
	startup := outer.Startup + inner.Startup
	// PG's clamp sits at the top of final_cost_nestloop and so reaches only
	// the tuple count, not the rescan term `initial_cost_nestloop` already
	// accumulated: a zero path row count would otherwise zero the whole
	// per-tuple charge.
	ntuples := math.Max(outerRows, 1) * math.Max(innerRows, 1)
	run := (outer.Total - outer.Startup) + outerRows*innerRescanTotal + cp.cpuTupleCost*ntuples
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
