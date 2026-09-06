package optimizer

// M0127-P5.5-d — the two structurally simple `createPlan` arms:
// `create_seqscan_plan` (createplan.c:2910) and `create_sort_plan`
// (createplan.c:2177).
//
// PG oracle: `create_seqscan_plan` (createplan.c:2910) reached through
// `create_scan_plan` (createplan.c:558), and `create_sort_plan`
// (createplan.c:2177) over `make_sort_from_pathkeys` (createplan.c:6482).
// Design: leftdeep-joins 03 §3, §5.3, §10.
//
// Why these two share a slice: both consume machinery earlier slices built and
// add none of their own. The seq-scan arm is `createIndexScanPlan`'s mirror
// over the same leaf resolver (`scanLeafFor`, createplanindex.go) with the
// probe machinery removed; the sort arm is the first arm with a CHILD path,
// and it exists now because P5.4c's merge paths already carry `PathSort`
// children (`sortPathFor`, joinpathsmerge.go) — the join arms of P5.5-e call
// `createPlan` on their children, so the sort arm must exist before the merge
// arm can be written.
//
// What each arm deliberately does NOT do:
//
//   - The seq-scan arm does not return `p.Rel.baseLeaf` verbatim even when the
//     leaf's base scan already IS a `*SeqScan`. PG rebuilds a fresh node, and
//     so does this arm: the emitted tree must never alias nodes the pre-search
//     pipeline still owns (`attachRelationLocalFilters` matches leaves by
//     POINTER identity), and rebuilding unconditionally means the identity
//     copy is exercised on every path rather than only on the rare
//     `*IndexScan`-leaf demotion. `scanIdentity` carries the four
//     `*SeqScan`-only fields for exactly this arm, so the rebuild is lossless.
//   - The sort arm does not reproduce `create_sort_plan`'s CP_SMALL_TLIST
//     request (createplan.c:2188: ask the child for a narrower tlist so the
//     sort moves less data). goopg's nodes have no tlist negotiation — a
//     node's schema is fixed by its builder — so the sort carries the child's
//     full width. A cost/width divergence, not a correctness one; ledgered.
//   - The sort arm does not resolve pathkeys to child tlist COLUMNS the way
//     `prepare_sort_from_pathkeys` (createplan.c:6300) does, with its EC
//     search and resjunk additions. goopg's executor Sort evaluates
//     `SortKey.Expr` against the child row directly, so the expression IS the
//     resolution; whether its ColumnRefs are in the child's coordinate space
//     is 03 §10's coordinate-map assertion (P5.5-f), which attaches at the
//     search boundary, not per arm.
//
// Live since M0127-P5.9 (2026-08-06): `GOOPG_PGSHAPED_DP` defaults ON and
// `planSelect` calls the search, so plans and rows DO move here. Validated by
// `createplansimple_test.go`, no longer in isolation.

import "fmt"

// createSeqScanPlan is `create_seqscan_plan` (createplan.c:2910) at goopg's
// fidelity: a fresh `*SeqScan` carrying the LEAF's identity, under the leaf's
// re-created local-qual `*Filter` wrappers.
//
// The interesting case is a leaf whose base scan is an `*IndexScan`: the
// pipeline chose an index probe for this relation, and the search costed a
// sequential scan cheaper — the arm DEMOTES the leaf to the seq scan the path
// priced. The reverse promotion is `createIndexScanPlan`'s job, and both go
// through `scanIdentityOf`, so the set of fields that survives either rewrite
// is stated once.
//
// The preconditions panic, per createplan.go's contract (a producer bug, not a
// recoverable condition), and each names the wrong answer it prevents:
//
//   - a parameterised seq-scan path is undischargeable — a seq scan reads no
//     parameter, so nothing above it can ever bind `RequiredOuter` and the
//     plan cannot be built (PG cannot construct one: `create_seqscan_path`'s
//     only caller passes `required_outer` from the rel's lateral refs, which
//     goopg's search has none of);
//   - pathkeys on a seq-scan path claim an ordering a heap scan does not
//     deliver, and a merge join above would trust it and emit wrong rows;
//   - index detail (`IndexInfo` / `IndexClauses`) on a seq-scan path means a
//     producer costed a probe and labelled it a seq scan — building either
//     interpretation silently discards the other.
func createSeqScanPlan(p *Path) Node {
	if p.Rel == nil {
		panic("createPlan: PathSeqScan with no RelOptInfo")
	}
	id, rewrap, ok := scanLeafFor(p.Rel.baseLeaf)
	if !ok {
		panic(fmt.Sprintf("createPlan: PathSeqScan over relset %#08x whose leaf is not a rebuildable base scan", uint32(p.Rel.Relids)))
	}
	if p.RequiredOuter != 0 {
		panic(fmt.Sprintf("createPlan: parameterised PathSeqScan over relset %#08x; a seq scan reads no parameter, so nothing can discharge it", uint32(p.Rel.Relids)))
	}
	if len(p.Pathkeys) != 0 {
		panic(fmt.Sprintf("createPlan: PathSeqScan over relset %#08x claims an ordering; a heap scan delivers none", uint32(p.Rel.Relids)))
	}
	if p.IndexInfo != nil || len(p.IndexClauses) != 0 {
		panic(fmt.Sprintf("createPlan: PathSeqScan over relset %#08x carries index detail; a costed probe was labelled a seq scan", uint32(p.Rel.Relids)))
	}
	return rewrap(&SeqScan{
		pos:   id.pos,
		Table: id.table,
		Alias: id.alias,
		// The schema is the LEAF's, never synthesised — every ColumnRef in
		// every clause of the search was resolved against these coordinates
		// (createplanindex.go's file header, loss #1).
		schema:                id.schema,
		EstRelRows:            id.estRelRows,
		SmallDim:              id.smallDim,
		UniqueKeys:            id.uniqueKeys,
		LockParentOID:         id.lockParentOID,
		SkipIfVanished:        id.skipIfVanished,
		InheritParentOID:      id.inheritParentOID,
		PrivilegeCheckRole:    id.privilegeCheckRole,
		PrivilegeCheckRoleSet: id.privilegeCheckRoleSet,
	})
}

// createSortPlan is `create_sort_plan` (createplan.c:2177): recurse into the
// one child, then wrap it in the executor's `*Sort` delivering the path's own
// pathkeys.
//
// The key translation is direction-only: `PathKey.SortAsc` was defined
// (pathkeys.go) with PG's ascending-true sense while the executor's
// `SortKey.Desc` is descending-true, so the arm negates; `NullsFirst` has the
// same sense on both sides. An empty pathkey list is a sort that orders by
// nothing — `sortPathFor` cannot produce one (it exists to deliver an
// ordering), so it panics as a producer bug rather than building a Sort the
// executor would run as an expensive no-op.
//
// `RequiredOuter` is deliberately NOT checked here: PG's `create_sort_plan`
// does not check it either, and the refusal belongs to the JOIN that would
// have to discharge it (`tryMergeJoinPath` already refuses a parameterised
// merge input before ever wrapping it in a sort, joinpathsmerge.go:344).
//
// The child's `outputLayout` (M0127-P5.5-e-i) passes through UNCHANGED, and
// that is the whole content of a sort's coordinate story: a sort reorders rows,
// never columns, so output column i is still the child's column i and still the
// same pre-search binding coordinate. Returning it rather than nil is what lets
// a merge join sit above a sorted child and still re-base its keys.
//
// The KEYS, however, do not pass through: they are `PathKey.Expr`s, written like
// every other expression the search reasons about in pre-search BINDING
// coordinates, while the emitted `*Sort` evaluates them against its CHILD's row.
// For a child whose `baseOffset` is 0 those coincide, which is why P5.5-d could
// ship without noticing; for a rel that was not first in binding order they do
// not, and the sort silently orders by whichever column landed at that index.
// `translateToLayout` (createplanjoin.go) re-bases them, the same function and
// therefore the same `scopeIgnore` policy the join arms use — rule #2, sibling
// paths must agree about what a coordinate means.
//
// A nil child layout means the coordinates are unknown (the C0 bridge's prebuilt
// subtree — see `outputLayout`'s doc). There is nothing to translate against, so
// the keys are emitted as written; the only producer that can reach this is the
// bridge, whose subtree was never expressed in binding coordinates in the first
// place.
// createAggPlan is the PathAgg arm (C-15): emit the path's aggregate spec
// over the built input with the path's strategy. The spec is COPIED, never
// rebound in place — sibling candidates share it, and the index-driven
// variant's narrowed clone must not leak into them. The B-01c input-target
// stamp is NOT applied here: the planner recomputes it on the emitted node
// after the producer returns (same site as today), ordered after any
// index-narrowing remap.
//
// The returned layout is nil: an Aggregate reorders columns (group keys +
// agg outputs + passthrough), so no binding map of the child survives it —
// the same nil `baseRelLayout` yields for any upper rel without a baseLeaf.
// Callers above the seam work in Nodes (Output schemas), never layouts.
func createAggPlan(p *Path) (Node, outputLayout) {
	if p.Agg == nil {
		panic("createPlan: PathAgg with no aggregate spec")
	}
	if len(p.Children) != 1 {
		panic(fmt.Sprintf("createPlan: PathAgg with %d children, want exactly 1", len(p.Children)))
	}
	child, _ := createPlanNode(p.Children[0])
	if child == nil {
		panic("createPlan: PathAgg over a child path that built no node")
	}
	out := *p.Agg
	out.Child = child
	out.Strategy = p.AggStrategy
	return &out, nil
}

// createDistinctPlan is the PathDistinct arm (C-16): emit the path's
// DISTINCT spec over the built input — `*Distinct` (hash dedup), or
// `DistinctOn` with all-output-columns keys when the path is Unique
// (streaming adjacent dedup over the producer-stacked Sort, C-16b).
// The KeyCols cover every output position, which is exactly full-row
// dedup; the child order contract ("equal keys contiguous") holds because
// the unique arm is only ever offered over the producer's own Sort.
//
// The returned layout is nil, as for the aggregate arm: DISTINCT preserves
// columns positionally (dedup, not projection), but the layout describes
// BINDING coordinates and the producers above the seam work in Nodes —
// the same nil `baseRelLayout` yields for any upper rel without a baseLeaf.
// Callers above the seam never read it (C-12 precedent).
func createDistinctPlan(p *Path) (Node, outputLayout) {
	if p.Distinct == nil {
		panic("createPlan: PathDistinct with no distinct spec")
	}
	if len(p.Children) != 1 {
		panic(fmt.Sprintf("createPlan: PathDistinct with %d children, want exactly 1", len(p.Children)))
	}
	child, _ := createPlanNode(p.Children[0])
	if child == nil {
		panic("createPlan: PathDistinct over a child path that built no node")
	}
	if p.Unique {
		return &DistinctOn{pos: p.Distinct.pos, Child: child, KeyCols: distinctAllKeyCols(child), schema: p.Distinct.schema}, nil
	}
	return &Distinct{pos: p.Distinct.pos, Child: child, schema: p.Distinct.schema}, nil
}

func createSortPlan(p *Path) (Node, outputLayout) {
	if len(p.Children) != 1 {
		panic(fmt.Sprintf("createPlan: PathSort with %d children, want exactly 1", len(p.Children)))
	}
	if len(p.Pathkeys) == 0 {
		panic("createPlan: PathSort with no pathkeys; a sort that orders by nothing")
	}
	child, childLayout := createPlanNode(p.Children[0])
	if child == nil {
		panic("createPlan: PathSort over a child path that built no node")
	}
	var index map[int]int
	if childLayout != nil {
		index = childLayout.bindingIndex()
	}
	keys := make([]SortKey, len(p.Pathkeys))
	for i, pk := range p.Pathkeys {
		if pk.Expr == nil {
			panic(fmt.Sprintf("createPlan: PathSort pathkey %d has no expression", i))
		}
		e := pk.Expr
		if index != nil {
			e = translateToLayout("sort key", e, childLayout, index)
		}
		keys[i] = SortKey{Expr: e, Desc: !pk.SortAsc, NullsFirst: pk.NullsFirst}
	}
	return &Sort{pos: child.Pos(), Child: child, Keys: keys}, childLayout
}
