package optimizer

import (
	"strings"
	"sync/atomic"
)

// Presorted aggregate keys — S8 Slice 2a of M0134-0001, retired by C-15.
// Port of PostgreSQL's adjust_group_pathkeys_for_groupagg
// (postgres/src/backend/optimizer/plan/planner.c:3229): the greedy covering
// key selection over internal ORDER BY / DISTINCT clauses now serves as the
// GROUP_AGG producer's sorted candidate keys (groupingpaths.go
// presortedAggKeysOrAbsent) instead of mutating the node. The mutating rule
// is gone; the key-selection half, FILTER-safety, volatility list, and the
// GUC seed below stay. Gated on the enable_presorted_aggregate GUC
// (default on).

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

// (Retired by C-15.) applyPresortedAggregateRule used to port
// adjust_group_pathkeys_for_groupagg (planner.c:3229) as a mutator: on
// success the child was wrapped in a Sort and a grouped aggregate switched
// to AggStrategySorted. The key selection survives verbatim as the GROUP_AGG
// producer's sorted-candidate keys (groupingpaths.go
// presortedAggKeysOrAbsent); the mutation is gone.
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
