package optimizer

// R47 slice 2 (K101): the ORDERED upper rel adjudicates the GROUP_AGG
// upper rel's surviving PathAgg candidates — PG's `create_ordered_paths`
// iterating ALL input paths (planner.c:5345+ `foreach(lc,
// input_rel->pathlist)`), which goopg could not execute while grouping
// collapsed to one node before ordering saw alternatives.
//
// Two pieces: `groupingEmissionPathkeys` translates a sorted
// candidate's input-coordinate emission order into output-coordinate
// pathkeys (the coordinate boundary `inputNodePathkeys` cannot cross —
// it returns nil through `*Aggregate`, upperorderedinput.go:184-186),
// and `electOrderedGrouping` runs the ordered-level loop at the
// planSelect normal ORDER BY arm through the existing `addOrderedPaths`
// + `setCheapest`/`getCheapestFractionalPath` tournament.
//
// Design: docs/design/not_ralph/plan_parity_fix_take2/r47-q4-upper-rel/
// SLICE2.md. Measurement, not a pick rule: the corpus arbitrates, and
// a no-flip outcome must still deliver the §3.3 census.

// groupingEmissionPathkeys is the emission order a sorted-aggregate
// candidate delivers its grouped rows in, expressed in the aggregate
// OUTPUT coordinates the ORDER BY keys were resolved against — so the
// ORDERED rel's no-sort check (`pathkeysContainedIn`) can fire across
// the grouping boundary. nil means "untranslatable": the caller offers
// a Sort over the candidate only (PG's cheapest-input arm), never a
// fabricated order.
//
// A sorted aggregate emits groups in the order they complete, which is
// the lexicographic order of its sorted input restricted to distinct
// group values. When the input sort's leading keys ARE the group keys
// positionally (the group-keys Sort variant), the emergence order is
// exactly that key order — extra trailing keys cannot change it, so
// they are allowed. Every other shape declines:
//
//   - hashed (or any non-sorted strategy): unordered output.
//   - neither-Sort-nor-GatherMerge child: the index variant rides a
//     `PathPrebuilt` seed, never a sort; the plain arm has no order at
//     all. (R56: a `PathGatherMerge` child translates — a merge emits
//     its inputs' order, the same contract a Sort gives.)
//   - `GroupKeyOrder != nil`: the narrowed index-remapped spec —
//     excluded even though its child check would decline anyway.
//   - grouping sets / empty groups / non-simple mode: mirrors the
//     executor's sorted-agg guard (operators_join_agg.go:2222), which
//     runs sorted aggregation only for Simple-mode, set-free,
//     grouped aggregation over a Sort child.
//   - non-`ColumnRef` group expressions or a run shorter than the full
//     group list: the output side cannot name the position.
//   - output-prefix mismatch: the group-prefix layout
//     ([groups|aggs|grouping|passthrough-after], plan.go:1310-1359)
//     is VERIFIED per candidate (`outCols[j].Name ==
//     groupExprName(groups[j])`), never assumed.
//
// Pure function of (tree node, unbuilt path): no stamps, no registry,
// no mutation. Losers stay unbuilt Paths — only the elected winner is
// ever built, by the caller through `createPlanNode`.
func groupingEmissionPathkeys(aggNode *Aggregate, cand *Path) []PathKey {
	if aggNode == nil || cand == nil || cand.Kind != PathAgg {
		return nil
	}
	spec := cand.Agg
	if spec == nil {
		return nil
	}
	if cand.AggStrategy != AggStrategySorted || spec.GroupingSets != nil ||
		len(spec.GroupExprs) == 0 || spec.Mode != AggModeSimple ||
		spec.GroupKeyOrder != nil {
		return nil
	}
	// R56: the worker-sort-under-GatherMerge no-split arm
	// (partialaggupper.go) delivers group-key order through a
	// `PathGatherMerge` child, not a `PathSort` — a Gather Merge emits
	// the merged order of its sorted inputs, the same order-delivery
	// contract a Sort gives (the contract PG relies on when it feeds
	// Gather Merge inputs into `create_ordered_paths`). Without
	// this the R56 candidate evicts the leader-sort candidate under
	// identical pathkeys and the loop below loses its only translatable
	// candidate — declining to a legacy-priced ORDER BY seed on every
	// query the new arm touches. The positional group-key coverage
	// check below is unchanged, so a merge on other keys still declines
	// exactly like a mis-sorted Sort.
	if len(cand.Children) == 0 || cand.Children[0] == nil ||
		(cand.Children[0].Kind != PathSort && cand.Children[0].Kind != PathGatherMerge) {
		return nil
	}
	childPK := cand.Children[0].Pathkeys
	groups := spec.GroupExprs
	for j, g := range groups {
		// Bare group keys only: the output side names positions, and
		// only a column has a name. Both sides are input-coordinate
		// here, so `exprEqual`'s positional `Index` equality is the
		// match (Name/SourceTableIdx excluded, exprwalk.go:555+).
		if _, ok := g.(*ColumnRef); !ok {
			return nil
		}
		if j >= len(childPK) || !exprEqual(childPK[j].Expr, g) {
			return nil
		}
	}
	outCols := aggNode.Output()
	if len(outCols) < len(groups) {
		return nil
	}
	for j, g := range groups {
		if outCols[j].Name != groupExprName(g) {
			return nil
		}
	}
	emitted := make([]PathKey, len(groups))
	for j := range groups {
		emitted[j] = PathKey{
			Expr:       &ColumnRef{Index: j, Name: outCols[j].Name, Type: outCols[j].Type},
			SortAsc:    childPK[j].SortAsc,
			NullsFirst: childPK[j].NullsFirst,
		}
	}
	return emitted
}

// electOrderedGrouping runs the R47 slice-2 loop: every surviving
// GROUP_AGG PathAgg candidate is offered on the ORDERED upper rel —
// as-is when its translated emission order already delivers the ORDER
// BY, under a `create_sort_path` otherwise — through the same
// `addOrderedPaths` body the normal arm uses. It returns
// (winner, true) with the winner's spec copied back onto `agg.node`
// (descending through `*Sort` only, exactly like grouping's own
// `*aggNode = *built` copy-back), or (nil, false) to run the normal
// `createOrderedPaths` call on the pristine rel.
//
// Decline is always pre-mutation OR restored: the ORDERED rel's
// `Pathlist` + cheapest fields are snapshotted before the
// per-candidate adds (sizing needs no snapshot —
// `sizeUpperRelFromNode` unconditionally overwrites from the same
// input the normal call would size from), and every decline path
// restores them, so a decline is byte-identical to the loop never
// having run. (Without restore a priced, valid loop candidate could
// win the post-decline election — "decline" would become an
// EXTRA-flip vector.)
//
// Gates (all pre-mutation): the ordered input node IS `agg.node`
// (pointer equality — a HAVING filter, window stage, min-max wrap or
// ProjectSet re-wrap declines); the grouping rel holds ≥2 PathAgg
// with 0 PathFinalizeAgg (a parallel split declines: the loop's
// serial-only ORDERED rel could otherwise elect a serial plan the
// grouping rel would have lost to Finalize under parallel knobs);
// at least one candidate translates (a Sort-only loop could only
// re-price, never change the election versus the normal call).
func electOrderedGrouping(u *upperRels, agg *aggregateSurface, node Node, keys []SortKey, stmtPos int, cp costParams, tupleFraction, limitTuples float64) (Node, bool) {
	decline := func() (Node, bool) { return nil, false }
	if u == nil || len(keys) == 0 || agg == nil || agg.node == nil || node != agg.node {
		return decline()
	}
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, tupleFraction)
	var cands []*Path
	for _, p := range grouped.Pathlist {
		if p == nil {
			continue
		}
		switch p.Kind {
		case PathFinalizeAgg:
			return decline()
		case PathAgg:
			cands = append(cands, p)
		}
	}
	if len(cands) < 2 {
		return decline()
	}
	translated := make([][]PathKey, len(cands))
	anyTranslated := false
	for i, c := range cands {
		translated[i] = groupingEmissionPathkeys(agg.node, c)
		if translated[i] != nil {
			anyTranslated = true
		}
	}
	if !anyTranslated {
		return decline()
	}

	ordered := fetchUpperRel(u, UpperOrdered, 0, tupleFraction)
	// Same input the normal call would size from (the finished aggregate
	// node), so elected and declined paths size identically.
	sizeUpperRelFromNode(ordered, agg.node)
	savedPathlist := append([]*Path(nil), ordered.Pathlist...)
	savedTotal, savedStartup, savedParam := ordered.CheapestTotal, ordered.CheapestStartup, ordered.CheapestParameterized
	restore := func() (Node, bool) {
		ordered.Pathlist = savedPathlist
		ordered.CheapestTotal, ordered.CheapestStartup, ordered.CheapestParameterized = savedTotal, savedStartup, savedParam
		return decline()
	}

	sortKeys := pathkeysForSortKeys(keys)
	for i, c := range cands {
		// Shallow copy: the translated pathkeys replace the candidate's
		// input-coordinate ones for the no-sort comparison. `Rel` stays
		// the grouping rel (as filed); the Sort-over arm prices from
		// the input's Rows/Cost/Rel — the aggregate OUTPUT rows at
		// output width, which is what an ORDER BY sort runs over.
		// `keys` is non-empty here (the arm guarantees
		// `len(s.OrderBy) > 0`); `createSortPlan` panics on empty
		// Pathkeys, so a future empty-`keys` caller must not reuse this
		// call shape blindly.
		offer := *c
		offer.Pathkeys = translated[i]
		addOrderedPaths(ordered, &offer, sortKeys, cp, limitTuples)
	}
	setCheapest(ordered)

	best := getCheapestFractionalPath(ordered, tupleFraction)
	if best == nil {
		return restore()
	}
	built, _ := createPlanNode(best)
	switch b := built.(type) {
	case *Sort:
		// Winners from `addOrderedPaths` are Sort-over-candidate or
		// the candidate itself — descend exactly one level. Deeper
		// shapes cannot arise here; anything else restores.
		ba, ok := b.Child.(*Aggregate)
		if !ok {
			return restore()
		}
		b.pos = stmtPos
		*agg.node = *ba
	case *Aggregate:
		*agg.node = *b
	default:
		return restore()
	}
	// The strategy changed under the node: recompute the keys-only keep
	// (above still unknown — the B-01c above-aware re-stamp before
	// return covers the rest), as the aggregate stage does after
	// grouping elects.
	stampAggregateInputTarget(agg.node, nil)
	return built, true
}
