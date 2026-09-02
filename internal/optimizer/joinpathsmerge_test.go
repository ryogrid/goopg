package optimizer

// M0127-P5.4c-i — `sort_inner_and_outer`.
//
// The claims worth pinning here are structural, not "a merge path appeared":
//
//  1. the sort-key list is per EQUIVALENCE CLASS, not per clause — two clauses
//     that are transitively one restriction sort on one column and both stay
//     merge clauses;
//  2. the key orientation is a property of the PAIR, like `isKeyableFor`'s — the
//     same clause faces the other way when the sides swap;
//  3. one path per ordering, because this join's OUTPUT ordering is what decides
//     whether a merge above it needs a sort at all; and
//  4. the sort is skipped, and paid for, exactly when the input's own ordering
//     says so — the branch that makes P5.4c-ii's ordered inputs worth having.

import "testing"

// orderedRel is a rel whose single (cheapest) path already delivers `keys` — the
// shape P5.4c-ii's ordered index paths will produce and that nothing in the
// search produces yet.
func orderedRel(relids RelSet, rows float64, cost Cost, keys []PathKey) *RelOptInfo {
	rel := newRelOptInfo(relids, rows, 32)
	addPath(rel, &Path{Kind: PathIndexScan, Rel: rel, Rows: rows, Cost: cost, Pathkeys: keys}, "test")
	setCheapest(rel)
	return rel
}

// paramRel is a rel whose only path is parameterised by `by` — a third relation,
// so `addPathsToJoinrel`'s own PATH_PARAM_BY_REL refusals do not fire and the
// merge arm's `required_outer` test is what has to reject it.
func paramRel(relids RelSet, rows float64, by RelSet) *RelOptInfo {
	rel := newRelOptInfo(relids, rows, 32)
	addPath(rel, &Path{
		Kind: PathIndexScan, Rel: rel, Rows: rows,
		Cost: Cost{Startup: 0, Total: 5}, RequiredOuter: by,
	}, "test")
	setCheapest(rel)
	return rel
}

func mergePathsOf(rel *RelOptInfo) []*Path {
	var out []*Path
	for _, p := range rel.Pathlist {
		if p.Kind == PathMergeJoin {
			out = append(out, p)
		}
	}
	return out
}

// TestMergeKeyGroups_DedupesByEquivalenceClass is the rule that makes a merge
// join over an inferred-equality cluster affordable. At ({a,b}, {c}) with
// `a.x = c.x` and `b.x = c.x`, the two clauses are one equivalence class:
// `a.x = b.x` already holds inside the outer, so ONE sort key orders the outer
// for both. Emitting two would charge for a two-column sort that does no extra
// work and would then fail to match a one-column ordered input.
//
// Both clauses must nevertheless survive as merge clauses — PG drops pathkeys
// here, never clauses (`select_outer_pathkeys_for_merge` reduces the pathkey
// list; `find_mergeclauses_for_outer_pathkeys` still returns them all).
func TestMergeKeyGroups_DedupesByEquivalenceClass(t *testing.T) {
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)
	ac := equiClauseOn(a, c, 10, 12)
	bc := equiClauseOn(b, c, 11, 12)
	ac.ecID, bc.ecID = 7, 7

	groups := mergeKeyGroups([]*restrictInfo{ac, bc}, a|b)
	if len(groups) != 1 {
		t.Fatalf("two clauses of one equivalence class produced %d sort keys, want 1", len(groups))
	}
	if len(groups[0].clauses) != 2 {
		t.Fatalf("the deduped key serves %d clauses, want both", len(groups[0].clauses))
	}
	if !exprEqual(groups[0].outerKey.Expr, col(10)) {
		t.Fatalf("the surviving key must be the FIRST clause's outer operand")
	}
}

// TestMergeKeyGroups_DedupesIdenticalExpressions covers the same reduction one
// level down. goopg's pathkeys are syntactic (design ch. 04 §2.1), so two
// clauses can induce the identical sort key without the classifier having put
// them in one class — PG cannot reach this case because a canonical pathkey is
// per-EC by construction.
func TestMergeKeyGroups_DedupesIdenticalExpressions(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	groups := mergeKeyGroups([]*restrictInfo{equiClause(a, b), equiClause(a, b)}, a)
	if len(groups) != 1 {
		t.Fatalf("two clauses on the same expression produced %d sort keys, want 1", len(groups))
	}
	if len(groups[0].clauses) != 2 {
		t.Fatalf("the deduped key serves %d clauses, want both", len(groups[0].clauses))
	}
}

// TestMergeKeyGroups_OrientationFollowsThePair: the clause records `left`/`right`
// as written, and either side can be the outer. A mutation that trusted
// `leftKey` to be the outer operand passes the first direction and sorts the
// wrong column in the second — which is a WRONG PLAN, not a slow one, since the
// merge would then compare unsorted input.
func TestMergeKeyGroups_OrientationFollowsThePair(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	ri := equiClauseOn(a, b, 3, 9)

	fwd := mergeKeyGroups([]*restrictInfo{ri}, a)
	if len(fwd) != 1 || !exprEqual(fwd[0].outerKey.Expr, col(3)) || !exprEqual(fwd[0].innerKey.Expr, col(9)) {
		t.Fatalf("outer={a}: keys oriented wrongly: %+v", fwd)
	}
	rev := mergeKeyGroups([]*restrictInfo{ri}, b)
	if len(rev) != 1 || !exprEqual(rev[0].outerKey.Expr, col(9)) || !exprEqual(rev[0].innerKey.Expr, col(3)) {
		t.Fatalf("outer={b}: keys must swap with the pair: %+v", rev)
	}
}

// TestMergeKeyGroups_MissingOperandsRefuseTheWholeArm: a key clause with no
// recorded operand split has no expression to sort on. Skipping just that clause
// would silently drop a qual from the merge path's clause list — the merge would
// return rows the join must not return. Refusing the arm leaves the clause where
// the hash and nested-loop arms already carry it.
func TestMergeKeyGroups_MissingOperandsRefuseTheWholeArm(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	broken := equiClause(a, b)
	broken.rightKey = nil
	if groups := mergeKeyGroups([]*restrictInfo{equiClause(a, b), broken}, a); groups != nil {
		t.Fatalf("a clause with no operand split must refuse the arm, got %d groups", len(groups))
	}
}

// TestSortInnerAndOuter_OnePathPerSortKey is the ordering loop
// (joinpath.c:1447-1466). Two independent merge keys give two equally-costed
// results that differ only in ORDER, and PG has no basis at this level for
// preferring either — so it generates both, and a merge one level up finds the
// partner it needs already sorted. `addPath` keeps them precisely because their
// pathkeys are incomparable; on cost alone one would prune the other.
func TestSortInnerAndOuter_OnePathPerSortKey(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 10000, 100), scanRel(b, 5000, 50)
	joinrel := newRelOptInfo(a|b, 2000, 64)
	keys := []*restrictInfo{equiClauseOn(a, b, 1, 2), equiClauseOn(a, b, 3, 4)}

	sortInnerAndOuter(joinrel, outer, inner, defaultCostParams(), keys, nil)

	paths := mergePathsOf(joinrel)
	if len(paths) != 2 {
		t.Fatalf("two independent merge keys produced %d paths, want one per key", len(paths))
	}
	if exprEqual(paths[0].Pathkeys[0].Expr, paths[1].Pathkeys[0].Expr) {
		t.Fatalf("the two paths must lead with DIFFERENT sort keys")
	}
	for i, p := range paths {
		if len(p.Pathkeys) != 2 {
			t.Fatalf("path %d orders on %d keys, want both", i, len(p.Pathkeys))
		}
		if len(p.HashKeys) != 2 {
			t.Fatalf("path %d keys on %d clauses, want both mergeclauses", i, len(p.HashKeys))
		}
	}
}

// TestSortInnerAndOuter_OutputOrderingIsTheOuters — `build_join_pathkeys`
// (pathkeys.c:1295): a merge join emits its outer's order. This is the fact the
// ordering loop above exists to exploit, so it is asserted rather than assumed.
func TestSortInnerAndOuter_OutputOrderingIsTheOuters(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	outer, inner := scanRel(a, 1000, 10), scanRel(b, 800, 8)
	joinrel := newRelOptInfo(a|b, 500, 64)
	sortInnerAndOuter(joinrel, outer, inner, defaultCostParams(),
		[]*restrictInfo{equiClauseOn(a, b, 5, 6)}, nil)

	paths := mergePathsOf(joinrel)
	if len(paths) != 1 {
		t.Fatalf("one merge key produced %d paths, want 1", len(paths))
	}
	if len(paths[0].Pathkeys) != 1 || !exprEqual(paths[0].Pathkeys[0].Expr, col(5)) {
		t.Fatalf("join ordering = %+v, want the OUTER operand col(5)", paths[0].Pathkeys)
	}
	// Both sides had to be sorted: neither seq scan carries an ordering.
	for side, child := range paths[0].Children {
		if child.Kind != PathSort {
			t.Fatalf("child %d is %v, want an explicit Sort over an unordered input", side, child.Kind)
		}
	}
}

// TestSortInnerAndOuter_SkipsSortWhenInputAlreadyOrdered is `try_mergejoin_path`
// :1091-1097. The branch is unreachable from the live search today — no path
// carries pathkeys — and is written now because it is the CONSUMER half of
// P5.4c-ii: landing it here means that slice adds ordered index paths and gets
// the saving, rather than having to add both halves and discover the interface
// only then.
//
// The saving is asserted as a cost inequality against the same join over an
// equally-cheap but UNORDERED outer, so the test fails if the sort is charged
// anyway.
func TestSortInnerAndOuter_SkipsSortWhenInputAlreadyOrdered(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	cp := defaultCostParams()
	keys := []*restrictInfo{equiClauseOn(a, b, 5, 6)}
	scanCost := Cost{Startup: 0, Total: 100}

	ordered := orderedRel(a, 10000, scanCost, []PathKey{{Expr: col(5), SortAsc: true}})
	unordered := orderedRel(a, 10000, scanCost, nil)
	inner := scanRel(b, 5000, 50)

	costOf := func(outer *RelOptInfo) *Path {
		joinrel := newRelOptInfo(a|b, 2000, 64)
		sortInnerAndOuter(joinrel, outer, inner, cp, keys, nil)
		paths := mergePathsOf(joinrel)
		if len(paths) != 1 {
			t.Fatalf("got %d merge paths, want 1", len(paths))
		}
		return paths[0]
	}

	pre, plain := costOf(ordered), costOf(unordered)
	if pre.Children[0].Kind == PathSort {
		t.Fatalf("an outer that already delivers the ordering must not be re-sorted")
	}
	if plain.Children[0].Kind != PathSort {
		t.Fatalf("an unordered outer must be sorted")
	}
	if !(pre.Cost.Total < plain.Cost.Total) {
		t.Fatalf("skipping the sort must be cheaper: %v vs %v", pre.Cost.Total, plain.Cost.Total)
	}
	// The saving must be the sort's own cost, not a rounding difference.
	if want := costSortRun(cp, 10000, relNCols(unordered)).Total; plain.Cost.Total-pre.Cost.Total < want*0.9 {
		t.Fatalf("saving %v is far below the sort cost %v — the sort was charged twice or not at all",
			plain.Cost.Total-pre.Cost.Total, want)
	}
}

// TestSortInnerAndOuter_RefusesAParameterisedResult — `try_mergejoin_path`
// :1073-1081. A merge join discharges nothing, so an input parameterised by a
// THIRD relation leaves the join parameterised. `param_source_rels` is empty in
// v1, so PG's own overlap test rejects it; there is no `allow_star_schema_join`
// escape for merge. This keeps P5.4b-ii-b-1's invariant intact: every JOIN path
// in the search is unparameterised.
func TestSortInnerAndOuter_RefusesAParameterisedResult(t *testing.T) {
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)
	outer := paramRel(a, 100, c) // parameterised by c, not by the inner
	inner := scanRel(b, 500, 5)
	joinrel := newRelOptInfo(a|b, 200, 64)

	sortInnerAndOuter(joinrel, outer, inner, defaultCostParams(),
		[]*restrictInfo{equiClauseOn(a, b, 1, 2)}, nil)

	if got := len(mergePathsOf(joinrel)); got != 0 {
		t.Fatalf("a still-parameterised merge result must be refused, got %d paths", got)
	}
}

// TestAddPathsToJoinrel_MergeArmRunsForAnEqualityPair wires the arm to its
// caller: the same clause set that yields a hash path must also yield a merge
// path, and a pair with no usable equality must yield neither.
func TestAddPathsToJoinrel_MergeArmRunsForAnEqualityPair(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	cp := defaultCostParams()

	joinrel := newRelOptInfo(a|b, 2000, 64)
	if err := addPathsToJoinrel(nil, joinrel, scanRel(a, 10000, 100), scanRel(b, 5000, 50),
		[]*restrictInfo{equiClause(a, b)}, cp); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	if len(mergePathsOf(joinrel)) == 0 {
		t.Fatalf("an equality pair must produce a merge path")
	}

	bare := newRelOptInfo(a|b, 2000, 64)
	if err := addPathsToJoinrel(nil, bare, scanRel(a, 100, 2), scanRel(b, 50, 1),
		[]*restrictInfo{plainClause(a | b)}, cp); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	if got := len(mergePathsOf(bare)); got != 0 {
		t.Fatalf("an inequality-only pair must produce no merge path, got %d", got)
	}
}

// TestSortInnerAndOuter_ResidualRidesTheJoinOutput: clauses that could not be
// merge keys are still evaluated by the join, and on the tuples that already
// matched the keys — the same charge the hash arm makes. A mutation that dropped
// the residual would produce a cheaper path returning too many rows.
func TestSortInnerAndOuter_ResidualRidesTheJoinOutput(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	cp := defaultCostParams()
	keys := []*restrictInfo{equiClauseOn(a, b, 5, 6)}
	residual := []*restrictInfo{plainClause(a | b)}

	withRes := newRelOptInfo(a|b, 2000, 64)
	sortInnerAndOuter(withRes, scanRel(a, 10000, 100), scanRel(b, 5000, 50), cp, keys, residual)
	without := newRelOptInfo(a|b, 2000, 64)
	sortInnerAndOuter(without, scanRel(a, 10000, 100), scanRel(b, 5000, 50), cp, keys, nil)

	got, base := mergePathsOf(withRes), mergePathsOf(without)
	if len(got) != 1 || len(base) != 1 {
		t.Fatalf("expected one merge path each, got %d / %d", len(got), len(base))
	}
	if len(got[0].Residual) != 1 {
		t.Fatalf("merge path carries %d residual clauses, want the 1 inequality", len(got[0].Residual))
	}
	want := qualEvalCost(cp, 1, 2000)
	if diff := got[0].Cost.Total - base[0].Cost.Total; diff < want*0.99 || diff > want*1.01 {
		t.Fatalf("residual charged %v, want %v (one qual over the join's OUTPUT rows)", diff, want)
	}
}

// TestSortPathFor_ChargesTheSortOnTopOfItsInput pins `cost_sort`'s shape: the
// comparison work is STARTUP (nothing emerges until the sort completes) on top
// of the subpath's total, and the ordering is what the sort was asked for.
func TestSortPathFor_ChargesTheSortOnTopOfItsInput(t *testing.T) {
	cp := defaultCostParams()
	rel := newRelOptInfo(relsetOf(0), 1000, 32)
	sub := &Path{Kind: PathSeqScan, Rel: rel, Rows: 1000, Cost: Cost{Startup: 0, Total: 42}}
	keys := []PathKey{{Expr: col(1), SortAsc: true}}

	s := sortPathFor(sub, keys, cp)
	run := costSortRun(cp, 1000, relNCols(rel))
	if s.Cost.Startup != sub.Cost.Total+run.Startup {
		t.Fatalf("sort startup = %v, want input total + comparison cost %v", s.Cost.Startup, sub.Cost.Total+run.Startup)
	}
	if s.Cost.Total != sub.Cost.Total+run.Total {
		t.Fatalf("sort total = %v, want %v", s.Cost.Total, sub.Cost.Total+run.Total)
	}
	if s.Rows != sub.Rows {
		t.Fatalf("a sort must not change the row count: %v vs %v", s.Rows, sub.Rows)
	}
	if !pathkeysContainedIn(s.Pathkeys, keys) {
		t.Fatalf("the sort must deliver the ordering it was built for")
	}
	// The Sort belongs to the merge candidate, not to the input relation.
	if len(rel.Pathlist) != 0 {
		t.Fatalf("sortPathFor must not publish the Sort into the rel's pathlist")
	}
}
