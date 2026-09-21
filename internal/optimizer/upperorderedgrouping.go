package optimizer

import (
	"fmt"
	"os"
)

// R47 slice 2 (K101): the ORDERED upper rel adjudicates the GROUP_AGG
// upper rel's surviving PathAgg candidates — PG's `create_ordered_paths`
// iterating ALL input paths (planner.c:5345+ `foreach(lc,
// input_rel->pathlist)`), which goopg could not execute while grouping
// collapsed to one node before ordering saw alternatives.
//
// Two pieces: `groupingEmissionPathkeys` translates a sorted
// candidate's input-coordinate emission order into output-coordinate
// pathkeys (the coordinate boundary the walk cannot cross by DESCENDING
// — M0144-0011a gave `inputNodePathkeys` its own node-level twin,
// `aggregateEmissionPathkeys`, which performs the same positional
// translation for a finished `*Aggregate`; the two must decline on the
// same shapes),
// and `electOrderedGrouping` runs the ordered-level loop at the
// planSelect normal ORDER BY arm through the existing `addOrderedPaths`
// + `setCheapest`/`getCheapestFractionalPath` tournament.
//
// Design: docs/design/not_ralph/plan_parity_fix_take2/r47-q4-upper-rel/
// SLICE2.md. Measurement, not a pick rule: the corpus arbitrates, and
// a no-flip outcome must still deliver the §3.3 census.

// traceGroupDecline emits which specific groupingEmissionPathkeys check
// declined a candidate — M0141-S2b-5's instrumentation, reusing
// GOOPG_PGSHAPED_DP_TRACE (dpTrace, joinsearchtrace.go) rather than adding a
// third diagnostic variable, same rationale as pathTraceEnabled in
// pathtrace.go. Diagnostic only: never changes which candidate is chosen.
func traceGroupDecline(reason string, cand *Path) {
	if !dpTrace || cand == nil {
		return
	}
	strategy := 0
	if cand.Agg != nil {
		strategy = int(cand.AggStrategy)
	}
	fmt.Fprintf(os.Stderr, "DPGROUP decline reason=%s strategy=%d\n", reason, strategy)
}

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
		traceGroupDecline("strategy-or-mode", cand)
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
		traceGroupDecline("no-sort-or-gathermerge-child", cand)
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
			traceGroupDecline(fmt.Sprintf("group-expr[%d]-not-columnref(%T)", j, g), cand)
			return nil
		}
		if j >= len(childPK) || !exprEqual(childPK[j].Expr, g) {
			traceGroupDecline(fmt.Sprintf("group-expr[%d]-not-leading-child-sortkey", j), cand)
			return nil
		}
	}
	outCols := aggNode.Output()
	if len(outCols) < len(groups) {
		traceGroupDecline("fewer-outcols-than-groups", cand)
		return nil
	}
	for j, g := range groups {
		if outCols[j].Name != groupExprName(g) {
			traceGroupDecline(fmt.Sprintf("outcol[%d]-name-mismatch(%s!=%s)", j, outCols[j].Name, groupExprName(g)), cand)
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
	if dpTrace {
		fmt.Fprintf(os.Stderr, "DPGROUP translated ok keys=%d strategy=%d\n", len(emitted), int(cand.AggStrategy))
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
// ProjectSet re-wrap declines); the grouping rel holds ≥1 PathAgg
// with 0 PathFinalizeAgg (a parallel split declines: the loop's
// serial-only ORDERED rel could otherwise elect a serial plan the
// grouping rel would have lost to Finalize under parallel knobs);
// at least one candidate translates (a Sort-only loop could only
// re-price, never change the election versus the normal call).
//
// M0144-0011a-2 lowered that candidate minimum from 2 to 1. PG has no
// minimum at all — `create_ordered_paths` iterates the whole input
// pathlist, `foreach(lc, input_rel->pathlist)`
// (`postgres/src/backend/optimizer/plan/planner.c:5337`), so a LONE
// `AggPath` is offered on the ORDERED rel exactly like one of many. The
// `< 2` form was the remaining divergence from that loop, and it was
// removed for PG-faithfulness, not for a number.
//
// What the corpus census measured
// (`analysis/m0144/m0144-0011a-2-ordered-seam-census.md`): on TPC-DS
// SF0.25 the relaxation turns 37 `cands<2(1)` declines into 26
// elections plus 11 `anyTranslated=false`, and it is SHAPE-INERT —
// all 99 plan shapes are byte-identical with costs stripped, and
// `pg-plan-parity-diff.py` reports the same match (2), the same
// `missingnode` (25) and the same nine `CATEGORIES-EXCL-MATCH` counts.
// That shape result is the expected one: with the `*Aggregate` arm
// (M0144-0011a) and the identity-`Project` descent (M0144-0011a-3) in
// place, `inputNodePathkeys` derives from the FINISHED node the same
// claim this loop derives from the unbuilt `*Path`, so the two routes
// elect the same plan.
//
// It is NOT cost-inert: ten queries (Q3, Q19, Q42, Q52, Q55, Q71, Q72,
// Q85, Q91, Q93 — the ones whose seed carried a PARTIAL ordering claim,
// `keys>0 contained=false`) print a different cost on their ORDER BY
// `Sort`, because that Sort is now priced by `addOrderedPaths` over the
// `PathAgg` candidate instead of by the prebuilt seed's
// `DeriveLegacyDisplayCost`. Repricing an upper-rel Sort through
// `cost_sort` rather than the legacy display estimate is what the
// ORDERED rel was built for in the first place (upperordered.go's file
// header), so this is the intended direction, not a side effect.
func electOrderedGrouping(u *upperRels, agg *aggregateSurface, node Node, keys []SortKey, stmtPos int, cp costParams, tupleFraction, limitTuples float64, narrowKeep []int) (Node, bool) {
	decline := func(reason string) (Node, bool) {
		if dpTrace {
			fmt.Fprintf(os.Stderr, "DPGROUP loop-decline reason=%s\n", reason)
		}
		return nil, false
	}
	if u == nil || len(keys) == 0 || agg == nil || agg.node == nil || node == nil {
		return decline("gate-precondition")
	}
	// M0145-0006 slice 3: the input node no longer has to BE `agg.node` —
	// it may be a chain of positional-identity `*Project`s over it. Anything
	// else (a HAVING filter, a window stage, a min-max wrap, a ProjectSet
	// re-wrap, a non-identity projection) still declines, so the relaxation
	// is exactly the rename case and nothing wider.
	wrapped := node != agg.node
	if wrapped && !identityProjectChainTo(node, agg.node) {
		return decline("gate-precondition")
	}
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, tupleFraction)
	var cands []*Path
	for _, p := range grouped.Pathlist {
		if p == nil {
			continue
		}
		switch p.Kind {
		case PathFinalizeAgg:
			return decline("parallel-finalize-agg-present")
		case PathAgg:
			cands = append(cands, p)
		}
	}
	// PG's minimum is one path, not two (planner.c:5337 — see the doc
	// comment above). A lone translatable candidate must still be offered.
	if len(cands) < 1 {
		return decline(fmt.Sprintf("cands<1(%d)", len(cands)))
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
		return decline("anyTranslated=false")
	}

	ordered := fetchUpperRel(u, UpperOrdered, 0, tupleFraction)
	// Same input the normal call would size from (the finished aggregate
	// node), so elected and declined paths size identically.
	sizeUpperRelFromNode(ordered, agg.node)
	// M0141-S2a-fix1-sweep-a: the SAME keep-set the normal
	// `createOrderedPaths` arm would narrow with applies unchanged here —
	// sibling paths, same currency (pattern_sibling_paths_must_agree).
	// M0145-0006 slice 3 keeps that true when `node` is a rename chain over
	// `agg.node`: a positional-identity `*Project` publishes the same number
	// of columns at the same positions, and `deriveOrderedSortInputKeep`
	// (which the caller ran against `node`) names COLUMN POSITIONS, so the
	// keep-set it produced addresses the same columns of `agg.node`.
	narrowOrderedRelWidths(ordered, agg.node, narrowKeep)
	savedPathlist := append([]*Path(nil), ordered.Pathlist...)
	savedTotal, savedStartup, savedParam := ordered.CheapestTotal, ordered.CheapestStartup, ordered.CheapestParameterized
	savedSearchCandidates, savedSearchCandidateKeys := ordered.SearchCandidates, ordered.SearchCandidateKeys
	restore := func(reason string) (Node, bool) {
		ordered.Pathlist = savedPathlist
		ordered.CheapestTotal, ordered.CheapestStartup, ordered.CheapestParameterized = savedTotal, savedStartup, savedParam
		ordered.SearchCandidates, ordered.SearchCandidateKeys = savedSearchCandidates, savedSearchCandidateKeys
		return decline(reason)
	}
	// M0141-S2b-7: this rel's own candidate set for `addIncrementalSortPaths`
	// (incrementalsortpaths.go) — the third arm the normal `createOrderedPaths`
	// call populates from `searchedRelOf(input)`, which does not exist here
	// (the input is a GROUP_AGG rel's PathAgg, never a searched join/scan
	// root). `cands` IS the candidate Pathlist at this seam, and `translated`
	// IS each candidate's validated output-coordinate ordering claim
	// (nil where `groupingEmissionPathkeys` declined) — the exact two things
	// `createOrderedPaths` derives via `searchedRelOf`/
	// `validatedSearchCandidateKeys`, already computed above for a different
	// purpose (the no-sort/Sort-over election) and reused here rather than
	// rederived. Restored on every decline path below like the other
	// mutated `ordered` fields, since a later plain `createOrderedPaths`
	// call on this SAME rel (same registry, kind, relids, tupleFraction)
	// would otherwise inherit a stale GROUP_AGG-shaped candidate set when
	// its own `searchedRelOf(input)` is nil.
	ordered.SearchCandidates = cands
	ordered.SearchCandidateKeys = translated

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
	// M0140-0006b-2: the upper-rel Gather reader (same funnel as
	// createOrderedPaths above). No-op today; on decline the restore below
	// resets Pathlist, so a filed Gather cannot leak past a decline.
	generateUpperRelGatherPaths(ordered, cp)
	setCheapest(ordered)

	best := getCheapestFractionalPath(ordered, tupleFraction)
	if best == nil {
		return restore("best=nil")
	}
	built, _ := createPlanNode(best)
	switch b := built.(type) {
	case *Sort:
		// Winners from `addOrderedPaths` are Sort-over-candidate or
		// the candidate itself — descend exactly one level. Deeper
		// shapes cannot arise here; anything else restores.
		ba, ok := b.Child.(*Aggregate)
		if !ok {
			return restore("sort-child-not-aggregate")
		}
		b.pos = stmtPos
		*agg.node = *ba
		if dpTrace {
			fmt.Fprintf(os.Stderr, "DPGROUP elected shape=Sort-over-Aggregate strategy=%d\n", int(ba.Strategy))
		}
	case *Aggregate:
		*agg.node = *b
		if dpTrace {
			fmt.Fprintf(os.Stderr, "DPGROUP elected shape=bare-Aggregate strategy=%d\n", int(b.Strategy))
		}
	case *IncrementalSort:
		// M0141-S2b-7: the third arm's own winner shape — same
		// descend-one-level rule as the *Sort case above
		// (`createIncrementalSortPlan` wraps exactly one child, built from
		// the `PathAgg` candidate `addIncrementalSortPaths` offered).
		ba, ok := b.Child.(*Aggregate)
		if !ok {
			return restore("incrementalsort-child-not-aggregate")
		}
		b.pos = stmtPos
		*agg.node = *ba
		if dpTrace {
			fmt.Fprintf(os.Stderr, "DPGROUP elected shape=IncrementalSort-over-Aggregate strategy=%d\n", int(ba.Strategy))
		}
	default:
		return restore("winner-shape-unexpected")
	}
	// The strategy changed under the node: recompute the keys-only keep
	// (above still unknown — the B-01c above-aware re-stamp before
	// return covers the rest), as the aggregate stage does after
	// grouping elects.
	stampAggregateInputTarget(agg.node, nil)
	if wrapped {
		// M0145-0006 slice 3: the elected spec was copied back ONTO
		// `agg.node` in place above, so the rename chain — whose bottom
		// `*Project` still points at that same node — already carries it.
		// What must not happen is returning `built`: its top was built over
		// the BARE aggregate, and handing that to the caller would drop the
		// projection and publish the aggregate's own column labels instead
		// of the statement's.
		//
		// So the sort top (when the winner has one) is re-parented over the
		// chain, and a bare-aggregate winner returns the chain itself. The
		// re-parenting is coordinate-safe for the reason the chain was
		// admitted at all: an identity projection re-assigns no position, so
		// the sort keys — resolved by the caller against `node`'s schema and
		// compared here against claims in `agg.node`'s — address the same
		// columns on either side of it.
		switch b := built.(type) {
		case *Sort:
			b.Child = node
		case *IncrementalSort:
			b.Child = node
		default:
			return node, true
		}
		return built, true
	}
	return built, true
}

// identityProjectChainTo reports whether `top` reaches `target` through
// positional-identity `*Project`s only — M0145-0006 slice 3's admission rule
// for `electOrderedGrouping`.
//
// The measured case is a pure RENAME: TPC-DS Q21 reaches the ordered seam as
// `Project{Aggregate}` relabelling two aggregate outputs, which is why the
// pointer-equality gate declined it (`inputNodePathkeys` had the same blind
// spot until M0144-0011a-3 taught its walk the identity-Project descent; this
// is the election-side twin of that fix).
//
// `projectIsPositionalIdentity` is the shared predicate, so the two routes
// admit exactly the same projections — a wider rule here would let the
// election see through a projection the walk still stops at, and the two would
// then disagree about which plan the statement has.
func identityProjectChainTo(top Node, target *Aggregate) bool {
	if top == nil || target == nil {
		return false
	}
	for n := top; n != nil; {
		if a, ok := n.(*Aggregate); ok {
			return a == target
		}
		p, ok := n.(*Project)
		if !ok || !projectIsPositionalIdentity(p) {
			return false
		}
		n = p.Child
	}
	return false
}
