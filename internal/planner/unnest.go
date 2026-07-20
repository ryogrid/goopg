package planner

import (
	"os"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/parser"
)

// subqueryUnnestOn is the package-level kill-switch for the whole
// sublink pull-up pass. Initialised to "on" (1), i.e. current
// behaviour. Two consumers:
//
//   - the rollback path for the S1 decorrelation work (design bundle
//     docs/design/correlated-subquery-planning, phase S1): flipping it
//     off restores the SubPlan-only plans, which the S2 execution
//     engine keeps fast, so neither setting carries a correctness risk;
//   - the semantics matrix (ch.07 V1), which runs every case twice —
//     once decorrelated and once through the SubPlan path — because the
//     count-bug and NULL rows are exactly where the transformed and
//     untransformed plans can silently diverge.
//
// Unlike `enable_nestloop_index` this has no GUC yet; see
// SetSubqueryUnnestEnabled.
var subqueryUnnestOn atomic.Bool

func init() {
	subqueryUnnestOn.Store(true)
	// Default ON since phase S6 (D6.2 minimal) landed: with index-driven
	// semi/anti NLI + the scalar probe-cheap policy, decorrelation no
	// longer regresses the selective TPC-H shapes (measured — see
	// IMPLEMENTATION-TODO.md Stage 6b). Rollback switches:
	// SetIndexKeyHarvestEnabled(false) from Go, or the environment
	// variable GOOPG_INDEXKEY_HARVEST=off at server start (operational
	// kill switch, same spirit as the planned GOOPG_SUBPLAN_RESCAN).
	indexKeyHarvestOn.Store(os.Getenv("GOOPG_INDEXKEY_HARVEST") != "off")
}

// SetSubqueryUnnestEnabled flips the sublink pull-up pass on or off.
// Test-only API, mirroring SetNLIEnabled: there is deliberately no
// `enable_subquery_unnest` GUC, because unlike a plan-shape toggle this
// switch also changes which correctness bugs are reachable, and a SQL
// surface would invite it into production configs.
func SetSubqueryUnnestEnabled(on bool) {
	subqueryUnnestOn.Store(on)
}

// subqueryUnnestEnabled reports whether the pull-up pass should run.
func subqueryUnnestEnabled() bool { return subqueryUnnestOn.Load() }

// --- S1a pull-up guards -----------------------------------------------
//
// Design bundle docs/design/correlated-subquery-planning §2.5 of
// 03-planner-decorrelation-extensions.md. Every helper below answers the
// same question the pull-up gates historically forgot to ask: *what* is
// being pulled up, and *where does it sit* in the predicate. Each one is
// a bail — the SubPlan path already produces PostgreSQL's answer for the
// shapes they reject, so refusing to decorrelate is always safe.

// inExprTopConjunct locates the top-level conjunct of filter.Predicate
// that the given InExpr occupies, and reports whether the pull-up may
// proceed at all.
//
// The sublink qualifies only when it *is* a top-level conjunct, or is the
// operand of a single `NOT` wrapping a top-level conjunct — mirroring the
// EXISTS gate in unnestExistsExpr. The second form flips the join to its
// dual (negateFlip), exactly the way that gate flips `negated`.
//
// Why this is load-bearing: findFilterContainingInExpr matches the InExpr
// *anywhere* in the predicate (findExprInExpr descends through OR and NOT),
// while the callers' conjunct removal only ever matches top-level
// conjuncts. For a sublink buried under OR/NOT the join was installed but
// the predicate never shrank, so the driver loop re-found the same node and
// wrapped another join every iteration — an unbounded planning loop.
// Pulling the sublink out of an OR is also semantically wrong (a semi/anti
// join applies it unconditionally, losing the other arm), so the fix is to
// bail rather than to remove the conjunct more cleverly.
func inExprTopConjunct(filter *Filter, in *InExpr) (topConjunct Expr, negateFlip bool, ok bool) {
	var target Expr = in
	for _, c := range splitAnd(filter.Predicate) {
		if c == target {
			return c, false, true
		}
		if u, isNot := c.(*UnaryOp); isNot && u.Op == parser.OpNot && u.Operand == target {
			return c, true, true
		}
	}
	return nil, false, false
}

// inExprIsPlainEquality reports whether in is the plain equality sublink
// form — `x IN (SELECT …)`, `x NOT IN (SELECT …)`, or `x = ANY (SELECT …)`.
//
// Only that form may become a semi/anti join here, because the rewrite
// hardcodes an OpEq join predicate. The parser encodes the other
// quantified comparisons differently (internal/parser/select.go):
//
//	x <> ALL (…) / x < ALL (…) / x > ANY (…) → AnyOp set, AllOp per quantifier
//	x != ANY (…)                             → NotEqualAny (an OR of !=)
//	x = ALL (…)                              → NOT(InExpr{NotEqualAny})
//
// Rewriting any of those with an equality join silently returns a
// different result set — `<> ALL` became a semi join, i.e. the exact
// complement of the correct answer. Upstream is stricter still: its
// pull-up handles ANY and EXISTS sublinks only, never ALL
// (pull_up_sublinks_qual_recurse, src/backend/optimizer/prep/prepjointree.c).
//
// Follow-up: `x <> ALL (…)` is equivalent to `x NOT IN (…)` and could be
// routed to the existing NullAware anti-join path instead of bailing.
// Deliberately not attempted here — correctness first.
func inExprIsPlainEquality(in *InExpr) bool {
	return in.AnyOp == parser.OpUnknown && !in.AllOp && !in.NotEqualAny
}

// subqueryANDReachable reports whether target is reachable from conjunct
// through AND-transparent expression nodes only: comparisons, arithmetic,
// nested ANDs and casts. An OR, NOT, CASE or function call on the path
// means the scalar sublink is not unconditionally applied to every row.
//
// The scalar rewrite replaces the sublink with a column of a GROUP BY
// aggregate joined with an INNER join, so an outer row whose correlation
// group is empty disappears from the result *before* the surrounding
// expression is evaluated. Under a top-level conjunct that is the correct
// outcome (the comparison would have been NULL, hence false). Under an OR
// it is not: the other arm never gets its chance, which is how
// `a = 2 OR b > (SELECT sum(…) …)` lost the `a = 2` rows.
func subqueryANDReachable(conjunct Expr, target *SubqueryExpr) bool {
	if conjunct == nil {
		return false
	}
	if conjunct == Expr(target) {
		return true
	}
	switch x := conjunct.(type) {
	case *BinaryOp:
		if x.Op == parser.OpOr {
			return false
		}
		return subqueryANDReachable(x.Left, target) || subqueryANDReachable(x.Right, target)
	case *CastExpr:
		return subqueryANDReachable(x.Operand, target)
	}
	return false
}

// nullOnEmptyAggregates lists the aggregates whose value over zero input
// rows is NULL. Only these may drive the scalar INNER-join rewrite: the
// rewrite drops outer rows with an empty correlation group, which matches
// PostgreSQL only when the subquery would have returned NULL for them (a
// NULL comparison filters the row anyway).
//
// count() is the counterexample that made this a live bug: it returns 0,
// so `x > (SELECT count(c) …)` must *keep* unmatched outer rows. The
// `count(*)` spelling was already rejected by the Star check, which is
// precisely why the bug hid — the obvious probe is the one spelling that
// happens to be correct.
var nullOnEmptyAggregates = map[string]bool{
	"min": true,
	"max": true,
	"avg": true,
	"sum": true,
}

// nullPreservingScalarTarget reports whether e yields NULL whenever the
// aggregate result it is built from is NULL.
//
// The scalar subquery's Project wrapper can convert the aggregate's NULL
// into a non-NULL value, which reintroduces the count-bug even for a
// whitelisted aggregate: `COALESCE(sum(b), 0)` returns 0 over an empty
// group, so unmatched outer rows must survive — but the INNER join drops
// them. Arithmetic stays strict (`0.5 * sum` is NULL when sum is), which
// is what TPC-H Q20 relies on; function calls and CASE do not.
func nullPreservingScalarTarget(e Expr) bool {
	switch x := e.(type) {
	case *ColumnRef:
		return true
	case *IntegerConst, *NumericConst, *StringConst, *BooleanConst, *NullConst:
		return true
	case *CastExpr:
		return nullPreservingScalarTarget(x.Operand)
	case *UnaryOp:
		return nullPreservingScalarTarget(x.Operand)
	case *BinaryOp:
		// AND/OR are not strict: `false AND NULL` is false.
		if x.Op == parser.OpAnd || x.Op == parser.OpOr {
			return false
		}
		return nullPreservingScalarTarget(x.Left) && nullPreservingScalarTarget(x.Right)
	}
	return false
}

// existsBodySafeForPullup mirrors the refusal conditions of upstream's
// simplify_EXISTS_query (src/backend/optimizer/plan/subselect.c), which
// gates convert_EXISTS_sublink_to_join.
//
// EXISTS asks only whether the body yields at least one row, so upstream
// discards the target list, GROUP BY, DISTINCT and ORDER BY — none of them
// can turn zero rows into some rows or vice versa — but refuses outright
// when the body has aggregates, HAVING or OFFSET, and accepts LIMIT only
// when it is a positive constant (which it then drops).
//
// Two of those refusals were live wrong-results bugs here:
//
//   - an ungrouped aggregate body is a *tautology* (the aggregate always
//     produces exactly one row) so the EXISTS is always true; building a
//     semi join on the aggregate's output turned it into a selective filter;
//   - a LIMIT inside the body survived the pull-up and became a *global*
//     limit on the semi-join build side, so only one correlation key could
//     ever match.
//
// goopg is stricter than upstream on one point: any *Aggregate on the
// body's own spine is refused, including a GROUP BY with no aggregate
// calls, which upstream would simplify away. Grouping rewrites the body's
// output schema, and the join key is bound by name against that schema, so
// allowing it would need key-rebinding work that buys nothing measurable.
// *Distinct is refused for the same reason (it is also absent from
// clonePlanReplacingOuter, so it could not be pulled up regardless).
//
// Only the body's *own* query level is inspected: the walk stops at the
// first join or scan. An Aggregate or Limit below that belongs to a
// derived table in the body's FROM — a separate query level, which
// upstream's per-Query hasAggs/limitCount flags likewise ignore — and its
// clauses stay meaningful after the pull-up.
func existsBodySafeForPullup(node Node) bool {
	switch n := node.(type) {
	case nil:
		return true
	case *Aggregate:
		return false
	case *Distinct:
		return false
	case *Limit:
		// OFFSET changes which rows survive, so it can turn a
		// non-empty body empty: never strippable.
		if n.Offset != nil {
			return false
		}
		if !isPositiveConstLimit(n.Limit) {
			return false
		}
		return existsBodySafeForPullup(n.Child)
	case *Filter:
		return existsBodySafeForPullup(n.Child)
	case *Project:
		return existsBodySafeForPullup(n.Child)
	case *Sort:
		return existsBodySafeForPullup(n.Child)
	}
	// A join, a scan, or any other node ends the body's own spine.
	return true
}

// isPositiveConstLimit reports whether e is a constant row count greater
// than zero. `LIMIT 0` makes EXISTS unconditionally false and must never
// be dropped; a non-constant limit cannot be reasoned about at plan time.
func isPositiveConstLimit(e Expr) bool {
	for {
		c, ok := e.(*CastExpr)
		if !ok {
			break
		}
		e = c.Operand
	}
	lit, ok := e.(*IntegerConst)
	return ok && lit.Value > 0
}

// stripPositiveConstLimits removes the Limit nodes that
// existsBodySafeForPullup accepted, mirroring simplify_EXISTS_query's
// "we can drop the LIMIT" step. Only reachable for bodies that already
// passed that gate, so every Limit on the spine here is strippable.
//
// The walk mirrors existsBodySafeForPullup's and stops at the first join
// or scan: a Limit inside a derived table belongs to that subquery's own
// definition and must be preserved.
func stripPositiveConstLimits(node Node) Node {
	switch n := node.(type) {
	case *Limit:
		child := stripPositiveConstLimits(n.Child)
		if n.Offset == nil && isPositiveConstLimit(n.Limit) {
			return child
		}
		n.Child = child
		return n
	case *Filter:
		n.Child = stripPositiveConstLimits(n.Child)
	case *Project:
		n.Child = stripPositiveConstLimits(n.Child)
	case *Sort:
		n.Child = stripPositiveConstLimits(n.Child)
	}
	return node
}

// countSublinksInExpr counts the pull-up candidates present in a predicate:
// IN / EXISTS / scalar sublinks that still carry an inner plan. Nested
// sublinks inside another sublink's plan are not counted — walkExprTree
// does not descend into Plan — which is what the driver loops want, since
// they only ever rewrite sublinks at this level.
func countSublinksInExpr(e Expr) int {
	n := 0
	walkExprTree(e, func(x Expr) {
		switch t := x.(type) {
		case *InExpr:
			if t.Plan != nil {
				n++
			}
		case *ExistsExpr:
			if t.Plan != nil {
				n++
			}
		case *SubqueryExpr:
			if t.Plan != nil {
				n++
			}
		}
	})
	return n
}

// pushConjunctsBelowSemiAnti sinks f's sublink-free conjuncts below the
// semi/anti join chain sitting directly under it, onto the chain's true
// outer input. S6 (D6.2), design bundle
// docs/design/correlated-subquery-planning.
//
// Semantics: a semi/anti join only *filters* its outer side — it never
// duplicates, drops-and-extends, or projects inner columns — so for any
// outer-side predicate p, σ_p(outer ⋉ inner) ≡ σ_p(outer) ⋉ inner (and
// likewise for ▷). Every conjunct left on f after the pull-up loops
// references the outer schema by construction (semi/anti output IS the
// outer schema), so column indices need no translation on the way down.
//
// Why it matters: the conjuncts sunk here are exactly the ones the
// SubPlan path evaluated *before* the sublink via AND short-circuit.
// TPC-H Q4 probes its EXISTS for only the ~57 K date-qualified orders on
// the SubPlan path; without this sink the decorrelated semi join drives
// from all 1.5 M rows — and the NLI cost gate, seeing the raw scan
// estimate, refuses the index-driven form outright. Sinking restores
// both the driving cardinality and the gate's view of it.
//
// Conjuncts that still carry a sublink stay on f: their evaluation
// point is unchanged either way, and the EXPLAIN/driver machinery
// expects surviving SubPlan filters above the join.
func pushConjunctsBelowSemiAnti(f *Filter) {
	top, ok := f.Child.(*Join)
	if !ok || (top.Type != JoinTypeSemi && top.Type != JoinTypeAnti) {
		return
	}
	var sinkable, keep []Expr
	for _, c := range splitAnd(f.Predicate) {
		if bc, isBool := c.(*BooleanConst); isBool && bc.Value {
			continue // tautology left by a completed pull-up
		}
		if countSublinksInExpr(c) == 0 {
			sinkable = append(sinkable, c)
		} else {
			keep = append(keep, c)
		}
	}
	if len(sinkable) == 0 {
		return
	}
	// Descend to the bottom of the semi/anti chain.
	bottom := top
	for {
		next, isJoin := bottom.Left.(*Join)
		if !isJoin || (next.Type != JoinTypeSemi && next.Type != JoinTypeAnti) {
			break
		}
		bottom = next
	}
	if inner, isF := bottom.Left.(*Filter); isF {
		// Merge into an existing outer-side Filter rather than stacking.
		inner.Predicate = combineAnd(append(sinkable, splitAnd(inner.Predicate)...))
	} else {
		bottom.Left = &Filter{
			pos:       f.Pos(),
			Predicate: combineAnd(sinkable),
			Child:     bottom.Left,
		}
	}
	// Each semi/anti join in the chain publishes its outer's schema;
	// wrapping the outer in a Filter changes no widths or names, so
	// the cached schemas stay valid.
	if len(keep) == 0 {
		f.Predicate = &BooleanConst{pos: f.Pos(), Value: true}
	} else {
		f.Predicate = combineAnd(keep)
	}
}

// unnestSubqueriesInPlan walks the plan tree and attempts to
// unnest any SubqueryExpr found in Filter predicates. This is
// the post-pass called after the initial plan tree is built
// and predicates have been pushed into joins.
func unnestSubqueriesInPlan(node Node) Node {
	if node == nil {
		return nil
	}
	if !subqueryUnnestEnabled() {
		return node
	}
	switch n := node.(type) {
	case *Filter:
		n.Child = unnestSubqueriesInPlan(n.Child)
		// Each loop below rewrites one sublink per iteration and relies
		// on the rewrite removing (or substituting) that sublink from the
		// predicate to make progress. The `remaining >= before` break is a
		// permanent belt: it converts any future mismatch between "where
		// the sublink was found" and "which conjunct got removed" from an
		// unbounded planning loop into a correct — merely unoptimised —
		// plan, because whatever is left simply stays a SubPlan.
		for {
			before := countSublinksInExpr(n.Predicate)
			sub := findSubqueryInExpr(n.Predicate)
			if sub == nil {
				break
			}
			newOuter, err := unnestSubquery(sub, node)
			if err != nil || newOuter == nil {
				break
			}
			node = newOuter
			f, ok := newOuter.(*Filter)
			if !ok {
				return newOuter
			}
			n = f
			if countSublinksInExpr(n.Predicate) >= before {
				break
			}
		}
		// M0040-0002: also try to unnest IN (subquery) expressions
		for {
			before := countSublinksInExpr(n.Predicate)
			in := findInExprInExpr(n.Predicate)
			if in == nil {
				break
			}
			newOuter, err := unnestInExpr(in, node)
			if err != nil || newOuter == nil {
				break
			}
			node = newOuter
			f, ok := newOuter.(*Filter)
			if !ok {
				return newOuter
			}
			n = f
			if countSublinksInExpr(n.Predicate) >= before {
				break
			}
		}
		// M0061-0001: EXISTS / NOT EXISTS → semi-join / anti-join.
		// Same shape as the IN loop above; runs after IN-unnesting so
		// any IN inside an EXISTS subquery has already been pulled up.
		for {
			before := countSublinksInExpr(n.Predicate)
			ex := findExistsExprInExpr(n.Predicate)
			if ex == nil {
				break
			}
			newOuter, err := unnestExistsExpr(ex, node)
			if err != nil || newOuter == nil {
				break
			}
			node = newOuter
			f, ok := newOuter.(*Filter)
			if !ok {
				return newOuter
			}
			n = f
			if countSublinksInExpr(n.Predicate) >= before {
				break
			}
		}
		// M0040-0004: walk remaining SubqueryExpr/InExpr inner plans
		// even when those expressions cannot be pulled up to this level
		// (e.g. Q20's lineitem scalar subquery inside the partsupp IN
		// clause, where the outer IN itself has no equijoin correlation).
		n.Predicate = walkSubqueryPlansInExpr(n.Predicate)
		// S6 (D6.2): once every sublink at this Filter has been
		// processed, sink the remaining sublink-free conjuncts BELOW
		// any semi/anti join chain the pull-ups installed. Doing it
		// after the loops (not inside the rewrites) matters: a second
		// sublink in the same predicate (Q21's NOT EXISTS next to its
		// EXISTS) must stay visible to the driver loops above.
		pushConjunctsBelowSemiAnti(n)
	case *Join:
		n.Left = unnestSubqueriesInPlan(n.Left)
		n.Right = unnestSubqueriesInPlan(n.Right)
	case *Project:
		n.Child = unnestSubqueriesInPlan(n.Child)
	case *Aggregate:
		n.Child = unnestSubqueriesInPlan(n.Child)
	case *Sort:
		n.Child = unnestSubqueriesInPlan(n.Child)
	case *Limit:
		n.Child = unnestSubqueriesInPlan(n.Child)
	}
	return node
}

// walkSubqueryPlansInExpr walks an expression tree and recursively
// applies unnestSubqueriesInPlan to the inner plan of every
// SubqueryExpr and InExpr node found. It is called after the
// pull-up loops in unnestSubqueriesInPlan so that subqueries that
// cannot be lifted to the current join level still have their own
// inner plan trees optimised.
func walkSubqueryPlansInExpr(e Expr) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *SubqueryExpr:
		x.Plan = unnestSubqueriesInPlan(x.Plan)
	case *MultiAssignSubqRow:
		x.Plan = unnestSubqueriesInPlan(x.Plan)
	case *InExpr:
		x.Plan = unnestSubqueriesInPlan(x.Plan)
	case *ExistsExpr:
		x.Plan = unnestSubqueriesInPlan(x.Plan)
	case *BinaryOp:
		x.Left = walkSubqueryPlansInExpr(x.Left)
		x.Right = walkSubqueryPlansInExpr(x.Right)
	case *UnaryOp:
		x.Operand = walkSubqueryPlansInExpr(x.Operand)
	case *FuncCall:
		for i := range x.Args {
			x.Args[i] = walkSubqueryPlansInExpr(x.Args[i])
		}
	case *CaseExpr:
		if x.Operand != nil {
			x.Operand = walkSubqueryPlansInExpr(x.Operand)
		}
		for i := range x.Whens {
			x.Whens[i].When = walkSubqueryPlansInExpr(x.Whens[i].When)
			x.Whens[i].Then = walkSubqueryPlansInExpr(x.Whens[i].Then)
		}
		x.Else = walkSubqueryPlansInExpr(x.Else)
	case *ExtractExpr:
		x.Source = walkSubqueryPlansInExpr(x.Source)
	}
	return e
}

func findSubqueryInExpr(e Expr) *SubqueryExpr {
	if e == nil {
		return nil
	}
	if s, ok := e.(*SubqueryExpr); ok {
		return s
	}
	switch x := e.(type) {
	case *BinaryOp:
		if s := findSubqueryInExpr(x.Left); s != nil {
			return s
		}
		return findSubqueryInExpr(x.Right)
	case *UnaryOp:
		return findSubqueryInExpr(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if s := findSubqueryInExpr(a); s != nil {
				return s
			}
		}
	case *CaseExpr:
		if x.Operand != nil {
			if s := findSubqueryInExpr(x.Operand); s != nil {
				return s
			}
		}
		for _, w := range x.Whens {
			if s := findSubqueryInExpr(w.When); s != nil {
				return s
			}
			if s := findSubqueryInExpr(w.Then); s != nil {
				return s
			}
		}
		if x.Else != nil {
			return findSubqueryInExpr(x.Else)
		}
	case *ExtractExpr:
		return findSubqueryInExpr(x.Source)
	}
	return nil
}

// canUnnestSubquery checks whether a SubqueryExpr is a candidate
// for unnesting into a GROUP BY aggregate + hash join.
func canUnnestSubquery(sub *SubqueryExpr) bool {
	plan := sub.Plan
	// Unwrap Project wrapper — the subquery's target list produces
	// a Project node wrapping the Aggregate.
	if proj, ok := plan.(*Project); ok {
		plan = proj.Child
	}
	agg, ok := plan.(*Aggregate)
	if !ok {
		return false
	}
	if len(agg.Aggs) != 1 {
		return false
	}
	call := agg.Aggs[0]
	if call.Star || call.Distinct {
		return false
	}
	// S1a guard: the rewrite drops outer rows whose correlation group is
	// empty, which only matches PostgreSQL when the subquery's value over
	// zero rows is NULL — see nullOnEmptyAggregates.
	if !nullOnEmptyAggregates[strings.ToLower(call.Name)] {
		return false
	}
	// ... and when the target list does not convert that NULL back into a
	// value the outer comparison can succeed on (nullPreservingScalarTarget).
	if proj, hasProj := sub.Plan.(*Project); hasProj {
		if len(proj.Targets) != 1 {
			return false
		}
		if !nullPreservingScalarTarget(proj.Targets[0]) {
			return false
		}
	}
	params := collectUnnestParams(agg)
	if params == nil {
		return false
	}
	if len(params) == 0 {
		return false
	}
	// S6 (D6.2) selectivity-aware policy — a measured amendment to the
	// bundle's D6.1 ("decorrelation is structural, not costed"): on this
	// executor, decorrelating a scalar whose inner plan is already an
	// index-probe shape is a LOSS, because the executor's CorrSubqOps
	// path rescans that shape per distinct outer key without rebuilding
	// (Q17: rebuilds=1, rescans=6667). Rewriting it into a whole-table
	// GROUP BY + join was measured at SF1 as Q17 58.27 s → 86.65 s and
	// Q20 12.29 s → 26.57 s. A NON-probe-shaped inner has no such cheap
	// path — Q2's Aggregate over a 4-table join was rebuilt per call at
	// ≈26 ms and decorrelating it measured 10.87 s → 3.36 s. Upstream
	// can decorrelate unconditionally only because it also has
	// index-driven and parallel joins to execute the result with;
	// until goopg does, the inner's shape decides.
	if innerPlanIsIndexProbeCheap(sub.Plan) {
		return false
	}
	return true
}

// innerPlanIsIndexProbeCheap mirrors the executor's planIsIndexScanBased
// criterion (internal/executor/expr.go): IndexScan, Project(IndexScan),
// or Aggregate over either — the shapes the executor's CorrSubqOps cache
// re-Opens per outer key instead of rebuilding. Kept in lockstep with
// that function: if the executor learns to rescan a new shape cheaply,
// this predicate must learn it too, or scalars of that shape will
// decorrelate away from the better path.
func innerPlanIsIndexProbeCheap(n Node) bool {
	switch x := n.(type) {
	case *IndexScan:
		return true
	case *Project:
		return innerPlanIsIndexProbeCheap(x.Child)
	case *Aggregate:
		return innerPlanIsIndexProbeCheap(x.Child)
	case *Filter:
		// A filter over an index probe is still probe-cheap (the executor's
		// filterOp is stateless and re-Open-safe) — this is TPC-H Q20's
		// date-windowed `sum(l_quantity)` over the composite FK index.
		// Measured 2026-07-21: decorrelating it costs 24.2 s vs 12.3 s on
		// the SubPlan path even before the rescan win.
		return innerPlanIsIndexProbeCheap(x.Child)
	}
	return false
}

func collectUnnestParams(node Node) []unnestParam {
	var params []unnestParam
	outerInEquijoin := make(map[*OuterColumnRef]bool)
	walkPlanExprs(node, func(e Expr) {
		bin, ok := e.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			return
		}
		outer, col := extractEquijoinPair(bin.Left, bin.Right)
		if outer != nil && col != nil {
			params = append(params, unnestParam{OuterRef: outer, SubCol: col})
			outerInEquijoin[outer] = true
		}
	})
	// D3.0: also harvest correlations the inner planner absorbed into an
	// IndexScan's equality probe key. On an indexed inner table the
	// correlation equijoin `l_orderkey = o_orderkey` never appears as a
	// Filter conjunct — it becomes `IndexScan.Key = OuterColumnRef` — so
	// without this the all-accounted check below bails on every indexed
	// correlated subquery, which is why decorrelation never fired on the
	// TPC-H schema (bundle W1). See harvestIndexKeyParams.
	for _, p := range harvestIndexKeyParams(node) {
		params = append(params, p)
		outerInEquijoin[p.OuterRef] = true
	}
	// Every OuterColumnRef in the plan must be accounted for by
	// an equijoin pair. If any OuterColumnRef appears outside an
	// equijoin, the subquery is not unnestable.
	allAccounted := true
	walkPlanExprs(node, func(e Expr) {
		if o, ok := e.(*OuterColumnRef); ok {
			if !outerInEquijoin[o] {
				allAccounted = false
			}
		}
	})
	if !allAccounted {
		return nil
	}
	return params
}

// harvestIndexKeyParams finds correlation equijoins that the inner
// planner folded into an IndexScan's equality probe key(s) and returns
// them as unnestParams, exactly as if they had been written as Filter
// conjuncts `Index.Columns[i] = OuterColumnRef`.
//
// Only the equality probe (`Key`, and each `Keys[i]`) is harvested.
// `LowKey`/`HighKey` are range bounds, not equijoins — a correlation
// there is genuinely non-decorrelatable and must keep failing the
// all-accounted check so the shape stays a SubPlan (matrix M14).
//
// The synthesised SubCol references the index's i-th column resolved
// against the scan's own output schema, so the group-by / hash-join key
// built from it lands on the right column. A key column that is not
// present in the scan schema (should not happen for a base-table index)
// is skipped, which makes the OuterColumnRef unaccounted and the whole
// subquery bail — the safe direction.
// Gated OFF by default pending index-driven semi/anti execution.
// Measured at SF1 on 2026-07-21 with the harvest enabled: Q2 10.87 s →
// 3.36 s and Q22 7.83 s → 1.66 s, but Q4 3.87 s → 276.08 s, Q17 58.27 s →
// 86.65 s, Q20 12.29 s → 26.57 s. The regressions are not a defect in this
// harvest — the decorrelated plans are correct — but a consequence of
// goopg executing every semi/anti join as a HASH join. A selective
// correlated EXISTS (Q4 probes ~57 K date-filtered orders through
// idx_lineitem_orderkey) becomes a hash semi join that scans and hashes
// all 6 M lineitem rows. Upstream can decorrelate unconditionally because
// it also has index-driven and parallel semi joins; goopg does not yet.
//
// Bundle phase S6 (D6.2, NLI semi/anti with a residual-bearing inner) is
// the prerequisite; this flag flips on there, with a re-measure.
var indexKeyHarvestOn atomic.Bool

// SetIndexKeyHarvestEnabled toggles harvesting correlation equijoins out
// of an inner IndexScan probe key. Test-only, like SetSubqueryUnnestEnabled.
func SetIndexKeyHarvestEnabled(on bool) { indexKeyHarvestOn.Store(on) }

func harvestIndexKeyParams(node Node) []unnestParam {
	if !indexKeyHarvestOn.Load() {
		return nil
	}
	var out []unnestParam
	var walk func(Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if is, ok := n.(*IndexScan); ok {
			schema := is.Output()
			harvestKey := func(keyExpr Expr, col int) {
				oc, ok := keyExpr.(*OuterColumnRef)
				if !ok || is.Index == nil || col >= len(is.Index.Columns) {
					return
				}
				name := is.Index.Columns[col]
				idx := -1
				for i, sc := range schema {
					if sc.Name == name {
						idx = i
						break
					}
				}
				if idx < 0 {
					return
				}
				out = append(out, unnestParam{
					OuterRef: oc,
					SubCol: &ColumnRef{
						pos:            oc.Pos(),
						Index:          idx,
						Name:           name,
						Type:           schema[idx].Type,
						SourceTableIdx: schema[idx].SourceTableIdx,
					},
				})
			}
			if is.Key != nil {
				harvestKey(is.Key, 0)
			}
			for i, k := range is.Keys {
				harvestKey(k, i)
			}
		}
		// Recurse through the single/dual-child plan nodes an inner
		// subquery body can contain (mirrors walkPlanExprs' structure,
		// but only descends — the leaf work is above).
		switch x := n.(type) {
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *Filter:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Distinct:
			walk(x.Child)
		case *DistinctOn:
			walk(x.Child)
		case *MultiHashJoin:
			for _, tbl := range x.Tables {
				walk(tbl)
			}
		case *LockRows:
			walk(x.Child)
		}
	}
	walk(node)
	return out
}

// planHasOuterRef reports whether any OuterColumnRef survives anywhere
// in a (cloned, decorrelated) inner plan. After the D3.0 harvest +
// clone rewrite the inner plan must be self-contained; a surviving
// OuterColumnRef means a correlation was harvested as a param but not
// actually neutralised in the clone (e.g. a partial multi-key harvest),
// and installing such a join would evaluate an unbound reference at
// runtime. Callers treat true as "bail to the SubPlan path".
func planHasOuterRefRemaining(node Node) bool {
	found := false
	walkPlanExprs(node, func(e Expr) {
		if _, ok := e.(*OuterColumnRef); ok {
			found = true
		}
	})
	return found
}

func extractEquijoinPair(a, b Expr) (*OuterColumnRef, *ColumnRef) {
	if o, ok := a.(*OuterColumnRef); ok {
		if c, ok := b.(*ColumnRef); ok {
			return o, c
		}
	}
	if o, ok := b.(*OuterColumnRef); ok {
		if c, ok := a.(*ColumnRef); ok {
			return o, c
		}
	}
	return nil, nil
}

func walkPlanExprs(node Node, visit func(Expr)) {
	if node == nil {
		return
	}
	switch n := node.(type) {
	case *Join:
		walkPlanExprs(n.Left, visit)
		walkPlanExprs(n.Right, visit)
		if n.Predicate != nil {
			walkExprTree(n.Predicate, visit)
		}
		if n.LeftKey != nil {
			walkExprTree(n.LeftKey, visit)
		}
		if n.RightKey != nil {
			walkExprTree(n.RightKey, visit)
		}
	case *NestedLoopIndexJoin:
		// S6: a sublink's inner plan runs the full planning pipeline,
		// so it can contain NLI nodes. Without this case the
		// all-accounted OuterColumnRef check never looked inside them —
		// an outer ref hiding in an NLI residual or probe key escaped
		// accounting and the pull-up proceeded over an unbound
		// reference.
		walkPlanExprs(n.Outer, visit)
		walkPlanExprs(n.Inner, visit)
		if n.Predicate != nil {
			walkExprTree(n.Predicate, visit)
		}
	case *Filter:
		walkPlanExprs(n.Child, visit)
		if n.Predicate != nil {
			walkExprTree(n.Predicate, visit)
		}
	case *Project:
		walkPlanExprs(n.Child, visit)
		for _, t := range n.Targets {
			walkExprTree(t, visit)
		}
	case *Aggregate:
		walkPlanExprs(n.Child, visit)
		for _, g := range n.GroupExprs {
			walkExprTree(g, visit)
		}
		for _, a := range n.Aggs {
			if a.Arg != nil {
				walkExprTree(a.Arg, visit)
			}
			if a.Arg2 != nil {
				walkExprTree(a.Arg2, visit)
			}
			for _, ea := range a.ExtraArgs {
				walkExprTree(ea, visit)
			}
			for _, sk := range a.OrderBy {
				walkExprTree(sk.Expr, visit)
			}
			for _, sk := range a.WithinGroupOrderBy {
				walkExprTree(sk.Expr, visit)
			}
		}
	case *Sort:
		walkPlanExprs(n.Child, visit)
		for _, k := range n.Keys {
			walkExprTree(k.Expr, visit)
		}
	case *Limit:
		walkPlanExprs(n.Child, visit)
		if n.Limit != nil {
			walkExprTree(n.Limit, visit)
		}
		if n.Offset != nil {
			walkExprTree(n.Offset, visit)
		}
	case *SeqScan:
	case *IndexScan:
		if n.Key != nil {
			walkExprTree(n.Key, visit)
		}
		if n.LowKey != nil {
			walkExprTree(n.LowKey, visit)
		}
		if n.HighKey != nil {
			walkExprTree(n.HighKey, visit)
		}
	case *WindowAgg:
		walkPlanExprs(n.Child, visit)
		for _, p := range n.PartitionBy {
			walkExprTree(p, visit)
		}
		for _, k := range n.OrderBy {
			walkExprTree(k.Expr, visit)
		}
	case *Values:
		for _, row := range n.Rows {
			for _, e := range row {
				walkExprTree(e, visit)
			}
		}
	case *GenerateSubscripts:
		walkExprTree(n.ArrExpr, visit)
		walkExprTree(n.Dim, visit)
		if n.Reversed != nil {
			walkExprTree(n.Reversed, visit)
		}
	case *GenerateSeries:
		walkExprTree(n.Start, visit)
		walkExprTree(n.Stop, visit)
		if n.Step != nil {
			walkExprTree(n.Step, visit)
		}
	case *FromUnnest:
		walkExprTree(n.ArrExpr, visit)
	case *PgInputErrorInfo:
		walkExprTree(n.Value, visit)
		walkExprTree(n.Type, visit)
	case *PgGetPublicationTables:
		for _, a := range n.Args {
			walkExprTree(a, visit)
		}
	case *PgAvailableWalSummaries:
		// no sub-expressions to walk
	case *PgGetSequenceData:
		for _, a := range n.Args {
			walkExprTree(a, visit)
		}
	case *PgOptionsToTable:
		if n.Arg != nil {
			walkExprTree(n.Arg, visit)
		}
	case *VerifyHeapam:
		if n.Arg != nil {
			walkExprTree(n.Arg, visit)
		}
		if n.StartBlock != nil {
			walkExprTree(n.StartBlock, visit)
		}
		if n.EndBlock != nil {
			walkExprTree(n.EndBlock, visit)
		}
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			walkPlanExprs(tbl, visit)
		}
		for _, f := range n.Filters {
			walkExprTree(f, visit)
		}
	case *RecursiveUnion:
		walkPlanExprs(n.Anchor, visit)
		walkPlanExprs(n.Recursive, visit)
	case *CTEScan:
		walkPlanExprs(n.Child, visit)
	case *CTEDMLPrefix:
		for _, dml := range n.DMls {
			walkPlanExprs(dml, visit)
		}
		walkPlanExprs(n.Body, visit)
	case *SetOp:
		walkPlanExprs(n.Left, visit)
		walkPlanExprs(n.Right, visit)
	case *Distinct:
		walkPlanExprs(n.Child, visit)
	case *DistinctOn:
		walkPlanExprs(n.Child, visit)
	case *ProjectSet:
		walkPlanExprs(n.Child, visit)
		for _, e := range n.OtherExprs {
			walkExprTree(e, visit)
		}
		for _, uc := range n.UnnestCols {
			walkExprTree(uc.ArrExpr, visit)
		}
	case *LockRows:
		walkPlanExprs(n.Child, visit)
	}
}

// walkExprTree visits every expression node in a tree, calling
// visit for each. Named walkExprTree to avoid collision with
// the parser-level walkExpr in planner.go.
func walkExprTree(e Expr, visit func(Expr)) {
	if e == nil {
		return
	}
	visit(e)
	switch x := e.(type) {
	case *BinaryOp:
		walkExprTree(x.Left, visit)
		walkExprTree(x.Right, visit)
	case *UnaryOp:
		walkExprTree(x.Operand, visit)
	case *CastExpr:
		walkExprTree(x.Operand, visit)
	case *FuncCall:
		for _, a := range x.Args {
			walkExprTree(a, visit)
		}
	case *CaseExpr:
		walkExprTree(x.Operand, visit)
		for _, w := range x.Whens {
			walkExprTree(w.When, visit)
			walkExprTree(w.Then, visit)
		}
		walkExprTree(x.Else, visit)
	case *ExtractExpr:
		walkExprTree(x.Source, visit)
	case *RowExpr:
		for _, elem := range x.Elems {
			walkExprTree(elem, visit)
		}
	case *MultiAssignSubqElem:
		// Visit the shared row node so planHasOuterRef can detect
		// the inner plan's outer references.
		visit(x.Row)
	}
}

func buildUnnestedSubquery(sub *SubqueryExpr, params []unnestParam) (Node, Schema, error) {
	plan := sub.Plan
	if proj, ok := plan.(*Project); ok {
		plan = proj.Child
	}
	agg, ok := plan.(*Aggregate)
	if !ok {
		return nil, nil, &PlanError{Pos: sub.Pos(), Code: "XX000", Message: "buildUnnestedSubquery: inner plan is not an Aggregate"}
	}
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	groupExprs := make([]Expr, len(params))
	for i, p := range params {
		replace[p.OuterRef] = p.SubCol
		groupExprs[i] = cloneExprLeaf(p.SubCol)
	}
	child, err := clonePlanReplacingOuter(agg.Child, replace)
	if err != nil {
		return nil, nil, err
	}
	newAgg := &Aggregate{
		pos:        agg.Pos(),
		Child:      child,
		GroupExprs: groupExprs,
		Aggs:       []AggregateCall{cloneAggregateCall(agg.Aggs[0])},
	}
	schema := make(Schema, 0, len(params)+1)
	for _, p := range params {
		schema = append(schema, SchemaColumn{Name: p.SubCol.Name, Type: p.SubCol.Type})
	}
	schema = append(schema, SchemaColumn{
		Name: agg.Aggs[0].Name,
		Type: agg.Aggs[0].Type,
	})
	newAgg.schema = schema
	return newAgg, schema, nil
}

func clonePlanReplacingOuter(node Node, replace map[*OuterColumnRef]*ColumnRef) (Node, error) {
	if node == nil {
		return nil, nil
	}
	switch n := node.(type) {
	case *Join:
		left, err := clonePlanReplacingOuter(n.Left, replace)
		if err != nil {
			return nil, err
		}
		right, err := clonePlanReplacingOuter(n.Right, replace)
		if err != nil {
			return nil, err
		}
		jn := *n
		jn.Left = left
		jn.Right = right
		if n.Predicate != nil {
			jn.Predicate = cloneExprReplacingOuter(n.Predicate, replace)
		}
		if n.LeftKey != nil {
			jn.LeftKey = cloneExprReplacingOuter(n.LeftKey, replace)
		}
		if n.RightKey != nil {
			jn.RightKey = cloneExprReplacingOuter(n.RightKey, replace)
		}
		return &jn, nil
	case *Filter:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		f := *n
		f.Child = child
		if n.Predicate != nil {
			f.Predicate = cloneExprReplacingOuter(n.Predicate, replace)
		}
		return &f, nil
	case *Project:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		pr := *n
		pr.Child = child
		pr.Targets = make([]Expr, len(n.Targets))
		for i, t := range n.Targets {
			pr.Targets[i] = cloneExprReplacingOuter(t, replace)
		}
		return &pr, nil
	case *Aggregate:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		a := *n
		a.Child = child
		a.GroupExprs = make([]Expr, len(n.GroupExprs))
		for i, g := range n.GroupExprs {
			a.GroupExprs[i] = cloneExprReplacingOuter(g, replace)
		}
		a.Aggs = make([]AggregateCall, len(n.Aggs))
		for i, ag := range n.Aggs {
			a.Aggs[i] = ag
			if ag.Arg != nil {
				a.Aggs[i].Arg = cloneExprReplacingOuter(ag.Arg, replace)
			}
			if ag.Arg2 != nil {
				a.Aggs[i].Arg2 = cloneExprReplacingOuter(ag.Arg2, replace)
			}
			for j, ea := range ag.ExtraArgs {
				a.Aggs[i].ExtraArgs[j] = cloneExprReplacingOuter(ea, replace)
			}
		}
		return &a, nil
	case *Sort:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		s := *n
		s.Child = child
		s.Keys = make([]SortKey, len(n.Keys))
		for i, k := range n.Keys {
			s.Keys[i] = SortKey{Expr: cloneExprReplacingOuter(k.Expr, replace), Desc: k.Desc, NullsFirst: k.NullsFirst}
		}
		return &s, nil
	case *Limit:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		l := *n
		l.Child = child
		if n.Limit != nil {
			l.Limit = cloneExprReplacingOuter(n.Limit, replace)
		}
		if n.Offset != nil {
			l.Offset = cloneExprReplacingOuter(n.Offset, replace)
		}
		return &l, nil
	case *SeqScan:
		c := *n
		return &c, nil
	case *IndexScan:
		// D3.0 clone crux: an equality probe key that is a harvested
		// correlation (an *OuterColumnRef in the replace map) would,
		// after replacement, become the scan's OWN column — a circular
		// self-probe (probe lineitem's l_orderkey index by l_orderkey).
		// The equality is now enforced by the enclosing semi/anti/
		// GROUP-BY join, so drop it: convert the scan to a SeqScan,
		// preserving the alias (Q7's lesson — a dropped alias silently
		// mis-binds self-joins). Any probe key that is NOT a harvested
		// correlation (a constant, or an inner-bound ref) is preserved
		// as an equality Filter above the SeqScan; likewise the range
		// bounds. Keys that are genuinely non-correlated leave the scan
		// an IndexScan (the else branch).
		isHarvested := func(e Expr) bool {
			oc, ok := e.(*OuterColumnRef)
			if !ok {
				return false
			}
			_, inMap := replace[oc]
			return inMap
		}
		correlated := isHarvested(n.Key)
		for _, k := range n.Keys {
			if isHarvested(k) {
				correlated = true
			}
		}
		if !correlated {
			c := *n
			if n.Key != nil {
				c.Key = cloneExprReplacingOuter(n.Key, replace)
			}
			if len(n.Keys) > 0 {
				c.Keys = make([]Expr, len(n.Keys))
				for i, k := range n.Keys {
					c.Keys[i] = cloneExprReplacingOuter(k, replace)
				}
			}
			if n.LowKey != nil {
				c.LowKey = cloneExprReplacingOuter(n.LowKey, replace)
			}
			if n.HighKey != nil {
				c.HighKey = cloneExprReplacingOuter(n.HighKey, replace)
			}
			return &c, nil
		}
		seq := &SeqScan{
			pos:                   n.pos,
			Table:                 n.Table,
			Alias:                 n.Alias,
			schema:                n.schema,
			PrivilegeCheckRole:    n.PrivilegeCheckRole,
			PrivilegeCheckRoleSet: n.PrivilegeCheckRoleSet,
		}
		// Preserve any non-correlation probe keys / range bounds as a
		// Filter above the SeqScan. `indexColRef` resolves the index's
		// i-th column against the scan schema so the rebuilt equality
		// binds the right slot.
		indexColRef := func(col int) *ColumnRef {
			if n.Index == nil || col >= len(n.Index.Columns) {
				return nil
			}
			name := n.Index.Columns[col]
			for i, sc := range n.schema {
				if sc.Name == name {
					return &ColumnRef{pos: n.pos, Index: i, Name: name, Type: sc.Type, SourceTableIdx: sc.SourceTableIdx}
				}
			}
			return nil
		}
		var conds []Expr
		if n.Key != nil && !isHarvested(n.Key) {
			if col := indexColRef(0); col != nil {
				conds = append(conds, &BinaryOp{pos: n.pos, Op: parser.OpEq, Left: col, Right: cloneExprReplacingOuter(n.Key, replace)})
			}
		}
		for i, k := range n.Keys {
			if isHarvested(k) {
				continue
			}
			if col := indexColRef(i); col != nil {
				conds = append(conds, &BinaryOp{pos: n.pos, Op: parser.OpEq, Left: col, Right: cloneExprReplacingOuter(k, replace)})
			}
		}
		if n.LowKey != nil {
			if col := indexColRef(0); col != nil {
				conds = append(conds, &BinaryOp{pos: n.pos, Op: parser.OpGe, Left: col, Right: cloneExprReplacingOuter(n.LowKey, replace)})
			}
		}
		if n.HighKey != nil {
			if col := indexColRef(0); col != nil {
				conds = append(conds, &BinaryOp{pos: n.pos, Op: parser.OpLe, Left: col, Right: cloneExprReplacingOuter(n.HighKey, replace)})
			}
		}
		if len(conds) > 0 {
			return &Filter{pos: n.pos, Child: seq, Predicate: combineAnd(conds)}, nil
		}
		return seq, nil
	case *MultiHashJoin:
		c := *n
		c.Tables = make([]Node, len(n.Tables))
		for i, tbl := range n.Tables {
			cloned, err := clonePlanReplacingOuter(tbl, replace)
			if err != nil {
				return nil, err
			}
			c.Tables[i] = cloned
		}
		c.Filters = nil
		if n.Filters != nil {
			c.Filters = make([]Expr, len(n.Filters))
			for i, f := range n.Filters {
				c.Filters[i] = cloneExprReplacingOuter(f, replace)
			}
		}
		c.Keys = make([]MultiHashKey, len(n.Keys))
		for i, k := range n.Keys {
			c.Keys[i] = k
		}
		return &c, nil
	case *Values:
		c := *n
		c.Rows = make([][]Expr, len(n.Rows))
		for i, row := range n.Rows {
			c.Rows[i] = make([]Expr, len(row))
			for j, e := range row {
				c.Rows[i][j] = cloneExprReplacingOuter(e, replace)
			}
		}
		return &c, nil
	default:
		return nil, &PlanError{Pos: node.Pos(), Code: "XX000", Message: "clonePlanReplacingOuter: unsupported plan node"}
	}
}

func cloneExprReplacingOuter(e Expr, replace map[*OuterColumnRef]*ColumnRef) Expr {
	if e == nil {
		return nil
	}
	if o, ok := e.(*OuterColumnRef); ok {
		if c, found := replace[o]; found {
			cl := *c
			return &cl
		}
		cl := *o
		return &cl
	}
	switch x := e.(type) {
	case *ColumnRef:
		cl := *x
		return &cl
	case *BinaryOp:
		return &BinaryOp{
			pos:        x.Pos(),
			Op:         x.Op,
			Left:       cloneExprReplacingOuter(x.Left, replace),
			Right:      cloneExprReplacingOuter(x.Right, replace),
			ResultType: x.ResultType,
		}
	case *UnaryOp:
		return &UnaryOp{
			pos:     x.Pos(),
			Op:      x.Op,
			Operand: cloneExprReplacingOuter(x.Operand, replace),
		}
	case *FuncCall:
		cl := *x
		cl.Args = make([]Expr, len(x.Args))
		for i, a := range x.Args {
			cl.Args[i] = cloneExprReplacingOuter(a, replace)
		}
		return &cl
	case *CaseExpr:
		cl := *x
		if x.Operand != nil {
			cl.Operand = cloneExprReplacingOuter(x.Operand, replace)
		}
		cl.Whens = make([]CaseWhen, len(x.Whens))
		for i, w := range x.Whens {
			cl.Whens[i] = CaseWhen{
				When: cloneExprReplacingOuter(w.When, replace),
				Then: cloneExprReplacingOuter(w.Then, replace),
			}
		}
		if x.Else != nil {
			cl.Else = cloneExprReplacingOuter(x.Else, replace)
		}
		return &cl
	case *CastExpr:
		return &CastExpr{pos: x.Pos(), Operand: cloneExprReplacingOuter(x.Operand, replace), TargetType: x.TargetType, SourceType: x.SourceType, Typmod: x.Typmod}
	case *ExtractExpr:
		cl := *x
		cl.Source = cloneExprReplacingOuter(x.Source, replace)
		return &cl
	default:
		return cloneExprLeaf(x)
	}
}

// cloneExprSubstituteAggIdx0 clones an expression tree (typically
// a scalar subquery's outer Project target) and substitutes every
// ColumnRef whose Index == 0 with `aggColRef`. This preserves the
// Project's expression (e.g. `0.5 * sum`) while pointing the
// agg-result reference at the new merged-coord aggColRef during
// scalar subquery decorrelation. M0071-0002-followup.
func cloneExprSubstituteAggIdx0(e Expr, aggColRef *ColumnRef) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *ColumnRef:
		if x.Index == 0 {
			cl := *aggColRef
			return &cl
		}
		cl := *x
		return &cl
	case *BinaryOp:
		return &BinaryOp{
			pos:        x.Pos(),
			Op:         x.Op,
			Left:       cloneExprSubstituteAggIdx0(x.Left, aggColRef),
			Right:      cloneExprSubstituteAggIdx0(x.Right, aggColRef),
			ResultType: x.ResultType,
		}
	case *UnaryOp:
		return &UnaryOp{
			pos:     x.Pos(),
			Op:      x.Op,
			Operand: cloneExprSubstituteAggIdx0(x.Operand, aggColRef),
		}
	case *FuncCall:
		cl := *x
		cl.Args = make([]Expr, len(x.Args))
		for i, a := range x.Args {
			cl.Args[i] = cloneExprSubstituteAggIdx0(a, aggColRef)
		}
		return &cl
	case *CaseExpr:
		cl := *x
		if x.Operand != nil {
			cl.Operand = cloneExprSubstituteAggIdx0(x.Operand, aggColRef)
		}
		cl.Whens = make([]CaseWhen, len(x.Whens))
		for i, w := range x.Whens {
			cl.Whens[i] = CaseWhen{
				When: cloneExprSubstituteAggIdx0(w.When, aggColRef),
				Then: cloneExprSubstituteAggIdx0(w.Then, aggColRef),
			}
		}
		if x.Else != nil {
			cl.Else = cloneExprSubstituteAggIdx0(x.Else, aggColRef)
		}
		return &cl
	case *CastExpr:
		return &CastExpr{pos: x.Pos(), Operand: cloneExprSubstituteAggIdx0(x.Operand, aggColRef), TargetType: x.TargetType, SourceType: x.SourceType, Typmod: x.Typmod}
	case *ExtractExpr:
		cl := *x
		cl.Source = cloneExprSubstituteAggIdx0(x.Source, aggColRef)
		return &cl
	default:
		return cloneExprLeaf(x)
	}
}

func cloneExprLeaf(e Expr) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *IntegerConst:
		c := *x
		return &c
	case *NumericConst:
		c := *x
		return &c
	case *StringConst:
		c := *x
		return &c
	case *NullConst:
		c := *x
		return &c
	case *BooleanConst:
		c := *x
		return &c
	case *ParamRef:
		c := *x
		return &c
	case *TypedStringLit:
		c := *x
		return &c
	case *IntervalLit:
		c := *x
		return &c
	case *ColumnRef:
		c := *x
		return &c
	case *OuterColumnRef:
		c := *x
		return &c
	default:
		return e
	}
}

func cloneAggregateCall(call AggregateCall) AggregateCall {
	c := call
	if call.Arg != nil {
		c.Arg = cloneExprLeaf(call.Arg)
	}
	if call.Arg2 != nil {
		c.Arg2 = cloneExprLeaf(call.Arg2)
	}
	if len(call.ExtraArgs) > 0 {
		c.ExtraArgs = make([]Expr, len(call.ExtraArgs))
		for i, ea := range call.ExtraArgs {
			c.ExtraArgs[i] = cloneExprLeaf(ea)
		}
	}
	return c
}

func findFilterContainingSubquery(node Node, target *SubqueryExpr) (*Filter, Expr) {
	if node == nil {
		return nil, nil
	}
	switch n := node.(type) {
	case *Filter:
		conjuncts := splitAnd(n.Predicate)
		for _, c := range conjuncts {
			if containsExpr(c, target) {
				return n, c
			}
		}
		return findFilterContainingSubquery(n.Child, target)
	case *Join:
		if f, c := findFilterContainingSubquery(n.Left, target); f != nil {
			return f, c
		}
		return findFilterContainingSubquery(n.Right, target)
	case *Project:
		return findFilterContainingSubquery(n.Child, target)
	case *Aggregate:
		return findFilterContainingSubquery(n.Child, target)
	case *Sort:
		return findFilterContainingSubquery(n.Child, target)
	case *Limit:
		return findFilterContainingSubquery(n.Child, target)
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			if f, c := findFilterContainingSubquery(tbl, target); f != nil {
				return f, c
			}
		}
	}
	return nil, nil
}

func containsExpr(e, target Expr) bool {
	if e == nil {
		return false
	}
	if e == target {
		return true
	}
	switch x := e.(type) {
	case *BinaryOp:
		return containsExpr(x.Left, target) || containsExpr(x.Right, target)
	case *UnaryOp:
		return containsExpr(x.Operand, target)
	case *FuncCall:
		for _, a := range x.Args {
			if containsExpr(a, target) {
				return true
			}
		}
	case *CaseExpr:
		if x.Operand != nil && containsExpr(x.Operand, target) {
			return true
		}
		for _, w := range x.Whens {
			if containsExpr(w.When, target) || containsExpr(w.Then, target) {
				return true
			}
		}
		if x.Else != nil && containsExpr(x.Else, target) {
			return true
		}
	case *ExtractExpr:
		return containsExpr(x.Source, target)
	}
	return false
}

func replaceExprInConjunct(e, target, replacement Expr) Expr {
	if e == target {
		return replacement
	}
	switch x := e.(type) {
	case *BinaryOp:
		return &BinaryOp{
			pos:   x.Pos(),
			Op:    x.Op,
			Left:  replaceExprInConjunct(x.Left, target, replacement),
			Right: replaceExprInConjunct(x.Right, target, replacement),
		}
	case *UnaryOp:
		return &UnaryOp{
			pos:     x.Pos(),
			Op:      x.Op,
			Operand: replaceExprInConjunct(x.Operand, target, replacement),
		}
	case *FuncCall:
		cl := *x
		cl.Args = make([]Expr, len(x.Args))
		for i, a := range x.Args {
			cl.Args[i] = replaceExprInConjunct(a, target, replacement)
		}
		return &cl
	case *CaseExpr:
		cl := *x
		if x.Operand != nil {
			cl.Operand = replaceExprInConjunct(x.Operand, target, replacement)
		}
		cl.Whens = make([]CaseWhen, len(x.Whens))
		for i, w := range x.Whens {
			cl.Whens[i] = CaseWhen{
				When: replaceExprInConjunct(w.When, target, replacement),
				Then: replaceExprInConjunct(w.Then, target, replacement),
			}
		}
		if x.Else != nil {
			cl.Else = replaceExprInConjunct(x.Else, target, replacement)
		}
		return &cl
	case *ExtractExpr:
		cl := *x
		cl.Source = replaceExprInConjunct(x.Source, target, replacement)
		return &cl
	}
	return e
}

// unnestSubquery attempts to unnest a SubqueryExpr from an outer
// Filter. Returns a new outer plan tree or nil if unnesting fails.
func unnestSubquery(sub *SubqueryExpr, outer Node) (Node, error) {
	if !canUnnestSubquery(sub) {
		return nil, nil
	}
	plan := sub.Plan
	if proj, ok := plan.(*Project); ok {
		plan = proj.Child
	}
	params := collectUnnestParams(plan.(*Aggregate))
	if len(params) == 0 {
		return nil, nil
	}
	subPlan, subSchema, err := buildUnnestedSubquery(sub, params)
	if err != nil {
		return nil, err
	}
	// D3.0 belt: if the clone did not fully neutralise the correlation
	// (e.g. a partial multi-key harvest), leave the sublink as a SubPlan
	// rather than install a join over an unbound OuterColumnRef.
	if planHasOuterRefRemaining(subPlan) {
		return nil, nil
	}
	filter, conjunct := findFilterContainingSubquery(outer, sub)
	if filter == nil {
		return nil, nil
	}
	// S1a guard: refuse when the sublink is not unconditionally applied to
	// every row of the conjunct (OR / NOT / CASE / function argument). The
	// INNER-join rewrite would drop empty-group rows before the rest of the
	// expression ever ran. See subqueryANDReachable.
	if !subqueryANDReachable(conjunct, sub) {
		return nil, nil
	}
	outerChild := filter.Child
	outerWidth := len(outerChild.Output())
	aggColRef := &ColumnRef{
		pos:   sub.Pos(),
		Index: outerWidth + len(params),
		Name:  subSchema[len(params)].Name,
		Type:  subSchema[len(params)].Type,
	}
	// M0071-0002-followup: if the scalar subquery had a Project
	// wrapper above its Aggregate (e.g. Q20's `SELECT 0.5 *
	// sum(l_quantity) FROM lineitem WHERE ...`), the Project's
	// target expression must be preserved when substituting the
	// scalar SubqueryExpr in the outer Filter's predicate.
	// Without this, only the raw aggregate result column is
	// substituted (`sum`), losing the Project's expression
	// (`0.5 * sum`). For Q20 this caused `ps_availqty > sum`
	// instead of `ps_availqty > 0.5 * sum`, which over-filters
	// every row (sum is ~2x the threshold) → 0 rows.
	var replacement Expr = aggColRef
	if proj, ok := sub.Plan.(*Project); ok && len(proj.Targets) == 1 {
		// Clone the Project's target expression, substituting any
		// ColumnRef with Index == 0 (the agg result's position in
		// the original Aggregate's output) with the new
		// merged-coord aggColRef.
		replacement = cloneExprSubstituteAggIdx0(proj.Targets[0], aggColRef)
	}
	newConjunct := replaceExprInConjunct(conjunct, sub, replacement)
	conjuncts := splitAnd(filter.Predicate)
	newConjuncts := make([]Expr, 0, len(conjuncts))
	for _, c := range conjuncts {
		if c == conjunct {
			newConjuncts = append(newConjuncts, newConjunct)
		} else {
			newConjuncts = append(newConjuncts, c)
		}
	}
	filter.Predicate = combineAnd(newConjuncts)

	// M0054-0008: handle multi-parameter correlation. The inner
	// Aggregate now groups by every correlation column (built by
	// buildUnnestedSubquery), and the outer join needs to bind
	// every (outer-col, inner-col) pair. The first pair becomes
	// the hash key (LeftKey/RightKey); additional pairs go into
	// the join Predicate as AND-conjuncts so the hash-join's
	// post-match evaluation filters out rows that match the first
	// key but disagree on the remaining keys. Without this fix,
	// a Q20-shape subquery with `l_partkey = ps_partkey AND
	// l_suppkey = ps_suppkey` would match on l_partkey alone and
	// produce wrong sums.
	outerKeyExprs := make([]*ColumnRef, len(params))
	innerKeyExprs := make([]*ColumnRef, len(params))
	for i, p := range params {
		outerKeyExprs[i] = &ColumnRef{
			pos:            p.OuterRef.Pos(),
			Index:          p.OuterRef.Index,
			Name:           p.OuterRef.Name,
			Type:           p.OuterRef.Type,
			SourceTableIdx: p.OuterRef.SourceTableIdx,
		}
		innerKeyExprs[i] = &ColumnRef{
			pos:            p.SubCol.Pos(),
			Index:          outerWidth + i, // i-th group-by column in subSchema
			Name:           p.SubCol.Name,
			Type:           p.SubCol.Type,
			SourceTableIdx: p.SubCol.SourceTableIdx,
		}
	}
	// Build the join Predicate as AND of all per-pair equalities.
	var joinPredicate Expr = &BinaryOp{
		pos: sub.Pos(), Op: parser.OpEq,
		Left:  outerKeyExprs[0],
		Right: innerKeyExprs[0],
	}
	for i := 1; i < len(params); i++ {
		eq := &BinaryOp{
			pos: sub.Pos(), Op: parser.OpEq,
			Left:  outerKeyExprs[i],
			Right: innerKeyExprs[i],
		}
		joinPredicate = &BinaryOp{pos: sub.Pos(), Op: parser.OpAnd, Left: joinPredicate, Right: eq}
	}
	mergedSchema := make(Schema, outerWidth+len(subSchema))
	copy(mergedSchema, outerChild.Output())
	for i, sc := range subSchema {
		mergedSchema[outerWidth+i] = sc
	}
	join := &Join{
		pos:       sub.Pos(),
		Type:      JoinTypeInner,
		Algo:      JoinAlgoHash,
		Left:      outerChild,
		Right:     subPlan,
		Predicate: joinPredicate,
		// Hash key uses the FIRST param's pair. The remaining
		// per-pair equalities are enforced as residuals via the
		// Predicate above.
		LeftKey:  outerKeyExprs[0],
		RightKey: &ColumnRef{pos: sub.Pos(), Index: 0, Name: params[0].SubCol.Name, Type: params[0].SubCol.Type},
		schema:   mergedSchema,
	}
	filter.Child = join
	return outer, nil
}

// --- M0040-0002: IN (subquery) → semi-join unnest ---

// findInExprInExpr walks an expression tree looking for an
// InExpr node whose source is a subquery (Plan != nil).
func findInExprInExpr(e Expr) *InExpr {
	if e == nil {
		return nil
	}
	if in, ok := e.(*InExpr); ok && in.Plan != nil {
		return in
	}
	switch x := e.(type) {
	case *BinaryOp:
		if s := findInExprInExpr(x.Left); s != nil {
			return s
		}
		return findInExprInExpr(x.Right)
	case *UnaryOp:
		return findInExprInExpr(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if s := findInExprInExpr(a); s != nil {
				return s
			}
		}
	case *CaseExpr:
		if x.Operand != nil {
			if s := findInExprInExpr(x.Operand); s != nil {
				return s
			}
		}
		for _, w := range x.Whens {
			if s := findInExprInExpr(w.When); s != nil {
				return s
			}
			if s := findInExprInExpr(w.Then); s != nil {
				return s
			}
		}
		if x.Else != nil {
			return findInExprInExpr(x.Else)
		}
	case *ExtractExpr:
		return findInExprInExpr(x.Source)
	}
	return nil
}

// correlatedInOperandSafeToUnnest verifies that the single
// correlation pair unnestInExpr will use as the join key actually
// encodes the original `in.Operand IN (subquery)` predicate, not
// merely "a correlated row exists".
//
// unnestInExpr always builds the join's (LeftKey, RightKey) from
// params[0] (the correlation equijoin pair pulled out of the
// subquery's own WHERE clause) — it never looks at in.Operand or at
// what the subquery actually SELECTs. That substitution is sound
// only when:
//
//  1. the correlation's outer-scope side (params[0].OuterRef) is the
//     SAME column as in.Operand. Otherwise the join checks "does a
//     row exist whose correlation column matches some unrelated outer
//     column" instead of "is in.Operand among the selected values" —
//     a different predicate, silently wrong for both IN and NOT IN.
//  2. the correlation's subquery-scope side (params[0].SubCol) is the
//     SAME column the subquery projects. clonePlanReplacingOuter
//     folds the correlation predicate into a tautology inside the
//     cloned inner plan (relying on the join key to reinstate it); if
//     the subquery selects a different column, that fold silently
//     discards the correlation as a real filter.
//
// When both hold, every row that survives the correlation predicate
// necessarily has a projected value equal to in.Operand and never
// NULL (the equality correlation can't be satisfied by a NULL) — so
// even a NOT IN Anti join is correct here without per-correlation-
// group NullAware tracking (unlike the general correlated NOT IN
// case, which still needs that — M0122-0011 deferral ledger).
func correlatedInOperandSafeToUnnest(in *InExpr, params []unnestParam) bool {
	if len(params) != 1 {
		return false
	}
	operand, ok := in.Operand.(*ColumnRef)
	if !ok {
		return false
	}
	outerRef := params[0].OuterRef
	if operand.Index != outerRef.Index || operand.SourceTableIdx != outerRef.SourceTableIdx || operand.Name != outerRef.Name {
		return false
	}
	proj, ok := in.Plan.(*Project)
	if !ok || len(proj.Targets) != 1 {
		return false
	}
	selected, ok := proj.Targets[0].(*ColumnRef)
	if !ok {
		return false
	}
	subCol := params[0].SubCol
	return selected.Index == subCol.Index && selected.SourceTableIdx == subCol.SourceTableIdx && selected.Name == subCol.Name
}

// canUnnestInExpr checks whether an IN (subquery) is a candidate
// for unnesting into a semi-join.  The inner plan must have a
// correlation predicate of the shape `inner_col = outer_ref`
// (every OuterColumnRef participates in an equijoin pair).
//
// M0062-0004: nested IN subqueries are accepted **iff** every
// nested IN is itself unnestable. This unblocks Q20 (depth-2 IN
// chain). The recursion cap prevents pathological nesting.
func canUnnestInExpr(in *InExpr) bool {
	return canUnnestInExprDepth(in, 0)
}

// canUnnestInExprDepth is the recursive worker for
// canUnnestInExpr. The depth cap is intentionally low: real TPC-H
// queries don't nest beyond 2 levels, and an unbounded recursion
// would let a hostile or pathologically nested predicate run a
// quadratic walkPlanExprs scan per level.
const maxNestedInUnnestDepth = 4

func canUnnestInExprDepth(in *InExpr, depth int) bool {
	if depth > maxNestedInUnnestDepth {
		return false
	}
	plan := in.Plan
	if plan == nil {
		return false
	}
	// S1a guard: the rewrite hardcodes an equality join predicate, so only
	// the plain IN / NOT IN / `= ANY` form may be pulled up. See
	// inExprIsPlainEquality.
	if !inExprIsPlainEquality(in) {
		return false
	}
	// M0069-0005: non-correlated IN — the inner plan has zero
	// OuterColumnRefs and the outer key is the IN's left operand
	// (`x IN (SELECT y FROM ...)` becomes a SemiJoin on x = y).
	// This is the cleanest case: no clone-with-replacement, no
	// equijoin-pair extraction. Q20 hits this shape (the outer
	// `s_suppkey IN (SELECT ps_suppkey FROM partsupp WHERE ...)`).
	if in.IsNonCorrelated {
		if !isUnnestableNonCorrelatedIn(in) {
			return false
		}
		// Nested INs inside the inner plan must be either
		// unnestable themselves or, more commonly for Q20, will
		// still execute as cached non-correlated subqueries — the
		// outer unnest doesn't gate on the inner.
		return true
	}
	// Collect equijoin pairs — all OuterColumnRefs must be in
	// equality joins with inner ColumnRefs.
	params := collectUnnestParams(plan)
	if params == nil || len(params) == 0 {
		return false
	}
	// unnestInExpr always keys the resulting join on the correlation
	// pair (params[0]), never on in.Operand directly — that only
	// encodes the same predicate as the original `operand IN
	// (subquery)` when the correlation identifies exactly the value
	// being tested. See correlatedInOperandSafeToUnnest's doc comment
	// for the two required conditions and why a mismatch would
	// silently change the query's meaning, not just miss an
	// optimization.
	if !correlatedInOperandSafeToUnnest(in, params) {
		return false
	}
	// M0062-0004: nested IN subqueries are now accepted *if* each
	// is itself unnestable. The pre-fix blanket reject blocked
	// Q20's depth-2 IN. The recursive `unnestSubqueriesInPlan`
	// call inside `unnestInExpr` (at the inner-plan-clone step)
	// will lift the inner IN up first; this gate just admits the
	// outer.
	allNestedUnnestable := true
	walkPlanExprs(plan, func(e Expr) {
		if in2, ok := e.(*InExpr); ok && in2.Plan != nil {
			if !canUnnestInExprDepth(in2, depth+1) {
				allNestedUnnestable = false
			}
		}
	})
	if !allNestedUnnestable {
		return false
	}
	return true
}

// isUnnestableNonCorrelatedIn checks the structural preconditions
// for unnesting a non-correlated IN: the inner plan has exactly one
// output column. Without this the Semi/Anti-join rewriting would
// mis-shape the join.
//
// M0122-0011: NOT IN is now unnestable too (previously rejected —
// "NOT IN requires anti-semi-join semantics which are out of scope
// for M0069-0005"). unnestNonCorrelatedInExpr builds a
// JoinTypeAnti with NullAware=true, which the executor gives
// NOT IN's three-valued-NULL semantics instead of the plain
// NOT-EXISTS-shaped Anti join.
//
// M0122-0011 follow-up: the LHS operand no longer has to be a bare
// ColumnRef. Join.LeftKey/RightKey are general Expr and the hash-join
// executor evaluates them with evalExpr (operators_join_agg.go's
// evalHashKey), so `f(x) IN (subquery)` or `a+b IN (subquery)` unnest
// exactly like `x IN (subquery)` — the operand is already fully
// resolved in the outer scope by planInExpr's resolveExpr call, same
// as any other outer-scope expression.
func isUnnestableNonCorrelatedIn(in *InExpr) bool {
	if in.Operand == nil {
		return false
	}
	if in.Plan == nil {
		return false
	}
	if len(in.Plan.Output()) != 1 {
		return false
	}
	return true
}

// unnestInExpr rewrites an IN (subquery) as a semi-join.
//
//	Filter(column IN (SELECT inner_col FROM ... WHERE inner_col = outer.col), outer)
//	→  JoinTypeSemi(outer, inner_plan_clone)
//
// The inner plan is cloned with OuterColumnRef → ColumnRef
// replacement so it no longer depends on the outer scope.
func unnestInExpr(in *InExpr, outer Node) (Node, error) {
	if !canUnnestInExpr(in) {
		return nil, nil
	}
	// M0069-0005: non-correlated IN takes a separate path that
	// doesn't go through clonePlanReplacingOuter (no
	// OuterColumnRefs to replace).
	if in.IsNonCorrelated {
		return unnestNonCorrelatedInExpr(in, outer)
	}
	params := collectUnnestParams(in.Plan)
	if len(params) == 0 {
		return nil, nil
	}
	// Replace OuterColumnRefs in the inner plan with their
	// corresponding ColumnRefs so the inner plan is self-contained.
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	for _, p := range params {
		replace[p.OuterRef] = p.SubCol
	}
	innerPlan, err := clonePlanReplacingOuter(in.Plan, replace)
	if err != nil {
		return nil, err
	}
	// Recursively unnest any scalar subqueries still inside the
	// inner plan (e.g. Q20's lineitem aggregate inside the
	// partsupp IN subquery).
	innerPlan = unnestSubqueriesInPlan(innerPlan)

	// Find the Filter that wraps the outer node.
	filter, _ := findFilterContainingInExpr(outer, in)
	if filter == nil {
		return nil, nil
	}
	// S1a guard: the sublink must occupy a top-level conjunct (optionally
	// under a single NOT, which flips the join to its dual). Anything else
	// stays a SubPlan — see inExprTopConjunct.
	conjunct, negateFlip, topOK := inExprTopConjunct(filter, in)
	if !topOK {
		return nil, nil
	}
	effNegated := in.Negated != negateFlip

	outerChild := filter.Child
	outerWidth := len(outerChild.Output())
	innerWidth := len(innerPlan.Output())

	// Build semi-join keys.
	outerKey := &ColumnRef{
		pos:            params[0].OuterRef.Pos(),
		Index:          params[0].OuterRef.Index,
		Name:           params[0].OuterRef.Name,
		Type:           params[0].OuterRef.Type,
		SourceTableIdx: params[0].OuterRef.SourceTableIdx,
	}
	// innerKey.Index is the position of the inner plan's output column in the
	// merged (outer ++ inner) schema. innerKey.Name MUST match the inner plan's
	// actual output column name — NOT params[0].SubCol.Name (the equijoin column).
	// reresolveJoinByName.predRebind re-binds keys by Name: if innerKey.Name is
	// the equijoin column ("f1") but the inner plan projects a different column
	// ("f2"), predRebind won't find "f1" on the right side and falls back to the
	// left, silently setting innerKey.Index to the outer "f1" position (0), which
	// corrupts the hash-join key and produces 0 matches.
	innerOutName := params[0].SubCol.Name
	if out := innerPlan.Output(); len(out) > 0 {
		innerOutName = out[0].Name
	}
	innerKey := &ColumnRef{
		pos:            params[0].SubCol.Pos(),
		Index:          outerWidth,
		Name:           innerOutName,
		Type:           params[0].SubCol.Type,
		SourceTableIdx: 0, // inner output column; no outer source identity
	}

	// Mark the root Project of the inner plan as IsolatedScope so the NLI
	// rewriter does not convert the SemiJoin into an NLI (mirrors the
	// non-correlated path; M0071-0002).
	if proj, ok := innerPlan.(*Project); ok {
		proj.IsolatedScope = true
	}

	// Drop the IN conjunct from the filter — the join encodes the equality
	// via (LeftKey, RightKey) and the SemiJoin type. Keeping it in the
	// filter would re-evaluate it on the outer-only semi-join output where
	// innerKey.Index (outerWidth) is out of range.
	conjuncts := splitAnd(filter.Predicate)
	newConjuncts := make([]Expr, 0, len(conjuncts))
	for _, c := range conjuncts {
		if c != conjunct {
			newConjuncts = append(newConjuncts, c)
		}
	}
	if len(newConjuncts) == 0 {
		filter.Predicate = &BooleanConst{pos: in.Pos(), Value: true}
	} else {
		filter.Predicate = combineAnd(newConjuncts)
	}

	semiPred := &BinaryOp{pos: in.Pos(), Op: parser.OpEq, Left: outerKey, Right: innerKey}
	joinType := JoinTypeSemi
	if effNegated {
		joinType = JoinTypeAnti
	}
	_ = innerWidth
	join := &Join{
		pos:       in.Pos(),
		Type:      joinType,
		Algo:      JoinAlgoHash,
		Left:      outerChild,
		Right:     innerPlan,
		Predicate: semiPred,
		LeftKey:   outerKey,
		RightKey:  innerKey,
		schema:    append(Schema(nil), outerChild.Output()...),
	}
	filter.Child = join
	return outer, nil
}

// unnestNonCorrelatedInExpr handles the non-correlated case where
// the inner plan has zero OuterColumnRefs (M0069-0005).
//
//	Filter(outer_col IN (SELECT inner_col FROM inner_plan), outer)
//	→ Join(outer, inner_plan, on outer_col = inner_col, semi)
//
// This is structurally identical to the correlated path's tail
// (Join construction + Filter rewrite) but skips the inner-plan
// clone and the equijoin-pair extraction since there is nothing
// outer-bound in the inner plan.
func unnestNonCorrelatedInExpr(in *InExpr, outer Node) (Node, error) {
	if !isUnnestableNonCorrelatedIn(in) {
		return nil, nil
	}
	if in.Operand == nil {
		return nil, nil
	}
	innerPlan := in.Plan
	// Recursively unnest any subqueries still inside the inner
	// plan so nested IN/EXISTS/scalar inside the partsupp filter
	// (the lineitem aggregate in Q20) are pulled up first.
	innerPlan = unnestSubqueriesInPlan(innerPlan)

	// M0071-0002: mark the Project at the root of the inner
	// subquery scope as IsolatedScope so the NLI rewriter
	// (`internal/planner/nl_index_join.go::tryBuildNLI`) declines
	// to convert the SemiJoin into an NLI. The conversion would
	// flip pickInnerSide's outer/inner roles and shift inner-side
	// Filter ColumnRefs by `partsupp_width`, breaking the inner
	// subquery's Filter (e.g. Q20's `p_name LIKE 'forest%'`
	// resolved at part's idx 1 → idx 4 mismatch). M0063-0001's
	// existing IsolatedScope gate already covers view-rename
	// wrappers; this hooks the same mechanism for IN-unnested
	// inner subqueries so the SemiJoin's Right side stays a
	// hash-built isolated scope.
	if proj, ok := innerPlan.(*Project); ok {
		proj.IsolatedScope = true
	}

	innerOut := innerPlan.Output()
	if len(innerOut) != 1 {
		return nil, nil
	}
	// The inner key is the single output column of the inner plan.
	innerKey := &ColumnRef{
		pos:   in.Pos(),
		Index: 0, // will be re-indexed below to point into the merged schema
		Name:  innerOut[0].Name,
		Type:  innerOut[0].Type,
	}

	filter, _ := findFilterContainingInExpr(outer, in)
	if filter == nil {
		return nil, nil
	}
	// S1a guard: same top-level-conjunct requirement as the correlated
	// path — see inExprTopConjunct.
	conjunct, negateFlip, topOK := inExprTopConjunct(filter, in)
	if !topOK {
		return nil, nil
	}
	effNegated := in.Negated != negateFlip

	outerChild := filter.Child
	outerWidth := len(outerChild.Output())
	innerWidth := len(innerPlan.Output())

	// Re-index inner key into the merged (outer ++ inner) coord.
	innerKey.Index = outerWidth

	// The outer key is the IN's left operand itself, already
	// resolved against outerChild's coord by planInExpr — no
	// ColumnRef reconstruction needed since LeftKey/RightKey accept
	// any Expr (see isUnnestableNonCorrelatedIn's doc comment).
	outerKey := in.Operand

	// M0069-0005: drop the IN conjunct from the filter — the
	// join encodes the equality via (LeftKey, RightKey) and the
	// SemiJoin type. Mirrors the EXISTS unnest (M0061-0001) which
	// drops the EXISTS conjunct rather than replacing it with a
	// predicate. Keeping the predicate in the filter would cause
	// the planner to re-evaluate it on the join's output (which
	// is outer-only after Semi) where innerKey's outerWidth
	// index reaches past the row width and trips
	// `column ref %s/%d out of range`.
	conjuncts := splitAnd(filter.Predicate)
	newConjuncts := make([]Expr, 0, len(conjuncts))
	for _, c := range conjuncts {
		if c != conjunct {
			newConjuncts = append(newConjuncts, c)
		}
	}
	if len(newConjuncts) == 0 {
		filter.Predicate = &BooleanConst{pos: in.Pos(), Value: true}
	} else {
		filter.Predicate = combineAnd(newConjuncts)
	}

	// M0069-0005: use JoinTypeSemi with outer-only output schema.
	// JoinTypeInner with mergedSchema would widen the output by
	// the inner column, which corrupts column-index references in
	// any upstream operator (Q18's Aggregate over a 3-table FROM
	// hit exactly this — the SUM(l_quantity) reference moved past
	// the outer width). The executor's JoinTypeSemi already emits
	// just the probe row; preserving the outer schema here keeps
	// every upstream column index valid.
	//
	// M0122-0011: `x NOT IN (subquery)` uses JoinTypeAnti with
	// NullAware=true instead — the executor gives it NOT IN's
	// three-valued-NULL semantics (a NULL anywhere in the subquery
	// output, or in x itself, generally excludes the row — see the
	// NullAware doc comment on the Join struct) rather than the
	// plain NOT-EXISTS-shaped Anti join.
	semiPred := &BinaryOp{pos: in.Pos(), Op: parser.OpEq, Left: outerKey, Right: innerKey}
	_ = innerWidth
	joinType := JoinTypeSemi
	if effNegated {
		joinType = JoinTypeAnti
	}
	join := &Join{
		pos:       in.Pos(),
		Type:      joinType,
		Algo:      JoinAlgoHash,
		Left:      outerChild,
		Right:     innerPlan,
		Predicate: semiPred,
		LeftKey:   outerKey,
		RightKey:  innerKey,
		NullAware: effNegated,
		schema:    append(Schema(nil), outerChild.Output()...),
	}
	filter.Child = join
	return outer, nil
}

// findFilterContainingInExpr walks the plan tree looking for the
// Filter node that wraps inner containing the given conjunct
// expression (the IN expression).
func findFilterContainingInExpr(node Node, target *InExpr) (*Filter, Expr) {
	if node == nil {
		return nil, nil
	}
	if f, ok := node.(*Filter); ok {
		if c := findExprInExpr(f.Predicate, func(e Expr) bool {
			return e == target
		}); c != nil {
			return f, c
		}
	}
	switch n := node.(type) {
	case *Join:
		if f, c := findFilterContainingInExpr(n.Left, target); f != nil {
			return f, c
		}
		return findFilterContainingInExpr(n.Right, target)
	case *Filter:
		return findFilterContainingInExpr(n.Child, target)
	case *Project:
		return findFilterContainingInExpr(n.Child, target)
	case *Aggregate:
		return findFilterContainingInExpr(n.Child, target)
	case *Sort:
		return findFilterContainingInExpr(n.Child, target)
	case *Limit:
		return findFilterContainingInExpr(n.Child, target)
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			if f, c := findFilterContainingInExpr(tbl, target); f != nil {
				return f, c
			}
		}
	}
	return nil, nil
}

// findExprInExpr returns the first non-nil expression in the
// tree for which match returns true.  Returns nil if no match.
func findExprInExpr(e Expr, match func(Expr) bool) Expr {
	if e == nil {
		return nil
	}
	if match(e) {
		return e
	}
	switch x := e.(type) {
	case *BinaryOp:
		if r := findExprInExpr(x.Left, match); r != nil {
			return r
		}
		return findExprInExpr(x.Right, match)
	case *UnaryOp:
		return findExprInExpr(x.Operand, match)
	case *FuncCall:
		for _, a := range x.Args {
			if r := findExprInExpr(a, match); r != nil {
				return r
			}
		}
	}
	return nil
}

// --- M0061-0001: EXISTS / NOT EXISTS → semi-join / anti-join ---

// findExistsExprInExpr walks an expression tree looking for an
// ExistsExpr that lives at a top-level conjunct position. Like the
// IN-unnesting pass we reuse the structural walker; the
// canUnnestExistsExpr gate then checks that the EXISTS conjunct can
// be safely lifted (correlated equijoin only).
func findExistsExprInExpr(e Expr) *ExistsExpr {
	if e == nil {
		return nil
	}
	if ex, ok := e.(*ExistsExpr); ok && ex.Plan != nil {
		return ex
	}
	switch x := e.(type) {
	case *BinaryOp:
		if s := findExistsExprInExpr(x.Left); s != nil {
			return s
		}
		return findExistsExprInExpr(x.Right)
	case *UnaryOp:
		return findExistsExprInExpr(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if s := findExistsExprInExpr(a); s != nil {
				return s
			}
		}
	case *CaseExpr:
		if x.Operand != nil {
			if s := findExistsExprInExpr(x.Operand); s != nil {
				return s
			}
		}
		for _, w := range x.Whens {
			if s := findExistsExprInExpr(w.When); s != nil {
				return s
			}
			if s := findExistsExprInExpr(w.Then); s != nil {
				return s
			}
		}
		if x.Else != nil {
			return findExistsExprInExpr(x.Else)
		}
	case *ExtractExpr:
		return findExistsExprInExpr(x.Source)
	}
	return nil
}

// liftInnerOnlyFilterConjuncts walks `node` and removes from
// every Filter's Predicate any top-level conjunct that is NOT
// a trivial tautology and contains NO OuterColumnRef
// (assumes stripOuterRefConjuncts has already removed those).
// The removed conjuncts are returned in caller order so the
// caller can lift them to a parent join's Predicate. Used by
// M0063-0004's EXISTS-unnesting tail.
func liftInnerOnlyFilterConjuncts(node Node) []Expr {
	if node == nil {
		return nil
	}
	var lifted []Expr
	switch n := node.(type) {
	case *Filter:
		conjs := splitAnd(n.Predicate)
		out := make([]Expr, 0, len(conjs))
		for _, c := range conjs {
			// Tautology check: ColumnRef = ColumnRef same Index/Name.
			if bin, ok := c.(*BinaryOp); ok && bin.Op == parser.OpEq {
				if l, lok := bin.Left.(*ColumnRef); lok {
					if r, rok := bin.Right.(*ColumnRef); rok {
						if l.Index == r.Index && l.Name == r.Name {
							continue
						}
					}
				}
			}
			// BooleanConst(true) → drop.
			if bc, ok := c.(*BooleanConst); ok && bc.Value {
				continue
			}
			lifted = append(lifted, c)
		}
		// Replace inner Filter predicate with `true`.
		n.Predicate = &BooleanConst{pos: n.pos, Value: true}
		_ = out
		lifted = append(lifted, liftInnerOnlyFilterConjuncts(n.Child)...)
	case *Project:
		lifted = append(lifted, liftInnerOnlyFilterConjuncts(n.Child)...)
	case *Aggregate:
		lifted = append(lifted, liftInnerOnlyFilterConjuncts(n.Child)...)
	case *Sort:
		lifted = append(lifted, liftInnerOnlyFilterConjuncts(n.Child)...)
	case *Limit:
		lifted = append(lifted, liftInnerOnlyFilterConjuncts(n.Child)...)
	}
	return lifted
}

// unwrapTrivialWrappers strips Project(Filter(true, x)) /
// Project(x) wrappers when they don't change the row shape. Used
// by M0063-0004's EXISTS-unnesting tail to expose a bare
// *SeqScan to `pickInnerSide` so NLI can fire for the resulting
// hash Semi / Anti join.
func unwrapTrivialWrappers(n Node) Node {
	for {
		switch x := n.(type) {
		case *Filter:
			if bc, ok := x.Predicate.(*BooleanConst); ok && bc.Value {
				n = x.Child
				continue
			}
			return n
		case *Project:
			// Identity Project: Targets are ColumnRef{Index: i}
			// in order, len matches Child schema, and the
			// Project carries no IsolatedScope flag.
			if x.IsolatedScope {
				return n
			}
			child := x.Child
			if child == nil {
				return n
			}
			if len(x.Targets) != len(child.Output()) {
				return n
			}
			identity := true
			for i, t := range x.Targets {
				cr, ok := t.(*ColumnRef)
				if !ok || cr.Index != i {
					identity = false
					break
				}
			}
			if !identity {
				return n
			}
			n = child
			continue
		default:
			return n
		}
	}
}

// stripOuterRefConjuncts walks `node` and removes from every
// Filter's Predicate any top-level conjunct that still references
// an OuterColumnRef. Used by `unnestExistsExpr` (M0062-0005)
// after cloning the inner plan: the residual conjuncts we lifted
// to the join's Predicate must not also be evaluated by the
// inner Filter (they reference unbound OuterColumnRefs at the
// inner plan's scope). Conjuncts that became self-equality
// tautologies via the equijoin `replace` map are also dropped.
func stripOuterRefConjuncts(node Node) {
	if node == nil {
		return
	}
	switch n := node.(type) {
	case *Filter:
		conjs := splitAnd(n.Predicate)
		out := make([]Expr, 0, len(conjs))
		for _, c := range conjs {
			hasOuter := false
			tautology := false
			walkExprTree(c, func(e Expr) {
				if _, ok := e.(*OuterColumnRef); ok {
					hasOuter = true
				}
			})
			if bin, ok := c.(*BinaryOp); ok && bin.Op == parser.OpEq {
				if l, lok := bin.Left.(*ColumnRef); lok {
					if r, rok := bin.Right.(*ColumnRef); rok {
						if l.Index == r.Index && l.Name == r.Name {
							tautology = true
						}
					}
				}
			}
			if hasOuter || tautology {
				continue
			}
			out = append(out, c)
		}
		if len(out) == 0 {
			n.Predicate = &BooleanConst{pos: n.pos, Value: true}
		} else {
			n.Predicate = combineAnd(out)
		}
		stripOuterRefConjuncts(n.Child)
	case *Project:
		stripOuterRefConjuncts(n.Child)
	case *Aggregate:
		stripOuterRefConjuncts(n.Child)
	case *Sort:
		stripOuterRefConjuncts(n.Child)
	case *Limit:
		stripOuterRefConjuncts(n.Child)
	case *Join:
		stripOuterRefConjuncts(n.Left)
		stripOuterRefConjuncts(n.Right)
	}
}

// existsUnnestPlan describes how an EXISTS subquery's correlation
// predicate decomposes for unnesting:
//   - Params: equijoin pairs that become the join's hash key(s).
//   - Residuals: the original (uncloned) BinaryOp / general
//     conjunct expressions that reference an OuterColumnRef but
//     are NOT a pure column-equijoin pair. Examples: `<>`,
//     range comparisons, function calls. After unnesting these
//     are lifted onto the join's `Predicate` with both inner
//     ColumnRefs shifted by `outerWidth` and OuterColumnRefs
//     rewritten to ColumnRefs at their outer-side index.
//
// (M0062-0005.)
type existsUnnestPlan struct {
	Params    []unnestParam
	Residuals []Expr
}

// collectExistsUnnestParamsAndResiduals splits the inner plan's
// top-level conjuncts (anywhere a Filter sits) into:
//   - equijoin params (existing collectUnnestParams logic), and
//   - non-equi residuals that reference an OuterColumnRef.
//
// Returns nil if the plan has any OuterColumnRef that does NOT
// appear in either bucket — those would still be correlated after
// the lift, which we cannot handle.
func collectExistsUnnestParamsAndResiduals(node Node) *existsUnnestPlan {
	var params []unnestParam
	var residuals []Expr
	outerInEquijoin := make(map[*OuterColumnRef]bool)
	outerInResidual := make(map[*OuterColumnRef]bool)

	// Walk every Filter in the inner plan; only top-level
	// conjuncts of a Filter's Predicate may be lifted (an
	// OuterColumnRef nested inside an OR or a function call
	// can't be cleanly removed from the inner plan).
	var walkFilters func(Node)
	walkFilters = func(n Node) {
		if n == nil {
			return
		}
		switch x := n.(type) {
		case *Filter:
			walkFilters(x.Child)
			for _, c := range splitAnd(x.Predicate) {
				bin, ok := c.(*BinaryOp)
				if ok && bin.Op == parser.OpEq {
					outer, col := extractEquijoinPair(bin.Left, bin.Right)
					if outer != nil && col != nil {
						params = append(params, unnestParam{OuterRef: outer, SubCol: col})
						outerInEquijoin[outer] = true
						continue
					}
				}
				// Non-equijoin conjunct. If it mentions an
				// OuterColumnRef and ONLY has top-level outer
				// refs (no OuterColumnRef nested under e.g. a
				// further OR or CASE), record it as a residual.
				var refs []*OuterColumnRef
				outerOK := true
				walkExprTree(c, func(e Expr) {
					if oc, ok := e.(*OuterColumnRef); ok {
						refs = append(refs, oc)
					}
				})
				_ = outerOK
				if len(refs) > 0 {
					residuals = append(residuals, c)
					for _, oc := range refs {
						outerInResidual[oc] = true
					}
				}
			}
		case *Project:
			walkFilters(x.Child)
		case *Aggregate:
			walkFilters(x.Child)
		case *Sort:
			walkFilters(x.Child)
		case *Limit:
			walkFilters(x.Child)
		case *Join:
			walkFilters(x.Left)
			walkFilters(x.Right)
		}
	}
	walkFilters(node)

	// D3.0: harvest the correlation the inner planner folded into an
	// IndexScan equality key (see collectUnnestParams / harvestIndexKeyParams).
	// This is what makes EXISTS decorrelate on the TPC-H schema: Q4's
	// `l_orderkey = o_orderkey` lives in the index probe, not a Filter,
	// and the residual `l_commitdate < l_receiptdate` is lifted by the
	// Filter walk above — the two compose into a semi join with a residual.
	for _, p := range harvestIndexKeyParams(node) {
		params = append(params, p)
		outerInEquijoin[p.OuterRef] = true
	}

	// Verify every OuterColumnRef is accounted for.
	allAccounted := true
	walkPlanExprs(node, func(e Expr) {
		if o, ok := e.(*OuterColumnRef); ok {
			if !outerInEquijoin[o] && !outerInResidual[o] {
				allAccounted = false
			}
		}
	})
	if !allAccounted {
		return nil
	}
	return &existsUnnestPlan{Params: params, Residuals: residuals}
}

// canUnnestExistsExpr accepts a correlated EXISTS subquery whose
// correlation predicate can be split into equijoin keys plus
// liftable non-equijoin residuals. M0062-0005 added the residual
// path to handle Q21's `l2.l_suppkey <> l1.l_suppkey` correlation
// alongside the equi-pair `l2.l_orderkey = l1.l_orderkey`.
//
// Non-correlated EXISTS already collapses to a single inner-plan
// execution via M0058-0001's IsNonCorrelated cache; converting it
// to a join is unnecessary churn and would lose the cache hit when
// the same EXISTS appears across many queries.
//
// EXISTS subqueries with nested IN / EXISTS that themselves can
// not be unnested are rejected — leaving them as SubPlans is safer
// than introducing a join with a still-correlated inner plan.
func canUnnestExistsExpr(ex *ExistsExpr) bool {
	plan := ex.Plan
	if plan == nil {
		return false
	}
	if ex.IsNonCorrelated {
		return false
	}
	// S1a guard: EXISTS asks only "any rows?", but the body can carry
	// clauses whose meaning does not survive being turned into a
	// semi/anti-join build side — see existsBodySafeForPullup.
	if !existsBodySafeForPullup(plan) {
		return false
	}
	eup := collectExistsUnnestParamsAndResiduals(plan)
	if eup == nil || len(eup.Params) == 0 {
		// Need at least one equijoin to drive the hash join.
		return false
	}
	var hasNestedSub bool
	walkPlanExprs(plan, func(e Expr) {
		if in2, ok := e.(*InExpr); ok && in2.Plan != nil {
			hasNestedSub = true
		}
		if ex2, ok := e.(*ExistsExpr); ok && ex2.Plan != nil {
			hasNestedSub = true
		}
		if sq2, ok := e.(*SubqueryExpr); ok && sq2.Plan != nil {
			hasNestedSub = true
		}
	})
	if hasNestedSub {
		return false
	}
	return true
}

// unnestExistsExpr rewrites a top-level EXISTS / NOT EXISTS
// conjunct into a semi-join / anti-join.
//
//	Filter(EXISTS(SELECT ... WHERE inner = outer.col)     , outer)
//	→  Filter(other_preds, JoinTypeSemi(outer, inner_clone))
//
//	Filter(NOT EXISTS(SELECT ... WHERE inner = outer.col) , outer)
//	→  Filter(other_preds, JoinTypeAnti(outer, inner_clone))
//
// `NOT EXISTS` arrives from the parser as `UnaryOp("NOT",
// ExistsExpr{Negated: false})` — we look for the EXISTS at a
// top-level conjunct position OR wrapped in a single NOT UnaryOp
// at a top-level conjunct position.
//
// The inner plan is cloned with OuterColumnRef → ColumnRef
// replacement so it is self-contained; remaining inner-plan
// optimisation runs via the recursive unnestSubqueriesInPlan call
// below.
//
// The join's output schema is the LEFT (outer) schema only —
// downstream column indices are unchanged from before unnesting.
func unnestExistsExpr(ex *ExistsExpr, outer Node) (Node, error) {
	if !canUnnestExistsExpr(ex) {
		return nil, nil
	}
	eup := collectExistsUnnestParamsAndResiduals(ex.Plan)
	if eup == nil || len(eup.Params) == 0 {
		return nil, nil
	}
	params := eup.Params
	// Only params[0] becomes the hash key below; a second equijoin pair
	// (a composite-index correlation, or two Filter equi-conjuncts) would
	// have to ride as a residual. A NON-equi residual is fine and is the
	// common case (Q21's `l2.l_suppkey <> l1.l_suppkey`), but an *equi*
	// residual on a semi/anti join interacts badly with the downstream
	// NLI rewrite (it can be extracted as a competing probe key and the
	// pair silently dropped), so refuse the pull-up and let the SubPlan
	// path — always correct — serve a multi-equijoin EXISTS. No TPC-H
	// query needs this; composite-correlated EXISTS decorrelation is a
	// recorded follow-up.
	if len(params) > 1 {
		return nil, nil
	}

	// Find the Filter that contains this ExistsExpr conjunct.
	filter, conjunct := findFilterContainingExistsExpr(outer, ex)
	if filter == nil {
		return nil, nil
	}

	// The EXISTS may sit at a top-level conjunct directly, or be
	// wrapped in a `NOT` UnaryOp (the parser's representation of
	// `NOT EXISTS`). Determine the actual top-level conjunct and
	// whether the EXISTS is negated.
	negated := ex.Negated
	conjuncts := splitAnd(filter.Predicate)
	topConjunct := Expr(nil)
	for _, c := range conjuncts {
		if c == conjunct {
			topConjunct = c
			break
		}
		if u, ok := c.(*UnaryOp); ok && u.Op == parser.OpNot {
			if u.Operand == conjunct {
				topConjunct = c
				negated = !negated
				break
			}
		}
	}
	if topConjunct == nil {
		// EXISTS is buried in a non-conjunct context (e.g. an
		// OR branch or inside a function call). Cannot lift to
		// a join without changing semantics.
		return nil, nil
	}

	// Replace OuterColumnRefs with ColumnRefs and clone the inner
	// plan so it no longer carries outer-scope dependencies.
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	for _, p := range params {
		replace[p.OuterRef] = p.SubCol
	}
	innerPlan, err := clonePlanReplacingOuter(ex.Plan, replace)
	if err != nil {
		return nil, err
	}
	// M0062-0005: strip any conjuncts from the cloned inner plan's
	// Filters that still reference an OuterColumnRef. The residual
	// conjuncts have been lifted to the join's Predicate; if they
	// stayed in the inner plan the executor would try to evaluate
	// an unbound OuterColumnRef at build time. The equi-pair
	// conjuncts were already neutralised by the `replace` map (they
	// became self-equality tautologies) — those are also removed
	// for cleanliness.
	if len(eup.Residuals) > 0 {
		stripOuterRefConjuncts(innerPlan)
	}
	// Always strip even if no residuals — the equi-pair conjuncts
	// have been replaced by self-equality tautologies and should
	// be removed for plan tidiness AND so M0063-0004's NLI rewrite
	// can see a bare SeqScan beneath.
	stripOuterRefConjuncts(innerPlan)
	// S1a guard: drop the LIMIT that existsBodySafeForPullup accepted.
	// Left in place it would cap the whole semi-join build side rather
	// than each correlation group, so only one key could ever match —
	// mirrors simplify_EXISTS_query's "we can drop the LIMIT" step.
	innerPlan = stripPositiveConstLimits(innerPlan)
	innerPlan = unnestSubqueriesInPlan(innerPlan)
	// M0063-0004: simplify trivial wrappers so M0054-0006 NLI can
	// recognise the inner side as a *SeqScan in `pickInnerSide`.
	// Project(Filter(true, x)) → Project(x) → x when the Project
	// is identity AND the Filter's predicate is true. EXISTS
	// shapes whose inner Filter only has the equi-pair (now a
	// stripped tautology) end up as bare SeqScans — Q21's first
	// EXISTS qualifies; the NOT EXISTS doesn't (its inner Filter
	// still has `l_receiptdate > l_commitdate`).
	innerPlan = unwrapTrivialWrappers(innerPlan)
	// M0097-0146: Strip a non-identity Project wrapper from the inner EXISTS
	// plan so the hash build uses the raw scan rows rather than the projected
	// output (e.g. `SELECT 1 FROM pg_type WHERE ...` projects [1] which does
	// NOT contain the pg_type.oid key column). The SubCol.Index values were
	// computed against the SeqScan/FROM binding schema, so the inner plan
	// must expose that schema — not a projected subset. A Project that
	// does NOT contain the key column in its output targets is stripped.
	if proj, ok := innerPlan.(*Project); ok && !proj.IsolatedScope {
		// Check whether RightKey column index is accessible in the projected
		// output. For EXISTS inner plans the equijoin key is always a column
		// from the scan (SubCol.Index = index in the scan schema, not in the
		// project output). Strip the project so the hash sees the scan row.
		innerPlan = proj.Child
		// Unwrap any Filter(true) that might now be at the top.
		innerPlan = unwrapTrivialWrappers(innerPlan)
	}
	// D3.0 belt: the equijoin residuals were lifted onto the join
	// predicate below and the equi-pair keys neutralised in the clone;
	// if any OuterColumnRef still survives in the inner plan the harvest
	// was incomplete, so bail to the SubPlan path rather than build a
	// semi/anti join over an unbound reference.
	if planHasOuterRefRemaining(innerPlan) {
		return nil, nil
	}
	var innerOnlyLifted []Expr

	outerChild := filter.Child
	outerWidth := len(outerChild.Output())

	// Build keys. The probe (outer) key uses the outer column ref
	// at its original index; the build (inner) key uses the inner
	// ColumnRef adjusted to point into the inner plan's own
	// schema, NOT a merged schema, because the join's output is
	// outer-only and the build side reads from a row of inner
	// width alone (see executor's openLazyHashJoin path for
	// BuildLeft=false).
	outerKey := &ColumnRef{
		pos:            params[0].OuterRef.Pos(),
		Index:          params[0].OuterRef.Index,
		Name:           params[0].OuterRef.Name,
		Type:           params[0].OuterRef.Type,
		SourceTableIdx: params[0].OuterRef.SourceTableIdx,
	}
	// The executor's evalHashKey is given a padded row of width
	// (leftWidth + rightWidth). For semi/anti the right key reads
	// the inner column from the right-side region of that padded
	// row, so its Index must be `outerWidth + innerColIndex`.
	innerKey := &ColumnRef{
		pos:            params[0].SubCol.Pos(),
		Index:          outerWidth + params[0].SubCol.Index,
		Name:           params[0].SubCol.Name,
		Type:           params[0].SubCol.Type,
		SourceTableIdx: params[0].SubCol.SourceTableIdx,
	}

	// Drop the EXISTS conjunct (or its wrapping NOT) entirely;
	// the join encodes the equality predicate via (LeftKey,
	// RightKey) and the negation via Type=Anti.
	newConjuncts := make([]Expr, 0, len(conjuncts))
	for _, c := range conjuncts {
		if c != topConjunct {
			newConjuncts = append(newConjuncts, c)
		}
	}
	if len(newConjuncts) == 0 {
		// All conjuncts consumed — keep the Filter wrapping but
		// with a true-predicate so downstream code that recurses
		// through Filter still finds the join.
		filter.Predicate = &BooleanConst{pos: ex.Pos(), Value: true}
	} else {
		filter.Predicate = combineAnd(newConjuncts)
	}

	joinType := JoinTypeSemi
	if negated {
		joinType = JoinTypeAnti
	}

	// M0062-0005: lift non-equijoin residuals (e.g. Q21's
	// `l2.l_suppkey <> l1.l_suppkey`) into the join's Predicate
	// after rewriting indices: OuterColumnRef → ColumnRef at the
	// outer's original Index, inner ColumnRef → ColumnRef at
	// `outerWidth + originalIndex`. The join evaluates Predicate
	// against the (outer ++ inner) joined row.
	//
	// M0071-0003: the OuterColumnRef rewrite must re-resolve
	// `x.Index` against the actual outerChild.Output() schema by
	// Name. The binder set Index against FROM-cumulative offsets
	// at parse time; bushy / MHJ / NLI rewrites may have
	// reordered the runtime row layout (cf. M0064 the same
	// pattern in nl_index_join.go:399). Without this re-resolve,
	// Q21's residual `l1.l_suppkey` index points at the wrong
	// outer column → AntiJoin's per-row Predicate evaluates
	// against an unrelated value → silent over-match (rows = 0
	// vs canonical ~411).
	outerSchema := outerChild.Output()
	resolveOuterIdx := func(name string, fallback int, sourceTableIdx int16) int {
		// Disambiguate self-joins (Q21 has lineitem l1, l2, l3):
		// the binder's offset uses l1.offset; we look up the
		// matching offset by walking outerSchema's name list and
		// finding the one whose absolute position matches the
		// fallback. This preserves multi-binding semantics.
		if fallback >= 0 && fallback < len(outerSchema) && outerSchema[fallback].Name == name {
			// Position still aligns AND (when known) the source
			// identity also matches; trust the binder's index.
			if sourceTableIdx == 0 || outerSchema[fallback].SourceTableIdx == sourceTableIdx {
				return fallback
			}
			// Position aligns by Name but source-identity tells
			// us this slot belongs to a sibling self-join binding
			// — fall through to the SourceTableIdx-aware lookup.
		}
		// M0071-0009: when the OuterColumnRef knows its source
		// identity (SourceTableIdx > 0), prefer the column whose
		// schema entry matches both Name and SourceTableIdx. This
		// is what makes Q21's l3.l_suppkey <> l1.l_suppkey resolve
		// correctly even though `l_suppkey` appears multiple times
		// in the outer schema.
		if sourceTableIdx != 0 {
			for i, c := range outerSchema {
				if c.Name == name && c.SourceTableIdx == sourceTableIdx {
					return i
				}
			}
		}
		// The schema's positions diverged from FROM-cumulative
		// AND we have no source identity (or no match). Walk and
		// pick the first column with matching Name — safe ONLY
		// when the column name is unique in the outer schema.
		// For ambiguous cases (self-join) keep fallback.
		first := -1
		count := 0
		for i, c := range outerSchema {
			if c.Name == name {
				if first < 0 {
					first = i
				}
				count++
			}
		}
		if count == 1 {
			return first
		}
		return fallback
	}
	var joinPredicate Expr
	if len(eup.Residuals) > 0 || len(innerOnlyLifted) > 0 {
		var rewriteIdx func(Expr) Expr
		rewriteIdx = func(e Expr) Expr {
			if e == nil {
				return nil
			}
			switch x := e.(type) {
			case *OuterColumnRef:
				return &ColumnRef{
					pos:            x.Pos(),
					Index:          resolveOuterIdx(x.Name, x.Index, x.SourceTableIdx),
					Name:           x.Name,
					Type:           x.Type,
					SourceTableIdx: x.SourceTableIdx,
				}
			case *ColumnRef:
				cl := *x
				cl.Index = outerWidth + x.Index
				return &cl
			case *BinaryOp:
				return &BinaryOp{
					pos:   x.Pos(),
					Op:    x.Op,
					Left:  rewriteIdx(x.Left),
					Right: rewriteIdx(x.Right),
				}
			case *UnaryOp:
				return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: rewriteIdx(x.Operand)}
			case *FuncCall:
				args := make([]Expr, len(x.Args))
				for i, a := range x.Args {
					args[i] = rewriteIdx(a)
				}
				return &FuncCall{pos: x.Pos(), Name: x.Name, Args: args, Star: x.Star}
			default:
				return e
			}
		}
		var residualConj []Expr
		for _, r := range eup.Residuals {
			residualConj = append(residualConj, rewriteIdx(r))
		}
		// M0063-0004: inner-only conjuncts (no OuterColumnRef)
		// also need their inner ColumnRef indices shifted by
		// outerWidth so they evaluate against the joinBuf.
		for _, r := range innerOnlyLifted {
			residualConj = append(residualConj, rewriteIdx(r))
		}
		joinPredicate = combineAnd(residualConj)
	}

	join := &Join{
		pos:       ex.Pos(),
		Type:      joinType,
		Algo:      JoinAlgoHash,
		Left:      outerChild,
		Right:     innerPlan,
		Predicate: joinPredicate,
		LeftKey:   outerKey,
		RightKey:  innerKey,
		// Schema is the outer side only — semi/anti joins do not
		// project inner columns.
		schema: append(Schema(nil), outerChild.Output()...),
	}
	filter.Child = join
	return outer, nil
}

// findFilterContainingExistsExpr walks the plan tree looking for
// the Filter whose predicate contains the target ExistsExpr.
func findFilterContainingExistsExpr(node Node, target *ExistsExpr) (*Filter, Expr) {
	if node == nil {
		return nil, nil
	}
	if f, ok := node.(*Filter); ok {
		if c := findExprInExpr(f.Predicate, func(e Expr) bool {
			return e == target
		}); c != nil {
			return f, c
		}
	}
	switch n := node.(type) {
	case *Join:
		if f, c := findFilterContainingExistsExpr(n.Left, target); f != nil {
			return f, c
		}
		return findFilterContainingExistsExpr(n.Right, target)
	case *Filter:
		return findFilterContainingExistsExpr(n.Child, target)
	case *Project:
		return findFilterContainingExistsExpr(n.Child, target)
	case *Aggregate:
		return findFilterContainingExistsExpr(n.Child, target)
	case *Sort:
		return findFilterContainingExistsExpr(n.Child, target)
	case *Limit:
		return findFilterContainingExistsExpr(n.Child, target)
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			if f, c := findFilterContainingExistsExpr(tbl, target); f != nil {
				return f, c
			}
		}
	}
	return nil, nil
}
