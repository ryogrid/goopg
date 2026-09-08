package optimizer

import "fmt"

// B-01c third cut — COMPUTE-ONLY `window_input_target` (no plan mutation).
//
// This file derives the WindowAgg site's input keep-set — the columns the
// WindowAgg reads from its input row (PartitionBy ∪ OrderBy key columns ∪
// func args ∪ func Filters ∪ frame offsets) ∪ above-needed columns — stamps
// it on the WindowAgg node as a Target-like payload, and asserts the keep
// covers the partition/order inputs. It mirrors the Slice-1 sort-input
// pattern (sort_input_target.go: stamp + assert, zero behavior change): no
// Project insertion, no schema change, no cost change. A future applying cut
// may narrow the WindowAgg input from InputTarget; that cut is NOT this
// file.
//
// The three upper sites (buildAggregateStage, buildWindowStage, Sort
// construction) were reviewed together; this cut implements ONLY the
// buildWindowStage site. Group-first / window-chained shapes stamp each
// WindowAgg keys-only at construction.
//
// DERIVATION (existing walkers only — nothing invented):
//
//   - Window inputs: one visitColumnRefsByName (the fail-closed walkExprRefs
//     collector shared with collectJoinQualNames / mergeSortKeyNames) per
//     PartitionBy expression, per OrderBy key Expr, per func Arg, per func
//     Filter, and per frame offset Expr — the same per-expression
//     enumeration walkPlanExprs' WindowAgg arm performs (unnest.go,
//     B-01c third-cut step 1) and enclosingNodeScopeOf's WindowAgg arm
//     performs (enclosingtree.go:154-165). Unlike the group cut there is no
//     field-level decline rule: every expression field on this node is
//     enumerated by both walkers, so "unenumerable" can only come from the
//     collector's veto (unnamed ref, outer-scope ref, CTID/whole-row ref,
//     inner-scope plan, unknown type), never from a skipped field. A nil
//     Filter / nil frame offset contributes nothing (matching both arms,
//     which walk non-nil fields only); a nil frame contributes nothing.
//   - Above-needed columns: the plan chain above the WindowAgg walked
//     top-down via enclosingNodeScopeOf (enclosingtree.go:154 for the
//     WindowAgg arm), collecting each level's own expressions with the same
//     fail-closed collector. The walk stops AT the WindowAgg (its input
//     subtree is below the narrowing point, never above it) and descends
//     ONLY along the path to the WindowAgg, so off-path subtrees can neither
//     contribute nor poison.
//
// Anything unenumerable marks the derivation UNKNOWN (Known false), never
// invents a list. Unknown is the only safe answer: it declines any future
// applying cut back to today's full-width input bit-identically.
//
// LIFECYCLE: buildWindowStage stamps keys-only at construction (above not
// yet built). Chained WindowAggs (one per distinct window spec) each stamp
// their own node; nothing appends to a WindowAgg afterwards, so no re-stamp
// site exists (unlike the Aggregate passthrough append).

// windowWindowInputNames returns every column name the WindowAgg reads from
// its input row, and whether the answer is COMPLETE. Incomplete (false) means
// "decline": an unenumerable partition/order/arg/filter/offset expression.
// A nil Filter, nil frame, or nil frame offset contributes nothing (matching
// walkPlanExprs' WindowAgg arm, which walks non-nil fields only).
func windowWindowInputNames(w *WindowAgg) (map[string]bool, bool) {
	if w == nil {
		return nil, false
	}
	names := make(map[string]bool, len(w.PartitionBy)+len(w.OrderBy)+len(w.Funcs))
	visit := func(e Expr) bool {
		if e == nil {
			return true
		}
		if !visitColumnRefsByName(e, func(name string) { names[name] = true }) {
			return false
		}
		return true
	}
	for _, p := range w.PartitionBy {
		if !visit(p) {
			return nil, false
		}
	}
	for _, k := range w.OrderBy {
		if !visit(k.Expr) {
			return nil, false
		}
	}
	for i := range w.Funcs {
		f := &w.Funcs[i]
		for _, a := range f.Args {
			if !visit(a) {
				return nil, false
			}
		}
		if !visit(f.Filter) {
			return nil, false
		}
	}
	if w.Frame != nil {
		if !visit(w.Frame.StartOffset) || !visit(w.Frame.EndOffset) {
			return nil, false
		}
	}
	return names, true
}

// aboveWindowNeededNames returns every column name the plan chain above
// stopAt reads from the WindowAgg's output row, and whether the answer is
// COMPLETE. above is the tree root (or any ancestor-or-self of stopAt); the
// walk descends only along the path to stopAt, collecting each level's own
// expressions via enclosingNodeScopeOf. Incomplete (false) means "decline":
// stopAt unreachable from above, an unenumerated node kind on the path, or an
// unenumerable expression at any collected level.
func aboveWindowNeededNames(above Node, stopAt *WindowAgg) (map[string]bool, bool) {
	if stopAt == nil {
		return nil, false
	}
	path, ok := pathToWindowNode(above, stopAt)
	if !ok {
		return nil, false
	}
	names := make(map[string]bool, 8)
	for _, n := range path {
		if w, isWin := n.(*WindowAgg); isWin && w == stopAt {
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

// pathToWindowNode returns the chain of nodes from above down to stopAt
// (both inclusive), following enclosingNodeScopeOf children. ok == false
// means stopAt is not reachable from above through enumerated scopes —
// including the case where an unenumerated node kind sits on the way — and
// the caller must decline rather than collect a partial path.
func pathToWindowNode(above Node, stopAt *WindowAgg) ([]Node, bool) {
	if above == nil || stopAt == nil {
		return nil, false
	}
	if w, ok := above.(*WindowAgg); ok && w == stopAt {
		return []Node{above}, true
	}
	sc, ok := enclosingNodeScopeOf(above)
	if !ok {
		return nil, false
	}
	for _, c := range sc.children {
		if p, ok := pathToWindowNode(c, stopAt); ok {
			return append([]Node{above}, p...), true
		}
	}
	return nil, false
}

// deriveWindowInputKeep computes the WindowAgg input keep-set: ascending
// positions into the WindowAgg's input schema (win.Child.Output()) of the
// window-input columns plus the above-needed columns. above == nil means "no
// above information" (the construction-time stamp): the keep is the window
// inputs alone. ok == false ("unknown", decline) wherever the derivation does
// not apply: nil window, nil child, a declined window-input enumeration, or
// (with a non-nil above) an unenumerable above-chain.
//
// A name in the union that matches no input column contributes no position:
// above levels may name constants/params (which read no row column) and
// window inputs are checked separately by the coverage assert. In particular
// an above name outside the input schema is NOT unknown — only the
// collectors' vetoes are.
func deriveWindowInputKeep(win *WindowAgg, above Node) ([]int, bool) {
	if win == nil || win.Child == nil {
		return nil, false
	}
	keyNames, ok := windowWindowInputNames(win)
	if !ok {
		return nil, false
	}
	need := make(map[string]bool, len(keyNames)+8)
	for name := range keyNames {
		need[name] = true
	}
	if above != nil {
		aboveNames, ok := aboveWindowNeededNames(above, win)
		if !ok {
			return nil, false
		}
		for name := range aboveNames {
			need[name] = true
		}
	}
	in := win.Child.Output()
	keep := make([]int, 0, len(need))
	for i, col := range in {
		if need[col.Name] {
			keep = append(keep, i)
		}
	}
	return keep, true
}

// assertWindowInputTargetCoversKeys is the third-cut consistency assert: when
// the stamp is known, every enumerable window-input column — partition keys
// and order keys first among them — must survive in the keep. It panics
// loudly (fail-closed) instead of letting a future applying cut drop a
// column the window reads — the coverage translateToLayout needs, checked at
// stamp time. Unknown stamps assert nothing: declining is always safe. The
// ascending/unique/in-range shape is checked defensively too: the derivation
// scans the schema in order so a violation is an internal bug, not a bad
// query.
func assertWindowInputTargetCoversKeys(win *WindowAgg) {
	if win == nil || !win.InputTargetKnown {
		return
	}
	keep := win.InputTarget
	in := Schema{}
	if win.Child != nil {
		in = win.Child.Output()
	}
	prev := -1
	for _, c := range keep {
		if c <= prev || c < 0 || c >= len(in) {
			panic(fmt.Sprintf("createPlan: WindowAgg input target %v is not an ascending in-range subset of the %d-column input row", keep, len(in)))
		}
		prev = c
	}
	kept := make(map[string]bool, len(keep))
	for _, c := range keep {
		kept[in[c].Name] = true
	}
	keyNames, ok := windowWindowInputNames(win)
	if !ok {
		panic(fmt.Sprintf("createPlan: WindowAgg input target is marked known but its window inputs are not enumerable"))
	}
	for name := range keyNames {
		if !kept[name] {
			panic(fmt.Sprintf("createPlan: WindowAgg input target %v drops window-input column %q of a %d-column input row", keep, name, len(in)))
		}
	}
}

// stampWindowInputTarget derives the keep for win (window inputs ∪ above,
// see deriveWindowInputKeep), stamps it as the node's Target-like payload,
// and runs the coverage assert. Additive only: no node is inserted, no
// schema/cost is touched. Overwrite-only (never accumulates), so re-stamping
// a tree (the construction-time keys-only stamp, then a finalized
// above-aware stamp) recomputes from scratch each time.
func stampWindowInputTarget(win *WindowAgg, above Node) {
	if win == nil {
		return
	}
	keep, ok := deriveWindowInputKeep(win, above)
	if !ok {
		win.InputTarget, win.InputTargetKnown = nil, false
		return
	}
	win.InputTarget, win.InputTargetKnown = keep, true
	assertWindowInputTargetCoversKeys(win)
}
