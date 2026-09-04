package optimizer

import "fmt"

// B-01c Slice 1 — COMPUTE-ONLY `sort_input_target` (no plan mutation).
//
// This file derives the Sort site's input keep-set — sort-key columns ∪
// above-needed columns — stamps it on the Sort node as a Target-like payload,
// and asserts the keep covers the sort keys. It is the Slice-1 pattern of
// 588aa5fb5 (stamp + assert, zero behavior change): no Project insertion, no
// schema change, no cost change. A future applying cut may narrow the Sort
// input from InputTarget; that cut is NOT this file.
//
// The three upper sites (buildAggregateStage, buildWindowStage, Sort
// construction) were reviewed together; this cut implements ONLY the Sort
// construction site (planner.go ORDER BY Sorts). Group-second / window-last
// shapes are out of scope.
//
// DERIVATION (existing walkers only — nothing invented):
//
//   - Sort keys: one visitColumnRefsByName (the fail-closed walkExprRefs
//     collector shared with collectJoinQualNames / mergeSortKeyNames) per
//     key Expr — the same per-key walkExprTree enumeration walkPlanExprs'
//     Sort arm performs (unnest.go:1076-1080). Desc / NullsFirst are ordering
//     flags, not column reads, and contribute nothing by construction.
//   - Above-needed columns: the plan chain above the Sort walked top-down
//     via enclosingNodeScopeOf (enclosingtree.go:122-123 for the Sort arm),
//     collecting each level's own expressions with the same fail-closed
//     collector. The walk stops AT the Sort (its input subtree is below the
//     narrowing point, never above it) and descends ONLY along the path to
//     the Sort, so off-path subtrees can neither contribute nor poison.
//
// Anything unenumerable — an unenumerated node kind on the path, an
// unenumerable expression (unnamed ref, outer-scope ref, CTID/whole-row ref,
// inner-scope plan, unknown type) — marks the derivation UNKNOWN (Known
// false), never invents a list. Unknown is the only safe answer: it declines
// any future applying cut back to today's full-width input bit-identically.

// sortKeyColumnNames returns every column name the sort keys read, and whether
// the answer is COMPLETE. Incomplete (false) means "decline": an unenumerated
// key expression must keep its columns, the Slice-2 precedent (F3). A nil key
// Expr contributes nothing (it matches neither walkPlanExprs' nor
// enclosingNodeScopeOf's Sort arms, which both skip nils).
func sortKeyColumnNames(keys []SortKey) (map[string]bool, bool) {
	names := make(map[string]bool, len(keys))
	for _, k := range keys {
		if k.Expr == nil {
			continue
		}
		if !visitColumnRefsByName(k.Expr, func(name string) { names[name] = true }) {
			return nil, false
		}
	}
	return names, true
}

// aboveSortNeededNames returns every column name the plan chain above stopAt
// reads from the Sort's output row, and whether the answer is COMPLETE.
// above is the tree root (or any ancestor-or-self of stopAt); the walk
// descends only along the path to stopAt, collecting each level's own
// expressions via enclosingNodeScopeOf. Incomplete (false) means "decline":
// stopAt unreachable from above, an unenumerated node kind on the path, or an
// unenumerable expression at any collected level.
func aboveSortNeededNames(above Node, stopAt *Sort) (map[string]bool, bool) {
	if stopAt == nil {
		return nil, false
	}
	path, ok := pathToSortNode(above, stopAt)
	if !ok {
		return nil, false
	}
	names := make(map[string]bool, 8)
	for _, n := range path {
		if s, isSort := n.(*Sort); isSort && s == stopAt {
			continue
		}
		sc, ok := enclosingNodeScopeOf(n)
		if !ok {
			return nil, false
		}
		for _, e := range sc.exprs {
			if !visitColumnRefsByName(e, func(name string) { names[name] = true }) {
				return nil, false
			}
		}
	}
	return names, true
}

// pathToSortNode returns the chain of nodes from above down to stopAt
// (both inclusive), following enclosingNodeScopeOf children. ok == false
// means stopAt is not reachable from above through enumerated scopes —
// including the case where an unenumerated node kind sits on the way — and
// the caller must decline rather than collect a partial path.
func pathToSortNode(above Node, stopAt *Sort) ([]Node, bool) {
	if above == nil || stopAt == nil {
		return nil, false
	}
	if s, ok := above.(*Sort); ok && s == stopAt {
		return []Node{above}, true
	}
	sc, ok := enclosingNodeScopeOf(above)
	if !ok {
		return nil, false
	}
	for _, c := range sc.children {
		if p, ok := pathToSortNode(c, stopAt); ok {
			return append([]Node{above}, p...), true
		}
	}
	return nil, false
}

// deriveSortInputKeep computes the Sort input keep-set: ascending positions
// into the Sort's input schema (sort.Child.Output()) of the sort-key columns
// plus the above-needed columns. above == nil means "no above information"
// (the construction-time stamp): the keep is the sort keys alone. ok == false
// ("unknown", decline) wherever the derivation does not apply: nil sort, nil
// child, unenumerable keys, or (with a non-nil above) an unenumerable
// above-chain.
//
// A name in the union that matches no input column contributes no position:
// above levels may name constants/params (which read no row column) and keys
// are checked separately by the coverage assert. In particular an above name
// outside the input schema is NOT unknown — only the collectors' vetoes are.
func deriveSortInputKeep(sort *Sort, above Node) ([]int, bool) {
	if sort == nil || sort.Child == nil {
		return nil, false
	}
	keyNames, ok := sortKeyColumnNames(sort.Keys)
	if !ok {
		return nil, false
	}
	need := make(map[string]bool, len(keyNames)+8)
	for name := range keyNames {
		need[name] = true
	}
	if above != nil {
		aboveNames, ok := aboveSortNeededNames(above, sort)
		if !ok {
			return nil, false
		}
		for name := range aboveNames {
			need[name] = true
		}
	}
	in := sort.Child.Output()
	keep := make([]int, 0, len(need))
	for i, col := range in {
		if need[col.Name] {
			keep = append(keep, i)
		}
	}
	return keep, true
}

// assertSortInputTargetCoversKeys is the Slice-1 consistency assert: when the
// stamp is known, every enumerable sort-key column must survive in the keep.
// It panics loudly (fail-closed) instead of letting a future applying cut
// drop a column the sort reads — the coverage translateToLayout needs,
// checked at stamp time. Unknown stamps assert nothing: declining is always
// safe. The ascending/unique/in-range shape is checked defensively too: the
// derivation scans the schema in order so a violation is an internal bug,
// not a bad query.
func assertSortInputTargetCoversKeys(sort *Sort) {
	if sort == nil || !sort.InputTargetKnown {
		return
	}
	keep := sort.InputTarget
	in := Schema{}
	if sort.Child != nil {
		in = sort.Child.Output()
	}
	prev := -1
	for _, c := range keep {
		if c <= prev || c < 0 || c >= len(in) {
			panic(fmt.Sprintf("createPlan: Sort input target %v is not an ascending in-range subset of the %d-column input row", keep, len(in)))
		}
		prev = c
	}
	kept := make(map[string]bool, len(keep))
	for _, c := range keep {
		kept[in[c].Name] = true
	}
	keyNames, ok := sortKeyColumnNames(sort.Keys)
	if !ok {
		panic(fmt.Sprintf("createPlan: Sort input target is marked known but its sort keys are not enumerable"))
	}
	for name := range keyNames {
		if !kept[name] {
			panic(fmt.Sprintf("createPlan: Sort input target %v drops sort-key column %q of a %d-column input row", keep, name, len(in)))
		}
	}
}

// stampSortInputTarget derives the keep for sort (keys ∪ above, see
// deriveSortInputKeep), stamps it as the node's Target-like payload, and runs
// the coverage assert. Additive only: no node is inserted, no schema/cost is
// touched. Overwrite-only (never accumulates), so re-stamping a tree (the
// construction-time keys-only stamp, then the finalized above-aware stamp)
// recomputes the same-or-wider list.
func stampSortInputTarget(sort *Sort, above Node) {
	if sort == nil {
		return
	}
	keep, ok := deriveSortInputKeep(sort, above)
	if !ok {
		sort.InputTarget, sort.InputTargetKnown = nil, false
		return
	}
	sort.InputTarget, sort.InputTargetKnown = keep, true
	assertSortInputTargetCoversKeys(sort)
}
