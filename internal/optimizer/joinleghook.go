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
// narrowJoinLeg is that attachment point. For M0139-S1 it is
// UNCONDITIONALLY A DECLINE: it recognises exactly the legs a real
// narrowing pass WOULD have something to say about (a still-bare
// base-relation scan, optionally Filter-wrapped, that the existing narrow
// calls did not already cover) and counts them, but returns the (Node,
// outputLayout) pair completely unchanged either way. That is deliberate,
// not a stub left mid-implementation: S1's own pre-registered prediction is
// "no parity movement", and a function that never changes its input cannot
// move a plan by construction — the safest possible way to stand up one
// call site that every join constructor (hash, merge as a fallback when
// narrowMergeInput declines, plain NL, and NLI) now reaches uniformly
// through joinInputsFor, ready for M0139-S2 to fill in the SAME keep-set
// derivation narrowBuildInput already uses (buildKeepSet / joinKeepSet /
// neededKeepSet) rather than invent a second one.
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
// own preconditions.
func narrowJoinLeg(n Node, lay outputLayout) (Node, outputLayout) {
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
	return n, lay
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
