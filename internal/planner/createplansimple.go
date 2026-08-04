package planner

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
// Still inert: `GOOPG_PGSHAPED_DP` is OFF and nothing calls the search from
// `planSelect`, so no plan and no row can move. Validated in isolation by
// `createplansimple_test.go`.

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
		panic(fmt.Sprintf("createPlan: PathSeqScan over relset %#04x whose leaf is not a rebuildable base scan", uint16(p.Rel.Relids)))
	}
	if p.RequiredOuter != 0 {
		panic(fmt.Sprintf("createPlan: parameterised PathSeqScan over relset %#04x; a seq scan reads no parameter, so nothing can discharge it", uint16(p.Rel.Relids)))
	}
	if len(p.Pathkeys) != 0 {
		panic(fmt.Sprintf("createPlan: PathSeqScan over relset %#04x claims an ordering; a heap scan delivers none", uint16(p.Rel.Relids)))
	}
	if p.IndexInfo != nil || len(p.IndexClauses) != 0 {
		panic(fmt.Sprintf("createPlan: PathSeqScan over relset %#04x carries index detail; a costed probe was labelled a seq scan", uint16(p.Rel.Relids)))
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
	keys := make([]SortKey, len(p.Pathkeys))
	for i, pk := range p.Pathkeys {
		if pk.Expr == nil {
			panic(fmt.Sprintf("createPlan: PathSort pathkey %d has no expression", i))
		}
		keys[i] = SortKey{Expr: pk.Expr, Desc: !pk.SortAsc, NullsFirst: pk.NullsFirst}
	}
	return &Sort{pos: child.Pos(), Child: child, Keys: keys}, childLayout
}
