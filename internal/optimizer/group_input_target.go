package optimizer

import "fmt"

// B-01c second cut — COMPUTE-ONLY `group_input_target` (no plan mutation).
//
// This file derives the Aggregate site's input keep-set — the columns the
// Aggregate reads from its input row (group keys ∪ aggregate args ∪ internal
// ORDER BY / WITHIN GROUP ORDER BY args) ∪ above-needed columns — stamps it
// on the Aggregate node as a Target-like payload, and asserts the keep covers
// the group inputs. It mirrors the Slice-1 sort-input pattern
// (sort_input_target.go: stamp + assert, zero behavior change): no Project
// insertion, no schema change, no cost change. A future applying cut may
// narrow the Aggregate input from InputTarget; that cut is NOT this file.
//
// The three upper sites (buildAggregateStage, buildWindowStage, Sort
// construction) were reviewed together; this cut implements ONLY the
// buildAggregateStage site. Window-second shapes are out of scope.
//
// DERIVATION (existing walkers only — nothing invented):
//
//   - Group inputs: one visitColumnRefsByName (the fail-closed walkExprRefs
//     collector shared with collectJoinQualNames / mergeSortKeyNames) per
//     GroupExpr, per aggregate Arg / Arg2 / ExtraArg, and per OrderBy /
//     WithinGroupOrderBy key Expr — the same per-expression enumeration
//     walkPlanExprs' Aggregate arm performs (unnest.go:1054-1075), minus the
//     two fields that arm omits:
//   - any AggregateCall.Filter present → UNKNOWN. walkPlanExprs' Aggregate
//     arm now walks Filter (B-01c window cut), but the group derivation
//     keeps the field-level decline anyway: a future applying cut must
//     re-prove the filter's evaluation scope before consuming it, and the
//     decline is the safe side. (Same for Passthrough below.)
//   - any Aggregate.Passthrough present → UNKNOWN. walkPlanExprs now walks
//     Passthrough too (same cut), with the same intentional decline here.
//     (enclosingNodeScopeOf does — enclosingtree.go:143
//     — but that is the above-chain walker, whose extra width only ever
//     widens a keep; the stamped node's own reads need the node-level
//     walker, which omits it.)
//   - WithinGroupOrderBy / OrderBy args are enumerable via the existing
//     walkers (the arm walks both); an unenumerable arg vetoes like any
//     other expression. Star args (nil Arg, count(*)) contribute nothing.
//
//   - Above-needed columns: the plan chain above the Aggregate walked
//     top-down via enclosingNodeScopeOf (enclosingtree.go:143 for the
//     Aggregate arm), collecting each level's own expressions with the same
//     fail-closed collector. The walk stops AT the Aggregate (its input
//     subtree is below the narrowing point, never above it) and descends
//     ONLY along the path to the Aggregate, so off-path subtrees can neither
//     contribute nor poison.
//
// Anything unenumerable — an unenumerated node kind on the path, an
// unenumerable expression (unnamed ref, outer-scope ref, CTID/whole-row ref,
// inner-scope plan, unknown type) — marks the derivation UNKNOWN (Known
// false), never invents a list. Unknown is the only safe answer: it declines
// any future applying cut back to today's full-width input bit-identically.
//
// LIFECYCLE: buildAggregateStage stamps keys-only at construction (above not
// yet built, passthroughs not yet appended). Passthrough columns are appended
// later during target resolution (resolveColumnRefAfterAggregate /
// resolveTargetsAfterAggregate), which re-stamps: presence of any
// passthrough flips the payload to unknown per the decline rule above.

// groupAggregateInputNames returns every column name the Aggregate reads from
// its input row, and whether the answer is COMPLETE. Incomplete (false) means
// "decline": any Filter present, any Passthrough present, or an unenumerable
// group/arg/order expression. A nil Arg (count(*)) or nil order key Expr
// contributes nothing (matching walkPlanExprs' Aggregate arm, which walks
// non-nil fields only).
func groupAggregateInputNames(agg *Aggregate) (map[string]bool, bool) {
	if agg == nil {
		return nil, false
	}
	for i := range agg.Aggs {
		if agg.Aggs[i].Filter != nil {
			return nil, false
		}
	}
	if len(agg.Passthrough) > 0 {
		return nil, false
	}
	names := make(map[string]bool, len(agg.GroupExprs)+len(agg.Aggs))
	visit := func(e Expr) bool {
		if e == nil {
			return true
		}
		if !visitColumnRefsByName(e, func(name string) { names[name] = true }) {
			return false
		}
		return true
	}
	for _, g := range agg.GroupExprs {
		if !visit(g) {
			return nil, false
		}
	}
	for i := range agg.Aggs {
		a := &agg.Aggs[i]
		if !visit(a.Arg) || !visit(a.Arg2) {
			return nil, false
		}
		for _, ea := range a.ExtraArgs {
			if !visit(ea) {
				return nil, false
			}
		}
		for _, sk := range a.OrderBy {
			if !visit(sk.Expr) {
				return nil, false
			}
		}
		for _, sk := range a.WithinGroupOrderBy {
			if !visit(sk.Expr) {
				return nil, false
			}
		}
	}
	return names, true
}

// aboveAggregateNeededNames returns every column name the plan chain above
// stopAt reads from the Aggregate's output row, and whether the answer is
// COMPLETE. above is the tree root (or any ancestor-or-self of stopAt); the
// walk descends only along the path to stopAt, collecting each level's own
// expressions via enclosingNodeScopeOf. Incomplete (false) means "decline":
// stopAt unreachable from above, an unenumerated node kind on the path, or an
// unenumerable expression at any collected level.
func aboveAggregateNeededNames(above Node, stopAt *Aggregate) (map[string]bool, bool) {
	if stopAt == nil {
		return nil, false
	}
	path, ok := pathToAggregateNode(above, stopAt)
	if !ok {
		return nil, false
	}
	names := make(map[string]bool, 8)
	for _, n := range path {
		if a, isAgg := n.(*Aggregate); isAgg && a == stopAt {
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

// pathToAggregateNode returns the chain of nodes from above down to stopAt
// (both inclusive), following enclosingNodeScopeOf children. ok == false
// means stopAt is not reachable from above through enumerated scopes —
// including the case where an unenumerated node kind sits on the way — and
// the caller must decline rather than collect a partial path.
func pathToAggregateNode(above Node, stopAt *Aggregate) ([]Node, bool) {
	if above == nil || stopAt == nil {
		return nil, false
	}
	if a, ok := above.(*Aggregate); ok && a == stopAt {
		return []Node{above}, true
	}
	sc, ok := enclosingNodeScopeOf(above)
	if !ok {
		return nil, false
	}
	for _, c := range sc.children {
		if p, ok := pathToAggregateNode(c, stopAt); ok {
			return append([]Node{above}, p...), true
		}
	}
	return nil, false
}

// deriveAggregateInputKeep computes the Aggregate input keep-set: ascending
// positions into the Aggregate's input schema (agg.Child.Output()) of the
// group-input columns plus the above-needed columns. above == nil means "no
// above information" (the construction-time stamp): the keep is the group
// inputs alone. ok == false ("unknown", decline) wherever the derivation does
// not apply: nil aggregate, nil child, a declined group-input enumeration
// (Filter / Passthrough / unenumerable expression), or (with a non-nil above)
// an unenumerable above-chain.
//
// A name in the union that matches no input column contributes no position:
// above levels may name constants/params (which read no row column) and group
// inputs are checked separately by the coverage assert. In particular an above
// name outside the input schema is NOT unknown — only the collectors' vetoes
// are.
func deriveAggregateInputKeep(agg *Aggregate, above Node) ([]int, bool) {
	if agg == nil || agg.Child == nil {
		return nil, false
	}
	keyNames, ok := groupAggregateInputNames(agg)
	if !ok {
		return nil, false
	}
	need := make(map[string]bool, len(keyNames)+8)
	for name := range keyNames {
		need[name] = true
	}
	if above != nil {
		aboveNames, ok := aboveAggregateNeededNames(above, agg)
		if !ok {
			return nil, false
		}
		for name := range aboveNames {
			need[name] = true
		}
	}
	in := agg.Child.Output()
	keep := make([]int, 0, len(need))
	for i, col := range in {
		if need[col.Name] {
			keep = append(keep, i)
		}
	}
	return keep, true
}

// assertAggregateInputTargetCoversKeys is the second-cut consistency assert:
// when the stamp is known, every enumerable group-input column must survive
// in the keep. It panics loudly (fail-closed) instead of letting a future
// applying cut drop a column the aggregate reads — the coverage
// translateToLayout needs, checked at stamp time. Unknown stamps assert
// nothing: declining is always safe. The ascending/unique/in-range shape is
// checked defensively too: the derivation scans the schema in order so a
// violation is an internal bug, not a bad query.
func assertAggregateInputTargetCoversKeys(agg *Aggregate) {
	if agg == nil || !agg.InputTargetKnown {
		return
	}
	keep := agg.InputTarget
	in := Schema{}
	if agg.Child != nil {
		in = agg.Child.Output()
	}
	prev := -1
	for _, c := range keep {
		if c <= prev || c < 0 || c >= len(in) {
			panic(fmt.Sprintf("createPlan: Aggregate input target %v is not an ascending in-range subset of the %d-column input row", keep, len(in)))
		}
		prev = c
	}
	kept := make(map[string]bool, len(keep))
	for _, c := range keep {
		kept[in[c].Name] = true
	}
	keyNames, ok := groupAggregateInputNames(agg)
	if !ok {
		panic(fmt.Sprintf("createPlan: Aggregate input target is marked known but its group inputs are not enumerable"))
	}
	for name := range keyNames {
		if !kept[name] {
			panic(fmt.Sprintf("createPlan: Aggregate input target %v drops group-input column %q of a %d-column input row", keep, name, len(in)))
		}
	}
}

// stampAggregateInputTarget derives the keep for agg (group inputs ∪ above,
// see deriveAggregateInputKeep), stamps it as the node's Target-like payload,
// and runs the coverage assert. Additive only: no node is inserted, no
// schema/cost is touched. Overwrite-only (never accumulates), so re-stamping
// a tree (the construction-time keys-only stamp, then the finalized
// above-aware stamp, plus the passthrough-append re-stamp which flips to
// unknown) recomputes from scratch each time.
func stampAggregateInputTarget(agg *Aggregate, above Node) {
	if agg == nil {
		return
	}
	keep, ok := deriveAggregateInputKeep(agg, above)
	if !ok {
		agg.InputTarget, agg.InputTargetKnown = nil, false
		return
	}
	agg.InputTarget, agg.InputTargetKnown = keep, true
	assertAggregateInputTargetCoversKeys(agg)
}
