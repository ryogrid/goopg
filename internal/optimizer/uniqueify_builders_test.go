package optimizer

// M0142-0008c-3b: the two builders `jointypeForDirection`'s `uniqueSide`
// sentinel actually reaches — `addNestLoopPath` (`uniqueSideInner`) and
// `addNLIPaths` (`uniqueSideOuter`), per design doc §19.3's PG-asymmetric
// split. 3a proved the dispatch layer was inert (uniq computed, then
// discarded); these tests state 3b's different acceptance bar — the
// substitution must actually happen, not merely compile — by asserting the
// built Path's substituted child is the `createUniquePath` output (a
// `PathUnique` wrapping a `PathSort`), never the plain, un-deduplicated
// `CheapestTotal`.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAddNestLoopPath_UniqueSideInnerSubstitutesInner pins addNestLoopPath's
// half of 3b directly: with uniq == uniqueSideInner, the emitted nest loop's
// inner child must be createUniquePath's result, not inner.CheapestTotal.
func TestAddNestLoopPath_UniqueSideInnerSubstitutesInner(t *testing.T) {
	cp := defaultCostParams()
	outer := scanRel(relsetOf(0), 10000, 100)

	rhsRel, subpath, sjinfo := uniquePathFixture(500)
	rhsRel.Relids = relsetOf(1)
	rhsRel.CheapestTotal = subpath

	joinrel := newRelOptInfo(outer.Relids|rhsRel.Relids, 5000, 64)
	addNestLoopPath(joinrel, outer, rhsRel, cp, parser.JoinInner, nil, uniqueSideInner, sjinfo, semiAntiJoinFactors{})

	if len(joinrel.Pathlist) != 1 {
		t.Fatalf("got %d paths, want 1", len(joinrel.Pathlist))
	}
	p := joinrel.Pathlist[0]
	if len(p.Children) != 2 {
		t.Fatalf("Children = %v, want 2", p.Children)
	}
	if p.Children[0] != outer.CheapestTotal {
		t.Errorf("outer child was substituted; uniqueSideInner must only touch the inner")
	}
	if p.Children[1] == subpath {
		t.Fatal("inner child is the plain, un-deduplicated subpath — createUniquePath was not applied")
	}
	if p.Children[1] != rhsRel.CheapestUnique {
		t.Errorf("inner child = %p, want rhsRel.CheapestUnique (%p)", p.Children[1], rhsRel.CheapestUnique)
	}
	if p.Children[1].Kind != PathUnique {
		t.Errorf("inner child.Kind = %v, want PathUnique", p.Children[1].Kind)
	}
}

// TestAddNestLoopPath_UniqueSideInnerDeclinesWhenUnunique-ifiable states the
// fail-closed half: when createUniquePath itself declines (SemiCanBtree
// false), addNestLoopPath must add NOTHING rather than fall back to the
// plain, wrong-semantics inner join.
func TestAddNestLoopPath_UniqueSideInnerDeclinesWhenUnUniqueIfiable(t *testing.T) {
	cp := defaultCostParams()
	outer := scanRel(relsetOf(0), 10000, 100)

	rhsRel, subpath, sjinfo := uniquePathFixture(500)
	rhsRel.Relids = relsetOf(1)
	rhsRel.CheapestTotal = subpath
	sjinfo.SemiCanBtree = false

	joinrel := newRelOptInfo(outer.Relids|rhsRel.Relids, 5000, 64)
	addNestLoopPath(joinrel, outer, rhsRel, cp, parser.JoinInner, nil, uniqueSideInner, sjinfo, semiAntiJoinFactors{})

	if len(joinrel.Pathlist) != 0 {
		t.Fatalf("got %d paths, want 0 (createUniquePath declined)", len(joinrel.Pathlist))
	}
}

// TestAddNLIPaths_UniqueSideOuterSubstitutesOuter pins addNLIPaths' half of
// 3b: with uniq == uniqueSideOuter, the outer candidate this arm tests
// against every parameterized inner must be createUniquePath's result.
func TestAddNLIPaths_UniqueSideOuterSubstitutesOuter(t *testing.T) {
	cp := defaultCostParams()

	lhsRel, subpath, sjinfo := uniquePathFixture(500)
	lhsRel.Relids = relsetOf(0)
	lhsRel.CheapestTotal = subpath

	inner := nliInnerRel(relsetOf(1), 1000000, lhsRel.Relids, indexProbeCost(cp))
	joinrel := newRelOptInfo(lhsRel.Relids|inner.Relids, 5000, 64)
	addNLIPaths(nil, joinrel, lhsRel, inner, cp, parser.JoinInner, nil, 0, uniqueSideOuter, sjinfo, semiAntiJoinFactors{})

	if len(joinrel.Pathlist) == 0 {
		t.Fatal("got 0 paths, want at least 1 (indexed inner over a substituted outer)")
	}
	for _, p := range joinrel.Pathlist {
		if len(p.Children) != 2 {
			t.Fatalf("Children = %v, want 2", p.Children)
		}
		if p.Children[0] == subpath {
			t.Fatal("outer child is the plain, un-deduplicated subpath — createUniquePath was not applied")
		}
		if p.Children[0] != lhsRel.CheapestUnique {
			t.Errorf("outer child = %p, want lhsRel.CheapestUnique (%p)", p.Children[0], lhsRel.CheapestUnique)
		}
		if p.Children[0].Kind != PathUnique {
			t.Errorf("outer child.Kind = %v, want PathUnique", p.Children[0].Kind)
		}
	}
}

// TestAddNLIPaths_UniqueSideOuterDeclinesWhenUnUniqueIfiable mirrors the
// nest-loop decline control for the outer-substitution arm.
func TestAddNLIPaths_UniqueSideOuterDeclinesWhenUnUniqueIfiable(t *testing.T) {
	cp := defaultCostParams()

	lhsRel, subpath, sjinfo := uniquePathFixture(500)
	lhsRel.Relids = relsetOf(0)
	lhsRel.CheapestTotal = subpath
	sjinfo.SemiCanBtree = false

	inner := nliInnerRel(relsetOf(1), 1000000, lhsRel.Relids, indexProbeCost(cp))
	joinrel := newRelOptInfo(lhsRel.Relids|inner.Relids, 5000, 64)
	addNLIPaths(nil, joinrel, lhsRel, inner, cp, parser.JoinInner, nil, 0, uniqueSideOuter, sjinfo, semiAntiJoinFactors{})

	if len(joinrel.Pathlist) != 0 {
		t.Fatalf("got %d paths, want 0 (createUniquePath declined)", len(joinrel.Pathlist))
	}
}

// TestAddPathsToJoinrel_UniqueSideInner_PlanShape is 3b's end-to-end
// acceptance check, run through the real dispatch layer
// (`addPathsToJoinrel`/`jointypeForDirection`) rather than by calling a
// builder directly: a SEMI pair admitted only via the unique-ify fallback
// (§19.4's "must move the actual plan shape", not just "stay inert" — the
// opposite bar from 3a's own inertness test) must produce a demoted-INNER
// nested loop over the unique-ified inner.
func TestAddPathsToJoinrel_UniqueSideInner_PlanShape(t *testing.T) {
	cp := defaultCostParams()
	lhs, rhs, extra := relsetOf(0), relsetOf(1), relsetOf(2)

	rhsRel, subpath, sjinfo := uniquePathFixture(500)
	rhsRel.Relids = rhs
	rhsRel.CheapestTotal = subpath
	// Same "extra" trick as TestJointypeForDirection_UniqueIfyFallback: the
	// ordinary MinLefthand/MinRighthand containment check fails for both
	// orientations, so admission can only come through the fallback.
	sjinfo.MinLefthand, sjinfo.SynLefthand = lhs|extra, lhs|extra
	sjinfo.MinRighthand, sjinfo.SynRighthand = rhs, rhs

	lhsRel := scanRel(lhs, 10000, 100)
	joinrel := newRelOptInfo(lhs|rhs, 5000, 64)

	if err := addPathsToJoinrel(nil, joinrel, lhsRel, rhsRel, nil, cp, sjinfo); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}

	var nl *Path
	for _, p := range joinrel.Pathlist {
		if p.Kind == PathNestLoop {
			nl = p
			break
		}
	}
	if nl == nil {
		t.Fatalf("no PathNestLoop in %d paths, want one built over the unique-ified inner", len(joinrel.Pathlist))
	}
	if nl.Jointype != parser.JoinInner {
		t.Errorf("Jointype = %v, want JoinInner (PG's own demote-before-build, joinpath.c:116-121)", nl.Jointype)
	}
	if len(nl.Children) != 2 || nl.Children[1].Kind != PathUnique {
		t.Fatalf("Children = %v, want [outer, PathUnique]", nl.Children)
	}
	if nl.Children[1] != rhsRel.CheapestUnique {
		t.Error("nested loop's inner is not the cached createUniquePath result")
	}
}
