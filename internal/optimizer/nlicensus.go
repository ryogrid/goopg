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
