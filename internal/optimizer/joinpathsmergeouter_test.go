package optimizer

// M0127-P5.4c-ii-c — `generate_mergejoin_paths`.
//
// The claims worth pinning are the ones that change which plan wins, not "a
// merge path appeared" (P5.4c-i already pins that):
//
//  1. an ordered outer is consumed WITHOUT a sort, and is reachable only through
//     the pathlist — an ordered path is never the cheapest total, so an arm keyed
//     to `CheapestTotal` would find nothing;
//  2. the mergeclause list is a PREFIX of the outer's ordering and stops at the
//     first unserved position — an ordering whose FIRST key has no clause is
//     unusable, not usable on its later keys;
//  3. the result carries the outer's FULL ordering, which is what lets a merge
//     one level up skip its own sort;
//  4. truncation trades merge keys for a cheaper presorted inner, and the
//     clauses it drops become RESIDUAL rather than disappearing;
//  5. the inner ordering copies the outer's DIRECTION, so a descending outer is
//     usable; and
//  6. the "strictly cheaper" rule stops the loop from emitting a shorter-keyed
//     path that buys nothing.

import "testing"

// orderedPathRel is a rel with a cheap unordered path AND a dearer ordered one,
// which is the shape P5.4c-ii-b actually produces: an ordered index scan prices
// at `max_IO_cost` (no correlation statistic), so it loses on cost and survives
// `addPath` only on its pathkeys. Both paths are in the pathlist; `CheapestTotal`
// is the unordered one.
func orderedPathRel(relids RelSet, rows float64, keys []PathKey, orderedTotal float64) *RelOptInfo {
	rel := newRelOptInfo(relids, rows, 32)
	addPath(rel, &Path{Kind: PathSeqScan, Rel: rel, Rows: rows, Cost: Cost{Total: 10}})
	addPath(rel, &Path{Kind: PathIndexScan, Rel: rel, Rows: rows, Cost: Cost{Total: orderedTotal}, Pathkeys: keys})
	setCheapest(rel)
	return rel
}

func ascKeys(cols ...int) []PathKey {
	pks := make([]PathKey, len(cols))
	for i, c := range cols {
		pks[i] = PathKey{Expr: col(c), SortAsc: true}
	}
	return pks
}

// unsortedMergePaths returns the merge paths whose outer child is NOT a Sort —
// i.e. the ones only this arm can produce.
func unsortedMergePaths(rel *RelOptInfo) []*Path {
	var out []*Path
	for _, p := range mergePathsOf(rel) {
		if len(p.Children) == 2 && p.Children[0].Kind != PathSort {
			out = append(out, p)
		}
	}
	return out
}

// TestMatchUnsortedOuterMerge_ConsumesOrderedOuterWithoutSorting is claim 1. The
// ordered outer path costs MORE than the rel's cheapest, so it is not
// `CheapestTotal` and `sort_inner_and_outer` will never see it; the only way a
// merge reaches it is the pathlist walk. And having reached it, the merge must
// not re-sort it.
func TestMatchUnsortedOuterMerge_ConsumesOrderedOuterWithoutSorting(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer := orderedPathRel(a, 100, ascKeys(10), 40)
	inner := scanRel(b, 100, 10)
	joinrel := newRelOptInfo(a|b, 100, 32)
	// The ordered outer costs more than the cheapest one, so the merge built on
	// it costs more in TOTAL than the sorted-outer merge with the same result
	// ordering, and wins only on startup (it skips the sort). Since
	// M0127-P5.7-b add_path keeps such a path only under a tuple fraction, so
	// this arm is observed in the fast-start regime; what is under test is that
	// the arm REACHES the ordered path at all.
	joinrel.ConsiderStartup = true
	ri := equiClauseOn(a, b, 10, 11)

	if outer.CheapestTotal.Pathkeys != nil {
		t.Fatalf("fixture broken: the ordered path must NOT be cheapest-total")
	}
	if err := addPathsToJoinrel(nil, joinrel, outer, inner, []*restrictInfo{ri}, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}

	got := unsortedMergePaths(joinrel)
	if len(got) == 0 {
		t.Fatalf("no merge path consumed the ordered outer; only this arm can reach it")
	}
	for _, p := range got {
		if !pathkeysContainedIn(p.Children[0].Pathkeys, ascKeys(10)) {
			t.Fatalf("outer child does not deliver the merge ordering")
		}
	}
}

// TestMatchUnsortedOuterMerge_ClauseListIsAPrefixOfTheOrdering is claim 2, the
// asymmetry that is easy to get wrong in the permissive direction. An outer
// sorted by (x, y) joined only on `y` is NOT a `y`-ordered input: a merge
// consumes its input in sort order and cannot skip the leading column. PG stops
// at the first pathkey with no clause, so the whole arm declines here.
func TestMatchUnsortedOuterMerge_ClauseListIsAPrefixOfTheOrdering(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	// Ordered by col 10 then col 20; the only join clause keys on col 20.
	outer := orderedPathRel(a, 100, ascKeys(10, 20), 40)
	inner := scanRel(b, 100, 10)
	joinrel := newRelOptInfo(a|b, 100, 32)
	ri := equiClauseOn(a, b, 20, 11)

	if err := addPathsToJoinrel(nil, joinrel, outer, inner, []*restrictInfo{ri}, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	if got := unsortedMergePaths(joinrel); len(got) != 0 {
		t.Fatalf("an outer whose FIRST sort key has no clause must yield no unsorted merge, got %d", len(got))
	}
	// The sort-both arm is unaffected — the pair is still mergeable, just not
	// by exploiting this ordering.
	if len(mergePathsOf(joinrel)) == 0 {
		t.Fatalf("sort_inner_and_outer must still have produced a merge path")
	}
}

// TestMatchUnsortedOuterMerge_ResultKeepsTheOuterFullOrdering is claim 3. The
// merge keys on col 10 only, but the outer is sorted (10, 20) and the join
// preserves that — `merge_pathkeys` is `build_join_pathkeys` of the outer PATH's
// pathkeys, not of the merge keys. A merge above this one can consume the (10,
// 20) ordering, which is the entire compounding effect the arm exists for.
func TestMatchUnsortedOuterMerge_ResultKeepsTheOuterFullOrdering(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer := orderedPathRel(a, 100, ascKeys(10, 20), 40)
	inner := scanRel(b, 100, 10)
	joinrel := newRelOptInfo(a|b, 100, 32)
	ri := equiClauseOn(a, b, 10, 11)

	if err := addPathsToJoinrel(nil, joinrel, outer, inner, []*restrictInfo{ri}, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	got := unsortedMergePaths(joinrel)
	if len(got) == 0 {
		t.Fatalf("no merge path consumed the ordered outer")
	}
	for _, p := range got {
		if len(p.Pathkeys) != 2 {
			t.Fatalf("result ordering has %d keys, want the outer's full 2 — truncating to the merge keys loses the (10,20) order a merge above could use", len(p.Pathkeys))
		}
		if len(p.HashKeys) != 1 {
			t.Fatalf("merge keys = %d, want 1 — only col 10 has a clause", len(p.HashKeys))
		}
	}
}

// TestGenerateMergejoinPaths_TruncationDemotesDroppedClauseToResidual is claim 4
// and the one that would have been a silently wrong plan. With two merge keys the
// inner must be sorted (11, 21); the inner rel offers a path sorted on (11) only.
// PG's truncation loop takes it and demotes the second clause — `create_mergejoin_plan`
// computes qpqual as restrictlist minus mergeclauses. If the dropped clause simply
// vanished, the join would emit rows that never satisfied it.
func TestGenerateMergejoinPaths_TruncationDemotesDroppedClauseToResidual(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer := orderedPathRel(a, 100, ascKeys(10, 20), 40)

	// Inner: an expensive path sorted on both keys, and a CHEAP one sorted on
	// the first key only. The cheap prefix-sorted path is what the truncation
	// loop is for.
	inner := newRelOptInfo(b, 100, 32)
	addPath(inner, &Path{Kind: PathSeqScan, Rel: inner, Rows: 100, Cost: Cost{Total: 90}})
	addPath(inner, &Path{Kind: PathIndexScan, Rel: inner, Rows: 100, Cost: Cost{Total: 20}, Pathkeys: ascKeys(11)})
	setCheapest(inner)

	joinrel := newRelOptInfo(a|b, 100, 32)
	// Same reason as claim 1: the truncated merge keeps a cheaper-startup,
	// dearer-total shape, which add_path retains only under a tuple fraction
	// (M0127-P5.7-b).
	joinrel.ConsiderStartup = true
	c1 := equiClauseOn(a, b, 10, 11)
	c2 := equiClauseOn(a, b, 20, 21)
	if err := addPathsToJoinrel(nil, joinrel, outer, inner, []*restrictInfo{c1, c2}, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}

	var truncated *Path
	for _, p := range unsortedMergePaths(joinrel) {
		if len(p.HashKeys) == 1 && p.Children[1].Kind == PathIndexScan {
			truncated = p
		}
	}
	if truncated == nil {
		t.Fatalf("the prefix-sorted inner produced no truncated merge path")
	}
	if !exprEqual(truncated.HashKeys[0].leftKey, col(10)) {
		t.Fatalf("the surviving merge key must be the FIRST, the one the inner is sorted by")
	}
	found := false
	for _, r := range truncated.Residual {
		if r == c2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the dropped merge clause was not demoted to residual — the join would never evaluate it")
	}
}

// TestTrimMergeClausesForInnerPathkeys_StopsAtTheFirstUncoveredKey pins the
// trimmer directly, including the case the caller must survive: an inner ordering
// that covers NONE of the clauses yields an empty list rather than a wrong one.
func TestTrimMergeClausesForInnerPathkeys_StopsAtTheFirstUncoveredKey(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	c1 := equiClauseOn(a, b, 10, 11)
	c2 := equiClauseOn(a, b, 20, 21)
	all := []*restrictInfo{c1, c2}

	if got := trimMergeClausesForInnerPathkeys(all, ascKeys(11), a); len(got) != 1 || got[0] != c1 {
		t.Fatalf("a one-key inner ordering must keep exactly the first clause, got %d", len(got))
	}
	if got := trimMergeClausesForInnerPathkeys(all, ascKeys(11, 21), a); len(got) != 2 {
		t.Fatalf("the full inner ordering must keep both clauses, got %d", len(got))
	}
	if got := trimMergeClausesForInnerPathkeys(all, ascKeys(99), a); len(got) != 0 {
		t.Fatalf("an ordering covering no clause must trim to nothing, got %d", len(got))
	}
	if got := trimMergeClausesForInnerPathkeys(all, nil, a); got != nil {
		t.Fatalf("no pathkeys => no mergeclauses")
	}
}

// TestMergeInnerSortKeys_CopiesTheOuterDirection is claim 5. A merge needs only
// that the two sides AGREE on an ordering, so a DESCENDING outer is perfectly
// usable — the inner is sorted to match it (`make_inner_pathkeys_for_merge`
// copies pk_cmptype and pk_nulls_first, pathkeys.c:1911-1915). Assuming
// ascending here would silently produce a plan whose two inputs walk in opposite
// directions.
func TestMergeInnerSortKeys_CopiesTheOuterDirection(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	groups := mergeKeyGroups([]*restrictInfo{equiClauseOn(a, b, 10, 11)}, a)
	desc := []PathKey{{Expr: col(10), SortAsc: false, NullsFirst: true}}

	got := mergeInnerSortKeys(groups, desc, a)
	if len(got) != 1 {
		t.Fatalf("one clause => one inner key, got %d", len(got))
	}
	if got[0].SortAsc || !got[0].NullsFirst {
		t.Fatalf("the inner key must copy the outer's direction and null placement, got %+v", got[0])
	}
}

// TestMergeInnerSortKeys_OneOuterKeyCanOweSeveralInnerKeys is the P5.4c-i hole
// this slice had to close to build on. `a.x = c.x AND a.x = c.y` is ONE outer sort
// key and TWO inner ones: both clauses stay merge clauses, so the operator
// compares on both, and an inner sorted only by `c.x` would be fed to a merge
// expecting `(c.x, c.y)` order. PG carries `outersortkeys` and `innersortkeys` as
// independent lists precisely so they can differ in length.
func TestMergeInnerSortKeys_OneOuterKeyCanOweSeveralInnerKeys(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	c1 := equiClauseOn(a, b, 10, 11)
	c2 := equiClauseOn(a, b, 10, 21)

	groups := mergeKeyGroups([]*restrictInfo{c1, c2}, a)
	if len(groups) != 1 {
		t.Fatalf("one outer expression is one sort key, got %d groups", len(groups))
	}
	got := mergeInnerSortKeys(groups, []PathKey{groups[0].outerKey}, a)
	if len(got) != 2 {
		t.Fatalf("the inner owes an ordering on BOTH operands, got %d keys", len(got))
	}
	if !exprEqual(got[0].Expr, col(11)) || !exprEqual(got[1].Expr, col(21)) {
		t.Fatalf("inner keys in clause order, got %v", got)
	}
}

// TestFindMergeClausesForOuterPathkeys_TakesThePrefixOnly pins the selector
// directly: a matching key after a non-matching one is unreachable, because a
// merge cannot skip a sort column.
func TestFindMergeClausesForOuterPathkeys_TakesThePrefixOnly(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	groups := mergeKeyGroups([]*restrictInfo{
		equiClauseOn(a, b, 10, 11),
		equiClauseOn(a, b, 30, 31),
	}, a)

	if got := findMergeClausesForOuterPathkeys(ascKeys(10, 30), groups); len(got) != 2 {
		t.Fatalf("both ordering positions have clauses, want 2 matches, got %d", len(got))
	}
	if got := findMergeClausesForOuterPathkeys(ascKeys(10, 99, 30), groups); len(got) != 1 {
		t.Fatalf("the run must stop at the unserved position, got %d matches", len(got))
	}
	if got := findMergeClausesForOuterPathkeys(ascKeys(99, 10), groups); len(got) != 0 {
		t.Fatalf("an ordering whose FIRST key has no clause is unusable, got %d matches", len(got))
	}
}

// TestGetCheapestPathForPathkeys_ExcludesParameterisedAndKeepsTheIncumbent pins
// the two decisions in the selector that are not "cheapest wins": a parameterised
// path is never eligible (PG: "we do not consider parameterized inner paths
// here"), and an exact cost tie keeps the incumbent so the scan is stable in
// pathlist order.
func TestGetCheapestPathForPathkeys_ExcludesParameterisedAndKeepsTheIncumbent(t *testing.T) {
	rel := newRelOptInfo(relsetOf(0), 10, 32)
	keys := ascKeys(10)
	first := &Path{Kind: PathIndexScan, Rel: rel, Rows: 10, Cost: Cost{Total: 5}, Pathkeys: keys}
	tie := &Path{Kind: PathIndexScan, Rel: rel, Rows: 10, Cost: Cost{Total: 5}, Pathkeys: keys}
	cheapParam := &Path{Kind: PathIndexScan, Rel: rel, Rows: 10, Cost: Cost{Total: 1}, Pathkeys: keys, RequiredOuter: relsetOf(3)}

	got := getCheapestPathForPathkeys([]*Path{first, tie, cheapParam}, keys, totalCost)
	if got != first {
		t.Fatalf("an exact tie must keep the incumbent and a parameterised path must be ineligible")
	}
	if got := getCheapestPathForPathkeys([]*Path{cheapParam}, keys, totalCost); got != nil {
		t.Fatalf("a parameterised path is not a usable merge inner")
	}
	unordered := &Path{Kind: PathSeqScan, Rel: rel, Rows: 10, Cost: Cost{Total: 1}}
	if got := getCheapestPathForPathkeys([]*Path{unordered}, keys, totalCost); got != nil {
		t.Fatalf("an unordered path does not satisfy the requested ordering")
	}
}

// TestGenerateMergejoinPaths_StrictlyCheaperRuleSuppressesAPointlessTruncation is
// claim 6. When the inner path that satisfies the FULL key list is also the
// cheapest thing any shorter prefix can find, PG emits no truncated path: using
// fewer merge keys than the input allows is strictly worse (the demoted clause
// becomes per-tuple work) and the "strictly cheaper" test is what forbids it.
func TestGenerateMergejoinPaths_StrictlyCheaperRuleSuppressesAPointlessTruncation(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer := orderedPathRel(a, 100, ascKeys(10, 20), 40)

	inner := newRelOptInfo(b, 100, 32)
	addPath(inner, &Path{Kind: PathSeqScan, Rel: inner, Rows: 100, Cost: Cost{Total: 90}})
	// One ordered path, satisfying BOTH keys. Every prefix search finds this
	// same path, so nothing is ever strictly cheaper than it.
	addPath(inner, &Path{Kind: PathIndexScan, Rel: inner, Rows: 100, Cost: Cost{Total: 20}, Pathkeys: ascKeys(11, 21)})
	setCheapest(inner)

	joinrel := newRelOptInfo(a|b, 100, 32)
	// Same reason as claim 1: the truncated merge keeps a cheaper-startup,
	// dearer-total shape, which add_path retains only under a tuple fraction
	// (M0127-P5.7-b).
	joinrel.ConsiderStartup = true
	c1 := equiClauseOn(a, b, 10, 11)
	c2 := equiClauseOn(a, b, 20, 21)
	if err := addPathsToJoinrel(nil, joinrel, outer, inner, []*restrictInfo{c1, c2}, defaultCostParams()); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}

	for _, p := range unsortedMergePaths(joinrel) {
		if len(p.HashKeys) < 2 {
			t.Fatalf("emitted a %d-key merge where a 2-key one over the same inner exists — the strictly-cheaper rule must suppress it", len(p.HashKeys))
		}
	}
}
