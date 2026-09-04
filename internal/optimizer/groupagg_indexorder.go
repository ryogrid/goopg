package optimizer

import "github.com/goopg/goopg/internal/catalog"

// Index-ordered grouping input — S8 Slice 2c-i of M0134-0001. Port of the
// "path already sorted" half of PostgreSQL's
// get_useful_group_keys_orderings (postgres/src/backend/optimizer/path/
// pathkeys.c:466-550), whose alternate ordering candidate is built by
// group_keys_reorder_by_pathkeys (pathkeys.c:375-450): walk the input path's
// own pathkeys, move each one that matches a GROUP BY clause to the front,
// stop at the first mismatch, append the leftovers. goopg's optimizer has no
// path enumeration, so the search runs in the opposite direction: given the
// GROUP BY keys, ask the catalog for a usable btree index and test whether
// SOME ordering of the keys is exactly a leading prefix of that index's
// columns. See docs/design/0134-0001-p2-explain-format.md, section
// "S8 Slice 2c — Rule 3 (index-ordered grouping input): scope + decomposition"
// for the full correctness argument and the explicit out-of-scope list
// (partial-prefix matches need Incremental Sort — Slice 2c-ii, deferred; an
// ORDER-BY-aware ordering choice is Slice 2c-iii, deferred).
//
// Runs BEFORE applyPresortedAggregateRule / applyEnableHashAggRule so those
// never get the chance to wrap the child this rule has already made
// sorted-and-index-driven in a redundant Sort.

// applyIndexOrderedGroupingRule mutates aggNode in place: on a match the
// child is replaced with an ascending full-range *IndexOnlyScan or
// *IndexScan (nil Key/Keys/LowKey/HighKey — the executor RangeScans the
// whole index in ascending key order) and Strategy becomes
// AggStrategySorted, with NO Sort inserted. It returns without touching
// aggNode unless ALL of the following hold:
//
//   - the node is a plain grouped aggregate: Mode == AggModeSimple, no
//     grouping sets, len(GroupExprs) > 0, and Strategy is not already
//     AggStrategySorted (a query with an internal ORDER BY / DISTINCT
//     aggregate, or an already-sorted parallel Partial/Final split, has
//     already been handled and must not be double-touched);
//   - every GroupExpr is a plain column reference on the scanned table
//     (GROUP BY x*x and friends are out of scope — bail);
//   - the child is a bare *SeqScan, or *Filter{Child: *SeqScan};
//   - cat.IndexesOnTable(seqScan.Table) has a usable btree index: Method ==
//     "btree" (or unset), not HasPredicate, not DeclaredHash, and every
//     consumed leading column is default-ordered (!ColDescending[i] &&
//     !ColNullsFirst[i]) — mirrors tryPromoteOrderedIndexOnlyScan's filters
//     (planner.go:13349-13375) exactly;
//   - the group-key column NAMES equal idx.Columns[:len(GroupExprs)] as a
//     SET, i.e. the keys are a permutation of an exact leading prefix.
//     Partial prefixes fall through untouched (Slice 2c-ii, deferred).
//
// Deliberately NOT gated on DisableSeqScan / enable_seqscan: PG picks this
// index by cost, and the regress fixture only sets enable_seqscan = off to
// make PG's choice deterministic; gating on it here would reproduce the
// EXPLAIN output for the wrong reason and leave the rule dead for ordinary
// queries (design section "enable_seqscan = off").
//
// GroupExprs is never permuted (the pinned correctness argument in the
// design section: every downstream target-list/HAVING/ORDER BY binding is
// fixed to its written position, and a sorted GroupAggregate's group-boundary
// test is order-independent, so an uncompensated permutation would silently
// move data between output columns for no executor benefit). Instead the
// chosen index order is recorded in aggNode.GroupKeyOrder — indices into
// GroupExprs, in index-column order — consumed only by the `Group Key:`
// EXPLAIN renderer (internal/executor/operators_explain.go).
func applyIndexOrderedGroupingRule(aggNode *Aggregate, cat catalog.Catalog, ps PlannerSettings) {
	if aggNode == nil || cat == nil {
		return
	}
	if aggNode.Mode != AggModeSimple || aggNode.GroupingSets != nil ||
		len(aggNode.GroupExprs) == 0 || aggNode.Strategy == AggStrategySorted {
		return
	}
	// Without a cost model, goopg cannot tell whether an index-ordered
	// sorted plan actually beats a hash aggregate the way PG's cost_agg
	// does (the design section's "cost comparison ... simply falls back to
	// the original" — goopg has no such comparison to run). Firing
	// unconditionally regresses aggregates.sql's primary-key functional-
	// dependency block (`t1 group by a,b,c,d`, reduced elsewhere to `a,b`
	// before this rule ever sees it, with enable_hashagg left ON): PG picks
	// HashAggregate there, but this rule would wrongly promote it to
	// GroupAggregate + Index Only Scan. Every btg regress probe this rule
	// targets runs inside `SET enable_hashagg = off` (aggregates.sql:1275-
	// 1370), so mirror applyEnableHashAggRule's own bridge and require the
	// same session state.
	if ps.EnableHashAgg {
		return
	}

	// Every GroupExpr must be a plain column reference (bail on expressions
	// — the design's explicit scope line).
	groupCols := make([]string, len(aggNode.GroupExprs))
	for i, g := range aggNode.GroupExprs {
		cr, ok := g.(*ColumnRef)
		if !ok {
			return
		}
		groupCols[i] = cr.Name
	}
	groupSet := make(map[string]bool, len(groupCols))
	for _, n := range groupCols {
		if groupSet[n] {
			// Duplicate GROUP BY column names (e.g. `GROUP BY x, x`) cannot
			// be a permutation of a distinct-column index prefix; bail
			// conservatively rather than guess a mapping.
			return
		}
		groupSet[n] = true
	}

	// Child must be a bare *SeqScan, or *Filter{Child: *SeqScan}. The
	// *Filter wrapping, if present, is kept around the replacement scan
	// unchanged in shape (its predicate is not turned into an Index Cond —
	// out of scope).
	var filt *Filter
	var seqScan *SeqScan
	switch c := aggNode.Child.(type) {
	case *SeqScan:
		seqScan = c
	case *Filter:
		ss, ok := c.Child.(*SeqScan)
		if !ok {
			return
		}
		filt = c
		seqScan = ss
	default:
		return
	}
	if seqScan.Table == nil {
		return
	}

	for _, idx := range cat.IndexesOnTable(seqScan.Table) {
		if idx == nil || idx.DeclaredHash || idx.HasPredicate {
			continue
		}
		if idx.Method != "" && idx.Method != "btree" {
			continue
		}
		if len(idx.Columns) < len(groupCols) {
			continue
		}
		prefix := idx.Columns[:len(groupCols)]
		ordered := true
		prefixSet := make(map[string]bool, len(prefix))
		for i, c := range prefix {
			if i < len(idx.ColDescending) && idx.ColDescending[i] {
				ordered = false
				break
			}
			if i < len(idx.ColNullsFirst) && idx.ColNullsFirst[i] {
				ordered = false
				break
			}
			prefixSet[c] = true
		}
		if !ordered || len(prefixSet) != len(groupSet) {
			continue
		}
		match := true
		for c := range groupSet {
			if !prefixSet[c] {
				match = false
				break
			}
		}
		if !match {
			continue
		}

		newChild, ok := buildIndexOrderedScan(seqScan, idx, aggNode, filt)
		if !ok {
			continue
		}
		// The rule replaces the SeqScan with an IndexScan or an IndexOnlyScan;
		// a session that turned that shape off keeps its SeqScan (the toggles
		// are read through the same carrier walk currentSeqScanDisabled uses).
		// review/260831-2 X-8.
		if scanShapeDisabled(newChild, cat) {
			continue
		}

		// GroupKeyOrder: indices into GroupExprs, in index-column order.
		order := make([]int, len(prefix))
		for i, c := range prefix {
			for gi, gc := range groupCols {
				if gc == c {
					order[i] = gi
					break
				}
			}
		}

		aggNode.Child = newChild
		aggNode.Strategy = AggStrategySorted
		aggNode.GroupKeyOrder = order
		return
	}
}

// buildIndexOrderedScan builds the replacement child for aggNode: an
// ascending full-range *IndexOnlyScan (nil Key/Keys/LowKey/HighKey) when idx
// (key + INCLUDE columns) covers every column referenced above the scan —
// GroupExprs, every Agg's arguments/FILTER/internal ORDER BY, Passthrough,
// and (if present) the wrapping *Filter's predicate — exactly the "covered"
// computation tryPromoteOrderedIndexOnlyScan performs (planner.go:13380-
// 13411); otherwise a plain *IndexScan, which — like the *SeqScan it
// replaces — reads the full heap row and therefore needs no schema/position
// remap at all.
//
// On an *IndexOnlyScan build every ColumnRef above the scan is rewritten to
// the new child's (narrower) column positions via the generic cloneExprRefs
// driver (exprwalk.go). This is NOT the permutation the design section
// forbids: GroupExprs keeps its length and written order, only each
// element's .Index field (which physical column it reads) is corrected —
// the same bookkeeping every other scan-narrowing rewrite in this package
// performs (e.g. tryPromoteOrderedIndexOnlyScan's own
// Filter-survives-narrowing path, planner.go:13194-13205).
func buildIndexOrderedScan(seqScan *SeqScan, idx *catalog.Index, aggNode *Aggregate, filt *Filter) (Node, bool) {
	oldSchema := seqScan.Output()
	nonNarrowing := func() (Node, bool) {
		return &IndexScan{
			pos: seqScan.pos, Table: seqScan.Table, Alias: seqScan.Alias,
			Index: idx, schema: oldSchema,
			SmallDim: seqScan.SmallDim, UniqueKeys: seqScan.UniqueKeys,
		}, true
	}

	referenced := map[string]bool{}
	collect := func(e Expr) {
		if e == nil {
			return
		}
		WalkExprTree(e, func(sub Expr) {
			if cr, ok := sub.(*ColumnRef); ok {
				referenced[cr.Name] = true
			}
		})
	}
	for _, g := range aggNode.GroupExprs {
		collect(g)
	}
	for i := range aggNode.Aggs {
		a := &aggNode.Aggs[i]
		collect(a.Arg)
		collect(a.Arg2)
		for _, ea := range a.ExtraArgs {
			collect(ea)
		}
		collect(a.Filter)
		for _, ob := range a.OrderBy {
			collect(ob.Expr)
		}
		for _, ob := range a.WithinGroupOrderBy {
			collect(ob.Expr)
		}
	}
	for _, p := range aggNode.Passthrough {
		collect(p)
	}
	if filt != nil {
		collect(filt.Predicate)
	}

	idxColSet := make(map[string]bool, len(idx.Columns)+len(idx.IncludeColumns))
	for _, c := range idx.Columns {
		idxColSet[c] = true
	}
	for _, c := range idx.IncludeColumns {
		idxColSet[c] = true
	}
	for name := range referenced {
		if !idxColSet[name] {
			return nonNarrowing()
		}
	}

	// Build the narrowed IndexOnlyScan schema: oldSchema entries restricted
	// to the referenced columns, in oldSchema's original relative order (a
	// bare SeqScan's schema has one entry per table column with unique
	// names, so there is no ambiguity to resolve).
	newSchema := make(Schema, 0, len(referenced))
	newIndex := make(map[string]int, len(referenced))
	covered := make([]catalog.Column, 0, len(referenced))
	for _, sc := range oldSchema {
		if !referenced[sc.Name] {
			continue
		}
		col, found := tableColumnByName(seqScan.Table, sc.Name)
		if !found {
			// Should not happen for a bare-table scan schema, but fail
			// closed to the non-narrowing IndexScan rather than build an
			// IndexOnlyScan with an incomplete Covered list.
			return nonNarrowing()
		}
		newIndex[sc.Name] = len(newSchema)
		newSchema = append(newSchema, sc)
		covered = append(covered, col)
	}

	// remapChecked rewrites every ColumnRef in e to its position in
	// newIndex, built on cloneExprRefs (exprwalk.go) — the package's
	// generic, EXHAUSTIVE (32/32 Expr types, build-time-pinned by
	// TestExprSwitchInventoryIsPinned) rewrite driver, rather than a new
	// hand-written type switch. scopeVeto means any node that crosses into
	// a different coordinate space (a correlated subquery) — or any Expr
	// type the driver does not yet enumerate — makes cloneExprRefs return
	// (nil, false), which this function treats as "cannot narrow safely"
	// and falls back to the non-narrowing *IndexScan below. This is
	// exactly the discipline the design section demands: an untouched
	// ColumnRef kept at its pre-narrowing .Index would silently read the
	// wrong physical column.
	newIndexMap := newIndex
	remapChecked := func(e Expr) (Expr, bool) {
		return cloneExprRefs(e, scopeVeto, exprRewriter{
			Rewrite: func(x Expr) Expr {
				cr, ok := x.(*ColumnRef)
				if !ok {
					return x
				}
				nc := *cr
				nc.Index = newIndexMap[oldSchema[cr.Index].Name]
				return &nc
			},
		})
	}

	newGroupExprs := make([]Expr, len(aggNode.GroupExprs))
	for i, g := range aggNode.GroupExprs {
		ng, ok := remapChecked(g)
		if !ok {
			return nonNarrowing()
		}
		newGroupExprs[i] = ng
	}
	newAggs := make([]AggregateCall, len(aggNode.Aggs))
	for i := range aggNode.Aggs {
		a := aggNode.Aggs[i]
		var ok bool
		if a.Arg, ok = remapChecked(a.Arg); !ok {
			return nonNarrowing()
		}
		if a.Arg2, ok = remapChecked(a.Arg2); !ok {
			return nonNarrowing()
		}
		if a.Filter, ok = remapChecked(a.Filter); !ok {
			return nonNarrowing()
		}
		if len(a.ExtraArgs) > 0 {
			newExtra := make([]Expr, len(a.ExtraArgs))
			for j, ea := range a.ExtraArgs {
				if newExtra[j], ok = remapChecked(ea); !ok {
					return nonNarrowing()
				}
			}
			a.ExtraArgs = newExtra
		}
		if len(a.OrderBy) > 0 {
			newOrderBy := make([]SortKey, len(a.OrderBy))
			for j, ob := range a.OrderBy {
				ob.Expr, ok = remapChecked(ob.Expr)
				if !ok {
					return nonNarrowing()
				}
				newOrderBy[j] = ob
			}
			a.OrderBy = newOrderBy
		}
		if len(a.WithinGroupOrderBy) > 0 {
			newWG := make([]SortKey, len(a.WithinGroupOrderBy))
			for j, ob := range a.WithinGroupOrderBy {
				ob.Expr, ok = remapChecked(ob.Expr)
				if !ok {
					return nonNarrowing()
				}
				newWG[j] = ob
			}
			a.WithinGroupOrderBy = newWG
		}
		newAggs[i] = a
	}
	newPassthrough := make([]Expr, len(aggNode.Passthrough))
	for i, p := range aggNode.Passthrough {
		np, ok := remapChecked(p)
		if !ok {
			return nonNarrowing()
		}
		newPassthrough[i] = np
	}
	var newPredicate Expr
	if filt != nil {
		np, ok := remapChecked(filt.Predicate)
		if !ok {
			return nonNarrowing()
		}
		newPredicate = np
	}

	ios := &IndexOnlyScan{
		pos: seqScan.pos, Table: seqScan.Table, Alias: seqScan.Alias, RTID: seqScan.RTID, Index: idx,
		Covered: covered, schema: newSchema,
	}
	aggNode.GroupExprs = newGroupExprs
	aggNode.Aggs = newAggs
	aggNode.Passthrough = newPassthrough
	if filt != nil {
		filt.Predicate = newPredicate
		filt.Child = ios
		return filt, true
	}
	return ios, true
}

// tableColumnByName looks up a table's catalog.Column by name — the same
// linear scan tryPromoteOrderedIndexOnlyScan's inline closure performs
// (planner.go:13395-13402).
func tableColumnByName(tbl *catalog.Table, name string) (catalog.Column, bool) {
	for _, c := range tbl.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return catalog.Column{}, false
}
