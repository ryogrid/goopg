package optimizer

import (
	"math"
	"os"
	"strconv"

	"github.com/goopg/goopg/internal/executor/hashsize"
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
// pure functions plus the cost constants. Each is unit-tested against a
// hand-computed oracle value (cost_funcs_test.go).
//
// # Live since M0127-P5.9 (2026-08-06)
//
// The former banner here read "nothing selects on them yet (path generation is
// C3.2, selection is C4), so they cannot change a plan". That is FALSE at HEAD
// and was the most dangerous of the package's surviving inertness claims,
// because this is the file `hashJoinCost` lives in: `GOOPG_PGSHAPED_DP` now
// defaults ON (`pgShapedDPFromEnv` is `v != "0"`), `planSelect` calls the
// search, and `addHashJoinPath` (pathgen.go) prices every hash path the live
// search considers. A change to any function below MOVES PRODUCTION PLANS and
// carries the full planner bar (UNITS + SPOT + DS05), not a unit test alone.
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

	// enable* are PG's planner-method GUCs (take2 P2-05, B-17a/B-17d). True
	// means the method is enabled; a disabled method still PRODUCES its path
	// and increments Path.DisabledNodes, as PG 18 does.
	enableHashJoin  bool
	enableMergeJoin bool
	enableNestLoop  bool
	// enableSort is `enable_sort` (B-17a): cost_sort's own flag on top of the
	// input's count (costsize.c:2144). The Sort producer is sortPathFor.
	enableSort   bool
	enableMemoize bool
	// enableSeqScan / enableIndexScan / enableBitmapScan are `enable_seqscan`
	// / `enable_indexscan` / `enable_bitmapscan` (B-17d): cost_seqscan's,
	// cost_index's and cost_bitmap_heap_scan's own flags (costsize.c:295, 560,
	// 1023). The scan producers always generate the path and count, exactly
	// as the join producers do since P2-05.
	enableSeqScan    bool
	enableIndexScan  bool
	enableBitmapScan bool
	// enableGatherMerge is `enable_gathermerge` (C-19d): cost_gather_merge's
	// own flag (costsize.c:536, `path->path.disabled_nodes = input_disabled_
	// nodes + (enable_gathermerge ? 0 : 1)`). It is the COUNTING form of
	// `ParallelSettings.DisableGatherMerge`, whose comment asked for exactly
	// this conversion once P5-04 landed real GatherMerge paths. `cost_gather`
	// has no flag upstream, so there is no enableGather beside it.
	enableGatherMerge bool
	geqo            bool
	geqoThreshold   int
	geqoEffort      int
	geqoPoolSize    int
	geqoGenerations int
	geqoBias        float64
	geqoSeed        float64

	// maxParallelWorkersPerGather / minParallelTableScanBlocks /
	// minParallelIndexScanBlocks / parallelLeaderParticipation are the
	// parallel GUCs the path model reads (C-19a/b/c; see PlannerSettings for
	// the unit and zero-value rules). `compute_parallel_worker` reads the
	// first three (the index threshold from C-19c's partial index paths),
	// `get_parallel_divisor` the last.
	maxParallelWorkersPerGather int
	minParallelTableScanBlocks  int64
	minParallelIndexScanBlocks  int64
	parallelLeaderParticipation bool
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
		// take2 P2-03: the budget is work_mem * hash_mem_multiplier
		// (get_hash_memory_limit), expressed through the SAME helper the
		// executor's buildGeometry calls. Writing the bare default here again
		// is what let the two drift apart in the first place.
		workMem:         hashsize.HashMemLimit(hashsize.DefaultMemLimitBytes, hashsize.DefaultHashMemMultiplier),
		enableHashJoin:  true,
		enableMergeJoin: true,
		enableNestLoop:  true,
		enableSort:      true,
		enableSeqScan:   true,
		enableIndexScan: true,
		enableBitmapScan: true,
		enableGatherMerge: true,
		enableMemoize:   true,
		geqo:            GeqoEnabled(),
		geqoThreshold:   GeqoThreshold(),
		geqoEffort:      5,
		geqoBias:        2.0,
		// The registered boot values (internal/utils/misc/defaults.go):
		// max_parallel_workers_per_gather = 4 is a deliberate goopg default
		// (PG 18.3 ships 2 — workers here are goroutines, not scarce slots);
		// min_parallel_table_scan_size = 8MB = 1024 blocks,
		// min_parallel_index_scan_size = 512kB = 64 blocks and
		// parallel_leader_participation = on are PG's own.
		maxParallelWorkersPerGather: 4,
		minParallelTableScanBlocks:  8 * 1024 * 1024 / blockSizeBytes,
		minParallelIndexScanBlocks:  512 * 1024 / blockSizeBytes,
		parallelLeaderParticipation: true,
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
// from the scan's restriction qual. The parallel arm is costParallelSeqscan.
func costSeqscan(cp costParams, relPages int64, relTuples float64, numQualOps int) Cost {
	run := cp.seqPageCost*float64(relPages) +
		(cp.cpuTupleCost+cp.cpuOperatorCost*float64(numQualOps))*relTuples
	return Cost{Startup: 0, Total: run}
}

// costParallelSeqscan is cost_seqscan's `parallel_workers > 0` arm
// (costsize.c:335-353), the price of ONE worker's share of a partial seq scan.
// It returns the cost and the per-worker row count.
//
// Only the CPU run cost is divided by `get_parallel_divisor`; the disk run
// cost is charged in full to every worker — upstream's comment: "It may be
// possible to amortize some of the I/O cost, but probably not very much,
// because most operating systems already do aggressive prefetching. For now,
// we assume that the disk run cost can't be amortized at all." The row count
// is clamp_row_est(rows / divisor), "the number of tuples processed per
// worker". C-19b (take3 08 §8): this is what makes a partial scan a REAL path
// with a real cost rather than the post-pass's size rule.
func costParallelSeqscan(cp costParams, relPages int64, relTuples, rows float64, numQualOps, workers int) (Cost, float64) {
	d := getParallelDivisor(workers, cp.parallelLeaderParticipation)
	disk := cp.seqPageCost * float64(relPages)
	cpu := (cp.cpuTupleCost + cp.cpuOperatorCost*float64(numQualOps)) * relTuples / d
	return Cost{Startup: 0, Total: disk + cpu}, clampRowEst(rows / d)
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

// costSortRun reproduces `cost_tuplesort` (costsize.c:2144, called by
// `cost_sort`): what it costs to sort `inputRows` rows of `ncols` columns.
// Comparison work is charged as STARTUP — the sort must complete before the
// first row emerges — and a per-row emit at run. The caller adds the input
// path's cost.
//
// M0127-P5.9-k added the EXTERNAL-MERGE arm, which the milestone had skipped
// with the note "TPC-H sorts are small dimension outputs". That note was
// falsified by the thing this whole phase builds: a merge join sorts a JOIN
// INPUT, and TPC-H Q12's is 5 997 241 lineitem rows. Skipping the arm was not a
// neutral simplification, because the omission is ASYMMETRIC — the hash rival
// in the very same `addPath` comparison IS charged its spill in full
// (`hashJoinCost`'s `NBatch > 1` term, P5.7-a). A sort that will certainly
// write ~4.7 GB of runs was priced as if it fit in memory while the hash join
// that writes the same data was charged 1.3 M cost units for it, which is
// exactly the "two independently calibrated models competing inside one
// addPath comparison" that design ch. 04 §1 forbids. Measured consequence: the
// PG-shaped search chose Merge Join over a full ordered index scan of `orders`
// for five TPC-H queries where the flag-OFF planner hash-joins, at 2-4x the
// runtime (09 §3.7).
//
// C-13b (P4-04 cost arm) added the third branch: `limitTuples` is
// `cost_tuplesort`'s `limit_tuples` (costsize.c:1898) — the absolute
// count+offset bound the ORDERED upper rel carries, or <= 0 for "no useful
// LIMIT". With a useful bound the middle branch prices the bounded heap-sort
// (`N log2 K` comparisons); without one `output == tuples` and the branch's
// guard is false whenever the disk branch did not fire, which is exactly the
// two-branch shape this function had before. The merge-join side always
// passes -1 (no LIMIT context above an input sort).
//
// `ncols` sizes one row through `hashsize.EntryBytes` — the SAME byte model
// `spillPages` uses for the hash rival's batch files, so the two spill charges
// this function was added to reconcile are denominated in one currency. Zero
// means "column count unknown" and suppresses the disk arm, matching
// `hashJoinCost`'s reading of a zero `innerCols` as "assume no spill": an
// unknown width must not invent an I/O charge. The bounded arm is likewise
// unreachable at ncols == 0 (output_bytes is unknowable there): a bound sort
// of unknown width keeps the quicksort price, never a heap price derived
// from a guessed width.
//
// `avgVarBytes` is the row's variable-width payload, the second argument of
// that same `EntryBytes` — the one `hashJoinCost` has passed as the real
// `innerAvgVarBytes` since M0128-P3.1 while this function passed a literal 0
// (`docs/design/planner-spill-cost-calibration/DESIGN.md` §3.3, Cut 1). A
// sort of text-heavy rows was sized as if every column were fixed-width,
// which under-charges the spill in the direction §3.2 already errs. Callers
// hand in the rel's `AvgVarBytes`; zero stays a legitimate value ("no
// ANALYZE, or every column fixed-width") and changes nothing.
func costSortRun(cp costParams, inputRows float64, ncols int, avgVarBytes float64, limitTuples float64) Cost {
	// "We want to be sure the cost of a sort is never estimated as zero, even
	// if passed-in tuple count is zero. Besides, mustn't do log(0)..."
	// (costsize.c) — PG clamps rather than returning zero, and a zero here
	// would make a sort of a collapsed estimate free, which is the failure mode
	// P5.9-j closed on the nested-loop side.
	tuples := inputRows
	if tuples < 2.0 {
		tuples = 2.0
	}
	comparisonCost := 2.0 * cp.cpuOperatorCost
	// `output_tuples` (costsize.c:1918): the bound, when useful.
	output := tuples
	if limitTuples > 0 && limitTuples < tuples {
		output = limitTuples
	}
	startup := comparisonCost * tuples * math.Log2(tuples)

	if ncols > 0 && cp.workMem > 0 {
		inputBytes := tuples * hashsize.EntryBytes(ncols, avgVarBytes)
		outputBytes := output * hashsize.EntryBytes(ncols, avgVarBytes)
		sortMemBytes := float64(cp.workMem)
		if outputBytes > sortMemBytes {
			// Disk-based sort of all the tuples (costsize.c:1936): the
			// page/run math still sizes the INPUT — every tuple is
			// written and re-read — while the branch itself is chosen on
			// the OUTPUT size, so a bounded sort that fits in memory
			// never spills. Without a bound output == tuples and this
			// is the old condition exactly.
			npages := math.Ceil(inputBytes / blockSizeBytes)
			nruns := inputBytes / sortMemBytes
			mergeorder := tuplesortMergeOrder(cp.workMem)
			logRuns := 1.0
			if nruns > mergeorder {
				logRuns = math.Ceil(math.Log(nruns) / math.Log(mergeorder))
			}
			npageaccesses := 2.0 * npages * logRuns
			// "Assume 3/4ths of accesses are sequential, 1/4th are not."
			startup += npageaccesses * (cp.seqPageCost*0.75 + cp.randomPageCost*0.25)
		} else if tuples > 2*output || inputBytes > sortMemBytes {
			// Bounded heap-sort keeping just K tuples in memory
			// (costsize.c:1960): N log2 K comparisons with the slightly
			// higher constant PG tweaks for curve continuity at the
			// crossover (tuples == 2*output prices identically to the
			// quicksort arm below). Without a bound output == tuples
			// and neither disjunct can fire past the disk branch, so
			// the merge side's number is unchanged.
			startup = comparisonCost * tuples * math.Log2(2.0*output)
		}
	}

	// "a small amount (arbitrarily set equal to operator cost) per extracted
	// tuple" — NOT cpu_tuple_cost, because a Sort does no qual-checking or
	// projection.
	return Cost{Startup: startup, Total: startup + cp.cpuOperatorCost*tuples}
}

// costAgg is `cost_agg` (costsize.c:2682) for SORTED and HASHED — C-15's
// replacement for the three aggregate rules' outcome-guessing. (The PLAIN
// candidate is priced by the HASHED arm at 0 group columns and 1 group,
// which is term-for-term PG's PLAIN arm: no grouping comparisons, trans
// per input tuple, final once, emit once. No third enum value exists, by
// the same zero-value discipline that keeps ungrouped nodes hashed today.)
// Inputs mirror upstream's: per-aggregate trans/final costs, group-column
// count, group count, and the input's cost/rows. inNcols/inAvgVarBytes are
// the OMITTED spill arm's future inputs (kept so the resume does not
// re-plumb callers) — see the NO-spill note at the function tail.
//
// AggClauseCosts (F3): goopg's catalog HAS procost but the planner never
// reads it, so trans charges cpu_operator_cost per aggregate per input row
// and final charges cpu_operator_cost per output group, with zero startup
// terms — the legacy display number's shape with upstream's trans/final,
// per-input/per-group structure, which is what lets real procost plug in
// later. SORTED and HASHED therefore share exactly the same total CPU cost
// and differ only in startup (streams vs blocking), so sorted-wins-iff-
// input-ordered falls out without a special case (costsize.c:2720-2732).
//
// No spill arm exists (see the NO-spill note at the function tail):
// `aggregateOp` performs grouped aggregation in memory with no spill path,
// so there is nothing to charge. inNcols/inAvgVarBytes are its future
// inputs, kept so the resume does not re-plumb callers.
func costAgg(cp costParams, strategy AggStrategy, inputRows, inputStartup, inputTotal float64, numGroupCols int, numGroups float64, nAggs int, inNcols int, inAvgVarBytes float64) Cost {
	tuples := inputRows
	if tuples < 0 {
		tuples = 0
	}
	groups := numGroups
	if groups < 1 {
		groups = 1
	}
	transPerTuple := cp.cpuOperatorCost * float64(nAggs)
	finalPerGroup := cp.cpuOperatorCost * float64(nAggs)
	groupCmpPerTuple := cp.cpuOperatorCost * float64(numGroupCols)

	if strategy == AggStrategySorted {
		// Streams: startup is the input's; total adds trans + grouping
		// comparisons per input tuple, final + emit per group.
		startup := inputStartup
		total := inputTotal + transPerTuple*tuples + groupCmpPerTuple*tuples +
			finalPerGroup*groups + cp.cpuTupleCost*groups
		return Cost{Startup: startup, Total: total}
	}

	// SORTED and HASHED block on the input the same way: startup is the
	// input's total, plus trans per input tuple. (PLAIN reaches here only
	// via the HASHED arm at 0 group columns / 1 group — see above.)
	startup := inputTotal + transPerTuple*tuples
	if strategy == AggStrategyHashed {
		// Hash computation per input tuple.
		startup += groupCmpPerTuple * tuples
	}
	total := startup + finalPerGroup*groups + cp.cpuTupleCost*groups

	// NO spill arm — deliberately, not an omission. PG's arm charges
	// batches the executor actually writes; goopg's `aggregateOp`
	// "performs grouped aggregation in memory"
	// (operators_join_agg.go:1973) with no spill path at all, so every
	// spill page charged here would be I/O that never happens — and a
	// fictional charge flips real plans (measured: it drove Q3/Q10/Q13/Q18
	// to sorted, Q13 5.67 s → 8.71 s, all four away from PG's hash). A
	// memory-blind model that picks hash for a 100M-group query risks the
	// OOM instead, but that failure already exists today and a fake I/O
	// number does not fix it. Resume WITH executor spill support, pricing
	// the batches the executor really writes (the JOIN spill currency in
	// `spillPages` is the template, not PG's depth loop — goopg has no
	// recursive hash partitioning).
	// (inNcols/inAvgVarBytes are the spill arm's future inputs — kept so
	// the resume does not re-plumb every caller.)
	return Cost{Startup: startup, Total: total}
}

// tuplesortMergeOrder is `tuplesort_merge_order` (tuplesort.c): how many input
// tapes one merge pass can afford out of `allowedMem`, which is what turns a
// run count into a PASS count.
//
// The constants are upstream's, and they are the reason a 512 MB budget almost
// never multi-passes: MAXORDER caps the fan-in at 500 runs per pass, and
// goopg's default budget already buys that cap outright.
func tuplesortMergeOrder(allowedMem int64) float64 {
	const (
		tapeBufferOverhead = blockSizeBytes      // TAPE_BUFFER_OVERHEAD
		mergeBufferSize    = blockSizeBytes * 32 // MERGE_BUFFER_SIZE
		minOrder           = 6.0                 // MINORDER
		maxOrder           = 500.0               // MAXORDER
	)
	mOrder := float64(allowedMem) / float64(2*tapeBufferOverhead+mergeBufferSize)
	if mOrder < minOrder {
		mOrder = minOrder
	}
	if mOrder > maxOrder {
		mOrder = maxOrder
	}
	return mOrder
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

	// outerAvgVarBytes / innerAvgVarBytes are the average total variable-width
	// payload per row of each side — the `avgVarBytes` parameter of
	// innerBucketSize is `innerbucketsize` from estimate_hash_bucket_stats:
	// the share of the inner relation in one bucket. Zero means "no usable
	// statistic" and suppresses the bucket-walk term entirely. take2 P2-11.
	innerBucketSize float64

	// `hashsize.Choose`. Populated from RelOptInfo.AvgVarBytes; zero when no
	// ANALYZE stats exist (correct for fixed-width relations). M0128-P3.1.
	outerAvgVarBytes, innerAvgVarBytes float64
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
	// take2 P2-11: the bucket walk. PG charges
	//
	//	run_cost += hash_qual_cost.per_tuple * outer_matched_rows *
	//	            clamp_row_est(inner_path_rows * innerbucketsize) * 0.5
	//
	// (final_cost_hashjoin) — the comparisons a probe performs walking its
	// bucket. Without it a hash join on a low-ndistinct key is priced exactly
	// like one on a unique key, which is the degeneracy Q78 hit.
	//
	// innerBucketSize == 0 means "no usable statistic"; the term is then
	// SKIPPED rather than guessed, so a stats-less plan costs exactly as it did
	// before this change.
	if in.innerBucketSize > 0 {
		bucketTuples := clampRowEst(in.innerRows * in.innerBucketSize)
		run += cp.cpuOperatorCost * float64(in.numHashClauses) *
			in.outerRows * bucketTuples * 0.5
	}

	// M0128-P3.1: avgVarBytes from column stats replaces the hardcoded zero.
	sizing := hashsize.Choose(in.innerRows, in.innerCols, in.innerAvgVarBytes, cp.workMem)
	if sizing.NBatch > 1 {
		innerPages := spillPages(in.innerRows, in.innerCols, in.innerAvgVarBytes)
		outerPages := spillPages(in.outerRows, in.outerCols, in.outerAvgVarBytes)
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
func spillPages(rows float64, ncols int, avgVarBytes float64) float64 {
	if !(rows > 0) {
		return 0
	}
	return math.Ceil(rows * hashsize.EntryBytes(ncols, avgVarBytes) / blockSizeBytes)
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
func nestloopCost(cp costParams, outer, inner Cost, outerRows, innerRows, innerRescanStartup, innerRescanTotal float64) Cost {
	startup := outer.Startup + inner.Startup
	// PG's clamp sits at the top of final_cost_nestloop and so reaches only
	// the tuple count, not the rescan term `initial_cost_nestloop` already
	// accumulated: a zero path row count would otherwise zero the whole
	// per-tuple charge.
	ntuples := math.Max(outerRows, 1) * math.Max(innerRows, 1)
	// take2 P2-07, cost_nestloop (costsize.c:3304-3327):
	//
	//     inner_run_cost        = inner.total - inner.startup
	//     inner_rescan_run_cost = rescan.total - rescan.startup
	//     run_cost += inner_run_cost
	//     if outer_path_rows > 1:
	//         run_cost += (outer_path_rows - 1) * inner_rescan_run_cost
	//
	// The inner is scanned ONCE and rescanned outerRows-1 times, and both are
	// RUN costs: the inner's startup is paid once, at the join's startup, and
	// was previously being charged again on every outer row.
	innerRun := inner.Total - inner.Startup
	innerRescanRun := innerRescanTotal - innerRescanStartup
	run := (outer.Total - outer.Startup) + innerRun + cp.cpuTupleCost*ntuples
	if outerRows > 1 {
		run += (outerRows - 1) * innerRescanRun
	}
	return Cost{Startup: startup, Total: startup + run}
}

// mergeJoinCost reproduces final_cost_mergejoin (costsize.c:3837) at milestone
// fidelity: merge two already-costed (and sorted) inputs, charging a per-row
// merge. A cost_sort on an unsorted input is added by the caller when the input's
// pathkeys do not satisfy the merge clause (design ch. 06 §3.2) — the whole
// reason pathkeys exist.
func mergeJoinCost(cp costParams, outer, inner Cost, outerRows, innerRows, outputRows, outerEndSel, innerEndSel float64) Cost {
	startup := outer.Startup + inner.Startup
	// take2 P2-12, `mergejoinscansel` applied as final_cost_mergejoin does
	// (costsize.c:3686-3745): a merge join stops once one side passes the
	// other's maximum key, so only that FRACTION of each input's RUN cost is
	// paid. Both branches of PG's sort/no-sort split scale the same way — the
	// sort's own startup, which is where the full input read lives, is charged
	// UNSCALED, and only the read-out portion scales. goopg's sortPathFor has
	// that same shape, so applying the scaling to (Total - Startup) is correct
	// whether or not the input needed sorting.
	//
	// 1 means "no information" and charges the full pass, i.e. the behaviour
	// before this term. The scaling can only ever REDUCE cost, so an unknown
	// must not be allowed to.
	if outerEndSel <= 0 || outerEndSel > 1 {
		outerEndSel = 1
	}
	if innerEndSel <= 0 || innerEndSel > 1 {
		innerEndSel = 1
	}
	run := (outer.Total-outer.Startup)*outerEndSel + (inner.Total-inner.Startup)*innerEndSel +
		cp.cpuOperatorCost*(outerRows*outerEndSel+innerRows*innerEndSel) + cp.cpuTupleCost*outputRows
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

// gatherMergeCost reproduces cost_gather_merge (costsize.c:485): a Gather that
// MERGES its workers' already-ordered streams instead of interleaving them, so
// it pays for a k-way heap on top of everything cost_gather pays for.
//
// `sub` is the partial subpath's cost (upstream's input_startup_cost /
// input_total_cost, which create_gather_merge_path reads off the subpath),
// `workers` is `path->num_workers` — the SUBPATH's parallel_workers — and
// `outputRows` is compute_gather_rows(subpath), the same figure gatherCost
// takes.
//
// The five terms, in upstream's order (:510-533):
//
//	N               = num_workers + 1   // "add one … to account for the leader"
//	comparison_cost = 2.0 * cpu_operator_cost
//	startup        += comparison_cost * N * log2(N)   // heap creation
//	run            += rows * comparison_cost * log2(N) // per-tuple maintenance
//	run            += cpu_operator_cost * rows        // "like cost_merge_append"
//	startup        += parallel_setup_cost
//	run            += parallel_tuple_cost * rows * 1.05
//
// The 1.05 is upstream's, and so is its reason: "Since Gather Merge, unlike
// Gather, requires us to block until a tuple is available from every worker,
// we bump the IPC cost up a little bit as compared with Gather. For lack of a
// better idea, charge an extra 5%."
//
// `workers` must be > 0 (upstream asserts it): a zero-worker Gather Merge is
// the single_copy shape, which C-19d's producer refuses rather than builds
// (docs/design/planner-c19d-gather-paths/DESIGN.md §4.3). The clamp here is
// belt-and-braces so an N of 1 cannot make log2(N) zero and price the merge
// free.
func gatherMergeCost(cp costParams, sub Cost, workers int, outputRows float64) Cost {
	if workers < 1 {
		workers = 1
	}
	n := float64(workers) + 1
	logN := math.Log2(n)
	comparisonCost := 2.0 * cp.cpuOperatorCost

	startup := comparisonCost*n*logN + cp.parallelSetupCost
	run := outputRows*comparisonCost*logN +
		cp.cpuOperatorCost*outputRows +
		cp.parallelTupleCost*outputRows*gatherMergeIPCFactor

	return Cost{
		Startup: startup + sub.Startup,
		Total:   startup + run + sub.Total,
	}
}

// gatherMergeIPCFactor is cost_gather_merge's extra 5% on the IPC term
// (costsize.c:533). Named rather than written inline so a test can express the
// relationship "a Gather Merge's tuple transfer costs more than a Gather's"
// through the constant instead of pinning 1.05.
const gatherMergeIPCFactor = 1.05

// computeGatherRows reproduces compute_gather_rows (costsize.c:6625): a partial
// path's `rows` is the PER-WORKER count (cost_seqscan's parallel arm divides by
// get_parallel_divisor), so the Gather above it multiplies the same divisor
// back to recover the relation's own row count.
//
// It reads the divisor from the SUBPATH's worker count, never from the rel or
// the GUC, because that is the count the subpath was priced with — pricing a
// Gather with a divisor the child did not use is how a row estimate silently
// stops matching the cost beside it.
func computeGatherRows(sub *Path, cp costParams) float64 {
	if sub == nil {
		return 0
	}
	return clampRowEst(sub.Rows * getParallelDivisor(sub.ParallelWorkers, cp.parallelLeaderParticipation))
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
// GOOPG_INDEX_PROBE_MULT for measurement.
//
// **Calibrated to 2.0 on 2026-09-05** (C-20d). The knob had shipped at 1.0 —
// exactly the value this comment says under-costs goopg's probes — because
// the calibration it was created for was never run. Measured at SF=1, serial,
// pinned-statistics regime, fresh server per arm, values 24/24 MATCH against
// the multiplier-1 baseline on every arm:
//
//	        mult=1     mult=2     mult=4
//	Q5      21.60 s     4.07 s     6.84 s
//	Q7      15.72 s     5.86 s     6.24 s
//	Q9      13.17 s     7.06 s     7.18 s
//	Q3       6.25 s     2.67 s        —
//	suite  138.58 s   100.79 s        —
//
// 2 and 4 pick the same plans on the probed queries, so 2 is chosen as the
// smaller departure from PG's constants that still buys the whole win —
// raising it further is unjustified without evidence. Every other query moved
// within the noise band. The multiplier stays a knob rather than becoming a
// hard-coded 2 so the next recalibration (after the NL-probe execution work
// this comment describes) can be measured the same way.
var indexProbeCostMultiplier = indexProbeMultFromEnv(os.Getenv("GOOPG_INDEX_PROBE_MULT"))

// indexProbeMultFromEnv resolves GOOPG_INDEX_PROBE_MULT's raw value to the
// multiplier the planner uses. It is a named function rather than an inline
// envFloatDefault call so the flag-provenance table can resolve the SAME
// default the process resolves (flaglabels.go's flagResolvedState), which is
// what makes `unset(1)` a statement about the binary instead of a restated
// constant. Reading it through a helper is also what hid this flag from
// TestFlagProvenanceTableCoversPlannerEnv for an entire milestone — see that
// test's go/ast detector.
func indexProbeMultFromEnv(v string) float64 {
	if v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return indexProbeMultCalibrated
}

// indexProbeMultCalibrated is the validated default (see the block comment on
// indexProbeCostMultiplier for the measurement that set it). Named rather than
// inlined so the flag-provenance table resolves the same default the process
// does — `unset(2)` is then a statement about the binary, not a restated
// constant.
const indexProbeMultCalibrated = 2.0

// `multiHashJoinCost` costed goopg's N-way MultiHashJoin under the
// comparability invariant (design ch. 06 §4.1) — build every dimension hash
// table as startup, then one probe pass over the driving table — in the same
// PG units as the equivalent hash cascade, so add_path could rank the two.
// M0127-P6.2 deleted it with the node and its only caller
// (`generateMultiHashJoinPath`). `hashJoinCost` prices the cascade that
// remains.

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
