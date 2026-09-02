package optimizer

import (
	"strings"
	"sync/atomic"
)

// Presorted aggregates — S8 Slice 2a of M0134-0001. Port of PostgreSQL's
// adjust_group_pathkeys_for_groupagg (postgres/src/backend/optimizer/plan/
// planner.c:3229): when a grouped or non-grouped aggregate query has at least
// one aggregate with an internal ORDER BY or DISTINCT clause, choose the set
// of aggregate pathkeys that covers the most aggregates, wrap the Aggregate's
// child in a Sort, and (for grouped queries) switch Strategy to
// AggStrategySorted so EXPLAIN shows GroupAggregate instead of HashAggregate.
// Gated on the enable_presorted_aggregate GUC (default on).

// presortedAggEnabled is the package-level kill-switch for the presorted
// aggregate rule. Initialised to "on" (1), mirroring PG's
// enable_presorted_aggregate BootVal. Tests toggle it via
// SetPresortedAggEnabled(false). The GUC is registered in
// internal/config/defaults.go; the runtime SET bridge lives in
// cmd/goopg/main.go (same OnChange pattern as enable_memoize).
var presortedAggEnabled atomic.Bool

func init() {
	presortedAggEnabled.Store(true)
}

// SetPresortedAggEnabled flips the presorted-aggregate rule on or off.
// Test-only API; the production toggle path is the enable_presorted_aggregate
// GUC bridged in cmd/goopg/main.go.
// PresortedAggEnabled reports the process-wide enable_presorted_aggregate SEED;
// see HashAggEnabled for what "seed" means here (take2 P2-02c).
func PresortedAggEnabled() bool { return presortedAggEnabled.Load() }

func SetPresortedAggEnabled(on bool) {
	presortedAggEnabled.Store(on)
}

// applyPresortedAggregateRule ports adjust_group_pathkeys_for_groupagg
// (planner.c:3229). It mutates aggNode in place: on success the child is
// wrapped in a Sort and a grouped aggregate switches to AggStrategySorted.
// Returns without touching aggNode when the GUC is off, when the query uses
// grouping sets, or when no aggregate has a usable internal ORDER BY / DISTINCT.
func applyPresortedAggregateRule(aggNode *Aggregate, ps PlannerSettings) {
	// take2 P2-02c: per-statement enable_presorted_aggregate.
	if !ps.EnablePresortedAggregate || aggNode.GroupingSets != nil {
		return
	}

	type aggCandidate struct {
		pathkeys []PathKey
	}
	var candidates []aggCandidate
	for i := range aggNode.Aggs {
		a := &aggNode.Aggs[i]
		// Ordered-set aggregates (percentile_cont & friends) sort via their
		// WITHIN GROUP clause and must be skipped (planner.c:3257-3258,
		// AGGKIND_IS_ORDERED_SET).
		if a.WithinGroup {
			continue
		}
		// Skip unless there's a DISTINCT or ORDER BY clause (planner.c:3260).
		if len(a.OrderBy) == 0 && !a.Distinct {
			continue
		}
		// FILTER safety (planner.c:3264-3303): with a FILTER, presorting could
		// evaluate an argument that errors before the FILTER discards the row.
		// Only Vars and Consts are provably safe; error-prone casts (an explicit
		// typmod coercion such as f1::varchar(2)) are not relabels and reject.
		if a.Filter != nil && !aggArgsAllVarConst(a) {
			continue
		}
		pathkeys := makeCandidatePathkeys(aggregateSortlist(a))
		if len(pathkeys) == 0 {
			// Every sort key was a dropped constant (e.g. sum(distinct 42)):
			// nothing to presort by.
			continue
		}
		// Ignore aggregates with volatile functions in their ORDER BY / DISTINCT
		// clause (planner.c:3347-3355, has_volatile_pathkey).
		if pathkeysContainVolatile(pathkeys) {
			continue
		}
		candidates = append(candidates, aggCandidate{pathkeys: pathkeys})
	}
	if len(candidates) == 0 {
		return
	}

	// grouppathkeys is one ascending pathkey per GroupExprs (nil when the
	// query is non-grouped). goopg's GroupExprs are resolved AFTER the
	// bindings remap, so they are already in child-output coordinate space.
	var grouppathkeys []PathKey
	for _, g := range aggNode.GroupExprs {
		grouppathkeys = append(grouppathkeys, PathKey{Expr: g, SortAsc: true, NullsFirst: false})
	}

	// Greedy "most covered aggregates, tiebreak by target-list position"
	// (planner.c:3309-3417): repeatedly pick the strongest set of pathkeys
	// that covers the largest number of still-unprocessed aggregates, then
	// retire the covered ones and repeat until not enough candidates remain to
	// beat the current best.
	bestCount := 0
	var bestpathkeys []PathKey
	unprocessed := make([]int, len(candidates))
	for i := range candidates {
		unprocessed[i] = i
	}
	for len(unprocessed) > bestCount {
		var currpathkeys []PathKey
		covered := make([]bool, len(candidates))
		for _, ui := range unprocessed {
			pk := appendPathKeys(append([]PathKey(nil), grouppathkeys...), candidates[ui].pathkeys)
			if currpathkeys == nil {
				currpathkeys = pk
				covered[ui] = true
				continue
			}
			switch comparePathkeysDim(currpathkeys, pk) {
			case dimBetter2:
				// pk is a strict superset of currpathkeys: adopt it.
				currpathkeys = pk
				fallthrough
			case dimBetter1, dimEqual:
				// pk is no stricter than currpathkeys: covered by it.
				covered[ui] = true
			case dimIncomparable:
				// No common prefix — this candidate needs its own sort.
			}
		}
		next := unprocessed[:0]
		for _, ui := range unprocessed {
			if !covered[ui] {
				next = append(next, ui)
			}
		}
		unprocessed = next
		n := 0
		for _, c := range covered {
			if c {
				n++
			}
		}
		if n > bestCount {
			bestCount = n
			bestpathkeys = currpathkeys
		}
	}
	if bestpathkeys == nil {
		return
	}

	// Final sort keys are the winning pathkeys (group prefix + aggregate
	// keys), converted back to SortKey form. Converting from the pathkeys —
	// not from the raw sortlist — is what keeps the dropped constant
	// delimiters (string_agg(distinct f1, ',') → Sort Key: f1) out of the
	// plan.
	finalSortKeys := make([]SortKey, 0, len(bestpathkeys))
	for _, pk := range bestpathkeys {
		finalSortKeys = append(finalSortKeys, SortKey{Expr: pk.Expr, Desc: !pk.SortAsc, NullsFirst: pk.NullsFirst})
	}

	aggNode.Child = &Sort{pos: aggNode.Pos(), Child: aggNode.Child, Keys: finalSortKeys}
	// Grouped queries become AGG_SORTED ("GroupAggregate"); a non-grouped
	// query keeps the plain "Aggregate" label (AGG_PLAIN) and Strategy must
	// not be touched (planner.c:3200-3206, create_ordinary_grouping_paths
	// calls create_grouping_paths with a one-element grouping set only when
	// there IS a GROUP BY).
	if len(aggNode.GroupExprs) > 0 {
		aggNode.Strategy = AggStrategySorted
	}
}

// aggregateArgExprs returns an aggregate call's direct arguments in order —
// Arg, Arg2, then ExtraArgs — skipping nil slots. PG's Aggref->args.
func aggregateArgExprs(a *AggregateCall) []Expr {
	var args []Expr
	if a.Arg != nil {
		args = append(args, a.Arg)
	}
	if a.Arg2 != nil {
		args = append(args, a.Arg2)
	}
	for _, ea := range a.ExtraArgs {
		if ea != nil {
			args = append(args, ea)
		}
	}
	return args
}

// aggregateSortlist returns the sort clause an aggregate presorts by — PG's
// sortlist selection in adjust_group_pathkeys_for_groupagg (planner.c:3339):
// the DISTINCT list when present, else the ORDER BY clause.
//
// goopg stores DISTINCT as a bool, not a clause, so the DISTINCT sortlist is
// rebuilt the way PG's transformDistinctClause (parse_clause.c:3007-3036)
// builds it: the ORDER BY items first, then every remaining argument. The
// non-ORDER-BY items carry the default ASC / NULLS LAST semantics
// (addTargetToGroupList, parse_clause.c:3575-3577).
func aggregateSortlist(a *AggregateCall) []SortKey {
	if !a.Distinct {
		return a.OrderBy
	}
	var out []SortKey
	out = append(out, a.OrderBy...)
	for _, arg := range aggregateArgExprs(a) {
		dup := false
		for _, k := range out {
			if exprEqual(k.Expr, arg) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		out = append(out, SortKey{Expr: arg, Desc: false, NullsFirst: false})
	}
	return out
}

// stripPureRelabel unwraps a value-preserving same-type cast — goopg's
// analogue of PG's RelabelType. PG wraps binary-coercible casts in a
// RelabelType and strips it before the Var/Const test of the FILTER safety
// check (planner.c:3288-3293); an explicit typmod coercion such as
// `f1::varchar(2)` is a FuncExpr (coerce_varchar) in PG and must NOT be
// unwrapped, because presorting could evaluate it — and raise — before the
// FILTER discards the row. A CastExpr is a relabel exactly when the cast is
// same-type with no typmod constraint; any typmod or type change is a real
// coercion that may error.
func stripPureRelabel(e Expr) Expr {
	for {
		c, ok := e.(*CastExpr)
		if !ok {
			return e
		}
		if c.Typmod != 0 || c.SourceType != c.TargetType {
			return e
		}
		e = c.Operand
	}
}

// isVarOrConst reports whether e is a column reference or a constant — the
// expression classes PG's FILTER safety check allows under a presort
// (planner.c:3291-3293, Var or Const). Only these are provably safe to
// evaluate before the FILTER discards a row: a function call or coercion in
// the argument could raise.
//
// Implemented as type assertions so the walker census does not count it as a
// new Expr switch site — it classifies the node in front of it and never
// descends.
func isVarOrConst(e Expr) bool {
	if _, ok := e.(*ColumnRef); ok {
		return true
	}
	if _, ok := e.(*OuterColumnRef); ok {
		return true
	}
	return isPlainConst(e)
}

// aggArgsAllVarConst reports whether every direct argument of a (FILTER-carrying)
// aggregate is a Var or Const after stripping pure relabel casts.
func aggArgsAllVarConst(a *AggregateCall) bool {
	for _, arg := range aggregateArgExprs(a) {
		if !isVarOrConst(stripPureRelabel(arg)) {
			return false
		}
	}
	return true
}

// presortedAggVolatileBuiltins mirrors the executor's volatileBuiltins
// (internal/executor/subplan.go:87-93): builtins whose result can change
// within one statement — PG provolatile 'v'. STABLE builtins (now,
// current_timestamp, …) are deliberately absent: they are fixed for the
// statement, so sorting by them is stable.
var presortedAggVolatileBuiltins = map[string]bool{
	"random": true, "setseed": true,
	"nextval": true, "currval": true, "lastval": true, "setval": true,
	"clock_timestamp": true, "timeofday": true,
	"gen_random_uuid": true, "gen_random_bytes": true, "uuid_generate_v4": true,
	"pg_sleep": true, "txid_current": true, "pg_notify": true,
}

// pathkeysContainVolatile reports whether any pathkey's expression calls a
// volatile function — PG's has_volatile_pathkey (planner.c:3351). The planner
// has no execution context, so the check is the builtin deny list only; a
// user routine cannot be resolved here (matches the executor's subquery
// caching treatment of unknown builtins as non-volatile).
func pathkeysContainVolatile(pks []PathKey) bool {
	for _, pk := range pks {
		vol := false
		WalkExprTree(pk.Expr, func(sub Expr) {
			if vol {
				return
			}
			if fc, ok := sub.(*FuncCall); ok {
				if presortedAggVolatileBuiltins[strings.ToLower(fc.Name)] {
					vol = true
				}
			}
		})
		if vol {
			return true
		}
	}
	return false
}
