package optimizer

import "os"

// M0139-S1 — the missing attachment point (docs/milestones/0139-*.md
// "The structural blocker, already located").
//
// joinInputsFor (createplanjoin.go) already narrows a hash join's INNER
// (build) side (narrowBuildInput, narrowoutput.go, GOOPG_NARROW_BUILD) and
// BOTH sides of a merge join (narrowMergeInput). Three legs it never
// reaches, because nothing in the pipeline ever wraps them: a hash join's
// OUTER (probe) side, and both sides of every nested-loop shape (plain NL
// and NLI's outer — NLI's per-probe correlated INNER index scan is a
// distinct construction, see createplannl.go's `absorbableLeafCond`, and is
// covered here too only because it happens to flow through the same
// `innerNode`/`in.inner` pair before that absorption runs; nothing about
// the node this function sees or returns is specific to NLI). Those legs
// reach their join parent as a bare scan node with nowhere for a narrowing
// Project to attach.
//
// narrowJoinLeg is that attachment point. M0139-S1 (2026-09-15) stood it up
// as an UNCONDITIONAL DECLINE, proven byte-identical to its input by
// construction: it recognised exactly the legs a real narrowing pass would
// have something to say about (a still-bare base-relation scan, optionally
// Filter-wrapped, that the existing narrow calls did not already cover) and
// counted them, but returned the (Node, outputLayout) pair completely
// unchanged either way — the safest possible way to stand up one call site
// that every join constructor (hash, merge as a fallback when
// narrowMergeInput declines, plain NL, and NLI) reaches uniformly through
// joinInputsFor.
//
// M0139-S2 (this revision) fills the decline in: it reuses
// narrowBuildInput's existing keep-set derivation (buildKeepSet /
// joinKeepSet / neededKeepSet) rather than inventing a second one, and
// calls narrowPlanOutput with the result — the same "wrap in a *Project
// naming only the kept columns" mechanism GOOPG_NARROW_BUILD and
// GOOPG_NARROW_UPPER/GOOPG_NARROW_UPPER_SORT already ship. It is now
// genuinely load-bearing: a leg this hook narrows changes the plan (the new
// *Project node) and the row width flowing into whatever join sits above
// it.
//
// narrowLegHook resolves GOOPG_NARROW_LEG_HOOK at process start. Opt-out
// polarity (`=0` disables), matching every other narrowing flag in this
// family (GOOPG_NARROW_BUILD, GOOPG_NARROW_UPPER, GOOPG_NARROW_UPPER_SORT):
// the flag exists so a gate can measure the FLAG rather than the commit.
var narrowLegHook = narrowLegHookFromEnv(os.Getenv("GOOPG_NARROW_LEG_HOOK"))

// narrowLegHookFromEnv is the flag's polarity, factored out so tests
// resolve the same default the process starts with (flaglabels.go's
// contract: no literal restating a default elsewhere).
func narrowLegHookFromEnv(v string) bool { return v != "0" }

// legHookFireCount counts calls to narrowJoinLeg that found an
// eligible-but-currently-unhooked leg (flag on, node still a bare
// narrowable scan). It is the corpus measurement M0139-S1's design doc
// reports as "the pass fires N > 0 times" — a literal read of this counter
// over a real corpus plan run, not an argument. Test/measurement-only;
// production code never reads it.
var legHookFireCount int64

// narrowJoinLeg is the hook. Call it on both the outer and inner leg of
// every join AFTER any existing narrowing (narrowBuildInput,
// narrowMergeInput) has had its chance — that ordering is what lets this
// function's `already a *Project` check tell "already covered" apart from
// "nothing has looked at this leg yet" without duplicating either pass's
// own preconditions. `nliInner` must be true for, and only for, the inner
// leg of an NLI join (`kind == "PathNestLoop(NLI)"` at the joinInputsFor
// call site) — see the guard below for why.
func narrowJoinLeg(n Node, lay outputLayout, p *Path, nliInner bool) (Node, outputLayout) {
	if !narrowLegHook || n == nil {
		return n, lay
	}
	if _, already := n.(*Project); already {
		// narrowBuildInput / narrowMergeInput already attached one, or this
		// leg was a Project for some other reason upstream; either way
		// there is no missing attachment point here.
		return n, lay
	}
	if !isNarrowableLeaf(n) {
		// Not a base-relation scan: a sub-join, CTE scan, subquery, VALUES
		// list, etc. Those have no single leaf "keep set" of columns to
		// speak of at this seam, or are the upper-narrowing passes' job,
		// not this one.
		return n, lay
	}
	legHookFireCount++
	if nliInner {
		// Structural, not a keep-set question: `createNestLoopIndexJoinPlan`
		// / `createNestLoopIndexJoinPlanFused` (createplannl.go) peel this
		// exact (Node, outputLayout) pair with `absorbableLeafCond` and then
		// require the base to be a bare `*IndexScan` — `NestedLoopIndexJoin.
		// Inner` is typed `*IndexScan`, not `Node`, because the driver calls
		// `Rescan` on it with the outer slot bound per probe row. Wrapping
		// it in a `*Project` here is a plan-TIME panic ("NLI inner emitted a
		// *optimizer.Project, but NestedLoopIndexJoin.Inner is an
		// *IndexScan"), caught live by
		// TestQ2DecorrelatedGroupKeyResolvesInAggregateInput before this
		// guard existed. The milestone doc's own NL policy already excludes
		// this leg from the *keep-set* derivation (`deriveJoinKeepsAt`'s
		// "B-01a NL policy"); this is the second, independent reason it
		// must also never be WRAPPED. The outer/probe side of the same NLI
		// has no such constraint (`Outer` is typed `Node`) and narrows
		// normally below.
		return n, lay
	}
	// M0139-S2: the SAME three-tier derivation narrowBuildInput
	// (narrowoutput.go) already uses for a hash join's build side, reused
	// rather than duplicated, and applied uniformly to whatever leg reached
	// this hook (a hash join's outer/probe side, a nested-loop's plain or
	// NLI outer side, or either side of a join kind whose own narrowing arm
	// declined). Every refusal below returns the pair untouched, exactly
	// like the arms it reuses.
	//
	// Why this is safe for a leg under a nested loop, where
	// `deriveJoinKeepsAt` deliberately never stamps `JoinKeep` ("F3-
	// conservative, B-01a NL policy"): that policy is about the TIGHTEST
	// tier only, `joinKeepSet`, whose derivation walks the JOIN PATH's own
	// quals (`collectJoinQualNames`) and cannot see an NLI's per-row probe
	// key, which lives on the INNER path's IndexClauses instead. Declining
	// to stamp `JoinKeep` there makes `joinKeepSet` report "unknown" for any
	// such leg (never a wrong answer), and this function then falls through
	// to `buildKeepSet` / `neededKeepSet` — the same two fallback tiers
	// `narrowBuildInput` and `narrowMergeInput` already trust for every
	// join kind. Both are join-kind-agnostic by construction: `buildKeepSet`
	// reads a path's `Target` (computed from the statement-wide `NeededCols`
	// at path-creation time, Slice 1, independent of which join wraps the
	// scan), and `neededKeepSet` reads `Rel.NeededCols` directly — a NAME
	// set collected by a raw walk of the parse tree's WHERE/ON/FROM clauses
	// (`collectStmtColumnNames`), so an NLI's index-qual reference to an
	// outer column is already a name in that set, the same as any other
	// qual reference. `narrowcostinputs.go`'s safety argument for this same
	// chain ("this can only ever keep TOO MANY columns, never too few")
	// therefore holds here unchanged.
	if p == nil || p.Rel == nil || !p.Rel.NeededColsKnown {
		// Same refusal narrowBuildInput/narrowMergeInput apply: no path, no
		// rel, or an unknown needed set must not be read as "keep nothing".
		return n, lay
	}
	if keep, ok := joinKeepSet(n, p); ok {
		return narrowPlanOutput(n, lay, keep)
	}
	if keep, ok := buildKeepSet(n, p); ok {
		return narrowPlanOutput(n, lay, keep)
	}
	return narrowPlanOutput(n, lay, neededKeepSet(n.Output(), p.Rel.NeededCols))
}

// isNarrowableLeaf reports whether n is a base-relation scan, optionally
// wrapped in one or more *Filter (attachRelationLocalFilters, M0077-0001) —
// the same leaf shape narrowBuildInput's own precondition already requires
// of the leg it narrows (narrowoutput.go). Mirrors scanLeafFor's
// wrapper-peeling (createplanindex.go) without its index-choice-replacement
// machinery, which this seam does not need — and widens the recognised
// scan set to every base-relation scan kind goopg has, not only the two
// scanLeafFor's re-search contract cares about.
func isNarrowableLeaf(n Node) bool {
	for {
		f, ok := n.(*Filter)
		if !ok {
			break
		}
		if f.Child == nil {
			return false
		}
		n = f.Child
	}
	switch n.(type) {
	case *SeqScan, *IndexScan, *IndexOnlyScan, *BitmapHeapScan:
		return true
	default:
		return false
	}
}

// legHookFireCountAndReset returns and resets the M0139-S1 hook's fire
// counter. Test/measurement only.
func legHookFireCountAndReset() int64 {
	n := legHookFireCount
	legHookFireCount = 0
	return n
}
