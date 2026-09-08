package executor

// join_merge_key.go — M0127-P2.3, design leftdeep-joins/07 §2, the merge-join
// member of the multi-column-key family P2.1 (planner list) and P2.2 (hash
// executor) opened.
//
// Until P2.3 a goopg merge join sorted both sides on exactly ONE equi-pair
// (`plan.LeftKey` / `plan.RightKey`) and then evaluated the ENTIRE `Predicate`
// against every candidate pair inside an equal-key group. Two consequences,
// both of which PG does not have:
//
//   - group blow-up. PG's `MJCompare` walks every mergeclause before calling
//     two tuples equal (postgres/src/backend/executor/nodeMergejoin.c), so a
//     two-column merge key produces two-column groups. goopg grouped on the
//     leading column alone, so a low-cardinality lead — the merge-join analogue
//     of the M0125-0035b hash degeneracy, and the shape a constant-pinned qual
//     produces — turned the group into a cartesian product evaluated one
//     interpreted `evalExpr` at a time. Q97's FULL OUTER JOIN (M0125-0011) is
//     the recorded instance: the residual was what kept the ANSWER right, at
//     O(n·m) cost.
//   - residual work on an all-equijoin join, where PG's `joinqual` is empty.
//
// The key list itself is already published by P2.1 (`Join.HashKeys` is filled
// for `JoinAlgoMerge` too, and EXPLAIN renders it as `Merge Cond:`), so P2.3 is
// the consumer: it takes the planner's merge split (`ExecMergeKeyPlan`), sorts
// and compares on the full tuple, and leaves only the non-equijoin conjuncts
// for the per-pair check.
//
// Result identity. Nothing about which rows the join emits changes, for the two
// reasons the planner side is conservative about: a pair is folded in only when
// `compareDatum` answers the same question as `=` for its type, and a row with
// a NULL in any folded column moves from "grouped, then rejected by the
// residual because `x = NULL` is NULL" to "filed as a NULL key, never grouped".
// Both routes null-extend the row for an outer join and drop it for an inner
// one, which is why this is a cost change and not a semantic one.

import "github.com/goopg/goopg/internal/optimizer"

// initMergeKeys resolves this merge join's key list and residual. Called once
// at the top of runMergeJoin, before either side is keyed, since the two sides
// must agree on the key tuple's arity and order or the merge advance is
// meaningless.
//
// Kept in fields distinct from the hash path's execKeys/execResidual on
// purpose: the two are filled by different planner queries at different points
// in Open, and one joinOp only ever runs one of the two algorithms. Sharing the
// slots would make an accidental cross-read compile.
func (o *joinOp) initMergeKeys() {
	o.mergeKeys = nil
	o.mergeResidual = nil
	o.mergeCompiled = false
	if o.plan == nil {
		o.compileMergeExprs()
		return
	}
	plan := o.plan.ExecMergeKeyPlan()
	o.mergeKeys = plan.Keys
	o.mergeResidual = plan.Residual
	if len(o.mergeKeys) == 0 {
		// No key plan at all (a Join with no LeftKey). Keep the single-pair
		// slot populated with whatever the plan has, nil included, so
		// buildMergeSide raises the same "merge join key is nil" error it
		// raised before P2.3 rather than silently joining on nothing.
		o.mergeKeys = []optimizer.JoinKeyPair{{Left: o.plan.LeftKey, Right: o.plan.RightKey}}
		o.mergeResidual = o.plan.Predicate
	}
	o.compileMergeExprs()
}

// ensureMergeExprs compiles the merge slab for joinOps driven directly
// (unit tests hand-building mergeKeys without initMergeKeys). O(1) fast
// path (bool + length compares, no allocation). Length-CHANGING
// replacements recompile; same-length swaps or residual-only changes
// do not (mergeKeys/mergeResidual are assigned only inside
// initMergeKeys, which always recompiles — stronger than the hash
// arm's nil/bool check, same trust root).
func (o *joinOp) ensureMergeExprs() {
	if o.mergeCompiled && len(o.mergeKeyNodesL) == len(o.mergeKeys) {
		return
	}
	o.compileMergeExprs()
}

// compileMergeExprs compiles the merge-side key accessors and the residual
// conjunction into this join's merge ExprNode slab (E-05, the merge twin of
// compileExecExprs). Called from initMergeKeys so the compiled form derives
// from the SAME key split as the interpreted expressions. Rebuilds
// unconditionally (same staleness discipline as the hash slab); the
// residual slot is (re)created here so the per-pair path never allocates.
func (o *joinOp) compileMergeExprs() {
	o.mergeExprs = o.mergeExprs[:0]
	left := o.mergeSideKeyExprs(true)
	if cap(o.mergeKeyNodesL) < len(left) {
		o.mergeKeyNodesL = make([]int32, len(left))
	}
	o.mergeKeyNodesL = o.mergeKeyNodesL[:len(left)]
	for i, e := range left {
		o.mergeKeyNodesL[i] = o.mergeExprs.buildExpr(e)
	}
	right := o.mergeSideKeyExprs(false)
	if cap(o.mergeKeyNodesR) < len(right) {
		o.mergeKeyNodesR = make([]int32, len(right))
	}
	o.mergeKeyNodesR = o.mergeKeyNodesR[:len(right)]
	for i, e := range right {
		o.mergeKeyNodesR[i] = o.mergeExprs.buildExpr(e)
	}
	o.mergeResidualNode = o.mergeExprs.buildExpr(o.mergeResidual)
	if o.mergeResidSlot == nil {
		o.mergeResidSlot = &MaterializedSlot{}
	}
	o.mergeCompiled = true
}

// mergeSideKeyExprs picks one side's key expressions out of the pair list.
// isLeft selects the expressions that reference the left input; both are
// evaluated against the MERGED (left ++ right) row space, which is why
// buildMergeSide pads the row before evaluating.
func (o *joinOp) mergeSideKeyExprs(isLeft bool) []optimizer.Expr {
	out := make([]optimizer.Expr, len(o.mergeKeys))
	for i, k := range o.mergeKeys {
		if isLeft {
			out[i] = k.Left
		} else {
			out[i] = k.Right
		}
	}
	return out
}

// compareMergeKeys is the full-tuple comparator: lexicographic over the key
// columns in plan order, PG's `MJCompare` shape. Both the sort and the
// group-boundary test go through it, so the ordering the sides are merged on
// and the equality that closes a group cannot disagree.
func compareMergeKeys(a, b []Datum, pos int) (int, error) {
	for i := range a {
		cmp, err := compareDatum(a[i], b[i], pos)
		if err != nil {
			return 0, err
		}
		if cmp != 0 {
			return cmp, nil
		}
	}
	return 0, nil
}

// mergeResidualMatch evaluates what is left of the ON clause once the merge key
// has located the group — PG's `ExecQual(joinqual)` at EXEC_MJ_JOINTUPLES.
//
// nil residual (the all-equijoin case) short-circuits to true, which is the
// whole point of folding the pairs into the key: PG does no per-pair work on
// such a join and now neither does goopg.
func (o *joinOp) mergeResidualMatch(row Row) (bool, error) {
	if o.mergeResidual == nil {
		return true, nil
	}
	// E-05: compiled twin (nil short-circuit above stays — routing nil
	// through noExpr would invert to false). Dedicated hoisted slot,
	// rebound per pair (never o.slot — nextMerge reuses that for
	// output); fresh hasCTID forever (rebind sets only .row).
	o.ensureMergeExprs()
	o.mergeResidSlot.row = row
	v, err := evalFastExpr(o.mergeExprs, o.mergeResidualNode, o.mergeResidSlot, o.ctx)
	if err != nil {
		return false, err
	}
	ok := !v.IsNull() && v.Kind == KindBool && v.BoolValue()
	if !ok && o.joinFilterRemoved != nil {
		*o.joinFilterRemoved++
	}
	return ok, nil
}
