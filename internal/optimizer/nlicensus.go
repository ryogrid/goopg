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
const (
	spineRouteLegacy   = "pinned-spine"    // runJoinSearchBelowPinned + post-search splice
	spineRouteJointree = "jointree-pullup" // sublinks pulled into the IR before the search
)

// noteSublinkRoute records which route planned one statement's WHERE sublinks.
func noteSublinkRoute(route string) {
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
	if jt != parser.JoinSemi && jt != parser.JoinAnti {
		return
	}
	name := "semi"
	if jt == parser.JoinAnti {
		name = "anti"
	}
	fmt.Fprintf(os.Stderr, "NLIGATE jointype=%s gate=%s\n", name, reason)
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
