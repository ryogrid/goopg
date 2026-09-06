package optimizer

import (
	"github.com/goopg/goopg/internal/parser"
)

// M0127-P5.7-b — the LIMIT fraction: what makes a startup cost SELECTABLE.
//
// PG oracle: `preprocess_limit` (planner.c:2577) turns the query's LIMIT/OFFSET
// into `root->tuple_fraction`; `get_cheapest_fractional_path` (planner.c:6617)
// asks a rel which of its paths wins at that fraction; and
// `compare_fractional_path_costs` (pathnode.c:127) is the cost order the
// question is answered in. Design: leftdeep-joins 04 §4.1.
//
// # What was missing, and why it is not the cost model
//
// P5.7-a made `hashJoinCost`'s two numbers move independently and for the right
// reason — the inner's batch write is charged at startup because it happens
// during the build, the read-back at run. That is a strictly better cost
// function and it changed no plan, because NOTHING IN GOOPG SELECTED ON STARTUP
// COST. `setCheapest` has always computed `RelOptInfo.CheapestStartup`, and
// before this file no line of production code read it. A split that nothing
// selects on is bookkeeping.
//
// PG's answer is one number threaded from the query down to path selection: a
// LIMIT means only a fraction of the rows will ever be fetched, so the right
// path is the one cheapest to produce THAT fraction — `startup + fraction ×
// (total - startup)` — not the one cheapest to produce all of them. The number
// does two jobs, and this task lands both:
//
//   - it selects, through `getCheapestFractionalPath` at the search root; and
//   - it decides what is worth KEEPING, through `RelOptInfo.ConsiderStartup`
//     (path.go), because `add_path` may not retain a total-cost loser on the
//     strength of its startup cost when no fraction will ever be asked for.
//
// # Absolute vs fractional, the one subtlety
//
// `tuple_fraction` is overloaded on purpose (planner.c's own comment): a value
// > 1.0 is an absolute row COUNT, a value in (0,1) is a fraction of the result,
// and 0 means "all rows". `preprocessLimit` produces the absolute form from a
// constant LIMIT (it knows the count but not the result size, which is a
// per-rel fact) and `getCheapestFractionalPath` converts it against the rel's
// own `rows` at the moment of use. Collapsing the two representations early
// would require the row count at derivation time, which is exactly what the
// planner does not have there.
//
// # Live since M0127-P5.9 (2026-08-06), and it acquired a PRODUCER
//
// Two separate things changed, and P5.7-b's own ledger row named only the
// second as missing. `GOOPG_PGSHAPED_DP` defaults ON, so `finalPath` →
// `getCheapestFractionalPath` runs on real statements; and P5.9-b added the
// production producer this file was written without — `searchTupleFraction`
// (joinsearchseam.go:579), called from `planSelect` (planner.go:1128) at
// upstream's point in the order, before the search builds its first rel.
//
// The RETENTION half is live even for a query with no LIMIT, and that is the
// non-obvious part: `ConsiderStartup` is set from `tupleFraction > 0`, so a
// fraction of 0 does not mean "this file is doing nothing" — it means
// `comparePathCostsFuzzily` is actively PRUNING fast-start paths that goopg
// used to keep. Absent LIMIT is a decision here, not an abstention.
//
// One producer, one assignment site (planner.go:1128), and it is not a
// coverage gap: the only other `planSelect` arm that skips it is the
// `isSimpleSingle` index path, which is single-relation and never reaches the
// join search at all.

// unestimatableLimitFraction is PG's punt when LIMIT or OFFSET is not a
// constant: "for lack of a better idea, assume 10% of the plan's result is
// wanted" (planner.c:2641).
const unestimatableLimitFraction = 0.10

// limitEstimates is `preprocess_limit`'s pair of out-parameters (`count_est`,
// `offset_est`), which upstream also returns for the executor's benefit. The
// encoding is upstream's and all three states are load-bearing: 0 means the
// clause is not present (or is LIMIT ALL / a null OFFSET, which is the same
// thing), a positive value is the estimate, and -1 means present but not
// estimatable — the state that triggers the 10% punt rather than being treated
// as absent.
type limitEstimates struct {
	count  int64
	offset int64
}

// preprocessLimit is `preprocess_limit` (planner.c:2577): fold the query's
// LIMIT/OFFSET into the caller's tuple fraction.
//
// `lim` is the `*Limit` node above the join — goopg's already-resolved form of
// upstream's `parse->limitCount` / `parse->limitOffset`. A nil node means no
// LIMIT clause at all, which upstream expresses by not calling this function
// (its first line asserts one of the two is present), so the fraction passes
// through untouched.
//
// `tupleFraction` is what the CALLER already wanted, and it is not always 0:
// upstream's `standard_planner` starts from `cursor_tuple_fraction` for a
// declared cursor (planner.c:401), and a subquery inherits a fraction from the
// query above it. goopg has no such caller yet — every call site passes 0 —
// but the merge logic below is upstream's whole function precisely because the
// caller-supplied case is what the arms are about, and reconstructing them
// later from a simplified version is how the "use the smaller of the two"
// asymmetries get lost.
func preprocessLimit(lim *Limit, tupleFraction float64) (float64, limitEstimates) {
	est := limitEstimates{}
	if lim == nil {
		return tupleFraction, est
	}
	est.count = limitClauseEstimate(lim.Limit, true)
	est.offset = limitClauseEstimate(lim.Offset, false)
	if est.count == 0 && est.offset == 0 {
		// Neither clause present after estimation (LIMIT ALL / OFFSET NULL
		// land here too, which is upstream's "treat as not present").
		return tupleFraction, est
	}

	var limitFraction float64
	if est.count != 0 {
		// A LIMIT caps the ABSOLUTE number of tuples returned; a
		// non-constant one is the 10% punt (planner.c:2636-2650).
		if est.count < 0 || est.offset < 0 {
			limitFraction = unestimatableLimitFraction
		} else {
			// LIMIT plus OFFSET is the maximum number of tuples needed: the
			// rows skipped still have to be produced.
			limitFraction = float64(est.count) + float64(est.offset)
		}

		// Absolute and fractional values are compared with upstream's
		// heuristic (planner.c:2652-2688): both-absolute or both-fractional
		// take the smaller, and a mix assumes the absolute one is smaller.
		switch {
		case tupleFraction >= 1.0:
			if limitFraction >= 1.0 {
				// Both absolute.
				tupleFraction = min(tupleFraction, limitFraction)
			}
			// Caller absolute, limit fractional: keep the caller's value.
		case tupleFraction > 0.0:
			if limitFraction >= 1.0 {
				// Caller fractional, limit absolute: use the limit.
				tupleFraction = limitFraction
			} else {
				// Both fractional.
				tupleFraction = min(tupleFraction, limitFraction)
			}
		default:
			// No information from the caller: just use the limit.
			tupleFraction = limitFraction
		}
		return tupleFraction, est
	}

	if est.offset != 0 && tupleFraction > 0.0 {
		// OFFSET without LIMIT acts in the OPPOSITE direction (planner.c:2690):
		// it causes MORE tuples to be fetched, not fewer, so the fraction
		// grows. It only matters when the caller already wanted a fraction —
		// with no LIMIT and no caller fraction the whole result is fetched
		// anyway.
		if est.offset < 0 {
			limitFraction = unestimatableLimitFraction
		} else {
			limitFraction = float64(est.offset)
		}
		if tupleFraction >= 1.0 {
			if limitFraction >= 1.0 {
				// Both absolute: add them.
				tupleFraction += limitFraction
			} else {
				// Caller absolute, offset fractional: take the fractional one,
				// which upstream heuristically assumes is the larger.
				tupleFraction = limitFraction
			}
		} else {
			if limitFraction < 1.0 {
				// Both fractional: add them, and a sum that reaches 1 means
				// the whole result is wanted after all.
				tupleFraction += limitFraction
				if tupleFraction >= 1.0 {
					tupleFraction = 0.0
				}
			}
			// Caller fractional, offset absolute: keep the caller's value.
		}
	}
	return tupleFraction, est
}

// limitClauseEstimate is the per-clause half of `preprocess_limit`'s first
// block: 0 for "not present", -1 for "present but not a constant", and the
// constant's value otherwise, clamped the way upstream clamps it —
// `LIMIT <= 0` is forced to 1 (a zero-row limit is still a limit, and letting
// it be 0 would read as "no clause"), while a negative OFFSET is treated as
// absent, which is also what the executor does with it.
//
// goopg reads the resolved expression directly where upstream calls
// `estimate_expression_value`, which additionally CONST-FOLDS: `LIMIT 5 + 5`
// and a bound `LIMIT $1` are constants to PG and are the 10% punt here (ledger
// row 2026-08-05).
func limitClauseEstimate(e Expr, isCount bool) int64 {
	if e == nil {
		return 0
	}
	if _, isNull := e.(*NullConst); isNull {
		// LIMIT ALL, and a null OFFSET, which the executor also ignores.
		return 0
	}
	v, ok := constInt(e)
	if !ok {
		return -1
	}
	if isCount {
		if v <= 0 {
			return 1
		}
		return v
	}
	if v < 0 {
		return 0
	}
	return v
}

// limitTuplesFromEstimates is the absolute half of `preprocess_limit`'s
// arithmetic (planner.c:2636-2650) as `cost_tuplesort` consumes it: the
// `limit_tuples` bound, i.e. count+offset when the LIMIT is a usable bound,
// or -1 when it is not. C-13b reads it for the ORDERED upper rel; C-17's
// fraction threading is the other half and stays untouched.
//
// -1 (no bound) when: no LIMIT clause (an OFFSET alone is not a bound —
// upstream only sets `limit_tuples` from the count); WITH TIES; either
// clause present-but-not-constant (the 10% punt is a FRACTION for
// `tuple_fraction`, not an absolute bound for the heap).
//
// WITH TIES note: the `-1` is conservative goopg divergence, not PG
// fidelity. Upstream's PLANNER sets `limit_tuples = count+offset` with no
// ties exception (planner.c:1461-1462); it is the EXECUTOR's
// `compute_tuples_needed` that returns -1 for ties (nodeLimit.c:430-437),
// and PG 18.3 therefore costs the heap discount on a sort its executor
// runs unbounded. Matching that would price a discount that never
// executes; -1 overstates those rare sorts instead. Zero shape risk either
// way (single-candidate ORDERED rel).
func limitTuplesFromEstimates(count, offset int64, withTies bool) float64 {
	if withTies {
		return -1
	}
	if count == 0 {
		return -1
	}
	if count < 0 || offset < 0 {
		return -1
	}
	return float64(count) + float64(offset)
}

// limitTuplesForOrderedSort resolves the statement's LIMIT/OFFSET against
// the FROM-clause context and returns the `limit_tuples` bound C-13b hands
// the ORDERED upper rel, or -1. Resolution here mirrors the `*Limit` build
// below (same `resolveExpr`, same `limitClauseEstimate` states: 0 absent,
// positive estimate, -1 present-but-not-constant); on a resolution error it
// returns -1 and the later build raises the error itself.
func limitTuplesForOrderedSort(s *parser.SelectStmt, ctx *resolveContext) float64 {
	if s == nil || s.WithTies {
		return -1
	}
	var count, offset int64
	if s.Limit != nil {
		count = limitBoundClause(s.Limit, ctx, true)
	}
	if s.Offset != nil {
		offset = limitBoundClause(s.Offset, ctx, false)
	}
	return limitTuplesFromEstimates(count, offset, false)
}

// limitBoundClause estimates one LIMIT/OFFSET clause for the bound. Only
// bare literals (`*parser.IntegerConst`, `*parser.NullConst`) are resolved:
// they resolve without touching subqueries or CTE references, while
// anything else — a `LIMIT (SELECT …)`, a parameter, a column — would plan
// or probe real work only to be discarded here (the `*Limit` build resolves
// it again below). Skipping resolution is outcome-identical: goopg does not
// const-fold LIMIT expressions (ledger 2026-08-05) and `constInt` accepts
// only `*IntegerConst`, so every non-literal estimates as present-but-not-
// constant (-1) through the resolver too.
func limitBoundClause(e parser.Expr, ctx *resolveContext, isCount bool) int64 {
	switch e.(type) {
	case *parser.IntegerConst, *parser.NullConst:
		r, err := resolveExpr(e, ctx)
		if err != nil {
			return -1
		}
		return limitClauseEstimate(r, isCount)
	default:
		return -1
	}
}

// compareFractionalPathCosts is `compare_fractional_path_costs`
// (pathnode.c:127): -1, 0 or +1 as path1 is cheaper, the same, or more
// expensive than path2 FOR FETCHING `fraction` of the rows.
//
// A fraction outside (0,1) means "all rows", and upstream folds that back onto
// the plain total-cost order rather than treating it as an error — which is
// what makes it safe for a caller to pass an unconverted absolute count here:
// it degrades to total cost instead of producing nonsense.
func compareFractionalPathCosts(p1, p2 *Path, fraction float64) int {
	// disabled_nodes trumps all else (:133).
	if p1.DisabledNodes != p2.DisabledNodes {
		if p1.DisabledNodes < p2.DisabledNodes {
			return -1
		}
		return +1
	}
	if fraction <= 0.0 || fraction >= 1.0 {
		return comparePathCosts(p1, p2, totalCost)
	}
	cost1 := p1.Cost.Startup + fraction*(p1.Cost.Total-p1.Cost.Startup)
	cost2 := p2.Cost.Startup + fraction*(p2.Cost.Total-p2.Cost.Startup)
	switch {
	case cost1 < cost2:
		return -1
	case cost1 > cost2:
		return +1
	}
	return 0
}

// getCheapestFractionalPath is `get_cheapest_fractional_path` (planner.c:6617):
// the path that wins at this rel when only `tupleFraction` of its rows will be
// fetched. `setCheapest` must already have run on the rel.
//
// Three properties are upstream's and each one matters:
//
//   - `tupleFraction <= 0` returns `CheapestTotal` unchanged. "All rows" is not
//     a degenerate fraction to be evaluated, it is the case where startup cost
//     is irrelevant, so the answer is byte-identical to the pre-P5.7-b one.
//   - An ABSOLUTE fraction (>= 1.0) is converted here, against
//     `CheapestTotal.rows`, because this is the first place the row count and
//     the count wanted are both in hand (:6627).
//   - Parameterised paths are skipped (:6634). A path that only produces rows
//     once some outer relation supplies its parameter cannot represent the rel
//     to a caller that has no such outer — the same rule `setCheapest` applies
//     to the cheapest-startup slot.
//
// The incumbent starts as `CheapestTotal` and is only displaced by a strict
// improvement, so a fraction that ties changes nothing: this cannot make a plan
// move on noise.
func getCheapestFractionalPath(rel *RelOptInfo, tupleFraction float64) *Path {
	best := rel.CheapestTotal
	if tupleFraction <= 0.0 || best == nil {
		return best
	}
	// Convert an absolute count to a fraction; upstream does not clamp to
	// 0..1 because compareFractionalPathCosts already reads anything >= 1 as
	// "all rows".
	if tupleFraction >= 1.0 && best.Rows > 0 {
		tupleFraction /= best.Rows
	}
	for _, path := range rel.Pathlist {
		if path.RequiredOuter != 0 {
			continue
		}
		if path == best || compareFractionalPathCosts(best, path, tupleFraction) <= 0 {
			continue
		}
		best = path
	}
	return best
}
