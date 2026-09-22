package optimizer

// nlicensus.go — M0145-0007 slice 2: which route builds goopg's
// `*NestedLoopIndexJoin` nodes.
//
// # The question this instrument answers
//
// goopg builds NLI nodes two ways. The PG-shaped join search elects them as
// PATHS (`joinpathsnli.go`) and the lowering arms in `createplannl.go` build
// the node from the winning path — PG's own shape, where the method is a
// costed choice. The legacy pass `rewriteJoinsToNLI` (`nl_index_join.go`)
// instead REWRITES an already-built `*Join` into an NLI, and it prunes at
// `isSearchedTree` (P5.9-b) precisely so it never overrules the search.
//
// M0145-0001 assigned `rewriteJoinsToNLI` and `stampSemiProbePrices` to
// M0145-0007 ("NLI is a Path kind; probe cost rides the path"). The recon
// found they need no folding — they only ever see the population the search
// never built — which turns the remaining question into a measurement: how
// big is that population, and what is in it? A rewrite that builds nothing on
// the corpus can be deleted with the legacy pipeline at M0145-0008; one that
// builds SEMI/ANTI joins the search cannot elect is a coverage hole that has
// to be closed BEFORE the cutover, or those statements lose a plan.
//
// # Why a counter rather than reading the plans
//
// EXPLAIN prints the same node for both routes, so a plan capture cannot
// attribute one. The counters are the only way to tell them apart, and they
// are per-construction-site, which is where the attribution is unambiguous.
//
// Off unless `GOOPG_NLI_CENSUS=1`: one stderr line per node built, carrying
// the route and the join type, so a run's census is a `grep | sort | uniq -c`
// over the server log. There is no per-statement aggregation on purpose —
// `PlanWithSettings` recurses for subplans, so a per-call delta would nest and
// double-count; corpus totals by route and join type are what the question
// needs.

import (
	"fmt"
	"os"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// nliCensusEnabled gates the whole instrument. Read once, like `dpTrace`.
var nliCensusEnabled = os.Getenv("GOOPG_NLI_CENSUS") == "1"

// NLI construction routes, as they appear in the census lines.
const (
	nliRouteSearch  = "search"  // elected as a path, built by createplannl.go
	nliRouteRewrite = "rewrite" // built by rewriteJoinsToNLI's node walk
)

// noteNLIBuilt records one built `*NestedLoopIndexJoin`. Call it at the
// construction site, never at a call site that might not build one.
// `probe` names the inner index the join probes, which is what makes a single
// fire attributable to a statement: grep the corpus plan capture for the index
// name. Without it a count of 1 is a number nobody can act on.
func noteNLIBuilt(route string, jt JoinType, probe string) {
	if !nliCensusEnabled {
		return
	}
	if probe == "" {
		probe = "(unnamed)"
	}
	fmt.Fprintf(os.Stderr, "NLICENSUS route=%s jointype=%s probe=%s\n",
		route, nliCensusJoinTypeName(jt), probe)
}

// Sublink-planning routes, reported under the same env gate. The pinned-spine
// route (`runJoinSearchBelowPinned`, predp.go) is the LEGACY answer to
// pre-DP unnesting, and it is the only live caller of the splice/re-resolution
// family — `spliceSearchedSpine`, `layoutPosMap`, `remapByPosMap`,
// `remapOuterRefsInSubplan`, `remapSublinkOuterRefs`. M0145-0001 assigned that
// family to M0145-0007 ("single lowering translates once"), so how often the
// route fires is the same kind of question the NLI census answered: a family
// with no live route retires at the cutover, one that still plans corpus
// statements does not.
//
// M0145-0005 slice 7 retired the route on the JOINTREE arm — the planner's
// S5a gate is `!jointree`-guarded, so `pinned-spine` can only fire on the
// legacy pipeline now. A jointree statement whose pull-up declined every
// sublink conjunct takes the single-pass Filter arm plus the post-hoc unnest
// instead, and reports `jointree-posthoc`; the family itself stays until the
// M0145-0008 cutover deletes the legacy pipeline wholesale.
const (
	spineRouteLegacy   = "pinned-spine"     // runJoinSearchBelowPinned + post-search splice (legacy arm only)
	spineRouteJointree = "jointree-pullup"  // sublinks pulled into the IR before the search
	spineRoutePosthoc  = "jointree-posthoc" // pull-up declined; post-search unnest pins the spine
)

// sublinkRouteCounts is noteSublinkRoute's test-visible twin: bumped on
// every call, env gate or not, so a unit test can pin which route planned a
// statement without scraping stderr. This package's tests do not run
// parallel, so a plain map suffices.
var sublinkRouteCounts = map[string]int{}

// noteSublinkRoute records which route planned one statement's WHERE sublinks.
func noteSublinkRoute(route string) {
	sublinkRouteCounts[route]++
	if !nliCensusEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, "SUBLINKCENSUS route=%s\n", route)
}

// notePullupDecline records one WHERE conjunct's fate at the jointree
// sublink pull-up (M0145-0003). An empty reason means the conjunct WAS pulled
// up; anything else is the gate it fell at.
//
// The pull-up's coverage is what the M0145-0007 sublink-route census measured
// as under 2% of sublink-planning events, and "which arm to build next" is a
// question about the SHAPE of the other 98%. Guessing it from the task's
// deferred list ranks the arms by how big they sound; this ranks them by how
// often they actually fire.
func notePullupDecline(reason string) {
	if !nliCensusEnabled {
		return
	}
	if reason == "" {
		reason = "(pulled)"
	}
	fmt.Fprintf(os.Stderr, "PULLUPCENSUS decline=%s\n", reason)
}

// sublinkConjunctKind classifies a WHERE conjunct the EXISTS arm did not even
// recognise, by the Go type of the first sublink-bearing expression in it.
// Conjuncts with no sublink are not reported at all — an ordinary `a = 1` is
// not a missed pull-up — so the census counts only what a wider pull-up could
// in principle take.
//
// It deliberately does NOT enumerate the sublink Expr types in a switch. A
// census whose classifier has to be taught each new type reports the one it
// was never taught as "nothing here", which is the RC-1a defect class
// (exprwalk.go) wearing a measurement hat: the arm nobody built would be the
// arm that never appears. `ExprSubplans` is the shared "does this expression
// carry an inner plan" primitive, and `%T` names whatever it finds, so a new
// sublink type shows up in the census the day it is added.
func sublinkConjunctSite(c Expr) string {
	kind := sublinkConjunctKind(c)
	if kind == "" {
		return ""
	}
	return kind + "@" + sublinkConjunctPosition(c)
}

// sublinkConjunctPosition names WHERE inside the conjunct the first
// subplan-bearing node sits. M0145-0015 needs it because the remedy is
// decided by the position, not by the sublink kind: PG's
// `pull_up_sublinks_qual_recurse` recurses through AND and through a NOT
// wrapper (`prepjointree.c:789-845`) but **stops at every other clause type**,
// OR args included (`:877`). So an `ExistsExpr@or` is a correct decline that
// upstream makes too, while an `ExistsExpr@top` would be a real miss — and a
// census that reports only "ExistsExpr" cannot tell them apart.
//
// The walk is deliberately path-sensitive rather than a plain search: the
// OUTERMOST non-AND wrapper on the way down is what decides reachability, so
// an OR seen anywhere above the sublink outranks a NOT seen below it.
func sublinkConjunctPosition(c Expr) string {
	var walk func(e Expr, depth int, sawOr, sawNot bool) string
	walk = func(e Expr, depth int, sawOr, sawNot bool) string {
		if e == nil {
			return ""
		}
		if len(ExprSubplans(e)) > 0 {
			switch {
			case sawOr:
				return "or"
			case sawNot:
				return "not"
			case depth == 0:
				return "top"
			default:
				return "scalar"
			}
		}
		switch x := e.(type) {
		case *BinaryOp:
			nextOr := sawOr || x.Op == parser.OpOr
			// An AND below the conjunct root is still conjunct position:
			// splitAnd would have separated it had the caller split deeper.
			nextDepth := depth + 1
			if x.Op == parser.OpAnd {
				nextDepth = depth
			}
			if r := walk(x.Left, nextDepth, nextOr, sawNot); r != "" {
				return r
			}
			return walk(x.Right, nextDepth, nextOr, sawNot)
		case *UnaryOp:
			if x.Op == parser.OpNot {
				return walk(x.Operand, depth, sawOr, true)
			}
			return walk(x.Operand, depth+1, sawOr, sawNot)
		}
		// Any other container: the sublink is buried in a scalar expression.
		found := ""
		walkExprTree(e, func(n Expr) {
			if found == "" && n != e && len(ExprSubplans(n)) > 0 {
				found = "scalar"
			}
		})
		return found
	}
	if r := walk(c, 0, false, false); r != "" {
		return r
	}
	return "unknown"
}

func sublinkConjunctKind(c Expr) string {
	kind := ""
	walkExprTree(c, func(x Expr) {
		if kind != "" || len(ExprSubplans(x)) == 0 {
			return
		}
		kind = strings.TrimPrefix(fmt.Sprintf("%T", x), "*optimizer.")
	})
	return kind
}

// noteNLIPathGate records why `addNLIPaths` did or did not FILE a path for a
// semi/anti joinrel — a different question from `noteNLIBuilt`, which counts
// nodes the lowering actually built.
//
// M0145-0007 slice 2 concluded the cutover waits on `addNLIPaths` admitting
// SEMI/ANTI. Reading the function disproved that (it declines only
// `JoinRight`), and the knob-arm census then showed the pull-up already hands
// three of TPC-H's four semijoins to the search while search-built SEMI/ANTI
// NLIs stay at zero. That leaves exactly one question — is a path generated
// and out-costed, or never generated? — and it is answerable only at the
// decline points, because a path that is never filed leaves no other trace.
//
// Restricted to semi/anti on purpose: the inner/left population is large and
// already understood, and a census that logs it drowns the signal.
func noteNLIPathGate(jt parser.JoinType, reason string) {
	if !nliCensusEnabled {
		return
	}
	name := "other"
	switch jt {
	case parser.JoinInner:
		name = "inner"
	case parser.JoinLeft:
		name = "left"
	case parser.JoinRight:
		name = "right"
	case parser.JoinFull:
		name = "full"
	case parser.JoinSemi:
		name = "semi"
	case parser.JoinAnti:
		name = "anti"
	}
	fmt.Fprintf(os.Stderr, "NLIGATE jointype=%s gate=%s\n", name, reason)
}

// noteSemiJoinrelPaths dumps every path filed for a semi/anti joinrel with its
// kind and cost — M0145-0008's follow-up question, one level down.
//
// The timing A/B measured that the jointree arm runs TPC-H Q4 35x, Q20 27x and
// Q21 7x slower than the default arm, and M0145-0007 established why the SHAPE
// differs: the search files an NLI path for the pulled-up semijoin and
// `add_path` out-costs it. What no census so far shows is BY HOW MUCH and on
// which term, and that cannot be read off a plan — the losing path leaves no
// trace in the output.
//
// So the whole candidate set is dumped once per semi/anti joinrel, after every
// arm has filed. The winner is the minimum total, so the comparison the cost
// model actually made is reconstructible from the line alone, and the NLI's
// margin of loss is the number the next fix has to move.
func noteSemiJoinrelPaths(joinrel *RelOptInfo, jt parser.JoinType) {
	if !nliCensusEnabled || joinrel == nil {
		return
	}
	// Every joinrel the search forms is named, not just semi/anti. The Q4
	// investigation needed exactly this: the pulled-up EXISTS reached the
	// search (`PULLUPCENSUS decline=(pulled)`) and yet NO semi/anti joinrel
	// was ever processed, which a semi-only census reports as silence — and
	// silence is what sent one loop to the wrong conclusion. A census that
	// prints the jointype it DID see distinguishes "not formed" from "formed
	// as something else".
	name := traceJoinTypeName(jt)
	for _, p := range joinrel.Pathlist {
		if p == nil {
			continue
		}
		fmt.Fprintf(os.Stderr, "SEMICOST jointype=%s relids=%#08x kind=%s startup=%.2f total=%.2f rows=%.0f\n",
			name, uint32(joinrel.Relids), tracePathKind(p), p.Cost.Startup, p.Cost.Total, p.Rows)
	}
}

// notePullupClassify names WHICH of `classifyPulledQuals`' refusals fired.
//
// The seam already reports `seam-decline reason=pullup-classify`, which is
// where TPC-H Q4 dies on the jointree arm — the EXISTS is pulled up, the seam
// then refuses the pulled bodies, and because `pulled` has already suppressed
// the legacy pre-DP route the statement ends with no semijoin from either
// route and a 10x regression. One reason string for five distinct refusals is
// not enough to act on: the fix for "a body qual the search cannot consume" is
// not the fix for "no spanning qual".
func notePullupClassify(reason string) {
	if !nliCensusEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, "PULLUPCLASSIFY refusal=%s\n", reason)
}

// nliCensusJoinTypeName names the join type for the census line. It is
// deliberately separate from `traceJoinTypeName` (which speaks the parser
// enum) so the census stays readable without a conversion at every call.
func nliCensusJoinTypeName(jt JoinType) string {
	switch jt {
	case JoinTypeInner:
		return "inner"
	case JoinTypeLeft:
		return "left"
	case JoinTypeRight:
		return "right"
	case JoinTypeFull:
		return "full"
	case JoinTypeCross:
		return "cross"
	case JoinTypeSemi:
		return "semi"
	case JoinTypeAnti:
		return "anti"
	}
	return fmt.Sprintf("unknown(%d)", int(jt))
}

// cteRowsFallbackEnabled gates the M0129-S1 `rows<=1` CTE fallback in
// `initialRelRows` (joinsearch.go). M0145-0012 measurement apparatus, default
// ON so the default arm is today's behaviour; `GOOPG_CTE_ROWS_FALLBACK=off`
// removes the arm for a knob-arm A/B.
//
// The arm it gates is a goopg-only divergence: `set_cte_size_estimates` keeps
// the collapsed estimate and only floors it at 1 (`clamp_row_est`,
// postgres/src/backend/optimizer/path/costsize.c), so PostgreSQL has no
// counterpart to substituting the body's UNFILTERED row count. This flag
// exists to answer the ledger criterion `derived >= guard effect` by
// measurement rather than by argument, exactly as M0145-0011 did for the
// `outer-over-derived` firewall.
var cteRowsFallbackEnabled = os.Getenv("GOOPG_CTE_ROWS_FALLBACK") != "off"

// traceCTERowsFallback records one engagement of that arm: which CTE, the
// collapsed estimate it replaced, and the body row count it substituted. It
// rides the DP trace rather than the NLI census because the question it
// answers is "where does the guard change a cardinality the search then costs
// on", which is a search-level question.
func traceCTERowsFallback(name string, collapsed, bodyRows int64) {
	if !dpTraceEnabled() {
		return
	}
	fmt.Fprintf(os.Stderr, "CTEROWSFALLBACK cte=%s collapsed=%.0f body=%.0f\n",
		name, float64(collapsed), float64(bodyRows))
}

// noteRebaseFail names WHICH failure inside `rebasePulledQual` fired. The seam
// reports `PULLUPCLASSIFY refusal=rebase-failed` for all of them, and one
// string for several distinct causes is not enough to act on: "a body column
// the leaf map does not cover" and "the clone driver hit a node type it does
// not recognise" have different fixes.
func noteRebaseFail(reason string) {
	if !nliCensusEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, "REBASEFAIL cause=%s\n", reason)
}

// exprTypeName is the census spelling of an expression's Go type, with the
// package qualifier stripped so the lines stay readable.
func exprTypeName(e Expr) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", e), "*optimizer.")
}

// noteOnQualSublinks records each sublink-bearing conjunct of an explicit
// join's ON clause, with the join's TYPE — not merely "in an ON clause".
//
// The type is the whole point. PG runs the pull-up on ON quals under a
// legality boundary (`postgres/src/backend/optimizer/prep/prepjointree.c`,
// the `pull_up_sublinks_jointree_recurse` JoinExpr arm): INNER passes both
// sides' rels as available, LEFT passes only the RHS, RIGHT only the LHS, and
// FULL passes NOTHING — a sublink pulled out of a null-preserved side is a
// wrong-answer class, not a missed optimisation. A census that reported only
// "an ON clause holds an EXISTS" could not tell a pullable conjunct from one
// that must stay a SubPlan, so it would answer the wrong question.
//
// The site string is M0145-0015's `<kind>@<position>`, reused rather than
// re-derived: an EXISTS under an OR inside an ON clause is out of reach for
// the same reason it is out of reach inside a WHERE.
func noteOnQualSublinks(jt parser.JoinType, onPred Expr) {
	if !nliCensusEnabled || onPred == nil {
		return
	}
	for _, c := range splitAnd(onPred) {
		site := sublinkConjunctSite(c)
		if site == "" {
			continue
		}
		fmt.Fprintf(os.Stderr, "ONSUBLINK jointype=%s site=%s\n",
			traceJoinTypeName(jt), site)
	}
}
