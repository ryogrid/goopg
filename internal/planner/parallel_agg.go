package planner

// parallel_agg.go — P9 of docs/design/parallel-query/, chapter 06.
//
// Which aggregates may be split across a parallel boundary. The combine rules
// themselves live in the executor (parallel_agg_combine.go); this is the gate
// that decides whether a split is built at all.

import "strings"

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
		// A user aggregate is decomposable exactly when it declares a combine
		// function — which is what COMBINEFUNC is for, and which goopg already
		// parses and stores.
		return call.UserAgg.CombineFunc != ""
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
