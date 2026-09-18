package optimizer

// M0141-S2a-fix1-sweep-b — narrow WINDOW's internal sort cost inputs
// (costWindow's per-level `inNcols`/`inAvgVarBytes`, fed by `addWindowPaths`
// from `belowNode.Output()`, and `sizeWindowRelFromNode`'s rel-width) to the
// columns the statement actually needs, mirroring sweep-a's
// (ordered_input_narrow.go) "compute once ahead of costing, consume at the
// existing read site" shape for the WINDOW upper rel's own currency gap.
//
// PG's real window-input Sort (`create_one_window_path`,
// postgres/src/backend/optimizer/plan/planner.c:4620-4760) is priced via
// `create_sort_path`'s already-narrow `subpath->pathtarget->width`, exactly
// as the ORDERED-rel case sweep-a fixed; goopg's `windowOp` sorts
// internally (`operators_window.go` Open), so `costWindow`
// (windowsetoppaths.go) folds that sort into its own price, reading the
// FULL row of the node one level below instead.
//
// Two distinct quantities are narrowed here, both by NAME — a name outside
// a level's own input schema is never "unknown", only a collector veto is
// (the same rule window_input_target.go's own deriveWindowInputKeep states):
//
//   - the WINDOW CHAIN's per-level INPUT keep (deriveWindowChainNarrowKeeps):
//     what `belowNode.Output()` must supply at each stacked level — that
//     level's own window inputs (PARTITION BY ∪ ORDER BY ∪ function
//     args/filter/frame offsets, via window_input_target.go's
//     `windowWindowInputNames`) union whatever every level above it, and
//     ultimately the statement's own final SELECT list, needs. Computed
//     top-down so a name needed three levels up still survives at level 0.
//   - the WINDOW REL's own OUTPUT keep (deriveWindowRelNarrowKeep): what
//     `sizeWindowRelFromNode`'s rel.NCols/AvgVarBytes should describe —
//     purely the statement's final SELECT-list output, since that is the
//     only thing that reads the chain's OWN output row above this rel (no
//     own-names union: unlike an input-side keep, an output-side keep is
//     exactly what is needed of it, no more).
//
// Scope, matching sweep-a's own precedent exactly: only NCols/AvgVarBytes
// are narrowed. Rows and Width (both the rel's own and the per-call
// `inWidth` `costWindow` receives) are left exactly as
// `sizeWindowRelFromNode`/`nodeTupleWidth(belowNode)` already compute them —
// `pgSortRelationBytesCostEnabled()` (default off) is the only consumer of
// either, the same disposition ordered_input_narrow.go recorded for the
// ORDERED rel.
//
// Safety direction, same as sweep-a/R121: "unknown" declines to today's
// full-width sizing at every level, and a keep only ever narrows — never
// invents a missing column.

// deriveWindowChainNarrowKeeps computes, for each node in chain (bottom-up:
// chain[i].Child == chain[i-1] for i>0, chain[0].Child == the WINDOW stage's
// original input), the keep-set `addWindowPaths` should read at that level
// in place of `belowNode.Output()`'s full width: ascending positions into
// chain[i].Child.Output().
//
// ok == false ("decline", keep every level's full-width sizing) when
// aboveNames itself is unknown, any level's own window-input names are not
// fully enumerable (windowWindowInputNames' own decline contract), any
// level has a nil Child, or any level's union matches no input column at
// all (an empty keep is unrepresentable the same way
// deriveOrderedSortInputKeep declines one).
func deriveWindowChainNarrowKeeps(chain []*WindowAgg, aboveNames map[string]bool, aboveKnown bool) ([][]int, bool) {
	if !aboveKnown || len(chain) == 0 {
		return nil, false
	}
	keeps := make([][]int, len(chain))
	need := make(map[string]bool, len(aboveNames))
	for name := range aboveNames {
		need[name] = true
	}
	for i := len(chain) - 1; i >= 0; i-- {
		w := chain[i]
		if w == nil || w.Child == nil {
			return nil, false
		}
		ownNames, ok := windowWindowInputNames(w)
		if !ok {
			return nil, false
		}
		total := make(map[string]bool, len(ownNames)+len(need))
		for name := range ownNames {
			total[name] = true
		}
		for name := range need {
			total[name] = true
		}
		cols := w.Child.Output()
		keep := make([]int, 0, len(total))
		for idx, c := range cols {
			if total[c.Name] {
				keep = append(keep, idx)
			}
		}
		if len(keep) == 0 {
			return nil, false
		}
		keeps[i] = keep
		// Propagate to the next (lower) level: its "above" is everything
		// this level needed, own inputs included — a lower level may be
		// asked to preserve a column only this level reads, not just what
		// the statement's final SELECT list reads.
		need = total
	}
	return keeps, true
}

// deriveWindowRelNarrowKeep computes the WINDOW rel's own published-width
// keep-set: ascending positions into top.Output() (the rel's output row,
// PG's `output_target`) named by aboveNames. ok == false ("decline", keep
// today's full-width rel sizing) when aboveNames is unknown, top is nil, or
// the match is empty (unrepresentable, same rule as the chain keep above).
func deriveWindowRelNarrowKeep(top *WindowAgg, aboveNames map[string]bool, aboveKnown bool) ([]int, bool) {
	if !aboveKnown || top == nil {
		return nil, false
	}
	cols := top.Output()
	keep := make([]int, 0, len(aboveNames))
	for idx, c := range cols {
		if aboveNames[c.Name] {
			keep = append(keep, idx)
		}
	}
	if len(keep) == 0 {
		return nil, false
	}
	return keep, true
}

// narrowWindowRelWidth refines rel's NCols/AvgVarBytes — just published by
// `sizeWindowRelFromNode` — to the columns named by keep, positions into
// top.Output(). Mirrors `narrowOrderedRelWidths` (ordered_input_narrow.go):
// Rows and Width are left exactly as `sizeWindowRelFromNode` set them.
func narrowWindowRelWidth(rel *RelOptInfo, top *WindowAgg, keep []int) {
	if rel == nil || top == nil || len(keep) == 0 {
		return
	}
	cols := top.Output()
	narrowed := make([]SchemaColumn, 0, len(keep))
	for _, i := range keep {
		if i >= 0 && i < len(cols) {
			narrowed = append(narrowed, cols[i])
		}
	}
	if len(narrowed) == 0 {
		return
	}
	rel.NCols = len(narrowed)
	rel.AvgVarBytes = nodeAvgVarBytes(narrowed)
}
