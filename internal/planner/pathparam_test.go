package planner

// M0127-P5.4b-i — tests for the parameterised-path discipline (leftdeep-joins
// 03 §9). Every case here is about a path that only produces rows once some
// outer relation supplies its parameter, and about the consumers that must
// therefore treat it differently from an ordinary path.
//
// These tests matter more than their unreachability today suggests. Nothing in
// the planner generates a parameterised path yet (that is P5.4b-ii), so the
// rules cannot be validated by planning a query — they can only be validated by
// constructing the pathlists PG would build and asserting the same answers PG
// gives. That is what this file does, against `set_cheapest` (pathnode.c:272),
// `calc_nestloop_required_outer` (:2592) and PATH_PARAM_BY_REL (joinpath.c:46).

import "testing"

// paramPath builds a path over rel with the given cost and parameterisation.
func paramPath(rel *RelOptInfo, startup, total float64, reqOuter RelSet) *Path {
	return &Path{
		Kind:          PathIndexScan,
		Rel:           rel,
		Rows:          rel.Rows,
		Cost:          Cost{Startup: startup, Total: total},
		RequiredOuter: reqOuter,
	}
}

// TestSetCheapestIgnoresParameterizedForCheapestSlots is 03 §9 rule 1's whole
// point: a parameterised path that is cheaper than every unparameterised one
// must NOT become the rel's representative. Its cheapness is a fiction from the
// perspective of a consumer that cannot supply its parameter — it is cheap
// precisely because someone else is paying for the binding.
func TestSetCheapestIgnoresParameterizedForCheapestSlots(t *testing.T) {
	rel := newRelOptInfo(0b0001, 1000, 8)
	unparam := paramPath(rel, 0, 500, 0)
	cheapParam := paramPath(rel, 0, 5, 0b0010) // needs rel 2

	// Added parameterised-first so a positional bug cannot pass by accident.
	rel.Pathlist = []*Path{cheapParam, unparam}
	setCheapest(rel)

	if rel.CheapestTotal != unparam {
		t.Fatalf("CheapestTotal = %v, want the unparameterised path (a parameterised path must never be the rel's representative)", rel.CheapestTotal)
	}
	if rel.CheapestStartup != unparam {
		t.Fatalf("CheapestStartup = %v, want the unparameterised path", rel.CheapestStartup)
	}
	// PG prepends the cheapest unparameterised path to the list (pathnode.c:375).
	if len(rel.CheapestParameterized) != 2 ||
		rel.CheapestParameterized[0] != unparam ||
		rel.CheapestParameterized[1] != cheapParam {
		t.Fatalf("CheapestParameterized = %v, want [unparam, cheapParam]", rel.CheapestParameterized)
	}
}

// TestSetCheapestFallsBackToBestParameterized covers pathnode.c:377-383: with no
// unparameterised path the rel must still have a representative, so the best
// parameterised one takes the total slot — but NOT the startup slot, which PG
// leaves nil so that a LIMIT-driven consumer cannot silently pick a path it
// cannot bind.
func TestSetCheapestFallsBackToBestParameterized(t *testing.T) {
	rel := newRelOptInfo(0b0001, 100, 8)
	// Least parameterised wins over cheaper-but-more-parameterised: PG orders
	// on bms_subset_compare FIRST and only breaks ties on cost (:312-334).
	wide := paramPath(rel, 0, 1, 0b0110)    // needs rels 2 and 3, dirt cheap
	narrow := paramPath(rel, 0, 90, 0b0010) // needs rel 2 only
	rel.Pathlist = []*Path{wide, narrow}
	setCheapest(rel)

	if rel.CheapestTotal != narrow {
		t.Fatalf("CheapestTotal = %v, want the LEAST parameterised path even though it costs 90x more", rel.CheapestTotal)
	}
	if rel.CheapestStartup != nil {
		t.Fatalf("CheapestStartup = %v, want nil: PG never fills the startup slot from a parameterised path", rel.CheapestStartup)
	}
	if len(rel.CheapestParameterized) != 2 {
		t.Fatalf("CheapestParameterized has %d entries, want both parameterised paths", len(rel.CheapestParameterized))
	}
}

// TestSetCheapestBestParamTiesOnCost is the BMS_EQUAL arm (:317-322): identical
// parameterisation falls through to total cost.
func TestSetCheapestBestParamTiesOnCost(t *testing.T) {
	rel := newRelOptInfo(0b0001, 100, 8)
	dear := paramPath(rel, 0, 80, 0b0010)
	cheap := paramPath(rel, 0, 20, 0b0010)
	rel.Pathlist = []*Path{dear, cheap}
	setCheapest(rel)

	if rel.CheapestTotal != cheap {
		t.Fatalf("CheapestTotal = %v, want the cheaper of two equally-parameterised paths", rel.CheapestTotal)
	}
}

// TestSetCheapestBestParamKeepsIncumbentWhenIncomparable is the BMS_DIFFERENT
// arm (:335-343): when neither path has the least possible parameterisation, PG
// explicitly sits on the incumbent rather than picking on cost. Reproducing the
// non-obvious choice is the point — picking the cheaper here would be a
// plausible-looking divergence that only shows up as a plan difference.
func TestSetCheapestBestParamKeepsIncumbentWhenIncomparable(t *testing.T) {
	rel := newRelOptInfo(0b0001, 100, 8)
	first := paramPath(rel, 0, 80, 0b0010)  // needs rel 2
	second := paramPath(rel, 0, 20, 0b0100) // needs rel 3 — neither set contains the other
	rel.Pathlist = []*Path{first, second}
	setCheapest(rel)

	if rel.CheapestTotal != first {
		t.Fatalf("CheapestTotal = %v, want the incumbent: incomparable parameterisations are NOT decided on cost", rel.CheapestTotal)
	}
}

// TestSetCheapestPathkeyTieBreak covers pathnode.c:358-369: on an EXACTLY equal
// cost the better-sorted path wins. This is why set_cheapest uses the unfuzzed
// compare_path_costs — the fuzzy comparator would have swallowed the tie.
func TestSetCheapestPathkeyTieBreak(t *testing.T) {
	rel := newRelOptInfo(0b0001, 100, 8)
	unsorted := paramPath(rel, 10, 100, 0)
	sorted := paramPath(rel, 10, 100, 0)
	sorted.Pathkeys = []PathKey{{}}
	rel.Pathlist = []*Path{unsorted, sorted}
	setCheapest(rel)

	if rel.CheapestTotal != sorted {
		t.Fatalf("CheapestTotal = %v, want the better-sorted path on an exact cost tie", rel.CheapestTotal)
	}
	if rel.CheapestStartup != sorted {
		t.Fatalf("CheapestStartup = %v, want the better-sorted path on an exact cost tie", rel.CheapestStartup)
	}
}

// TestSetCheapestUnparameterizedTieKeepsFirst: with no sort order to separate
// them, an exact tie keeps the earliest-added path. This is the determinism
// guarantee (03 §8) — plan choice must not depend on map iteration or on which
// pair happened to be offered first at equal cost.
func TestSetCheapestUnparameterizedTieKeepsFirst(t *testing.T) {
	rel := newRelOptInfo(0b0001, 100, 8)
	first := paramPath(rel, 10, 100, 0)
	second := paramPath(rel, 10, 100, 0)
	rel.Pathlist = []*Path{first, second}
	setCheapest(rel)

	if rel.CheapestTotal != first || rel.CheapestStartup != first {
		t.Fatalf("an exact tie must keep the earliest-added path, got total=%v startup=%v", rel.CheapestTotal, rel.CheapestStartup)
	}
}

// TestCalcNestloopRequiredOuterDischargesInner is the rule that makes NLI legal
// at all: a nested loop SUPPLIES the parameter its inner needs from the outer,
// so that requirement is discharged and must not propagate upward. The old
// `generateNLIPath` declared `RequiredOuter: inner.Relids` instead — a path over
// a joinrel claiming to require a relation the joinrel contains, which is not
// merely a wrong value but a category error.
func TestCalcNestloopRequiredOuterDischargesInner(t *testing.T) {
	const relA, relB, relC RelSet = 0b0001, 0b0010, 0b0100

	if got := calcNestloopRequiredOuter(relA, 0, relB, relA); got != 0 {
		t.Fatalf("A ⋈nl B(param by A) = %#04b, want 0: the nested loop supplies A itself", got)
	}
	if got := calcNestloopRequiredOuter(relA, 0, relB, 0); got != 0 {
		t.Fatalf("an unparameterised pair must stay unparameterised, got %#04b", got)
	}
	// An inner parameterised by something OUTSIDE the join keeps that need.
	if got := calcNestloopRequiredOuter(relA, 0, relB, relA|relC); got != relC {
		t.Fatalf("got %#04b, want %#04b: C is not supplied by this join and must propagate", got, relC)
	}
	// The outer's own parameterisation passes through untouched (:2603).
	if got := calcNestloopRequiredOuter(relA, relC, relB, 0); got != relC {
		t.Fatalf("got %#04b, want %#04b: the outer's parameterisation is not discharged here", got, relC)
	}
}

// TestCalcNonNestloopRequiredOuterUnions is the contrast (pathnode.c:2618): hash
// and merge discharge nothing, because a build side is materialised whole before
// the probe begins. A union, not a subtraction.
func TestCalcNonNestloopRequiredOuterUnions(t *testing.T) {
	rel := newRelOptInfo(0b0001, 10, 8)
	o := paramPath(rel, 0, 1, 0b0100)
	i := paramPath(rel, 0, 1, 0b1000)
	if got := calcNonNestloopRequiredOuter(o, i); got != 0b1100 {
		t.Fatalf("got %#06b, want %#06b", got, 0b1100)
	}
	if got := calcNonNestloopRequiredOuter(nil, nil); got != 0 {
		t.Fatalf("got %#04b, want 0", got)
	}
}

// TestAddPathsToJoinrelRefusesParameterizedInputs is 03 §9 rule 2 at its
// consumer. A hash join over an input parameterised by the other side would be
// a plan that cannot be built, so no path at all is the correct answer — and the
// caller's empty-pathlist check (joinsearchlevel.go:110-112) is what turns that
// into a loud failure rather than a silent wrong plan.
func TestAddPathsToJoinrelRefusesParameterizedInputs(t *testing.T) {
	cp := defaultCostParams()

	newRelWithPath := func(relids RelSet, reqOuter RelSet) *RelOptInfo {
		r := newRelOptInfo(relids, 1000, 8)
		addPath(r, paramPath(r, 0, 100, reqOuter))
		setCheapest(r)
		return r
	}

	const relA, relB RelSet = 0b0001, 0b0010

	t.Run("outer parameterised by inner", func(t *testing.T) {
		outer := newRelWithPath(relA, relB)
		inner := newRelWithPath(relB, 0)
		joinrel := newRelOptInfo(relA|relB, 5000, 16)
		if err := addPathsToJoinrel(nil, joinrel, outer, inner, nil, cp); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
		if len(joinrel.Pathlist) != 0 {
			t.Fatalf("got %d paths, want 0: an outer parameterised by the inner is unbuildable in any method", len(joinrel.Pathlist))
		}
	})

	t.Run("inner parameterised by outer", func(t *testing.T) {
		outer := newRelWithPath(relA, 0)
		inner := newRelWithPath(relB, relA)
		joinrel := newRelOptInfo(relA|relB, 5000, 16)
		if err := addPathsToJoinrel(nil, joinrel, outer, inner, nil, cp); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
		// Hash refuses it outright and the plain nested loop declines because
		// it would mis-cost the rescan — but this is exactly the shape the NLI
		// arm exists for, and since P5.4b-ii-b-1 that arm supplies the one
		// path. Between P5.4b-i and that slice this assertion read "want 0";
		// the hole was knowingly opened and is now closed.
		if len(joinrel.Pathlist) != 1 {
			t.Fatalf("got %d paths, want exactly the NLI path", len(joinrel.Pathlist))
		}
		p := joinrel.Pathlist[0]
		if p.Kind != PathNestLoop {
			t.Fatalf("got kind %v, want PathNestLoop: PG has no separate NLI path type", p.Kind)
		}
		if p.RequiredOuter != 0 {
			t.Fatalf("got RequiredOuter %#04b, want 0: the nested loop discharges the inner's parameterisation", p.RequiredOuter)
		}
		if len(p.Children) != 2 || p.Children[1].RequiredOuter != relA {
			t.Fatal("the NLI path's inner child must be the parameterised path itself")
		}
	})

	t.Run("neither parameterised still generates paths", func(t *testing.T) {
		outer := newRelWithPath(relA, 0)
		inner := newRelWithPath(relB, 0)
		joinrel := newRelOptInfo(relA|relB, 5000, 16)
		if err := addPathsToJoinrel(nil, joinrel, outer, inner, nil, cp); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
		if len(joinrel.Pathlist) == 0 {
			t.Fatal("an unparameterised pair must always yield at least the nested loop")
		}
		for _, p := range joinrel.Pathlist {
			if p.RequiredOuter != 0 {
				t.Fatalf("join path over unparameterised inputs must be unparameterised, got %#04b", p.RequiredOuter)
			}
		}
	})
}

// TestJoinPathRowsComeFromChildPaths is 03 §9 rule 3. A parameterised inner's
// per-outer-row count lives on the PATH, and a cost primitive that reads
// `rel.Rows` instead would price an NLI as a full inner rescan — the "NLI
// costing is garbage" failure §9 is named after. The assertion is indirect
// because the cost functions are pure: two joins identical except for the child
// path's Rows must not cost the same.
func TestJoinPathRowsComeFromChildPaths(t *testing.T) {
	cp := defaultCostParams()

	build := func(innerPathRows float64) float64 {
		outer := newRelOptInfo(0b0001, 100, 8)
		addPath(outer, paramPath(outer, 0, 10, 0))
		setCheapest(outer)

		inner := newRelOptInfo(0b0010, 100000, 8)
		p := paramPath(inner, 0, 10, 0)
		p.Rows = innerPathRows // the ppi_rows carrier; rel.Rows stays 100000
		addPath(inner, p)
		setCheapest(inner)

		joinrel := newRelOptInfo(0b0011, 100, 16)
		addNestLoopPath(joinrel, outer, inner, cp, []*restrictInfo{{}})
		return joinrel.Pathlist[0].Cost.Total
	}

	full := build(100000)
	perProbe := build(1)
	if !(perProbe < full) {
		t.Fatalf("a child path claiming 1 row must cost less than one claiming 100000 (got %v vs %v); the cost primitive is reading rel.Rows, not path.Rows", perProbe, full)
	}
}
