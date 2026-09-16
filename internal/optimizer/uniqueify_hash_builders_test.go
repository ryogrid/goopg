package optimizer

// M0142-0008c-3c: the hash-join analogue of uniqueify_builders_test.go's 3b
// pins. `hash_inner_and_outer` (joinpath.c:2301-2341) substitutes the PROBE
// side for JOIN_UNIQUE_OUTER and the BUILD side for JOIN_UNIQUE_INNER — unlike
// the NL split (inner-only / outer-only across two different builders), a
// single hash builder must accept BOTH substitutions since one function
// serves both directions. These tests state that bar directly: the built
// Path's substituted child must be createUniquePath's output, never the
// plain, un-deduplicated CheapestTotal, with fail-closed declines mirrored
// from 3b's own controls.
//
// Reachability caveat (design doc §35): as of this task, a live
// GOOPG_PGSHAPED_DP_TRACE sweep of the full TPC-DS SF0.25 corpus shows ZERO
// `jointype=semi`/`anti` DPPATH lines — addPathsToJoinrel's SEMI/ANTI branch,
// and so this substitution, is not yet reached by any real query (same
// verdict 3b's own recon already reached, re-confirmed after
// -3i-plumbing-b2's flip). These tests are the same kind of direct,
// hand-built-input pin 3a/3b already used to keep provably-correct-but-
// currently-unreachable code out of "untested dead code" territory
// (`dead_code_is_not_a_reference_impl`).

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAddHashJoinPath_UniqueSideOuterSubstitutesProbe pins addHashJoinPath's
// JOIN_UNIQUE_OUTER half: with uniq == uniqueSideOuter, the emitted hash
// join's probe (outer) child must be createUniquePath's result.
func TestAddHashJoinPath_UniqueSideOuterSubstitutesProbe(t *testing.T) {
	cp := defaultCostParams()

	probeRel, subpath, sjinfo := uniquePathFixture(500)
	probeRel.Relids = relsetOf(0)
	probeRel.CheapestTotal = subpath

	buildRel := scanRel(relsetOf(1), 10000, 100)
	joinRel := newRelOptInfo(probeRel.Relids|buildRel.Relids, 5000, 64)

	addHashJoinPath(joinRel, probeRel, buildRel, cp, parser.JoinInner, nil, nil, 0, hashJoinFinalCostInput{}, uniqueSideOuter, sjinfo)

	if len(joinRel.Pathlist) != 1 {
		t.Fatalf("got %d paths, want 1", len(joinRel.Pathlist))
	}
	p := joinRel.Pathlist[0]
	if len(p.Children) != 2 {
		t.Fatalf("Children = %v, want 2", p.Children)
	}
	if p.Children[0] == subpath {
		t.Fatal("probe child is the plain, un-deduplicated subpath — createUniquePath was not applied")
	}
	if p.Children[0] != probeRel.CheapestUnique {
		t.Errorf("probe child = %p, want probeRel.CheapestUnique (%p)", p.Children[0], probeRel.CheapestUnique)
	}
	if p.Children[0].Kind != PathUnique {
		t.Errorf("probe child.Kind = %v, want PathUnique", p.Children[0].Kind)
	}
	if p.Children[1] != buildRel.CheapestTotal {
		t.Errorf("build child was substituted; uniqueSideOuter must only touch the probe")
	}
}

// TestAddHashJoinPath_UniqueSideInnerSubstitutesBuild pins the
// JOIN_UNIQUE_INNER half: with uniq == uniqueSideInner, the build child must
// be createUniquePath's result.
func TestAddHashJoinPath_UniqueSideInnerSubstitutesBuild(t *testing.T) {
	cp := defaultCostParams()

	probeRel := scanRel(relsetOf(0), 10000, 100)
	buildRel, subpath, sjinfo := uniquePathFixture(500)
	buildRel.Relids = relsetOf(1)
	buildRel.CheapestTotal = subpath

	joinRel := newRelOptInfo(probeRel.Relids|buildRel.Relids, 5000, 64)

	addHashJoinPath(joinRel, probeRel, buildRel, cp, parser.JoinInner, nil, nil, 0, hashJoinFinalCostInput{}, uniqueSideInner, sjinfo)

	if len(joinRel.Pathlist) != 1 {
		t.Fatalf("got %d paths, want 1", len(joinRel.Pathlist))
	}
	p := joinRel.Pathlist[0]
	if len(p.Children) != 2 {
		t.Fatalf("Children = %v, want 2", p.Children)
	}
	if p.Children[0] != probeRel.CheapestTotal {
		t.Errorf("probe child was substituted; uniqueSideInner must only touch the build side")
	}
	if p.Children[1] == subpath {
		t.Fatal("build child is the plain, un-deduplicated subpath — createUniquePath was not applied")
	}
	if p.Children[1] != buildRel.CheapestUnique {
		t.Errorf("build child = %p, want buildRel.CheapestUnique (%p)", p.Children[1], buildRel.CheapestUnique)
	}
	if p.Children[1].Kind != PathUnique {
		t.Errorf("build child.Kind = %v, want PathUnique", p.Children[1].Kind)
	}
}

// TestAddHashJoinPath_UniqueSideDeclinesWhenUnUniqueIfiable mirrors 3b's
// fail-closed control for both directions: when createUniquePath itself
// declines (SemiCanBtree false), addHashJoinPath must add NOTHING rather than
// fall back to the plain, wrong-semantics join.
func TestAddHashJoinPath_UniqueSideDeclinesWhenUnUniqueIfiable(t *testing.T) {
	cp := defaultCostParams()

	for _, uniq := range []uniqueSide{uniqueSideOuter, uniqueSideInner} {
		probeRel := scanRel(relsetOf(0), 10000, 100)
		buildRel, subpath, sjinfo := uniquePathFixture(500)
		buildRel.Relids = relsetOf(1)
		buildRel.CheapestTotal = subpath
		sjinfo.SemiCanBtree = false

		var p1, p2 *RelOptInfo = probeRel, buildRel
		if uniq == uniqueSideOuter {
			// The fixture must be on whichever side createUniquePath is
			// actually asked to unique-ify.
			p1, p2 = buildRel, probeRel
			p1.Relids, p2.Relids = relsetOf(0), relsetOf(1)
		}

		joinRel := newRelOptInfo(p1.Relids|p2.Relids, 5000, 64)
		addHashJoinPath(joinRel, p1, p2, cp, parser.JoinInner, nil, nil, 0, hashJoinFinalCostInput{}, uniq, sjinfo)

		if len(joinRel.Pathlist) != 0 {
			t.Fatalf("uniq=%v: got %d paths, want 0 (createUniquePath declined)", uniq, len(joinRel.Pathlist))
		}
	}
}

// TestAddPartialHashJoinPath_UniqueSideOuterAlwaysDeclines pins PG's own
// `save_jointype != JOIN_UNIQUE_OUTER` gate (joinpath.c:2419): a partial hash
// join's outer is a per-worker PARTIAL path, and create_unique_path's
// Sort+Unique has no parallel-safe shape to substitute in its place, so the
// whole arm must decline before even inspecting PartialPathlist.
func TestAddPartialHashJoinPath_UniqueSideOuterAlwaysDeclines(t *testing.T) {
	prevMode := gatherPathsMode
	gatherPathsMode = gatherPathsTop
	defer func() { gatherPathsMode = prevMode }()

	cp := defaultCostParams()
	s := &searchCtx{parallelModeOK: true}

	outerRel, subpath, sjinfo := uniquePathFixture(500)
	outerRel.Relids = relsetOf(0)
	outerRel.CheapestTotal = subpath
	outerRel.ConsiderParallel = true
	outerRel.PartialPathlist = []*Path{{Kind: PathSeqScan, Rel: outerRel, Rows: 500, ParallelSafe: true, ParallelWorkers: 2}}

	innerRel := scanRel(relsetOf(1), 10000, 100)
	innerRel.Pathlist = []*Path{innerRel.CheapestTotal}

	joinRel := newRelOptInfo(outerRel.Relids|innerRel.Relids, 5000, 64)
	joinRel.ConsiderParallel = true

	addPartialHashJoinPath(s, joinRel, outerRel, innerRel, cp, parser.JoinInner, nil, nil, 0, hashJoinFinalCostInput{}, uniqueSideOuter, sjinfo)

	if len(joinRel.PartialPathlist) != 0 {
		t.Fatalf("got %d partial paths, want 0 (JOIN_UNIQUE_OUTER must decline outright)", len(joinRel.PartialPathlist))
	}
}

// TestAddPartialHashJoinPath_UniqueSideInnerDeclinesNonParallelSafe documents
// today's consequence of PG's OWN requirement (joinpath.c:2462-2466): a
// JOIN_UNIQUE_INNER partial hash join may use ONLY the already-unique-ified
// inner, never an alternative, and only if that path is parallel-safe.
// createUniquePath's Sort+Unique never sets ParallelSafe (createuniquepath.go
// builds the Path literal with no such field), so this arm always declines
// today — a correct, conservative consequence of a real field, not a bug to
// paper over with a false ParallelSafe.
func TestAddPartialHashJoinPath_UniqueSideInnerDeclinesNonParallelSafe(t *testing.T) {
	prevMode := gatherPathsMode
	gatherPathsMode = gatherPathsTop
	defer func() { gatherPathsMode = prevMode }()

	cp := defaultCostParams()
	s := &searchCtx{parallelModeOK: true}

	outerRel := scanRel(relsetOf(0), 10000, 100)
	outerRel.ConsiderParallel = true
	outerRel.PartialPathlist = []*Path{{Kind: PathSeqScan, Rel: outerRel, Rows: 10000, ParallelSafe: true, ParallelWorkers: 2}}

	innerRel, subpath, sjinfo := uniquePathFixture(500)
	innerRel.Relids = relsetOf(1)
	innerRel.CheapestTotal = subpath
	innerRel.Pathlist = []*Path{subpath}

	joinRel := newRelOptInfo(outerRel.Relids|innerRel.Relids, 5000, 64)
	joinRel.ConsiderParallel = true

	// A dummy, non-nil key: addPartialHashJoinPath only reads len(keys) (V3's
	// "no-keys" veto) — its contents are never dereferenced before this
	// arm's own decline.
	dummyKeys := []*restrictInfo{{}}
	addPartialHashJoinPath(s, joinRel, outerRel, innerRel, cp, parser.JoinInner, dummyKeys, nil, 0, hashJoinFinalCostInput{}, uniqueSideInner, sjinfo)

	if len(joinRel.PartialPathlist) != 0 {
		t.Fatalf("got %d partial paths, want 0 (createUniquePath's result is never ParallelSafe)", len(joinRel.PartialPathlist))
	}
	if innerRel.CheapestUnique == nil {
		t.Fatal("createUniquePath was never even attempted")
	}
}

// TestAddPathsToJoinrel_UniqueSideInner_HashPlanShape is 3c's end-to-end
// acceptance check, run through the real dispatch layer
// (addPathsToJoinrel/jointypeForDirection) with a real equijoin clause so the
// keyed arm actually runs: a SEMI pair admitted only via the unique-ify
// fallback must produce a demoted-INNER hash join built over the
// unique-ified inner, mirroring TestAddPathsToJoinrel_UniqueSideInner_PlanShape
// (nestloop) for the hash builder.
func TestAddPathsToJoinrel_UniqueSideInner_HashPlanShape(t *testing.T) {
	cp := defaultCostParams()
	lhs, rhs, extra := relsetOf(0), relsetOf(1), relsetOf(2)

	rhsRel, subpath, sjinfo := uniquePathFixture(500)
	rhsRel.Relids = rhs
	rhsRel.CheapestTotal = subpath
	// Same "extra" trick as TestJointypeForDirection_UniqueIfyFallback / the
	// nestloop 3b test: the ordinary MinLefthand/MinRighthand containment
	// check fails for both orientations, so admission can only come through
	// the fallback.
	sjinfo.MinLefthand, sjinfo.SynLefthand = lhs|extra, lhs|extra
	sjinfo.MinRighthand, sjinfo.SynRighthand = rhs, rhs

	lhsRel := scanRel(lhs, 10000, 100)
	joinrel := newRelOptInfo(lhs|rhs, 5000, 64)

	l, r := jsCol(0, "a_k"), jsCol(0, "k")
	clause := &restrictInfo{
		clause: &BinaryOp{Op: parser.OpEq, Left: l, Right: r},
		relids: lhs | rhs, leftKey: l, rightKey: r,
		leftRelids: lhs, rightRelids: rhs, isEquijoin: true, ecID: noEquivClass,
	}

	if err := addPathsToJoinrel(nil, joinrel, lhsRel, rhsRel, []*restrictInfo{clause}, cp, sjinfo); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}

	var hj *Path
	for _, p := range joinrel.Pathlist {
		if p.Kind == PathHashJoin {
			hj = p
			break
		}
	}
	if hj == nil {
		t.Fatalf("no PathHashJoin in %d paths, want one built over the unique-ified inner", len(joinrel.Pathlist))
	}
	if hj.Jointype != parser.JoinInner {
		t.Errorf("Jointype = %v, want JoinInner (PG's own demote-before-build, joinpath.c:116-121)", hj.Jointype)
	}
	if len(hj.Children) != 2 || hj.Children[1].Kind != PathUnique {
		t.Fatalf("Children = %v, want [outer, PathUnique]", hj.Children)
	}
	if hj.Children[1] != rhsRel.CheapestUnique {
		t.Error("hash join's build side is not the cached createUniquePath result")
	}
}
