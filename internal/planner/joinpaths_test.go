package planner

// M0127-P5.4a — `add_paths_to_joinrel`'s unparameterised core.
//
// The two things worth proving here are not "a path was produced". They are:
//
//   1. the KEY/RESIDUAL split is a property of the (pair, clause) combination,
//      not of the clause alone — the same clause is a hash key at one join and
//      an ordinary qual at another; and
//   2. a path always exists for every pair the enumerator can offer, including
//      the cartesian ones phase 1's clauseless branch and phase 3 produce,
//      because `joinSearch` treats an empty pathlist as a hard failure.
//
// Both are checked structurally rather than by example, so a mutation that
// narrows either one is caught rather than merely re-costed.

import "testing"

// relsetOf builds a relset from base-relation indices, matching the
// `RelSet(1)<<i` convention buildInitialRels uses.
func relsetOf(idx ...int) RelSet {
	var s RelSet
	for _, i := range idx {
		s |= RelSet(1) << uint(i)
	}
	return s
}

// equiClause is a canonical two-sided equality whose operands live on the given
// relsets — the shape `restrictInfo` records for a hashable clause. The operand
// expressions are synthetic ColumnRefs keyed off the relset, which is enough for
// every consumer at this seam: the key/residual split reads only the relsets,
// and the merge arm reads the operands solely to compare them for identity
// (`mergeKeyGroups`). Two clauses over the same pair therefore look like the
// same sort key, which is what a caller wanting distinct keys must override.
func equiClause(left, right RelSet) *restrictInfo {
	return &restrictInfo{
		relids:      left | right,
		leftKey:     col(int(left)),
		rightKey:    col(int(right)),
		leftRelids:  left,
		rightRelids: right,
		isEquijoin:  true,
		ecID:        noEquivClass,
	}
}

// equiClauseOn is equiClause with explicit operand expressions, for the cases
// where two clauses over one pair must be distinguishable sort keys.
func equiClauseOn(left, right RelSet, leftCol, rightCol int) *restrictInfo {
	ri := equiClause(left, right)
	ri.leftKey, ri.rightKey = col(leftCol), col(rightCol)
	return ri
}

// plainClause is a join qual with no two-sided operand split: an inequality, or
// an equality one of whose operands straddles both sides.
func plainClause(relids RelSet) *restrictInfo {
	return &restrictInfo{relids: relids, ecID: noEquivClass}
}

// scanRel is an initial rel with one costed seq-scan path, ready to be an input
// to addPathsToJoinrel.
func scanRel(relids RelSet, rows float64, pages int64) *RelOptInfo {
	rel := newRelOptInfo(relids, rows, 32)
	generateScanPaths(rel, defaultCostParams(), pages, 0, 0, true)
	setCheapest(rel)
	return rel
}

// TestSplitJoinClauses_SameClauseKeysAtOnePairAndNotAnother is the point of the
// per-pair split. `a.x = b.y + c.z` has operands {a} and {b,c}. At the pair
// ({a}, {b,c}) each operand is computable on one side, so it is a hash key. At
// ({a,b}, {c}) the right operand straddles both sides and no key can be formed,
// so the identical clause must be evaluated as an ordinary qual.
//
// A mutation that decided keyability once per clause (say, from `isEquijoin`
// alone) passes the first case and fails the second.
func TestSplitJoinClauses_SameClauseKeysAtOnePairAndNotAnother(t *testing.T) {
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)
	ri := equiClause(a, b|c)

	keys, residual := splitJoinClauses(a, b|c, []*restrictInfo{ri})
	if len(keys) != 1 || len(residual) != 0 {
		t.Fatalf("({a},{b,c}): got %d keys / %d residual, want 1 / 0", len(keys), len(residual))
	}

	keys, residual = splitJoinClauses(a|b, c, []*restrictInfo{ri})
	if len(keys) != 0 || len(residual) != 1 {
		t.Fatalf("({a,b},{c}): got %d keys / %d residual, want 0 / 1", len(keys), len(residual))
	}
}

// TestSplitJoinClauses_OperandOrderDoesNotMatter: `clause_sides_match_join`
// (joinpath.c:2205) accepts the operands in either order, which is what lets
// one key set serve both build orientations of a hash join.
func TestSplitJoinClauses_OperandOrderDoesNotMatter(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	ri := equiClause(a, b)
	for _, tc := range []struct{ outer, inner RelSet }{{a, b}, {b, a}} {
		keys, residual := splitJoinClauses(tc.outer, tc.inner, []*restrictInfo{ri})
		if len(keys) != 1 || len(residual) != 0 {
			t.Fatalf("outer=%#04x inner=%#04x: got %d keys / %d residual, want 1 / 0",
				uint16(tc.outer), uint16(tc.inner), len(keys), len(residual))
		}
	}
}

// TestSplitJoinClauses_NonEquijoinIsAlwaysResidual: an inequality join qual has
// no operand split to hash on, at any pair.
func TestSplitJoinClauses_NonEquijoinIsAlwaysResidual(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	keys, residual := splitJoinClauses(a, b, []*restrictInfo{plainClause(a | b)})
	if len(keys) != 0 || len(residual) != 1 {
		t.Fatalf("got %d keys / %d residual, want 0 / 1", len(keys), len(residual))
	}
}

// TestAddPathsToJoinrel_EveryUsableEqualityBecomesAKey — multi-column hash keys
// are the rule, not a special case (05 §5). Three equalities over the same pair
// all key; the inequality beside them does not.
func TestAddPathsToJoinrel_EveryUsableEqualityBecomesAKey(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 10000, 100), scanRel(b, 500, 5)
	joinrel := newRelOptInfo(a|b, 5000, 64)
	clauses := []*restrictInfo{
		equiClause(a, b), equiClause(a, b), equiClause(a, b), plainClause(a | b),
	}
	if err := addPathsToJoinrel(joinrel, outer, inner, clauses, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	var hash *Path
	for _, p := range joinrel.Pathlist {
		if p.Kind == PathHashJoin {
			hash = p
		}
	}
	if hash == nil {
		t.Fatalf("no hash path generated for a pair with usable equalities")
	}
	if len(hash.HashKeys) != 3 {
		t.Fatalf("hash path keys on %d clauses, want all 3 usable equalities", len(hash.HashKeys))
	}
	if len(hash.Residual) != 1 {
		t.Fatalf("hash path carries %d residual clauses, want the 1 inequality", len(hash.Residual))
	}
}

// TestAddPathsToJoinrel_CartesianPairStillGetsAPath is the invariant that keeps
// `joinSearch`'s "joinrel has no paths" error unreachable. Phase 1's clauseless
// branch and phase 3's last-ditch pass both offer pairs with an EMPTY clause
// list; without an unconditional nested loop the whole search would fail on
// them.
func TestAddPathsToJoinrel_CartesianPairStillGetsAPath(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 100, 2), scanRel(b, 50, 1)
	joinrel := newRelOptInfo(a|b, 5000, 64)
	if err := addPathsToJoinrel(joinrel, outer, inner, nil, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	if len(joinrel.Pathlist) != 1 {
		t.Fatalf("cartesian pair produced %d paths, want exactly the nested loop", len(joinrel.Pathlist))
	}
	if got := joinrel.Pathlist[0].Kind; got != PathNestLoop {
		t.Fatalf("cartesian pair path kind = %v, want PathNestLoop", got)
	}
}

// TestAddPathsToJoinrel_InequalityOnlyPairGetsNoHash: a join whose only qual is
// `a.x < b.y` is hash-unjoinable, and the nested loop is again the only path.
func TestAddPathsToJoinrel_InequalityOnlyPairGetsNoHash(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 100, 2), scanRel(b, 50, 1)
	joinrel := newRelOptInfo(a|b, 500, 64)
	clauses := []*restrictInfo{plainClause(a | b)}
	if err := addPathsToJoinrel(joinrel, outer, inner, clauses, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	for _, p := range joinrel.Pathlist {
		if p.Kind == PathHashJoin {
			t.Fatalf("an inequality-only pair must not produce a hash path")
		}
	}
	if len(joinrel.Pathlist) == 0 {
		t.Fatalf("an inequality-only pair must still produce a path")
	}
}

// TestAddPathsToJoinrel_NestLoopCarriesEveryClauseAsResidual: a plain nested
// loop keys on nothing, so the equalities that a hash join would have keyed on
// rejoin the residual and are evaluated per tuple pair.
func TestAddPathsToJoinrel_NestLoopCarriesEveryClauseAsResidual(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 100, 2), scanRel(b, 50, 1)
	joinrel := newRelOptInfo(a|b, 500, 64)
	// This pair also produces a hash path, whose total cost beats the nested
	// loop's by an order of magnitude while its startup cost is worse (it has
	// to build the table first). Since M0127-P5.7-b that is a path add_path
	// KEEPS only under a tuple fraction — `consider_startup` — so the fast-start
	// regime is what this generation test has to observe in. The claim under
	// test is qual PLACEMENT on the loop, not whether the tournament retains it.
	joinrel.ConsiderStartup = true
	clauses := []*restrictInfo{equiClause(a, b), plainClause(a | b)}
	if err := addPathsToJoinrel(joinrel, outer, inner, clauses, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	var nl *Path
	for _, p := range joinrel.Pathlist {
		if p.Kind == PathNestLoop {
			nl = p
		}
	}
	if nl == nil {
		t.Fatalf("no nested-loop path generated")
	}
	if len(nl.HashKeys) != 0 {
		t.Fatalf("a plain nested loop must key on nothing, got %d keys", len(nl.HashKeys))
	}
	if len(nl.Residual) != 2 {
		t.Fatalf("nested loop carries %d residual clauses, want all 2", len(nl.Residual))
	}
}

// TestAddPathsToJoinrel_SymmetricPairResolvesToTheFirstOfferedOrder is the
// deterministic tie-break (built on M0125-0047's rule: a tie must resolve to a
// total order, never to whichever candidate arrived first by chance).
//
// `makeJoinRel` calls addPathsToJoinrel TWICE per pair, once per input order.
// When the two inputs are statistically identical — which a self-join makes
// unavoidable, since the two aliases are the same relation — the two hash
// orientations cost exactly the same. `addPath` resolves that by keeping the
// INCUMBENT on an exact tie, so the survivor is the orientation offered first,
// and the plan is stable. Repeating the whole construction proves the result
// does not depend on any iteration order.
func TestAddPathsToJoinrel_SymmetricPairResolvesToTheFirstOfferedOrder(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	cp := defaultCostParams()
	for iter := 0; iter < 20; iter++ {
		rel1, rel2 := scanRel(a, 200000, 2000), scanRel(b, 200000, 2000)
		joinrel := newRelOptInfo(a|b, 200000, 64)
		clauses := []*restrictInfo{equiClause(a, b)}
		// The two calls makeJoinRel makes, in its order.
		if err := addPathsToJoinrel(joinrel, rel1, rel2, clauses, cp); err != nil {
			t.Fatalf("addPathsToJoinrel(rel1, rel2): %v", err)
		}
		if err := addPathsToJoinrel(joinrel, rel2, rel1, clauses, cp); err != nil {
			t.Fatalf("addPathsToJoinrel(rel2, rel1): %v", err)
		}
		setCheapest(joinrel)

		var hashes []*Path
		for _, p := range joinrel.Pathlist {
			if p.Kind == PathHashJoin {
				hashes = append(hashes, p)
			}
		}
		if len(hashes) != 1 {
			t.Fatalf("iter %d: %d surviving hash paths, want 1 — the tied mirror image must be rejected", iter, len(hashes))
		}
		// Children[0] is the probe side: the rel offered as `outer` first.
		if hashes[0].Children[0] != rel1.CheapestTotal {
			t.Fatalf("iter %d: the tie resolved to the second-offered order", iter)
		}
	}
}

// TestAddPathsToJoinrel_MissingCheapestPathIsLoud: every rel the search offers
// has been through setCheapest, so a nil cheapest path is a broken invariant,
// not a pair to skip quietly.
func TestAddPathsToJoinrel_MissingCheapestPathIsLoud(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	good := scanRel(a, 100, 2)
	bare := newRelOptInfo(b, 100, 32) // no paths -> CheapestTotal nil
	joinrel := newRelOptInfo(a|b, 100, 64)
	if err := addPathsToJoinrel(joinrel, good, bare, nil, defaultCostParams()); err == nil {
		t.Fatalf("a nil inner cheapest path must be reported, not skipped")
	}
	if err := addPathsToJoinrel(joinrel, bare, good, nil, defaultCostParams()); err == nil {
		t.Fatalf("a nil outer cheapest path must be reported, not skipped")
	}
	if len(joinrel.Pathlist) != 0 {
		t.Fatalf("a refused pair must not have added paths")
	}
}

// TestQualPlacementIsExactlyOncePerJoinTree is 03 §5.4's placement rule stated
// as an invariant rather than an example: along ANY complete join tree over the
// relations, every join clause is applied at exactly one join — the lowest one
// whose relids cover it.
//
// The check runs all three spanning shapes of a 3-relation triangle. Under-
// application would mean a lost qual (wrong rows); over-application would mean
// a qual charged and evaluated twice (the bug class the post-hoc placement
// passes of 08 §3 exist to work around). Since `clausesFor` is what decides
// this and `splitJoinClauses` only re-labels its output, counting keys +
// residual per join is the honest total.
func TestQualPlacementIsExactlyOncePerJoinTree(t *testing.T) {
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)
	ab, bc, ac := equiClause(a, b), equiClause(b, c), equiClause(a, c)
	list := &restrictInfoList{all: []*restrictInfo{ab, bc, ac}}

	// The three left-deep shapes; the bushy shapes coincide with these at 3
	// relations (one side is always a singleton).
	trees := []struct {
		name     string
		lo1, lo2 RelSet // the level-2 pair
		hiOther  RelSet // the remaining relation, joined at level 3
	}{
		{"(a b) c", a, b, c},
		{"(a c) b", a, c, b},
		{"(b c) a", b, c, a},
	}
	for _, tr := range trees {
		applied := map[*restrictInfo]int{}
		count := func(outer, inner RelSet) {
			keys, residual := splitJoinClauses(outer, inner, list.clausesFor(outer, inner))
			for _, ri := range keys {
				applied[ri]++
			}
			for _, ri := range residual {
				applied[ri]++
			}
		}
		count(tr.lo1, tr.lo2)
		count(tr.lo1|tr.lo2, tr.hiOther)

		for _, ri := range list.all {
			if applied[ri] != 1 {
				t.Errorf("%s: clause %#04x applied %d times, want exactly 1",
					tr.name, uint16(ri.relids), applied[ri])
			}
		}
	}
}
