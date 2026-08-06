package planner

import (
	"os"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/catalog"
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
	indexKeyHarvestOn.Store(indexKeyHarvestFromEnv(os.Getenv("GOOPG_INDEXKEY_HARVEST")))
	// S5a (D3.1): run sublink pull-up BEFORE join-order search so
	// decorrelated semi/anti joins pin above the DP result and their
	// sunk residual conjuncts participate in join search. Default ON;
	// GOOPG_UNNEST_PREDP=off restores the historical post-DP position
	// (field rollback — the legacy call site is kept intact behind
	// this flag).
	unnestPreDPOn.Store(unnestPreDPFromEnv(os.Getenv("GOOPG_UNNEST_PREDP")))
}

// indexKeyHarvestFromEnv / unnestPreDPFromEnv are the two kill-switches'
// polarities, factored out of init so the provenance table (flaglabels.go) can
// render their unset defaults from the same functions production resolves them
// with; see memoizeFromEnv.
func indexKeyHarvestFromEnv(v string) bool { return v != "off" }
func unnestPreDPFromEnv(v string) bool     { return v != "off" }

// unnestPreDPOn gates the S5a pipeline reorder (pull-up before join
// search). See init above and runJoinSearchBelowPinned in predp.go.
var unnestPreDPOn atomic.Bool

// SetUnnestPreDPEnabled flips the S5a pre-DP pull-up position. Test
// hook, mirroring SetIndexKeyHarvestEnabled.
func SetUnnestPreDPEnabled(on bool) { unnestPreDPOn.Store(on) }

// unnestPreDPEnabled reports whether pull-up runs before join search.
func unnestPreDPEnabled() bool { return unnestPreDPOn.Load() }

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
	// S4a (D3.2): decompose via the shared collector — non-equi outer
	// conjuncts become liftable residuals instead of a bail. Scalars
	// keep requiring ≥1 equijoin pair (the GROUP-BY/hash key); a
	// zero-equijoin scalar stays a SubPlan.
	eup := collectUnnestParamsAndResiduals(agg)
	if eup == nil || len(eup.Params) == 0 {
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
				// D3.3: same Level-1-only rule as extractEquijoinPair —
				// a Level-2 key targets the grandparent, not the scope
				// being unnested; harvesting it would join against the
				// wrong schema. Left unharvested it stays unaccounted
				// and the sublink correctly remains a SubPlan.
				if oc.Level > 1 {
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
		case *OrdinalityWrap:
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
// walkPlanExprsDeep is walkPlanExprs with sublink descent (D3.3/S4b):
// every expression is reported together with its sublink-plan depth —
// the number of sublink `.Plan` boundaries between the walk root and
// the expression. A sublink's non-plan children (an IN's Operand and
// literal List) evaluate in the HOST scope and are therefore visited at
// the sublink's own depth; only entering the inner Plan increments it.
//
// The depth is what makes scope-escape analysis possible: an
// OuterColumnRef at planDepth d references, for Level L ≤ d, a scope
// inside the walk root's subtree (still valid if the root is pulled up
// as a join input — the ref's host is unchanged), while L > d reaches
// PAST the root. After a pull-up the outer query's row is no longer on
// the executor's OuterRows stack when the root runs as a join input, so
// such a ref would silently resolve against whatever occupies that
// stack slot — the grandparent-aliasing wrong-results hazard the
// shallow walkers cannot see.
func walkPlanExprsDeep(node Node, planDepth int, visit func(Expr, int)) {
	if node == nil {
		return
	}
	walkPlanExprs(node, func(e Expr) {
		visit(e, planDepth)
		deepVisitSublinkChildren(e, planDepth, visit)
	})
}

// walkExprTreeDeep is walkExprTree with the same sublink descent and
// depth contract as walkPlanExprsDeep.
func walkExprTreeDeep(e Expr, planDepth int, visit func(Expr, int)) {
	walkExprTree(e, func(sub Expr) {
		visit(sub, planDepth)
		deepVisitSublinkChildren(sub, planDepth, visit)
	})
}

// deepVisitSublinkChildren descends into the parts of a sublink node
// the shallow walkers skip: host-scope expression children at the same
// depth, and the inner Plan at depth+1.
func deepVisitSublinkChildren(e Expr, planDepth int, visit func(Expr, int)) {
	switch x := e.(type) {
	case *SubqueryExpr:
		walkPlanExprsDeep(x.Plan, planDepth+1, visit)
	case *ExistsExpr:
		walkPlanExprsDeep(x.Plan, planDepth+1, visit)
	case *InExpr:
		// Operand and List evaluate in the host scope; walkExprTree
		// treats the whole InExpr as a leaf, so without this descent an
		// OuterColumnRef hiding inside the operand is invisible to
		// every analysis built on the shallow walkers.
		walkExprTreeDeep(x.Operand, planDepth, visit)
		for _, le := range x.List {
			walkExprTreeDeep(le, planDepth, visit)
		}
		if x.Plan != nil {
			walkPlanExprsDeep(x.Plan, planDepth+1, visit)
		}
	case *ArraySubqueryExpr:
		walkPlanExprsDeep(x.Plan, planDepth+1, visit)
	case *MultiAssignSubqElem:
		if x.Row != nil && x.Row.Plan != nil {
			walkPlanExprsDeep(x.Row.Plan, planDepth+1, visit)
		}
	}
}

func planHasOuterRefRemaining(node Node) bool {
	found := false
	walkPlanExprs(node, func(e Expr) {
		if _, ok := e.(*OuterColumnRef); ok {
			found = true
		}
	})
	return found
}

// planSubtreeHasOuterRefDeep is planHasOuterRefRemaining with sublink
// descent: it reports whether ANY OuterColumnRef appears in the subtree,
// including inside the inner plan of a nested sublink.
//
// M0125-0041 uses it to decide whether a CTE body may be shared verbatim
// by a decorrelated clone. The shallow walker is not enough there: a
// correlated sublink *inside* the body holds its outer refs below a
// `.Plan` boundary, and sharing such a body under the CTE's name would
// let one consumer's row cache answer another consumer's scan. No
// level/depth arithmetic is attempted — any outer ref at all is a bail,
// which is the safe direction (the sublink stays a SubPlan).
func planSubtreeHasOuterRefDeep(node Node) bool {
	found := false
	walkPlanExprsDeep(node, 0, func(e Expr, _ int) {
		if _, ok := e.(*OuterColumnRef); ok {
			found = true
		}
	})
	return found
}

func extractEquijoinPair(a, b Expr) (*OuterColumnRef, *ColumnRef) {
	// D3.3 (S4b): only a Level-1 ref is a decorrelation key. The unnest
	// joins the sublink against its IMMEDIATE host; a Level-2 ref
	// targets the grandparent, and using it as a join key would bind
	// the key's Index against the wrong schema (reachable via the
	// driver's recursive descent into a body whose nested sublink
	// references the true outer query — the same escaping-ref family
	// the deep accounting check guards, but on the params side).
	if o, ok := a.(*OuterColumnRef); ok && o.Level <= 1 {
		if c, ok := b.(*ColumnRef); ok {
			return o, c
		}
	}
	if o, ok := b.(*OuterColumnRef); ok && o.Level <= 1 {
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
	case *OrdinalityWrap:
		walkPlanExprs(n.Child, visit)
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
	// M0127-P5.9-g: `p.SubCol` and the GROUP BY key it becomes live in two
	// different coordinate spaces, and only an accident of layout ever made
	// them agree.
	//
	// `SubCol` is recorded where the correlation was FOUND. On the Filter
	// path that is the conjunct's own space, which for a top-level Filter is
	// also `agg.Child`'s output — so the two coincide. On the index-key path
	// (`harvestIndexKeyParams`) the correlation is folded into an
	// `*IndexScan` probe, and the index is LEAF-relative: the walk descends
	// through joins and projects without ever accumulating an offset. The
	// group key, by contrast, is evaluated against `agg.Child`'s OUTPUT.
	//
	// Left-deep and unprojected, partsupp is the first relation of TPC-H
	// Q2's subquery body, so leaf-relative `ps_partkey/0` happened to equal
	// its output coordinate and the defect stayed invisible. Under
	// `GOOPG_PGSHAPED_DP` the search boundary publishes a rotated map
	// (P5.9-c): partsupp lands at offset 14 behind region/nation/supplier,
	// `ps_partkey/0` reads `r_regionkey`, and every European row groups
	// under the single key 3. `part.p_partkey = 3` matches nothing and Q2
	// returned 0 rows against 455 — a wrong answer, not an error.
	//
	// Resolve the group key against the schema that will actually be
	// indexed, and bail to the SubPlan path when it cannot be pinned
	// unambiguously (the R3-4 rule the EXISTS path already applies to
	// `SubCol.Index`: an in-range index pointing at the wrong column is the
	// silent case). `replace` keeps the UNRESOLVED ref on purpose — it is
	// substituted where the OuterColumnRef stood, which for an index probe
	// is the leaf space `SubCol` was harvested in.
	childSchema := agg.Child.Output()
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	groupExprs := make([]Expr, len(params))
	for i, p := range params {
		replace[p.OuterRef] = p.SubCol
		gk := resolveSubColInSchema(childSchema, p.SubCol)
		if gk == nil {
			return nil, nil, nil
		}
		groupExprs[i] = gk
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

// resolveSubColInSchema re-expresses a correlation's subquery-scope column
// `sc` as a ColumnRef into `schema` — the schema the CONSUMER will index —
// and returns nil when it cannot be pinned to exactly one column.
//
// M0127-P5.9-g. `sc` carries the coordinate of whichever site the correlation
// was collected from (a Filter conjunct, or an `*IndexScan` probe key, which
// is leaf-relative). Consumers evaluate it somewhere else: the decorrelated
// aggregate's GROUP BY reads `agg.Child`'s output, and the residual rewrite's
// join key reads the unwrapped inner plan's. Those spaces agree only when the
// column's relation happens to sit at the same offset in both, which is why
// this held for years on left-deep unprojected inner bodies and broke the
// moment the PG-shaped search rotated one.
//
// Identity first: when `sc.Index` already names the right column, it is
// returned unchanged, so every path that was already correct keeps its exact
// ColumnRef. Otherwise the column is looked up by name, disambiguated by
// SourceTableIdx when both sides carry one (a self-join puts the same name in
// the schema twice). A nil return is a BAIL, not an error — the caller leaves
// the correlated SubPlan in place, which is slower and right.
func resolveSubColInSchema(schema Schema, sc *ColumnRef) *ColumnRef {
	if sc == nil {
		return nil
	}
	stAgrees := func(col SchemaColumn) bool {
		return sc.SourceTableIdx == 0 || col.SourceTableIdx == 0 ||
			col.SourceTableIdx == sc.SourceTableIdx
	}
	clone := func(idx int) *ColumnRef {
		out := *sc
		out.Index = idx
		if idx >= 0 && idx < len(schema) {
			out.Type = schema[idx].Type
			if schema[idx].SourceTableIdx != 0 {
				out.SourceTableIdx = schema[idx].SourceTableIdx
			}
		}
		return &out
	}
	if sc.Index >= 0 && sc.Index < len(schema) &&
		strings.EqualFold(schema[sc.Index].Name, sc.Name) && stAgrees(schema[sc.Index]) {
		return clone(sc.Index)
	}
	found := -1
	for i, col := range schema {
		if !strings.EqualFold(col.Name, sc.Name) || !stAgrees(col) {
			continue
		}
		if found >= 0 {
			return nil // ambiguous: two candidates, no way to choose
		}
		found = i
	}
	if found < 0 {
		return nil
	}
	return clone(found)
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
			// S4a (ledger csq-S6 row 2): drop replacement-formed
			// tautologies at clone time. A correlation conjunct
			// `inner_col = OuterColumnRef` whose outer side maps to
			// that same inner column becomes `col = col` after
			// replacement — semantically inert here (the enclosing
			// join re-establishes the equality via its key, and a
			// NULL inner key never matches a hash probe anyway), but
			// it survived into Q20's decorrelated plan as visible
			// noise (`l_suppkey = l_suppkey`). Only pairs formed BY
			// the replacement are dropped; a user-written self
			// comparison (an IS NOT NULL idiom) is preserved.
			isReplacementTautology := func(c Expr) bool {
				bin, ok := c.(*BinaryOp)
				if !ok || bin.Op != parser.OpEq {
					return false
				}
				check := func(a, b Expr) bool {
					oc, ok := a.(*OuterColumnRef)
					if !ok {
						return false
					}
					mapped, inMap := replace[oc]
					if !inMap {
						return false
					}
					cr, ok := b.(*ColumnRef)
					return ok && mapped.Index == cr.Index && mapped.Name == cr.Name
				}
				return check(bin.Left, bin.Right) || check(bin.Right, bin.Left)
			}
			conjs := splitAnd(n.Predicate)
			kept := make([]Expr, 0, len(conjs))
			for _, c := range conjs {
				if isReplacementTautology(c) {
					continue
				}
				kept = append(kept, cloneExprReplacingOuter(c, replace))
			}
			if len(kept) == 0 {
				f.Predicate = &BooleanConst{pos: n.pos, Value: true}
			} else {
				f.Predicate = combineAnd(kept)
			}
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
			// M0125-0043: demoting the probe back to a full scan must not
			// change the relation's small-dimension answer — the sibling of
			// the promotions in nl_index_join.go / mhj_input_rewrite.go,
			// which copy the tag in the other direction.
			SmallDim: n.SmallDim,
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
	case *CTEScan:
		// M0125-0041: a correlated scalar sublink whose FROM is a WITH
		// reference — TPC-DS Q30/Q81's `(select avg(ctr_total_return)*1.2
		// from customer_total_return ctr2 where ctr1.ctr_state =
		// ctr2.ctr_state)`. Every earlier gate accepts these (avg is
		// NULL-on-empty, the target list is strict, the conjunct is
		// AND-reachable, one equijoin param, the inner is not
		// probe-cheap); the pull-up died HERE, on the default arm, and
		// the shape silently stayed a per-outer-row SubPlan.
		//
		// The correlation never lives inside the CTE body: WITH is not
		// LATERAL, so the body is a closed query level and the
		// correlation sits in a Filter *above* this node (the shape the
		// probe measured). The body is therefore shared verbatim rather
		// than rewritten — which is also what the CTE machinery already
		// does for two consumers of the same WITH item (planner.go's
		// Stage A inlining hands each consumer the same `ce.body`), and
		// what makes the decorrelated form cheap: the executor's
		// declaration-keyed ctx.CTERowCache materializes the body once and
		// both the outer CTEScan and this one replay it.
		//
		// That shared cache entry is exactly why a body carrying an outer
		// reference must bail instead: a rewritten body and an
		// un-rewritten consumer of the same DECLARATION would share one
		// cache entry, so the second scan would replay the first's rows.
		// (Keyed by declaration since M0125-0050 — same-named declarations
		// in disjoint scopes no longer collide, but two consumers of ONE
		// declaration still share, which is what this guards.)
		// A sublink nested inside the body can hold such a ref, so the
		// check descends through sublink plans.
		if planSubtreeHasOuterRefDeep(n.Child) {
			return nil, &PlanError{Pos: node.Pos(), Code: "XX000", Message: "clonePlanReplacingOuter: CTE body carries an outer reference"}
		}
		c := *n
		return &c, nil
	case *MaterializedCTEScan:
		// Sibling of the CTEScan arm: a data-modifying WITH item's
		// RETURNING rows, already materialized under the CTE name in
		// ctx.MaterializedCTEs before the outer query runs. It is a leaf
		// with no body to rewrite, so the copy is unconditional.
		c := *n
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
	case *SubqueryExpr:
		cl := *x
		cl.Plan = clonePlanVerbatimOrShare(x.Plan)
		return &cl
	case *ExistsExpr:
		cl := *x
		cl.Plan = clonePlanVerbatimOrShare(x.Plan)
		return &cl
	case *InExpr:
		cl := *x
		// The operand and literal list evaluate in the HOST scope (the
		// body being cloned), so they go through the ordinary replacing
		// clone; only the inner plan is copied verbatim.
		cl.Operand = cloneExprReplacingOuter(x.Operand, replace)
		if len(x.List) > 0 {
			cl.List = make([]Expr, len(x.List))
			for i, le := range x.List {
				cl.List[i] = cloneExprReplacingOuter(le, replace)
			}
		}
		cl.Plan = clonePlanVerbatimOrShare(x.Plan)
		return &cl
	case *ArraySubqueryExpr:
		cl := *x
		cl.Plan = clonePlanVerbatimOrShare(x.Plan)
		return &cl
	default:
		return cloneExprLeaf(x)
	}
}

// clonePlanVerbatimOrShare structurally clones a nested sublink's inner
// plan (D3.3/S4b — closes the F7 aliasing trap where clone and original
// shared the nested Plan pointer, so a later in-place pass mutated
// both trees).
//
// The clone is VERBATIM (empty replace map) by invariant: the deep
// escape check in collectUnnestParamsAndResiduals guarantees the nested
// plan contains no reference to the scope being unnested (any
// Level > planDepth ref bails the pull-up first), and its shallower
// refs are body-relative — the body remains their host after pull-up
// with unchanged relative depth, so they copy unchanged. Pointer-keyed
// replacement could not touch them anyway: the replace map holds the
// exact *OuterColumnRef pointers harvested from the body's own
// conjuncts, never pointers from inside a nested plan.
//
// On a clone failure (a node kind clonePlanReplacingOuter does not
// model) the ORIGINAL pointer is returned — the pre-D3.3 sharing
// behaviour, no worse than before. canUnnestExistsExpr prechecks
// clonability (planCloneSupported), making the fallback unreachable on
// the EXISTS pull-up path; other cloning paths degrade to sharing
// rather than failing outright.
func clonePlanVerbatimOrShare(p Node) Node {
	if p == nil {
		return nil
	}
	cl, err := clonePlanReplacingOuter(p, map[*OuterColumnRef]*ColumnRef{})
	if err != nil || cl == nil {
		return p
	}
	return cl
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
	case *SubqueryExpr:
		// D3.3: sublink nodes are NOT leaves — sharing them (the old
		// default-arm behaviour) aliased the nested Plan between clone
		// and original, so a later in-place pass mutated both trees.
		c := *x
		c.Plan = clonePlanVerbatimOrShare(x.Plan)
		return &c
	case *ExistsExpr:
		c := *x
		c.Plan = clonePlanVerbatimOrShare(x.Plan)
		return &c
	case *InExpr:
		c := *x
		c.Operand = cloneExprReplacingOuter(x.Operand, map[*OuterColumnRef]*ColumnRef{})
		if len(x.List) > 0 {
			c.List = make([]Expr, len(x.List))
			for i, le := range x.List {
				c.List[i] = cloneExprReplacingOuter(le, map[*OuterColumnRef]*ColumnRef{})
			}
		}
		c.Plan = clonePlanVerbatimOrShare(x.Plan)
		return &c
	case *ArraySubqueryExpr:
		c := *x
		c.Plan = clonePlanVerbatimOrShare(x.Plan)
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
	agg := plan.(*Aggregate)
	eup := collectUnnestParamsAndResiduals(agg)
	if eup == nil || len(eup.Params) == 0 {
		return nil, nil
	}
	// S4a (D3.2): a scalar with non-equi residual correlation (matrix
	// M16: `t1.b >= (SELECT min(y.b) FROM y WHERE y.a = t1.a AND
	// y.b <= t1.b)`) cannot ride the GROUP-BY-inner rewrite — the
	// residual references a pre-aggregation inner column against an
	// outer value, which no finite inner grouping expresses. It takes
	// the aggregate-above-join form instead.
	if len(eup.Residuals) > 0 {
		return unnestScalarWithResiduals(sub, outer, agg, eup)
	}
	params := eup.Params
	subPlan, subSchema, err := buildUnnestedSubquery(sub, params)
	if err != nil {
		return nil, err
	}
	// M0127-P5.9-g: a nil plan with no error is the group-key resolution
	// bailing (resolveSubColInSchema) — keep the correlated SubPlan.
	if subPlan == nil {
		return nil, nil
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
		//
		// M0127-P5.9-f: RightKey is `innerKeyExprs[0]`'s coordinate —
		// `outerWidth`, the group-by column's position in the MERGED
		// schema — not the inner-relative `0` it used to carry. The
		// executor evaluates both join keys against a merged slot
		// (`mergedKeySlot`, operators_join_agg.go), so `0` addressed the
		// OUTER side's first column: TPC-H Q17 hashed `part.p_partkey`
		// against `lineitem.l_orderkey` and the join matched nothing.
		// It was invisible because `reresolveJoinByName`
		// (applyJoinTreePosMap, bushy.go) rebinds join keys by NAME and
		// silently repaired it on every path that reached it — an
		// undeclared dependency on a later pass. P5.9-f's `*Aggregate`
		// opacity fix makes `buildBindingsPosMap` decline on this shape,
		// so that pass no longer runs and the latent index surfaced.
		// A separate ColumnRef (not the `innerKeyExprs[0]` pointer) so
		// key and Predicate stay independently rewritable, matching the
		// LeftKey line above.
		LeftKey: outerKeyExprs[0],
		RightKey: &ColumnRef{
			pos:            sub.Pos(),
			Index:          outerWidth,
			Name:           params[0].SubCol.Name,
			Type:           params[0].SubCol.Type,
			SourceTableIdx: params[0].SubCol.SourceTableIdx,
		},
		schema: mergedSchema,
	}
	filter.Child = join
	return outer, nil
}

// shiftExprColumnIdx deep-clones e with every ColumnRef.Index shifted
// by delta. Used by the aggregate-above-join scalar rewrite to move
// aggregate-argument references from inner-relative coordinates into
// the joined (left ++ inner) row. OuterColumnRefs cannot appear here
// (the collector's all-accounted guarantee); anything unmodelled is
// returned as-is, which is safe for ref-free leaves only — callers
// must have validated the expression via residualExprLiftable-class
// checks or the aggregate-arg guarantee.
func shiftExprColumnIdx(e Expr, delta int) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *ColumnRef:
		cl := *x
		cl.Index += delta
		return &cl
	case *BinaryOp:
		return &BinaryOp{pos: x.Pos(), Op: x.Op, Left: shiftExprColumnIdx(x.Left, delta), Right: shiftExprColumnIdx(x.Right, delta)}
	case *UnaryOp:
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: shiftExprColumnIdx(x.Operand, delta)}
	case *FuncCall:
		args := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			args[i] = shiftExprColumnIdx(a, delta)
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name, Args: args, Star: x.Star}
	case *CastExpr:
		cl := *x
		cl.Operand = shiftExprColumnIdx(x.Operand, delta)
		return &cl
	default:
		return e
	}
}

// unnestScalarWithResiduals decorrelates a scalar sublink whose
// correlation carries non-equi residuals (S4a / D3.2, matrix M16/M17).
// The GROUP-BY-inner rewrite cannot express a residual like
// `y.b <= t1.b` (it compares a pre-aggregation inner column against an
// outer value), so the aggregate moves ABOVE the join:
//
//	Filter(orig comparison with sub → aggcol)(
//	  Aggregate{GROUP BY outer cols ++ __unnest_ord; agg(shifted arg)}(
//	    Join{INNER, hash}(OrdinalityWrap(outer), raw inner clone)
//	      ON equijoins AND residuals ))
//
// The OrdinalityWrap appends a per-row ordinal to the outer side and
// that ordinal joins the GROUP BY key, so two fully-duplicate outer
// rows keep their multiplicity (matrix M17) — grouping by the outer
// columns alone would collapse them. The ordinal and aggregate columns
// sit AFTER the preserved outer-column prefix, so downstream column
// indices are unchanged, the same convention as the GROUP-BY-inner
// rewrite's merged schema.
//
// Legality is exactly D3.4's: the INNER join drops outer rows with an
// empty correlation group, which matches SubPlan semantics only for
// NULL-on-empty aggregates under a comparison — enforced by
// canUnnestSubquery before this function is reached.
func unnestScalarWithResiduals(sub *SubqueryExpr, outer Node, agg *Aggregate, eup *existsUnnestPlan) (Node, error) {
	params := eup.Params

	filter, conjunct := findFilterContainingSubquery(outer, sub)
	if filter == nil {
		return nil, nil
	}
	// Same S1a guard as the base rewrite: the sublink must be applied
	// unconditionally to every row of its conjunct.
	if !subqueryANDReachable(conjunct, sub) {
		return nil, nil
	}

	// Clone the RAW inner (the aggregate's input): equijoin refs are
	// neutralised via the replace map; residual conjuncts keep their
	// OuterColumnRefs and are stripped below (they re-appear on the
	// join predicate).
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	for _, p := range params {
		replace[p.OuterRef] = p.SubCol
	}
	innerRaw, err := clonePlanReplacingOuter(agg.Child, replace)
	if err != nil {
		return nil, err
	}
	stripOuterRefConjuncts(innerRaw)
	innerRaw = unnestSubqueriesInPlan(innerRaw)
	innerRaw = unwrapTrivialWrappers(innerRaw)
	if planHasOuterRefRemaining(innerRaw) {
		return nil, nil
	}

	outerChild := filter.Child
	outerSchema := outerChild.Output()
	outerWidth := len(outerSchema)
	innerSchema := innerRaw.Output()

	// M0127-P5.9-g (sibling of the GROUP-BY fix in buildUnnestedSubquery):
	// this rewrite indexes `SubCol` into `innerSchema` at `leftWidth + idx`
	// twice below — the per-pair join conjunct and the hash RightKey. That
	// schema is the inner body AFTER clonePlanReplacingOuter,
	// unnestSubqueriesInPlan and unwrapTrivialWrappers have all had a turn,
	// so it is even further from the space an index-probe harvest recorded
	// than the GROUP-BY case. Resolve once, up front, and bail together.
	subCols := make([]*ColumnRef, len(params))
	for i, p := range params {
		if subCols[i] = resolveSubColInSchema(innerSchema, p.SubCol); subCols[i] == nil {
			return nil, nil
		}
	}

	// Ordinal-tagged outer side (reuses the WITH ORDINALITY plan node
	// and its reopen-safe executor).
	ordSchema := make(Schema, 0, outerWidth+1)
	ordSchema = append(ordSchema, outerSchema...)
	ordSchema = append(ordSchema, SchemaColumn{Name: "__unnest_ord", Type: catalog.Type{Name: "int8"}})
	tagged := &OrdinalityWrap{
		pos:        sub.Pos(),
		Child:      outerChild,
		OrdColName: "__unnest_ord",
		schema:     ordSchema,
	}
	leftWidth := outerWidth + 1

	// Join predicate: every equijoin pair plus the lifted residuals,
	// all in joined-row (left ++ inner) coordinates.
	var conj []Expr
	for i, p := range params {
		conj = append(conj, &BinaryOp{
			pos: sub.Pos(), Op: parser.OpEq,
			Left: &ColumnRef{
				pos:            p.OuterRef.Pos(),
				Index:          p.OuterRef.Index,
				Name:           p.OuterRef.Name,
				Type:           p.OuterRef.Type,
				SourceTableIdx: p.OuterRef.SourceTableIdx,
			},
			Right: &ColumnRef{
				pos:            p.SubCol.Pos(),
				Index:          leftWidth + subCols[i].Index,
				Name:           subCols[i].Name,
				Type:           subCols[i].Type,
				SourceTableIdx: subCols[i].SourceTableIdx,
			},
		})
	}
	joinPred := combineAnd(conj)
	if resid := liftResidualConjuncts(eup.Residuals, nil, outerSchema, leftWidth); resid != nil {
		joinPred = &BinaryOp{pos: sub.Pos(), Op: parser.OpAnd, Left: joinPred, Right: resid}
	}

	joinSchema := make(Schema, 0, leftWidth+len(innerSchema))
	joinSchema = append(joinSchema, ordSchema...)
	joinSchema = append(joinSchema, innerSchema...)
	join := &Join{
		pos:       sub.Pos(),
		Type:      JoinTypeInner,
		Algo:      JoinAlgoHash,
		Left:      tagged,
		Right:     innerRaw,
		Predicate: joinPred,
		LeftKey: &ColumnRef{
			pos:            params[0].OuterRef.Pos(),
			Index:          params[0].OuterRef.Index,
			Name:           params[0].OuterRef.Name,
			Type:           params[0].OuterRef.Type,
			SourceTableIdx: params[0].OuterRef.SourceTableIdx,
		},
		// Merged-row coordinate: the lazy hash path evaluates BOTH
		// keys against a (leftWidth + rightWidth) padded row, with
		// the build/probe row copied into its own region — so the
		// right key must index into the right region.
		RightKey: &ColumnRef{
			pos:   params[0].SubCol.Pos(),
			Index: leftWidth + subCols[0].Index,
			Name:  subCols[0].Name,
			Type:  subCols[0].Type,
		},
		schema: joinSchema,
	}

	// Aggregate above the join: group by the preserved outer prefix
	// plus the ordinal; the aggregate argument shifts into joined-row
	// coordinates.
	groupExprs := make([]Expr, 0, leftWidth)
	for i, sc := range outerSchema {
		groupExprs = append(groupExprs, &ColumnRef{
			pos:            sub.Pos(),
			Index:          i,
			Name:           sc.Name,
			Type:           sc.Type,
			SourceTableIdx: sc.SourceTableIdx,
		})
	}
	groupExprs = append(groupExprs, &ColumnRef{
		pos:   sub.Pos(),
		Index: outerWidth,
		Name:  "__unnest_ord",
		Type:  catalog.Type{Name: "int8"},
	})
	call := cloneAggregateCall(agg.Aggs[0])
	if call.Arg != nil {
		call.Arg = shiftExprColumnIdx(call.Arg, leftWidth)
	}
	if call.Arg2 != nil {
		call.Arg2 = shiftExprColumnIdx(call.Arg2, leftWidth)
	}
	for i, ea := range call.ExtraArgs {
		call.ExtraArgs[i] = shiftExprColumnIdx(ea, leftWidth)
	}
	aggSchema := make(Schema, 0, leftWidth+1)
	aggSchema = append(aggSchema, ordSchema...)
	aggSchema = append(aggSchema, SchemaColumn{Name: agg.Aggs[0].Name, Type: agg.Aggs[0].Type})
	newAgg := &Aggregate{
		pos:        sub.Pos(),
		Child:      join,
		GroupExprs: groupExprs,
		Aggs:       []AggregateCall{call},
		schema:     aggSchema,
	}

	// Substitute the sublink in its conjunct with the aggregate
	// output column (via the Project target when one wraps the
	// aggregate — Q20's `0.5 * sum` lesson, M0071-0002-followup).
	aggColRef := &ColumnRef{
		pos:   sub.Pos(),
		Index: leftWidth,
		Name:  agg.Aggs[0].Name,
		Type:  agg.Aggs[0].Type,
	}
	var replacement Expr = aggColRef
	if proj, ok := sub.Plan.(*Project); ok && len(proj.Targets) == 1 {
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
	filter.Child = newAgg
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
	// S4a (D3.2): decompose the correlation into equijoin params plus
	// liftable residuals via the shared collector (the EXISTS
	// mechanism). A fully-unaccounted correlation still bails.
	eup := collectUnnestParamsAndResiduals(plan)
	if eup == nil {
		return false
	}
	// Correlated NOT IN is deliberately NOT extended to residual
	// shapes: its unnesting is only proven sound for the exact
	// operand-safe single-equijoin form below (the equality
	// correlation excludes NULLs, making the plain Anti join
	// correct). A residual reintroduces general three-valued
	// semantics per correlation group — that stays a SubPlan
	// (M0122-0011 deferral ledger).
	if in.Negated && len(eup.Residuals) > 0 {
		return false
	}
	if len(eup.Params) == 0 {
		// S4a zero-equijoin form: the IN's own operand equality
		// (`operand = projected value`) supplies the hash key, so —
		// unlike EXISTS — this stays a HASH semi join; the residuals
		// ride on the join predicate. NOT IN excluded above.
		if in.Negated || len(eup.Residuals) == 0 {
			return false
		}
		if !isUnnestableNonCorrelatedIn(in) {
			return false
		}
	} else {
		// unnestInExpr keys the resulting join on the correlation
		// pair (params[0]), never on in.Operand directly — that only
		// encodes the same predicate as the original `operand IN
		// (subquery)` when the correlation identifies exactly the
		// value being tested. See correlatedInOperandSafeToUnnest's
		// doc comment for the two required conditions and why a
		// mismatch would silently change the query's meaning, not
		// just miss an optimization.
		if !correlatedInOperandSafeToUnnest(in, eup.Params) {
			return false
		}
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
	eup := collectUnnestParamsAndResiduals(in.Plan)
	if eup == nil || (len(eup.Params) == 0 && len(eup.Residuals) == 0) {
		return nil, nil
	}
	params := eup.Params
	// Replace OuterColumnRefs in the inner plan with their
	// corresponding ColumnRefs so the inner plan is self-contained.
	// Residual refs are NOT in the map — clonePlanReplacingOuter
	// leaves them as OuterColumnRefs and stripOuterRefConjuncts
	// removes their conjuncts below (they are re-established on the
	// join predicate by liftResidualConjuncts).
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	for _, p := range params {
		replace[p.OuterRef] = p.SubCol
	}
	innerPlan, err := clonePlanReplacingOuter(in.Plan, replace)
	if err != nil {
		return nil, err
	}
	if len(eup.Residuals) > 0 {
		stripOuterRefConjuncts(innerPlan)
	}
	// Recursively unnest any scalar subqueries still inside the
	// inner plan (e.g. Q20's lineitem aggregate inside the
	// partsupp IN subquery).
	innerPlan = unnestSubqueriesInPlan(innerPlan)
	// S4a belt: a surviving OuterColumnRef means the decomposition
	// was incomplete — never build a join over an unbound reference.
	if planHasOuterRefRemaining(innerPlan) {
		return nil, nil
	}

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

	// Build semi-join keys. With ≥1 correlation pair the join is keyed
	// on params[0] (operand safety checked in canUnnestInExprDepth).
	// S4a zero-equijoin form: the IN's own operand equality supplies
	// the key — LeftKey is the operand expression itself (general
	// exprs allowed per M0122-0011), RightKey the inner output column.
	var outerKeyExpr Expr
	if len(params) > 0 {
		outerKeyExpr = &ColumnRef{
			pos:            params[0].OuterRef.Pos(),
			Index:          params[0].OuterRef.Index,
			Name:           params[0].OuterRef.Name,
			Type:           params[0].OuterRef.Type,
			SourceTableIdx: params[0].OuterRef.SourceTableIdx,
		}
	} else {
		outerKeyExpr = in.Operand
	}
	// innerKey.Index is the position of the inner plan's output column in the
	// merged (outer ++ inner) schema. innerKey.Name MUST match the inner plan's
	// actual output column name — NOT params[0].SubCol.Name (the equijoin column).
	// reresolveJoinByName.predRebind re-binds keys by Name: if innerKey.Name is
	// the equijoin column ("f1") but the inner plan projects a different column
	// ("f2"), predRebind won't find "f1" on the right side and falls back to the
	// left, silently setting innerKey.Index to the outer "f1" position (0), which
	// corrupts the hash-join key and produces 0 matches.
	innerOutName := ""
	innerOutType := catalog.Type{}
	innerPos := in.Pos()
	if len(params) > 0 {
		innerOutName = params[0].SubCol.Name
		innerOutType = params[0].SubCol.Type
		innerPos = params[0].SubCol.Pos()
	}
	if out := innerPlan.Output(); len(out) > 0 {
		innerOutName = out[0].Name
		if len(params) == 0 {
			innerOutType = out[0].Type
		}
	}
	innerKey := &ColumnRef{
		pos:            innerPos,
		Index:          outerWidth,
		Name:           innerOutName,
		Type:           innerOutType,
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

	var semiPred Expr = &BinaryOp{pos: in.Pos(), Op: parser.OpEq, Left: outerKeyExpr, Right: innerKey}
	// S4a (D3.2): AND the lifted residual conjuncts onto the join
	// predicate, exactly the EXISTS mechanism (shared rewriter).
	if resid := liftResidualConjuncts(eup.Residuals, nil, outerChild.Output(), outerWidth); resid != nil {
		semiPred = &BinaryOp{pos: in.Pos(), Op: parser.OpAnd, Left: semiPred, Right: resid}
	}
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
		LeftKey:   outerKeyExpr,
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
	case *OrdinalityWrap:
		stripOuterRefConjuncts(n.Child)
	}
}

// existsUnnestPlan describes how a sublink's correlation predicate
// decomposes for unnesting:
//   - Params: equijoin pairs that become the join's hash key(s).
//   - Residuals: the original (uncloned) BinaryOp / general
//     conjunct expressions that reference an OuterColumnRef but
//     are NOT a pure column-equijoin pair. Examples: `<>`,
//     range comparisons, function calls. After unnesting these
//     are lifted onto the join's `Predicate` with both inner
//     ColumnRefs shifted by the left side's width and
//     OuterColumnRefs rewritten to ColumnRefs at their outer-side
//     index (see liftResidualConjuncts).
//
// (M0062-0005; generalized to all three sublink kinds in S4a/D3.2.)
type existsUnnestPlan struct {
	Params    []unnestParam
	Residuals []Expr
}

// collectUnnestParamsAndResiduals splits the inner plan's
// top-level conjuncts (anywhere a Filter sits) into:
//   - equijoin params (existing collectUnnestParams logic), and
//   - non-equi residuals that reference an OuterColumnRef.
//
// Returns nil if the plan has any OuterColumnRef that does NOT
// appear in either bucket — those would still be correlated after
// the lift, which we cannot handle.
//
// residualExprLiftable reports whether a residual conjunct is built
// entirely from expression kinds liftResidualConjuncts knows how to
// index-rewrite. Anything else (CaseExpr, nested sublinks, row
// constructors, ...) would pass through the rewriter's default arm
// UNREWRITTEN and evaluate with stale column indices on the joined
// row — a silent-wrong-results class. Such conjuncts make the whole
// sublink non-liftable (the collector reports the refs unaccounted
// and the sublink stays a SubPlan).
//
// Kept in lockstep with liftResidualConjuncts' rewriteIdx switch —
// the two are the sibling paths of one contract.
func residualExprLiftable(e Expr) bool {
	if e == nil {
		return true
	}
	switch x := e.(type) {
	case *OuterColumnRef, *ColumnRef:
		return true
	case *IntegerConst, *NumericConst, *StringConst, *BooleanConst,
		*NullConst, *TypedStringLit, *IntervalLit:
		return true
	case *BinaryOp:
		return residualExprLiftable(x.Left) && residualExprLiftable(x.Right)
	case *UnaryOp:
		return residualExprLiftable(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if !residualExprLiftable(a) {
				return false
			}
		}
		return true
	}
	return false
}

// S4a (D3.2): this is the shared collector for EXISTS, IN and
// scalar sublinks. Historically only the EXISTS loop lifted
// residuals (the IN/scalar loops used collectUnnestParams, which
// bails on ANY non-equijoin outer ref); the residual mechanism is
// one and the same for all three — the difference between the
// loops is only how the resulting join is keyed and typed.
func collectUnnestParamsAndResiduals(node Node) *existsUnnestPlan {
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
				walkExprTree(c, func(e Expr) {
					if oc, ok := e.(*OuterColumnRef); ok {
						refs = append(refs, oc)
					}
				})
				// S4a: only record the conjunct as a residual when (a)
				// the index rewriter can handle every node in it
				// (residualExprLiftable) and (b) every ref targets the
				// IMMEDIATE outer scope. A Level>1 ref belongs to the
				// grandparent query; rewriting it against the immediate
				// outer schema would silently read the wrong scope —
				// exactly the hazard TestLowerTwoLevelForwarding's
				// fixture encodes. Such refs stay unaccounted and the
				// sublink remains a SubPlan.
				levelOK := true
				for _, oc := range refs {
					if oc.Level > 1 {
						levelOK = false
					}
				}
				if len(refs) > 0 && levelOK && residualExprLiftable(c) {
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

	// Verify every OuterColumnRef is accounted for — DEEPLY (D3.3/S4b).
	//
	// planDepth 0 refs are the body's own correlation and must be an
	// equijoin param or a lifted residual. This now includes refs hiding
	// inside a nested sublink's Operand (host-scope position the shallow
	// walker treats as a leaf): they can never be collected as params or
	// residuals, so they fail the check and the sublink stays a SubPlan —
	// previously, on the IN/scalar paths (which never had the EXISTS
	// blanket nested-sublink bail), such refs slipped through unaccounted
	// and the pull-up proceeded over a dangling reference.
	//
	// planDepth ≥ 1 refs live inside nested sublink plans. Level ≤
	// planDepth targets a scope inside the body's own subtree — the
	// nested sublink remains a SubPlan whose eval site pushes its host
	// row, so the ref stays valid after pull-up with unchanged relative
	// depth. Level > planDepth reaches PAST the body: after pull-up the
	// outer query's row is no longer on the OuterRows stack when the body
	// runs as a join input, and the ref would silently resolve against
	// whatever occupies that slot (wrong-scope aliasing, no error). Bail.
	allAccounted := true
	walkPlanExprsDeep(node, 0, func(e Expr, planDepth int) {
		o, ok := e.(*OuterColumnRef)
		if !ok {
			return
		}
		if planDepth == 0 {
			if !outerInEquijoin[o] && !outerInResidual[o] {
				allAccounted = false
			}
			return
		}
		if o.Level > planDepth {
			allAccounted = false
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
	eup := collectUnnestParamsAndResiduals(plan)
	if eup == nil {
		return false
	}
	// S4a (D3.2): ≥1 equijoin drives a hash semi/anti; zero equijoins
	// with liftable residuals drives a nested-loop semi/anti (M14).
	// Only a fully-unaccounted correlation (eup == nil above) or an
	// empty decomposition bails.
	if len(eup.Params) == 0 && len(eup.Residuals) == 0 {
		return false
	}
	// D6.3a: SubPlan-vs-NL-semi for the zero-equijoin shape — resolved
	// analytically in the NL semi's favor, so no cost consultation is
	// performed. The NL semi materialises the body WITHOUT its lifted
	// residuals exactly once and re-scans that set per outer row; the
	// SubPlan path re-runs the body's driving scan per call. Under the
	// estimateSubplanCostPerCall model those are the same quantity
	// (per-call scan work ≥ materialised output for every shape class:
	// a SeqScan body scans the table either way; an index-probe body's
	// probe yields the same match set the NL re-scans), so the SubPlan
	// can never be estimated cheaper — its only real advantage is the
	// S2 handles' per-param-value result caching, which this
	// deliberately rough model does not chase (ch.06 §4: precision
	// beyond ordering-safety is a non-goal). The keep-SubPlan branch
	// sketched in the D6.3a plan is therefore intentionally absent;
	// TestZeroEquijoinPrefersNLSemi pins the behavior.
	// D3.3 (S4b): nested sublinks inside the EXISTS body no longer bail
	// wholesale. The deep escape check inside
	// collectUnnestParamsAndResiduals (above) guarantees no ref inside a
	// nested sublink reaches past the body (Level > planDepth bails), so
	// a nested sublink rides into the semi/anti build side as an ordinary
	// SubPlan: its Level-1 refs keep targeting the body, which remains
	// its host after pull-up with unchanged relative depth, and
	// unnestExistsExpr's recursive unnestSubqueriesInPlan(innerPlan) call
	// optimises it in place.
	//
	// What must still bail is a nested sublink whose inner plan the
	// verbatim cloner cannot copy: cloneExprReplacingOuter would fall
	// back to SHARING the plan pointer (the F7 aliasing trap this stage
	// closes), so refuse the pull-up instead — the SubPlan path is
	// always correct.
	clonable := true
	walkPlanExprsDeep(plan, 0, func(e Expr, planDepth int) {
		if !clonable || planDepth != 0 {
			return
		}
		switch s := e.(type) {
		case *InExpr:
			if s.Plan != nil && !planCloneSupported(s.Plan) {
				clonable = false
			}
		case *ExistsExpr:
			if s.Plan != nil && !planCloneSupported(s.Plan) {
				clonable = false
			}
		case *SubqueryExpr:
			if s.Plan != nil && !planCloneSupported(s.Plan) {
				clonable = false
			}
		case *ArraySubqueryExpr:
			if s.Plan != nil && !planCloneSupported(s.Plan) {
				clonable = false
			}
		case *MultiAssignSubqElem:
			// UPDATE-only machinery with a deliberately SHARED row
			// node; not expected inside an EXISTS body, and the
			// verbatim cloner does not model its sharing. Bail.
			clonable = false
		}
	})
	return clonable
}

// planCloneSupported reports whether clonePlanReplacingOuter can
// structurally clone every node of the plan, including the plans of any
// nested sublinks (recursively). Used by canUnnestExistsExpr before
// lifting a body that carries nested sublinks: an unclonable nested
// plan would silently degrade to pointer sharing inside
// clonePlanVerbatimOrShare, resurrecting the aliasing hazard.
func planCloneSupported(node Node) bool {
	ok := true
	var walk func(Node)
	walk = func(n Node) {
		if n == nil || !ok {
			return
		}
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
		case *SeqScan:
		case *IndexScan:
		case *MultiHashJoin:
			for _, t := range x.Tables {
				walk(t)
			}
		case *Values:
		case *CTEScan:
			// M0125-0041 sibling of clonePlanReplacingOuter's CTEScan
			// arm, and it must agree with it: the body is shared
			// verbatim (so it is NOT walked as clonable structure), and
			// only a body carrying an outer reference is unclonable.
			if planSubtreeHasOuterRefDeep(x.Child) {
				ok = false
			}
		case *MaterializedCTEScan:
		default:
			ok = false
		}
	}
	walk(node)
	if !ok {
		return false
	}
	nestedOK := true
	walkPlanExprs(node, func(e Expr) {
		if !nestedOK {
			return
		}
		switch s := e.(type) {
		case *InExpr:
			if s.Plan != nil && !planCloneSupported(s.Plan) {
				nestedOK = false
			}
		case *ExistsExpr:
			if s.Plan != nil && !planCloneSupported(s.Plan) {
				nestedOK = false
			}
		case *SubqueryExpr:
			if s.Plan != nil && !planCloneSupported(s.Plan) {
				nestedOK = false
			}
		case *ArraySubqueryExpr:
			if s.Plan != nil && !planCloneSupported(s.Plan) {
				nestedOK = false
			}
		case *MultiAssignSubqElem:
			nestedOK = false
		}
	})
	return nestedOK
}

// resolveOuterSchemaIdx re-resolves an outer-side column reference by
// Name (and, when known, SourceTableIdx) against the actual outer
// schema. The binder set Index against FROM-cumulative offsets at parse
// time; bushy / MHJ / NLI rewrites may have reordered the runtime row
// layout (M0071-0003/-0009 — Q21's three lineitem aliases). Factored out
// of unnestExistsExpr in S4a so the IN and scalar residual lifts share
// the exact same resolution rules (sibling-path discipline).
func resolveOuterSchemaIdx(outerSchema Schema, name string, fallback int, sourceTableIdx int16) int {
	if fallback >= 0 && fallback < len(outerSchema) && outerSchema[fallback].Name == name {
		if sourceTableIdx == 0 || outerSchema[fallback].SourceTableIdx == sourceTableIdx {
			return fallback
		}
	}
	if sourceTableIdx != 0 {
		for i, c := range outerSchema {
			if c.Name == name && c.SourceTableIdx == sourceTableIdx {
				return i
			}
		}
	}
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

// liftResidualConjuncts rewrites lifted residual conjuncts (plus any
// inner-only lifted conjuncts) for evaluation against the joined
// (left ++ inner) row: an OuterColumnRef becomes a ColumnRef resolved
// against outerSchema (outer columns form the left prefix, indices
// unchanged), and an inner ColumnRef is shifted by innerShift (the
// left side's total width). Returns nil when there is nothing to lift.
func liftResidualConjuncts(residuals, innerOnly []Expr, outerSchema Schema, innerShift int) Expr {
	if len(residuals) == 0 && len(innerOnly) == 0 {
		return nil
	}
	var rewriteIdx func(Expr) Expr
	rewriteIdx = func(e Expr) Expr {
		if e == nil {
			return nil
		}
		switch x := e.(type) {
		case *OuterColumnRef:
			return &ColumnRef{
				pos:            x.Pos(),
				Index:          resolveOuterSchemaIdx(outerSchema, x.Name, x.Index, x.SourceTableIdx),
				Name:           x.Name,
				Type:           x.Type,
				SourceTableIdx: x.SourceTableIdx,
			}
		case *ColumnRef:
			cl := *x
			cl.Index = innerShift + x.Index
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
	var conj []Expr
	for _, r := range residuals {
		conj = append(conj, rewriteIdx(r))
	}
	for _, r := range innerOnly {
		conj = append(conj, rewriteIdx(r))
	}
	return combineAnd(conj)
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
	eup := collectUnnestParamsAndResiduals(ex.Plan)
	if eup == nil {
		return nil, nil
	}
	// S4a (D3.2): zero equijoin pairs with liftable residuals is now
	// accepted — a hash join is impossible (no key), so the result is a
	// nested-loop semi/anti join whose predicate carries the residuals
	// (matrix M14: `EXISTS (SELECT 1 FROM y WHERE y.b > t1.b)`). This is
	// O(N·M) exactly like the SubPlan it replaces, minus the per-call
	// operator lifecycle; D6.3a's cost gate later arbitrates SubPlan vs
	// NL semi where it matters.
	if len(eup.Params) == 0 && len(eup.Residuals) == 0 {
		return nil, nil
	}
	params := eup.Params
	// R3-4: composite (multi-equijoin) EXISTS decorrelates. params[0]
	// becomes the hash key as before; params[1:] ride as ordinary equi
	// conjuncts on the join predicate, which is exactly how the scalar
	// path has always handled its extra pairs and what the executor
	// already enforces — the lazy hash semi/anti re-evaluates the FULL
	// plan.Predicate against every bucket match, so an equi residual is
	// checked per candidate just like Q21's non-equi `<>`.
	//
	// This supersedes the S1c bail. That bail was correct-but-blunt: the
	// pre-S1c code used only params[0] as the key and SILENTLY DROPPED
	// the rest (over-matching), so refusing the pull-up was the right
	// emergency fix. Its stated fear — that the downstream NLI rewrite
	// might extract an extra pair as a competing probe key and lose the
	// first — is handled rather than avoided: collectCrossSideEquiKeys
	// harvests LeftKey/RightKey AND the predicate conjuncts together, so
	// a covering composite index consumes all pairs, and any pair the
	// index does not cover stays on the predicate and is re-checked by
	// the executor. Pinned by the indexed/unindexed pair of tests.
	//
	// The extra conjuncts are synthesised in the PRE-REWRITE coordinate
	// space (OuterColumnRef + inner-local ColumnRef) and handed to
	// liftResidualConjuncts alongside the real residuals, so the merged
	// coordinate mapping lives in exactly one place. Hand-building
	// merged indices here would duplicate that mapping — and the two
	// spaces differ (predicate refs are outer++inner, RightKey is
	// inner-child-local), which is precisely where a copy would drift.
	// (guarded: the M14 zero-equijoin shape has no params at all, and
	// slicing an empty slice from index 1 panics)
	var extraPairConjuncts []Expr
	for _, p := range params[min(1, len(params)):] {
		extraPairConjuncts = append(extraPairConjuncts, &BinaryOp{
			pos:   p.OuterRef.Pos(),
			Op:    parser.OpEq,
			Left:  p.OuterRef,
			Right: p.SubCol,
		})
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
		// For EXISTS inner plans the equijoin key is always a column from
		// the scan (SubCol.Index indexes the scan schema, not the project
		// output). Strip the project so the hash sees the scan row.
		innerPlan = proj.Child
		// Unwrap any Filter(true) that might now be at the top.
		innerPlan = unwrapTrivialWrappers(innerPlan)
	}
	// R3-4: validate that every correlation column actually resolves in
	// the inner plan's schema. The comment above this strip used to claim
	// a "check whether the key index is accessible" that was never
	// implemented, so a mismatch would surface as a wrong-column read at
	// runtime rather than a bail. It matters more now that params[1:] are
	// consumed too: with one key a bad index was mostly a self-join
	// aliasing accident, but every extra pair is another chance to index
	// past a schema the strip reshaped. Name-check as well as bounds-check
	// — an in-range index pointing at the wrong column is the silent case.
	innerSchema := innerPlan.Output()
	for _, p := range params {
		if p.SubCol.Index < 0 || p.SubCol.Index >= len(innerSchema) ||
			!strings.EqualFold(innerSchema[p.SubCol.Index].Name, p.SubCol.Name) {
			return nil, nil
		}
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

	// Belt (checked BEFORE any tree mutation below): a keyless
	// semi/anti needs at least one residual to serve as its join
	// predicate — an unconditional cross semi must never be built.
	if len(params) == 0 && len(eup.Residuals) == 0 {
		return nil, nil
	}

	// Build keys. The probe (outer) key uses the outer column ref
	// at its original index; the build (inner) key uses the inner
	// ColumnRef in MERGED outer++inner coordinates — see the shift
	// applied to innerKey below. (An earlier version of this comment
	// claimed the opposite, "the inner plan's own schema, NOT a merged
	// schema"; that was stale and contradicted the code two lines down.
	// Corrected in R3-4, whose composite pairs made the convention
	// load-bearing in a second place. Note the scalar multi-param
	// template DOES use inner-child-local RightKey indices, so the two
	// paths must not be copied into one another.)
	//
	// S4a (D3.2): with zero equijoin pairs there is no hash key —
	// the join runs as a nested-loop semi/anti whose Predicate
	// carries the lifted residuals (matrix M14); both keys stay nil.
	var outerKey, innerKey *ColumnRef
	if len(params) > 0 {
		outerKey = &ColumnRef{
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
		innerKey = &ColumnRef{
			pos:            params[0].SubCol.Pos(),
			Index:          outerWidth + params[0].SubCol.Index,
			Name:           params[0].SubCol.Name,
			Type:           params[0].SubCol.Type,
			SourceTableIdx: params[0].SubCol.SourceTableIdx,
		}
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
	// S4a: index rewriting for lifted residuals is shared with the IN and
	// scalar paths — see liftResidualConjuncts / resolveOuterSchemaIdx.
	// R3-4: params[1:] join the residual set here, so they pass through
	// the same OuterColumnRef->outer-index / inner-ColumnRef->+outerWidth
	// rewrite as every other lifted conjunct. Copy rather than append in
	// place: eup.Residuals belongs to the caller's collector result.
	residualsWithPairs := eup.Residuals
	if len(extraPairConjuncts) > 0 {
		residualsWithPairs = make([]Expr, 0, len(eup.Residuals)+len(extraPairConjuncts))
		residualsWithPairs = append(residualsWithPairs, eup.Residuals...)
		residualsWithPairs = append(residualsWithPairs, extraPairConjuncts...)
	}
	joinPredicate := liftResidualConjuncts(residualsWithPairs, innerOnlyLifted, outerSchema, outerWidth)

	algo := JoinAlgoHash
	if len(params) == 0 {
		// Zero equijoin pairs: no hash key exists. The executor runs
		// this as a materialising nested-loop semi/anti with emit-once
		// semantics (operators_join_agg.go runNestedLoop). The
		// residuals are guaranteed non-empty here (checked before any
		// tree mutation), so joinPredicate is non-nil.
		algo = JoinAlgoNestedLoop
	}
	join := &Join{
		pos:       ex.Pos(),
		Type:      joinType,
		Algo:      algo,
		Left:      outerChild,
		Right:     innerPlan,
		Predicate: joinPredicate,
		schema:    append(Schema(nil), outerChild.Output()...),
	}
	if outerKey != nil {
		join.LeftKey = outerKey
		join.RightKey = innerKey
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
