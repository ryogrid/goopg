package planner

// M0127-P5.4c-i — `sort_inner_and_outer` (joinpath.c:1357): the merge arm that
// assumes a sort is required and pays for it, which is the only merge arm that
// can exist before any path in the search carries an ordering of its own.
//
// PG oracle: `sort_inner_and_outer` (joinpath.c:1357), `try_mergejoin_path`
// (:1029), `select_outer_pathkeys_for_merge` (pathkeys.c:1659),
// `make_inner_pathkeys_for_merge` (pathkeys.c:1858), `build_join_pathkeys`
// (pathkeys.c:1295), `create_sort_path` / `cost_sort` (costsize.c:2144).
// Design: leftdeep-joins 03 §5.3, 04 §4.
//
// Why this is the FIRST half of P5.4c and not the whole of it. PG has two merge
// arms and they need different things:
//
//   - `sort_inner_and_outer` takes the two cheapest-total paths and sorts BOTH.
//     It needs nothing from the inputs — no ordered path has to exist anywhere —
//     so it is expressible today, in full, against the search as it stands.
//   - `generate_mergejoin_paths` (joinpath.c:1564), inside `match_unsorted_outer`,
//     exploits an outer that is ALREADY ordered, and truncates the mergeclause
//     list to find a cheaper inner. It is dead code until some path carries
//     pathkeys — which today no path does, because `generateScanPaths` emits only
//     seq scans and `pathparamindex.go`'s index paths do not yet record the
//     index's ordering. That is P5.4c-ii's, together with the jointype gauntlet
//     and the FULL-without-usable-clause error contract, both of which stay
//     unreachable while 03 §4.4 pins every non-INNER construct outside the
//     search.
//
// Splitting there keeps this slice falsifiable on its own: every path it emits
// is a path PG would emit for the same pair, and the sort it charges for is a
// sort PG would charge for.
//
// Still inert: `GOOPG_PGSHAPED_DP` is OFF and nothing calls the search from
// `planSelect`, so no plan can move. Validated in isolation by
// `joinpathsmerge_test.go`.

// mergeKeyGroup is one distinct sort key of a merge join, together with every
// clause that key serves.
//
// The grouping is PG's, and it is not a tidiness measure. PG builds the outer
// pathkey list from the mergeclauses' EQUIVALENCE CLASSES and drops duplicates
// (`select_outer_pathkeys_for_merge`, pathkeys.c:1697-1704), so two clauses in
// one class contribute ONE pathkey while both remain mergeclauses. Concretely:
// at the pair ({a,b}, {c}) with `a.x = c.x` and `b.x = c.x`, the two clauses are
// transitively one restriction, `a.x = b.x` already holds inside the outer, and
// sorting the outer by `a.x` therefore sorts it by `b.x` too. Emitting two
// pathkeys would charge for a two-column sort where one column does the work,
// and would then fail to match a one-column ordered input.
//
// goopg's `restrictInfo.ecID` is the same fact, so the dedupe carries over
// directly. Clauses with `noEquivClass` fall back to comparing the outer-side
// key EXPRESSION, which is the same statement one level down: two clauses that
// induce the same sort key are one pathkey whether the search knows they are one
// equivalence class or merely sees the same expression. PG never needs that
// fallback because a canonical pathkey is per-EC by construction; goopg's
// pathkeys are syntactic (pathkeys.go, design ch. 04 §2.1) and so can collide
// without the classifier having said so.
type mergeKeyGroup struct {
	outerKey PathKey
	innerKey PathKey
	clauses  []*restrictInfo
}

// mergeKeyGroups turns the pair's usable equalities into the ordered key groups
// a merge join sorts on, oriented so that `outerKey` is always computable on the
// outer side.
//
// `keys` is `splitJoinClauses`' key half, so every clause here has already
// passed `clause_sides_match_join` — its two operands land on opposite sides.
// That is exactly PG's mergeclause precondition minus the operator's
// `mergeopfamilies` test, which goopg cannot ask because it has no operator
// catalogue at this seam; `isEquijoin` is the standing analogue (the same one
// the hash arm uses) and the gap is ledgered.
//
// Orientation matters because the clause records `left`/`right` as the
// expression writer wrote them, and either side may be the outer at this pair.
// `select_mergejoin_clauses` records that per clause in `outer_is_left`
// (joinpath.c:2258); here it is recomputed per pair for the same reason
// `isKeyableFor` is — the same clause faces the other way at a different pair.
func mergeKeyGroups(keys []*restrictInfo, outer RelSet) []mergeKeyGroup {
	var groups []mergeKeyGroup
	// Index of the group already emitted for an equivalence class. Only
	// populated for real classes; `noEquivClass` clauses fall through to the
	// expression comparison below.
	byEC := map[int]int{}

	for _, ri := range keys {
		if ri == nil || ri.leftKey == nil || ri.rightKey == nil {
			// A key clause with no recorded operand split cannot be a merge
			// key: there is no expression to sort on. `isEquijoin` is set only
			// alongside both operands, so this is a broken restrictInfo rather
			// than a shape, and skipping it here would silently drop the qual.
			// Refusing the whole arm keeps the clause where it already is — a
			// hash key and/or a residual on the other arms' paths.
			return nil
		}
		outerExpr, innerExpr := ri.leftKey, ri.rightKey
		if !relsSubset(ri.leftRelids, outer) {
			outerExpr, innerExpr = ri.rightKey, ri.leftKey
		}

		if ri.ecID != noEquivClass {
			if g, ok := byEC[ri.ecID]; ok {
				groups[g].clauses = append(groups[g].clauses, ri)
				continue
			}
		}
		// PG's `pathkey_is_redundant` check in `make_inner_pathkeys_for_merge`
		// (pathkeys.c:1918), reached here through expression identity.
		if g := indexOfOuterKey(groups, outerExpr); g >= 0 {
			groups[g].clauses = append(groups[g].clauses, ri)
			continue
		}

		if ri.ecID != noEquivClass {
			byEC[ri.ecID] = len(groups)
		}
		groups = append(groups, mergeKeyGroup{
			// `make_canonical_pathkey(..., COMPARE_LT, false)`
			// (pathkeys.c:1819): ascending, nulls last. A merge join needs only
			// that the two sides AGREE on an ordering, so PG picks the default
			// one and `make_inner_pathkeys_for_merge` copies the outer
			// pathkey's direction and null placement to the inner (:1911-1915).
			outerKey: PathKey{Expr: outerExpr, SortAsc: true, NullsFirst: false},
			innerKey: PathKey{Expr: innerExpr, SortAsc: true, NullsFirst: false},
			clauses:  []*restrictInfo{ri},
		})
	}
	return groups
}

// indexOfOuterKey finds the group already sorting on `expr`, or -1.
func indexOfOuterKey(groups []mergeKeyGroup, expr Expr) int {
	for i := range groups {
		if exprEqual(groups[i].outerKey.Expr, expr) {
			return i
		}
	}
	return -1
}

// mergeInnerExpr re-derives one clause's INNER-side operand at this pair, the
// mirror of the orientation `mergeKeyGroups` computes for the outer side. It is
// recomputed rather than stored for the same reason the orientation itself is:
// the same clause faces the other way at a different pair.
func mergeInnerExpr(ri *restrictInfo, outer RelSet) Expr {
	if ri == nil || ri.leftKey == nil || ri.rightKey == nil {
		return nil
	}
	if relsSubset(ri.leftRelids, outer) {
		return ri.rightKey
	}
	return ri.leftKey
}

// mergeInnerSortKeys is `make_inner_pathkeys_for_merge` (pathkeys.c:1858): the
// ordering the INNER input must deliver, derived from the merge clauses and the
// OUTER pathkey that selected each group.
//
// Two things it does that a naive "one inner key per group" does not, and both
// are the difference between a valid merge and a wrong one:
//
//   - It emits one key per DISTINCT inner operand, not one per group. A group is
//     one outer sort key, and PG is explicit that several clauses on one outer
//     key may carry different inner operands ("multiple matching clauses might
//     have different ECs on the other side", `find_mergeclauses_for_outer_pathkeys`,
//     pathkeys.c:1670-1674) — `a.x = c.x AND a.x = c.y` is one outer key `a.x`
//     and two inner keys. Both clauses stay merge clauses, so the merge compares
//     on both; an inner sorted only by `c.x` would then be fed to an operator
//     that expects `(c.x, c.y)` order. Sorting the outer by `a.x` orders it by
//     `(a.x, a.x)` for free, which is why the two lists legitimately differ in
//     LENGTH — PG carries `outersortkeys` and `innersortkeys` as independent
//     lists for exactly this reason.
//   - It copies the direction and null placement from the OUTER pathkey
//     (:1911-1915) rather than assuming ascending/nulls-last. A merge join needs
//     only that the two sides AGREE, so when the outer's ordering is given —
//     P5.4c-ii-c's already-ordered outer, which may be any direction the index
//     provides — the inner must be sorted to match it, not to a default.
//
// `outerKeys[i]` supplies that direction for `groups[i]`; a short list falls back
// to the group's own default, which is what `sort_inner_and_outer` uses (it
// chooses both orderings itself, so they are ascending by construction).
//
// The dedupe is PG's `pathkey_is_redundant` (:1922) reached through pathkey
// identity rather than equivalence-class identity, the same one-level-down
// substitution `mergeKeyGroups` makes.
func mergeInnerSortKeys(groups []mergeKeyGroup, outerKeys []PathKey, outer RelSet) []PathKey {
	var keys []PathKey
	for i, g := range groups {
		dir := g.outerKey
		if i < len(outerKeys) {
			dir = outerKeys[i]
		}
		for _, ri := range g.clauses {
			e := mergeInnerExpr(ri, outer)
			if e == nil {
				continue
			}
			pk := PathKey{Expr: e, SortAsc: dir.SortAsc, NullsFirst: dir.NullsFirst}
			redundant := false
			for _, have := range keys {
				if pathKeyEqual(have, pk) {
					redundant = true
					break
				}
			}
			if !redundant {
				keys = append(keys, pk)
			}
		}
	}
	return keys
}

// sortInnerAndOuter is `sort_inner_and_outer` (joinpath.c:1357): one merge path
// per available sort ordering, each sorting both inputs explicitly.
//
// The loop over orderings is the part that looks like waste and is not. Every
// permutation of the merge keys yields a differently-sorted RESULT at
// essentially the same cost, and this join's result ordering is what decides
// whether a merge one level up needs a sort at all. PG has no basis for choosing
// here, so rather than guess it generates one path per key — that key first, the
// rest in their existing order — which guarantees a one-key merge above can find
// a partner path already sorted the way it needs (:1447-1466). `addPath` keeps
// them all only because their pathkeys are mutually incomparable; on cost alone
// all but one would be pruned.
//
// PG orders the base list by an EC "popularity" score and by `query_pathkeys`
// (`select_outer_pathkeys_for_merge`, pathkeys.c:1724-1826) so that the FIRST
// ordering is the most promising one. Neither input is available at this seam:
// goopg's pathkeys are syntactic rather than EC-canonical, so there are no
// `ec_members` to count future join partners in, and the search boundary has no
// `query_pathkeys` (03 §10 — the top-level sort is applied outside the searched
// subtree). The base order is therefore the clause order, which is stable and
// deterministic; the heuristic is a ranking of paths that all get generated
// anyway, so its absence costs a tie-break, not a path. Ledgered.
func sortInnerAndOuter(joinrel, outer, inner *RelOptInfo, cp costParams, keys, residual []*restrictInfo) {
	groups := mergeKeyGroups(keys, outer.Relids)
	if len(groups) == 0 {
		// PG's `if (extra->mergeclause_list == NIL) return` (:1372). A pair
		// with no mergeable equality has no merge path, which is why the
		// unconditional nested loop of `addPathsToJoinrel` is what keeps the
		// pathlist non-empty.
		return
	}
	for front := range groups {
		ordered := rotateToFront(groups, front)
		outerKeys := make([]PathKey, len(ordered))
		var mergeClauses []*restrictInfo
		for i, g := range ordered {
			outerKeys[i] = g.outerKey
			mergeClauses = append(mergeClauses, g.clauses...)
		}
		// `make_inner_pathkeys_for_merge` rather than one key per group: a
		// group that serves several clauses with DIFFERENT inner operands owes
		// the inner an ordering on each of them, or the merge compares on a
		// column the inner is not sorted by. See mergeInnerSortKeys.
		innerKeys := mergeInnerSortKeys(ordered, outerKeys, outer.Relids)
		// PG asserts here that the reordering used every mergeclause
		// (`find_mergeclauses_for_outer_pathkeys`, :1500-1501). Building the
		// clause list BY the groups rather than re-deriving it from the
		// pathkeys makes that a property of the construction instead of a
		// check — the groups partition `keys`, so the concatenation is a
		// permutation of it.
		addMergeJoinPath(joinrel, outer, inner, cp, outerKeys, innerKeys, mergeClauses, residual)
	}
}

// rotateToFront returns `groups` with element `front` moved to the head and the
// rest left in their relative order — PG's `lcons(front_pathkey,
// list_delete_nth_cell(list_copy(all_pathkeys), i))` (joinpath.c:1486-1489).
func rotateToFront(groups []mergeKeyGroup, front int) []mergeKeyGroup {
	out := make([]mergeKeyGroup, 0, len(groups))
	out = append(out, groups[front])
	for i, g := range groups {
		if i != front {
			out = append(out, g)
		}
	}
	return out
}

// addMergeJoinPath is `try_mergejoin_path` (joinpath.c:1029) for one ordering:
// sort whichever inputs are not already ordered, cost the merge, and offer it to
// `addPath`.
//
// Three things it does that are easy to leave out and expensive to leave out:
//
//   - The sort is SKIPPED when the input already delivers the ordering
//     (:1091-1097). Today no path carries pathkeys so the branch never fires,
//     but it is what makes P5.4c-ii's ordered index paths worth anything, and
//     writing it now means that slice adds a producer rather than also having to
//     add the consumer.
//   - A still-parameterised result is refused (:1073-1081). `param_source_rels`
//     is empty in v1 (`paramSourceRels`, joinpathsnli.go), so any non-empty
//     `required_outer` fails PG's own overlap test — no goopg-only gate is
//     needed here, unlike the NLI arm, because merge has no `allow_star_schema_join`
//     escape. The two `PATH_PARAM_BY_REL` refusals `sort_inner_and_outer` makes
//     first (:1397-1399) are the caller's: `addPathsToJoinrel` already made
//     them, for both directions, before reaching this arm.
//   - The join's output ordering is the OUTER's ordering (`build_join_pathkeys`,
//     pathkeys.c:1295 — "normally the same as the outer path's keys"). For a
//     merge join over sorted inputs that is exact, and it is the whole reason
//     the ordering loop above exists. PG additionally truncates the list to the
//     pathkeys higher levels could still use (`truncate_useless_pathkeys`);
//     goopg keeps it whole, which can only leave `addPath` distinguishing two
//     paths PG would have merged — more paths considered, never fewer, and
//     never a different winner on cost. Ledgered.
func addMergeJoinPath(joinrel, outer, inner *RelOptInfo, cp costParams, outerKeys, innerKeys []PathKey, mergeClauses, residual []*restrictInfo) {
	o, i := outer.CheapestTotal, inner.CheapestTotal
	if o == nil || i == nil {
		return
	}
	// `sort_inner_and_outer` chooses the result ordering itself, so the outer
	// sort keys ARE the result's pathkeys. The arm that consumes an ordering it
	// did not choose (P5.4c-ii-c) passes a different pair, which is why
	// `tryMergeJoinPath` takes the two separately.
	tryMergeJoinPath(joinrel, o, i, cp, outerKeys, outerKeys, innerKeys, mergeClauses, residual)
}

// tryMergeJoinPath is `try_mergejoin_path` proper (joinpath.c:1029) over two
// chosen PATHS, split out of `addMergeJoinPath` by P5.4c-ii-c.
//
// `resultKeys` is `merge_pathkeys` — the ordering the JOIN delivers, which is
// `build_join_pathkeys` of the outer PATH's ordering. It is separate from
// `outerSortKeys` because the two arms disagree about it: `sort_inner_and_outer`
// sorts the outer to an ordering of its own choosing, so they coincide, while
// `generate_mergejoin_paths` takes an outer that is already ordered and merges on
// a PREFIX of that ordering — the result keeps the outer's full, longer ordering,
// which is what lets a merge one level up skip its own sort.
//
// `outerSortKeys` / `innerSortKeys` are PG's `outersortkeys` / `innersortkeys`
// with PG's NIL convention: an empty list means "this side needs no sort". The
// explicit re-check below (:1091-1097) makes passing them harmless either way.
func tryMergeJoinPath(joinrel *RelOptInfo, o, i *Path, cp costParams, resultKeys, outerSortKeys, innerSortKeys []PathKey, mergeClauses, residual []*restrictInfo) {
	if o == nil || i == nil {
		return
	}
	// `calc_non_nestloop_required_outer` (:1071): a merge join discharges
	// nothing, so the result is parameterised by the union of its inputs'
	// requirements. With an empty `param_source_rels` that is only acceptable
	// when it is empty.
	req := calcNonNestloopRequiredOuter(o, i)
	if req != 0 && !relsOverlap(req, paramSourceRels()) {
		return
	}

	op, ip := o, i
	if len(outerSortKeys) > 0 && !pathkeysContainedIn(o.Pathkeys, outerSortKeys) {
		op = sortPathFor(o, outerSortKeys, cp)
	}
	if len(innerSortKeys) > 0 && !pathkeysContainedIn(i.Pathkeys, innerSortKeys) {
		ip = sortPathFor(i, innerSortKeys, cp)
	}

	// Row counts from the CHILD PATHS (03 §9 rule 3), as everywhere else in
	// path generation. A Sort propagates its subpath's count unchanged, so for
	// a parameterised input this is still the per-parameterisation count —
	// which the refusal above has already ruled out, but the rule is written
	// once rather than per-arm.
	cost := mergeJoinCost(cp, op.Cost, ip.Cost, op.Rows, ip.Rows, joinrel.Rows)
	// The residual is evaluated on the tuples that already matched on the
	// merge keys, i.e. the join's output — the same charge the hash arm makes
	// (`final_cost_mergejoin`'s qpqual term on `mergejointuples`,
	// costsize.c:4045).
	cost.Total += qualEvalCost(cp, len(residual), joinrel.Rows)

	addPath(joinrel, &Path{
		Kind:     PathMergeJoin,
		Rel:      joinrel,
		Rows:     joinrel.Rows,
		Cost:     cost,
		Pathkeys: resultKeys,
		// Children[0] is the outer (streaming left) side, Children[1] the
		// inner — the same convention the hash and nested-loop arms use, so
		// P5.5's createPlan reads one layout for every join kind. When a side
		// needed sorting the child IS the Sort path, so the Sort is a node in
		// the tree rather than a flag on the join.
		Children:      []*Path{op, ip},
		HashKeys:      mergeClauses,
		Residual:      residual,
		RequiredOuter: req,
	})
}

// sortPathFor wraps `sub` in an explicit Sort delivering `keys` — PG's
// `create_sort_path` (pathnode.c) over `cost_sort` (costsize.c:2144).
//
// PG does not build a SortPath here at all: `MergePath` carries
// `outersortkeys`/`innersortkeys` and `create_mergejoin_plan` materialises the
// Sort at plan time. goopg makes the Sort a real child path instead, which 03
// §5.3 asks for by name ("explicit Sort paths"). The two are the same plan at
// the same cost — a Sort's cost is added to the merge's input cost either way —
// and the difference is representational: a child path means P5.5's `createPlan`
// walks `Children` uniformly instead of learning a merge-only field, and it means
// `PathSort` (path.go) finally has a producer. Ledgered as a representational
// divergence.
//
// The Sort is deliberately NOT offered to `addPath`: it belongs to this merge
// candidate, not to the input relation's pathlist, and adding it there would let
// a sort generated for one pair change another pair's `CheapestTotal`. PG's
// equivalent paths are likewise private to the MergePath.
func sortPathFor(sub *Path, keys []PathKey, cp costParams) *Path {
	// `cost_sort` charges the comparison work as STARTUP — nothing emerges
	// until the sort is complete — on top of the subpath's total, and the
	// per-row emit at run.
	s := costSortRun(cp, sub.Rows)
	return &Path{
		Kind:          PathSort,
		Rel:           sub.Rel,
		Rows:          sub.Rows,
		Cost:          Cost{Startup: sub.Cost.Total + s.Startup, Total: sub.Cost.Total + s.Total},
		Pathkeys:      keys,
		Children:      []*Path{sub},
		RequiredOuter: sub.RequiredOuter,
		ParallelSafe:  sub.ParallelSafe,
	}
}
