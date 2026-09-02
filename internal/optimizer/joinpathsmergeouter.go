package optimizer

// M0127-P5.4c-ii-c — `generate_mergejoin_paths` (joinpath.c:1564), the merge arm
// inside `match_unsorted_outer` that EXPLOITS an ordering instead of buying one.
//
// PG oracle: `match_unsorted_outer` (joinpath.c:1795, merge half at :1998-2013),
// `generate_mergejoin_paths` (:1564), `find_mergeclauses_for_outer_pathkeys`
// (pathkeys.c:1631), `make_inner_pathkeys_for_merge` (pathkeys.c:1858),
// `trim_mergeclauses_for_inner_pathkeys` (pathkeys.c:1948),
// `get_cheapest_path_for_pathkeys` (pathkeys.c:441), `final_cost_mergejoin`'s
// materialize-inner decision (costsize.c:3986-4040).
// Design: leftdeep-joins 03 §5.3.
//
// This is the second half of P5.4c and the consumer P5.4c-i built a branch for.
// The two merge arms answer different questions:
//
//   - `sort_inner_and_outer` (P5.4c-i) asks "what does a merge cost if I sort
//     both sides?" It needs nothing from its inputs and always pays two sorts.
//   - this arm asks "some path already delivers an ordering — what is the
//     cheapest merge that USES it?" It pays at most one sort, and often none.
//     It could not exist before P5.4c-ii-b, because until an ordered index path
//     existed no path in the search carried pathkeys and every outer here would
//     have been skipped at the first line.
//
// Three PG behaviours are transcribed here because each one changes which plan
// wins, not merely how many are considered:
//
//  1. **The mergeclause list follows the OUTER's ordering, and stops.** PG walks
//     the outer path's pathkeys in order and takes the clauses matching each
//     (`find_mergeclauses_for_outer_pathkeys`); the first pathkey with no clause
//     ends the list, because a merge cannot skip a sort column. An outer sorted
//     by `(a.x, a.y)` joined on `a.y` alone is therefore NOT usable as an
//     `a.y`-ordered input — it is usable on nothing, and the arm declines. That
//     asymmetry is the reason the arm iterates the whole pathlist rather than
//     just the cheapest ordered path.
//  2. **The clause list is TRUNCATED to find a cheaper inner.** Having derived
//     the inner ordering the full clause set demands, PG then tries shorter and
//     shorter prefixes of it, looking for an inner path already sorted that far
//     (:1685-1782). Dropping a merge key demotes its clause to an ordinary qual —
//     strictly more per-tuple work — so PG only accepts a prefix whose inner path
//     is STRICTLY cheaper than everything found so far, which is what stops it
//     from using fewer merge keys than it could. Both cost axes are searched,
//     because a cheap-startup ordered inner is what wins under a LIMIT.
//  3. **The result keeps the OUTER's ordering, not the merge keys.** `merge_pathkeys`
//     is `build_join_pathkeys` of the outer path's pathkeys, which may be LONGER
//     than the merge key list (behaviour 1 truncates the clauses, never the
//     ordering). A merge on `(a.x)` over an outer sorted `(a.x, a.y)` emits an
//     `(a.x, a.y)`-ordered result, and a merge above it can consume that. This is
//     why `tryMergeJoinPath` takes the result ordering separately from the sort
//     keys.
//
// **The materialize-inner decision, and why goopg does not make it.** PG's
// mergejoin executor rewinds the inner side with mark/restore, so
// `final_cost_mergejoin` (costsize.c:3986-4040) must decide whether to interpose a
// Material node: mandatorily when the inner is used unsorted and its node type
// cannot mark/restore (a nestloop or merge below it cannot), and opportunistically
// when re-fetching looks dearer than buffering. goopg's merge executor does not
// rewind at all — `mergeJoinStream.bufferGroup` (internal/executor/join_merge_stream.go:616)
// consumes each inner equal-key group into memory, spilling past `work_mem` to an
// overflow file, and replays it from there. The materialisation PG chooses per PLAN
// is therefore already made per GROUP, unconditionally, in goopg's executor. Two
// consequences, both load-bearing:
//
//   - The correctness-mandatory arm (:3998-4014) has no goopg analogue. Any
//     presorted inner path is consumable here regardless of its kind, so this arm
//     may take a merge or nested-loop path as its inner where PG would first have
//     to wrap it. No `PathMaterial` kind is introduced, and one would be wrong:
//     it would buffer the inner twice.
//   - The COST of that buffering is not charged. `mergeJoinCost` prices one pass
//     over each input (`cost_funcs.go`), with no `rescanratio` term for duplicate
//     inner groups and no charge for the group file. PG's model has both. Ledgered
//     against the cost work, not approximated here — inventing a rescan factor
//     without `mergejoinscansel`'s duplicate estimate would move plans on a guess.
//
// **What this arm deliberately does not carry.** PG's `match_unsorted_outer` opens
// with a jointype gauntlet (`nestjoinOK` / `useallclauses`, joinpath.c:1833-1852):
// RIGHT, RIGHT-ANTI and FULL must use *all* the mergeclauses, so for them the
// truncation loop is skipped entirely and a partial match is rejected outright;
// and a FULL join with no usable clause must still produce a clauseless MERGE path
// (:1601-1609), merge being the only method that implements it, with
// `join_is_legal`'s refusal (joinrels.c:961-964) as the alternative. Neither is
// expressible here: 03 §4.4 pins every non-INNER construct outside the search, so
// `addPathsToJoinrel` carries no jointype to switch on. Both are ledgered rather
// than written as dead branches over a value that does not exist.
//
// No longer inert (M0127-P5.9, 2026-08-06): `GOOPG_PGSHAPED_DP` is ON by
// default and `planSelect` calls the search, so this IS on the production path.
// Validated in isolation by `joinpathsmergeouter_test.go`; corpus-level
// evidence is the DS05 arm in 09 §3.15.

// mergeOuterMatch is one outer sort key that the merge can actually use: the key
// group whose outer operand it sorts on, paired with the outer PATH's own pathkey
// at that position. The pathkey is kept, not just the group, because the inner
// must be sorted in the outer's direction and null placement — the outer's
// ordering is given here, not chosen.
type mergeOuterMatch struct {
	group    mergeKeyGroup
	outerKey PathKey
}

// matchUnsortedOuterMerge is the merge half of `match_unsorted_outer`
// (joinpath.c:1998-2013): every outer path that already carries an ordering gets
// its own family of merge paths.
//
// It iterates `outer.Pathlist` rather than just `outer.CheapestTotal`, which is
// the entire point — an ordered path is by construction NOT the cheapest total
// (an ordered index scan prices at `max_IO_cost`, P5.4c-ii-b), so it survives
// `addPath` only on its pathkeys and is reachable only through the pathlist. A
// version of this arm keyed to the cheapest path would find nothing.
//
// The two `PATH_PARAM_BY_REL` refusals PG makes here (:1874 for the inner,
// :1908-1909 for each outer) are partly the caller's: `addPathsToJoinrel` has
// already established that `inner.CheapestTotal` is not parameterised by the
// outer before reaching this arm, so only the per-outer-path test is made below —
// the pathlist can hold parameterised paths that the cheapest-total test did not
// cover.
func matchUnsortedOuterMerge(joinrel, outer, inner *RelOptInfo, cp costParams, keys, residual []*restrictInfo, mergeTuplesFor func([]*restrictInfo) float64) {
	innerCheapestTotal := inner.CheapestTotal
	if innerCheapestTotal == nil {
		return
	}
	groups := mergeKeyGroups(keys, outer.Relids)
	if len(groups) == 0 {
		// `extra->mergeclause_list == NIL` (:1602). Under the INNER-only pin
		// there is no FULL-join exception to make, so the whole arm declines.
		return
	}
	for _, op := range outer.Pathlist {
		if op == nil || len(op.Pathkeys) == 0 {
			// An unordered outer yields no mergeclauses at all
			// (`find_mergeclauses_for_outer_pathkeys` of an empty pathkey list
			// is empty), so PG's `generate_mergejoin_paths` returns at its
			// first test. Skipped here to keep the empty case one statement
			// rather than a wasted call — `sort_inner_and_outer` is the arm
			// that serves unordered inputs.
			continue
		}
		if pathParamByRel(op, inner) {
			continue
		}
		generateMergeJoinPaths(joinrel, inner, op, innerCheapestTotal, cp, groups, outer.Relids, residual, mergeTuplesFor)
	}
}

// generateMergeJoinPaths is `generate_mergejoin_paths` (joinpath.c:1564) for one
// already-ordered outer path.
func generateMergeJoinPaths(joinrel, inner *RelOptInfo, outerPath, innerCheapestTotal *Path, cp costParams, groups []mergeKeyGroup, outerRelids RelSet, residual []*restrictInfo, mergeTuplesFor func([]*restrictInfo) float64) {
	matched := findMergeClausesForOuterPathkeys(outerPath.Pathkeys, groups)
	if len(matched) == 0 {
		return
	}

	selected := make([]mergeKeyGroup, len(matched))
	outerKeys := make([]PathKey, len(matched))
	var mergeClauses []*restrictInfo
	for i, m := range matched {
		selected[i] = m.group
		outerKeys[i] = m.outerKey
		mergeClauses = append(mergeClauses, m.group.clauses...)
	}
	innerSortKeys := mergeInnerSortKeys(selected, outerKeys, outerRelids)
	if len(innerSortKeys) == 0 {
		return
	}

	// `merge_pathkeys` = `build_join_pathkeys(..., outerpath->pathkeys)`
	// (:1932). The outer's FULL ordering, which behaviour 3 in the file header
	// explains is generally longer than `outerKeys`. `truncate_useless_pathkeys`
	// is the same deliberate omission P5.4c-i ledgered: keeping the list whole
	// can only make `addPath` distinguish two paths PG would have merged.
	resultKeys := outerPath.Pathkeys

	// The first candidate: the cheapest-total inner, SORTED to the full key
	// list (:1622-1633). "Since a sort will be needed, only cheapest total cost
	// matters" — and `tryMergeJoinPath` skips the sort anyway if that path
	// already delivers the ordering, which is what makes the initialisation
	// below correct.
	tryMergeJoinPath(joinrel, outerPath, innerCheapestTotal, cp, resultKeys, nil, innerSortKeys, mergeClauses, residual, mergeTuplesFor)

	// The truncation search (:1685-1782). `cheapestTotalInner` /
	// `cheapestStartupInner` carry the best inner found SO FAR, and a candidate
	// is only accepted if it is strictly cheaper. That is what implements PG's
	// "we should consider only paths that are strictly cheaper than any path
	// found in an earlier iteration" — a shorter key prefix demotes a merge
	// clause to an ordinary qual, so it must buy something to be worth trying.
	//
	// The initialisation is the same rule applied to the path already emitted
	// above: if the cheapest-total inner needed no sort for the FULL key list,
	// it is a path this loop must not duplicate, so it seeds both trackers. If
	// it DID need a sort, the plan above is a different plan (sorted inner) and
	// the trackers start empty — PG's note that it does not reject
	// `inner_cheapest_total` merely for matching some shorter prefix.
	var cheapestTotalInner, cheapestStartupInner *Path
	if pathkeysContainedIn(innerCheapestTotal.Pathkeys, innerSortKeys) {
		cheapestTotalInner = innerCheapestTotal
		cheapestStartupInner = innerCheapestTotal
	}

	numSortKeys := len(innerSortKeys)
	for cnt := numSortKeys; cnt > 0; cnt-- {
		trial := innerSortKeys[:cnt]
		var newClauses []*restrictInfo
		haveNewClauses := false

		if ip := getCheapestPathForPathkeys(inner.Pathlist, trial, totalCost); ip != nil &&
			(cheapestTotalInner == nil || comparePathCosts(ip, cheapestTotalInner, totalCost) < 0) {
			newClauses, haveNewClauses = trimmedMergeClauses(mergeClauses, trial, cnt, numSortKeys, outerRelids), true
			if len(newClauses) > 0 {
				// Both sort-key lists are nil: the outer is ordered by
				// construction and this inner was SELECTED for already being
				// ordered, so neither side is sorted here.
				tryMergeJoinPath(joinrel, outerPath, ip, cp, resultKeys, nil, nil,
					newClauses, demoteDroppedMergeClauses(residual, mergeClauses, newClauses), mergeTuplesFor)
			}
			cheapestTotalInner = ip
		}

		// The same search on the STARTUP axis (:1734-1777). It is not
		// redundant: under a LIMIT the merge stops early, so an inner that is
		// dearer overall but cheaper to first row can win, and `addPath` keeps
		// both because they are incomparable on the two cost axes.
		if ip := getCheapestPathForPathkeys(inner.Pathlist, trial, startupCost); ip != nil &&
			(cheapestStartupInner == nil || comparePathCosts(ip, cheapestStartupInner, startupCost) < 0) {
			if ip != cheapestTotalInner {
				if !haveNewClauses {
					newClauses = trimmedMergeClauses(mergeClauses, trial, cnt, numSortKeys, outerRelids)
				}
				if len(newClauses) > 0 {
					tryMergeJoinPath(joinrel, outerPath, ip, cp, resultKeys, nil, nil,
						newClauses, demoteDroppedMergeClauses(residual, mergeClauses, newClauses), mergeTuplesFor)
				}
			}
			cheapestStartupInner = ip
		}
	}
}

// trimmedMergeClauses is PG's "select the right mergeclauses, if we didn't
// already" (:1711-1721): the full list when the prefix is the whole list, the
// trimmed list otherwise.
//
// PG asserts the trimmed list is non-empty. goopg returns it and lets the caller
// skip an empty one instead, because goopg's inner-key identity is syntactic
// rather than equivalence-class-based (design ch. 04 §2.1) and so can fail to
// match where PG's cannot. A false negative here costs a path, which is the
// standing consequence of syntactic pathkeys everywhere else too; an assertion
// would turn it into a planner error.
func trimmedMergeClauses(mergeClauses []*restrictInfo, trial []PathKey, cnt, numSortKeys int, outerRelids RelSet) []*restrictInfo {
	if cnt >= numSortKeys {
		return mergeClauses
	}
	return trimMergeClausesForInnerPathkeys(mergeClauses, trial, outerRelids)
}

// demoteDroppedMergeClauses is `create_mergejoin_plan`'s qpqual computation
// (createplan.c: "qpqual = restrictlist minus mergeclauses") brought forward to
// path generation, where 03 §5.4 puts every qual-placement decision.
//
// It is what makes truncation honest, and leaving it out is a silent wrong
// answer rather than a missed optimisation. Dropping a merge key does not drop
// the CLAUSE: the merge no longer keys on it, so it must be evaluated per
// surviving tuple instead. PG carries the whole restrictlist down to plan time
// and subtracts there, so the demotion is automatic; goopg decides the split here
// and would otherwise emit a join that never evaluates the clause at all.
//
// Costing follows from the same call, since `tryMergeJoinPath` charges
// `qualEvalCost` on the residual — which is exactly why PG's "strictly cheaper"
// rule exists: a shorter key list buys a cheaper input and pays for it in
// per-tuple qual work, and the search can only weigh that if the work is on the
// path.
//
// `kept` is always a PREFIX of `all` (`trimMergeClausesForInnerPathkeys` scans in
// order and stops), so the dropped set is the tail; the assertion-free slice is
// guarded by the length test rather than by trusting that.
func demoteDroppedMergeClauses(residual, all, kept []*restrictInfo) []*restrictInfo {
	if len(kept) >= len(all) {
		return residual
	}
	out := make([]*restrictInfo, 0, len(residual)+len(all)-len(kept))
	out = append(out, residual...)
	out = append(out, all[len(kept):]...)
	return out
}

// findMergeClausesForOuterPathkeys is `find_mergeclauses_for_outer_pathkeys`
// (pathkeys.c:1631): walk the outer path's ordering and collect the key groups it
// serves, stopping at the first position that serves none.
//
// The stop is the interesting half. PG's comment is that "any additional sort-key
// positions in the pathkeys are useless" — a merge join consumes its inputs in
// sort order, so it cannot use the third column of an ordering whose second
// column it has no clause for. The result is therefore a PREFIX of the outer's
// ordering, never a subsequence.
//
// PG matches a clause to a pathkey by equivalence class; goopg matches by outer
// operand identity, `mergeKeyGroups`' own substitution. Direction is deliberately
// NOT part of the match: a merge needs only that the two sides agree on an
// ordering, so a descending outer is perfectly usable and the direction is
// propagated to the inner (`mergeInnerSortKeys`) rather than required of the
// clause.
func findMergeClausesForOuterPathkeys(outerPathkeys []PathKey, groups []mergeKeyGroup) []mergeOuterMatch {
	var out []mergeOuterMatch
	used := make(map[int]bool, len(groups))
	for _, pk := range outerPathkeys {
		g := indexOfOuterKey(groups, pk.Expr)
		if g < 0 || used[g] {
			// `matched_restrictinfos == NIL` → break (:1642-1648). The
			// already-used case cannot arise from a canonical pathkey list (PG
			// asserts no two members share an EC) and is treated as the same
			// stop: taking a group twice would enter its clauses into the merge
			// list twice, which is a wrong plan rather than a missed one.
			break
		}
		used[g] = true
		out = append(out, mergeOuterMatch{group: groups[g], outerKey: pk})
	}
	return out
}

// trimMergeClausesForInnerPathkeys is `trim_mergeclauses_for_inner_pathkeys`
// (pathkeys.c:1948): the longest prefix of the merge clauses whose INNER operands
// are covered by a truncated inner ordering.
//
// It is a prefix scan over the clauses with a cursor over the pathkeys, and the
// two advance independently because several clauses can share one inner key. A
// clause that matches neither the current inner pathkey nor the next one ends the
// list; so does running out of pathkeys. The transcription differs from PG only
// in comparing inner operand expressions instead of equivalence classes.
func trimMergeClausesForInnerPathkeys(mergeClauses []*restrictInfo, pathkeys []PathKey, outerRelids RelSet) []*restrictInfo {
	if len(pathkeys) == 0 {
		return nil
	}
	var out []*restrictInfo
	pos := 0
	matchedPathkey := false
	for _, ri := range mergeClauses {
		ie := mergeInnerExpr(ri, outerRelids)
		if ie == nil {
			break
		}
		if !exprEqual(ie, pathkeys[pos].Expr) {
			// No clause matched the pathkey we are on: nothing further can
			// match either, because the clause list is grouped by inner key.
			if !matchedPathkey {
				break
			}
			if pos+1 >= len(pathkeys) {
				break
			}
			pos++
			matchedPathkey = false
		}
		if !exprEqual(ie, pathkeys[pos].Expr) {
			break
		}
		out = append(out, ri)
		matchedPathkey = true
	}
	return out
}

// getCheapestPathForPathkeys is `get_cheapest_path_for_pathkeys`
// (pathkeys.c:441) with `required_outer` empty: the cheapest UNPARAMETERISED path
// in the list that already delivers `keys`, on the given cost axis.
//
// The cost test precedes the pathkey test for the reason PG gives — a cost
// comparison is cheaper than a pathkey comparison — and the `<= 0` sense means an
// exact tie KEEPS the incumbent, so the scan is stable in pathlist order.
//
// Parameterised inner paths are excluded here, as PG excludes them (:1660-1664
// "Currently we do not consider parameterized inner paths here"). That is not an
// oversight being reproduced: a merge join discharges no parameter, so a
// parameterised inner would only produce a parameterised join that
// `tryMergeJoinPath` then refuses on `param_source_rels` anyway.
func getCheapestPathForPathkeys(paths []*Path, keys []PathKey, criterion costSelector) *Path {
	var matched *Path
	for _, p := range paths {
		if p == nil {
			continue
		}
		if matched != nil && comparePathCosts(matched, p, criterion) <= 0 {
			continue
		}
		if p.RequiredOuter == 0 && pathkeysContainedIn(p.Pathkeys, keys) {
			matched = p
		}
	}
	return matched
}
