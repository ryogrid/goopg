package optimizer

// parallel_agg.go — P9 of docs/design/parallel-query/, chapter 06.
//
// Which aggregates may be split across a parallel boundary. The combine rules
// themselves live in the executor (parallel_agg_combine.go); this is the gate
// that decides whether a split is built at all.

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// normalizeAggName matches how the executor's applyAgg and finishAgg dispatch
// on the name, so the whitelist and the transition function cannot disagree
// about what an aggregate is called.
func normalizeAggName(n string) string { return strings.ToLower(strings.TrimSpace(n)) }

// AggregateIsDecomposable reports whether an aggregate call can be split into
// partial + finalize.
//
// This is a WHITELIST, deliberately. The executor's applyAgg ends in a
// `default:` catch-all that does `st.count++; st.sum += arg.Int` for any
// unrecognised name, so a blacklist would let an aggregate added later split
// through that arm and return garbage. A name absent from this function is
// refused, not guessed at.
func AggregateIsDecomposable(call AggregateCall) bool {
	// Order-dependence and global-state requirements defeat the split
	// regardless of the function.
	if call.Distinct {
		// Each worker's `distinct` map sees only its own share, so dedup
		// would be per-worker rather than global.
		return false
	}
	if len(call.OrderBy) > 0 {
		// An ORDER BY inside the aggregate makes the result depend on input
		// order, which parallel scans do not preserve.
		return false
	}
	if call.UserAgg != nil {
		// Never decomposable. goopg's combineAggRuntime has no rule to invoke
		// a user combinefunc — it deep-merges the typed aggRuntime fields
		// (count, intResult, floatSx, ...), not transition-state datums — so a
		// user aggregate that declared COMBINEFUNC would still hit the
		// combineAggRuntime default arm and error at combine time. Refuse
		// fail-closed (M0134-0001 S10a); invoking the user combinefunc is a
		// separate, deferred slice.
		return false
	}

	switch normalizeAggName(call.Name) {
	case "count", "sum", "avg", "min", "max",
		"bool_and", "every", "bool_or",
		"bit_and", "bit_or", "bit_xor",
		"any_value",
		"var_pop", "var_samp", "variance", "stddev_pop", "stddev_samp", "stddev",
		"regr_count", "regr_avgx", "regr_avgy", "regr_sxx", "regr_syy", "regr_sxy",
		"covar_pop", "covar_samp", "regr_r2", "regr_slope", "regr_intercept", "corr":
		return true
	default:
		// Includes array_agg and string_agg (order-dependent), the
		// WITHIN GROUP ordered-set aggregates, and anything new.
		return false
	}
}

// AggregateIsOrderSensitive reports whether an aggregate's RESULT depends on
// the order its input arrives in.
//
// These are safe to split — they are refused by AggregateIsDecomposable
// already — but they are not safe to run above a plain Gather either, which is
// a separate and less obvious problem. The Gather concatenates worker batches
// in arrival order, so a leader-side string_agg over gathered rows returns its
// elements shuffled, and differently shuffled on every run.
//
// PostgreSQL tolerates exactly this: it marks these aggregates parallel-safe
// and their unordered result is documented as implementation-defined. goopg
// refuses instead, for two reasons. Its stated contract (chapter 09) is that a
// parallel plan returns what the serial plan returns, and a query that gave a
// stable answer before parallelism existed should not start shuffling. Copying
// PG's laxity here would buy nothing — these aggregates are not decomposable,
// so there is no parallel aggregation to gain, only a parallel scan.
//
// An explicit ORDER BY inside the aggregate makes it deterministic again,
// because the aggregate sorts its own input, so that form is not refused.
func AggregateIsOrderSensitive(call AggregateCall) bool {
	if len(call.OrderBy) > 0 {
		return false
	}
	switch normalizeAggName(call.Name) {
	case "array_agg", "string_agg", "json_agg", "jsonb_agg", "xmlagg",
		"json_object_agg", "jsonb_object_agg":
		return true
	}
	return false
}

// aggregateSplitIsSafe reports whether a whole Aggregate node may be split.
//
// Every call must be decomposable — one that is not poisons the entire node,
// since partial and finalize operate on the node's shared group map rather
// than per-call.
func aggregateSplitIsSafe(a *Aggregate) bool {
	if a == nil || a.Mode != AggModeSimple {
		return false
	}
	if a.GroupingSets != nil {
		// A grouping-sets node keeps one hash table per set and the set a
		// group belongs to is carried outside the group key that the partial
		// accumulator merges on, so a split would fold every level's groups
		// together. PostgreSQL likewise refuses to make a grouping-sets Agg
		// parallel-aware (optimizer/plan/planner.c, consider_groupingsets_paths
		// is reached only for the non-partial grouping path). M0125-0048.
		return false
	}
	if len(a.Aggs) == 0 {
		// A group-only node (SELECT DISTINCT lowered to GROUP BY) has nothing
		// to combine, and splitting it would emit each worker's group set as
		// if it were the whole one. Refused: the finalize has no rule that
		// deduplicates groups across workers.
		return false
	}
	for _, call := range a.Aggs {
		if !AggregateIsDecomposable(call) {
			return false
		}
	}
	return true
}

// ── the split cost model (chapter 11) ──────────────────────────────────────
//
// Relative cost constants, anchored on cOut = 1 (retrieving one group from a
// hash table — PG's cpu_tuple_cost). See chapter 11 §3.3 for the derivation
// and for the honest note that they are calibrated against one query.
const (
	// cXfer is one row crossing a Gather. PG charges 10x cpu_tuple_cost for a
	// shm_mq write plus the reader's copy-out; goopg batches into a channel,
	// so the per-tuple channel cost amortises away and what remains is
	// MaterializeForTransfer — a row copy, not an IPC round trip.
	cXfer = 2.0
	// cTrans is one aggregate transition call, charged per aggregate.
	cTrans = 1.0
	// cHash is the per-group-column hashing charge per input row, at PG's
	// cpu_operator_cost ratio.
	cHash = 0.25
	// cMerge is one partial state merged into the shared accumulator.
	//
	// This is the constant the gate turns on, and it is the one goopg does not
	// share with PG at all. PG ships partial states through the Gather as
	// tuples; goopg's Partial node emits no rows and merges into a
	// mutex-guarded map instead (chapter 11 §2.1), so this is a lock
	// acquisition plus a map probe plus a deep merge over aggRuntime's pointer
	// fields — and it does not parallelise.
	cMerge = 4.0
	// cOut is one group emitted.
	cOut = 1.0
)

// parallelDivisor reproduces upstream's get_parallel_divisor
// (postgres/src/backend/optimizer/path/costsize.c:6474): the leader's
// contribution counts only while it is positive, so d(1)=1.7, d(2)=2.4,
// d(3)=3.1, and d(w)=w from four workers up.
func parallelDivisor(workers int, leaderParticipates bool) float64 {
	if workers <= 0 {
		return 1
	}
	d := float64(workers)
	if leaderParticipates {
		if lc := 1.0 - 0.3*float64(workers); lc > 0 {
			d += lc
		}
	}
	return d
}

// groupsToRowsRatio estimates rho — per-worker groups times the divisor, over
// input rows — for an aggregate, or reports that it cannot.
//
// The whole model reduces to this one quantity. With the clamp
// Gw = min(ndistinct, R/d), the ratio Gw*d/R collapses to
//
//	rho = min(1, f*d)   where f = ndistinct/R
//
// so neither the group count nor the row count is needed in absolute terms —
// only their ratio, which is exactly what ANALYZE now stores as
// ColumnStats.NDistinctFrac (PG's negative stadistinct). That is what lets the
// gate work on a freshly started server, where TableStats.RowCount is still
// zero because it is never restored (ledger pq-P6).
//
// Multiple group columns multiply, assuming independence, as upstream's
// estimate_num_groups does (selfuncs.c:3664). The product of fractions is
// itself a fraction, so the clamp at 1 is the only bound needed.
func groupsToRowsRatio(a *Aggregate, d float64) (float64, bool) {
	if a == nil || len(a.GroupExprs) == 0 {
		// An ungrouped aggregate reduces everything to one row per worker.
		// Nothing to estimate and nothing that could go wrong.
		return 0, true
	}
	frac := 1.0
	seen := map[int]bool{}
	for _, ge := range a.GroupExprs {
		cr, ok := ge.(*ColumnRef)
		if !ok {
			// Expression group keys need estimate_num_groups' Var
			// decomposition, which this cut does not implement. Refusing to
			// estimate leaves today's behaviour rather than guessing.
			return 0, false
		}
		if seen[cr.Index] {
			continue
		}
		seen[cr.Index] = true
		cs := aggColumnStats(cr.Index, a.Child)
		if cs == nil || cs.NDistinctFrac <= 0 {
			return 0, false
		}
		frac *= cs.NDistinctFrac
	}
	rho := frac * d
	if rho > 1 {
		rho = 1
	}
	return rho, true
}

// aggColumnStats resolves a group key to its column statistics.
//
// It descends ONLY through the row-wise wrappers, and deliberately not through
// a Join: the fraction is relative to the BASE relation's row count, whereas a
// join-fed aggregate reads the join's output. TPC-H Q13 is the worked example —
// c_custkey is unique within customer (f = 1) but accounts for only a tenth of
// the 1.5M-row join output it actually aggregates, so applying the fraction
// there would refuse a split that reduces 10x. Those shapes keep today's
// ungated behaviour until absolute row counts exist (ledger pq-P10).
//
// It also does not descend through a Project, because Project.Targets[i] is an
// arbitrary expression over the child's schema: passing the output ordinal
// through unremapped returns whichever table column happens to sit at that
// position. That is a live bug in columnStatsForChild (selectivity.go:385) and
// this must not inherit it — refusing is a missed optimisation, a wrong
// n_distinct is a wrong decision.
func aggColumnStats(idx int, child Node) *catalog.ColumnStats {
	switch x := child.(type) {
	case *SeqScan:
		if x.Table == nil || x.Table.Stats == nil {
			return nil
		}
		if idx < 0 || idx >= len(x.Table.Stats.Columns) {
			return nil
		}
		return &x.Table.Stats.Columns[idx]
	case *Filter:
		return aggColumnStats(idx, x.Child)
	}
	return nil
}

// splitAggregateIsProfitable is the gate.
//
// It compares only the two alternatives that differ — split against no-split —
// which share their entire subtree below the aggregate, so the shared term
// cancels and no absolute cost model is needed (chapter 11 §2). Dividing the
// comparison through by the input row count and substituting Gw = rho*R/d:
//
//	split wins  iff  cXfer + (A*cTrans + k*cHash)*(1 - 1/d)  >  rho*A*cMerge + (rho/d)*cOut
//
// Refuses when the ratio cannot be estimated. Refusal restores the pre-P9
// shape — the Gather moves below the aggregate — which is slower but never
// dangerous; accepting without evidence risks N group tables with no spill
// path, and an out-of-memory is not a slow query.
func splitAggregateIsProfitable(a *Aggregate, workers int, leaderParticipates bool) bool {
	if a == nil || workers <= 0 {
		return false
	}
	d := parallelDivisor(workers, leaderParticipates)
	if d <= 1 {
		return false
	}
	rho, ok := groupsToRowsRatio(a, d)
	if !ok {
		return false
	}
	nAggs := float64(len(a.Aggs))
	if nAggs < 1 {
		nAggs = 1
	}
	perRow := nAggs*cTrans + float64(len(a.GroupExprs))*cHash
	saving := cXfer + perRow*(1-1/d)
	cost := rho*nAggs*cMerge + (rho/d)*cOut
	return saving > cost
}
