package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
)

// EX1-01 — per-leaf scan deform bound from a Build-time consumer walk.
//
// Q6-class scans full-deform every column of every scanned tuple while only a
// handful are ever read downstream. This slice computes, at Build time, a
// per-SeqScan deform bound — the exclusive upper bound [0, bound) the leaf
// deforms on the survivor path — by walking the already-materialized plan
// ABOVE the leaf in leaf-local ColumnRef.Index space (the same coordinate
// space the take5 prefilter uses for its MaxCols).
//
// Threading is top-down: Build is parent→child, so the bound is computed at
// each parent and threaded down to the child — the same direction as the
// Filter-above-SeqScan prefilter precedent, which peeks at the child plan and
// stamps the built child. Both Build paths (buildNode and the buildRec slab
// twin) thread it; Gather/GatherMerge capture it into the worker buildChild
// closure.
//
// Bound algebra. The threaded value is the highest child-output ColumnRef
// index referenced so far, starting at deformBoundNone ("nothing referenced
// yet") at the plan root. deformBoundFull is sticky: once any shape on the
// path declines, max() keeps it full all the way to the leaf.
//
//   - Pass-through nodes (Filter, Sort, Limit, LockRows, Result, identity
//     Project, Gather, GatherMerge) keep the incoming coordinates
//     (Output()==Child.Output()) so they fold their own consumer expressions
//     into the running bound and propagate it unchanged.
//   - Aggregate drops the accumulated ancestor bound — it is in
//     aggregate-output (groups+aggs) space and cannot map through the
//     reshape — then reads its arms (GroupExprs, AggregateCall
//     Arg/Arg2/ExtraArgs/Filter/OrderBy/WithinGroupOrderBy, Passthrough,
//     all in child-output space) as consumers first, re-narrowing the fresh
//     bound, and the walk continues below. This is what moves Q6-class
//     scans (Aggregate→Filter→SeqScan).
//   - Join maps the merged-space Predicate AND the remapped above-join
//     refs through the join's output layout, per side (EX1-02): the
//     canonical source is Join.Predicate split by the exprSide cutoff
//     (Index < leftWidth=len(Left.Output()) → left as-is, else right at
//     Index-leftWidth). Inner/left/right/full/cross output is left++right
//     (above refs split by leftWidth); semi/anti output is left-only (all
//     above refs to the left; the right narrows by keys+residual only).
//     Unknown shapes decline BOTH sides to full. Every mapped index is
//     range-checked against its side's output width; an unmappable index
//     fails that side to full (an unattributable one — negative index or
//     declined expression — fails both). Each side's bound is the union of
//     remapped above refs, remapped keys/residual, and the below-walk refs
//     folded while descending further. NestedLoopIndexJoin threads only
//     its Outer through the left-side rule; inner rescans stay out
//     (EX1-02b).
//   - Project-through (EX1-02): an identity Project (identity = Targets
//     exactly ColumnRef(i)->i, checked syntactically) folds its targets as
//     genuine child-space consumers and propagates the bound unchanged.
//     An all-bare-ColumnRef order-preserving (non-decreasing) projection
//     maps through the same way: it materialises every target, so each is
//     a genuine child-space read, and for in-order targets folding covers
//     the remapped above prefix too. Any other Project — expressions,
//     funcalls, consts, or reordered (decreasing) bare targets, whose
//     above prefix would remap to a non-prefix set — resets to full width
//     below.
//   - WHOLEROW (MergeWholeRowRef, ColumnRef with Index<0), RowExpr,
//     CTIDExpr, TableOidExpr, FuncCall, subquery shapes, outer refs, and
//     every other expression the whitelist does not positively understand
//     decline to full deform. Unknown plan nodes decline the same way.
//   - Distinct forces full width below: distinctOp deduplicates AND sorts on
//     the whole row (rowKey over every column plus a full-row sort
//     comparator), so it consumes every child column even though its output
//     coincides positionally with its child's. Propagating a narrow bound
//     through it would let dedup compare stale tail slots. The design lists
//     Distinct among the pass-throughs by the Output()==Child.Output()
//     coordinate criterion; the consumption criterion dominates here, and
//     the safe direction (full) agrees with section 3's "only ever EXCLUDES
//     columns no consumer reads".
//
// Why values cannot change: the bound only ever excludes columns no consumer
// on the path reads, and every shape the walk does not positively understand
// defaults to full deform. scanRow/schema stay full-width; only the deform
// window narrows. When the effective bound equals the column count the scan
// takes the exact pre-EX1-01 path (no behaviour change, no poison).
const (
	// deformBoundNone threads while no consumer reference has been recorded.
	deformBoundNone = -1
	// deformBoundFull is sticky full width: max() with it never narrows.
	deformBoundFull = int(^uint(0) >> 1)
)

// deformScanRefs records the highest ColumnRef.Index e reads in *maxRef,
// reporting whether e is positively understood as reading only indexed
// columns. This is deliberately a whitelist mirroring prefilterSafeExpr's
// understood set (plain column reads, constants, and the transparent
// comparison/arithmetic/boolean/cast/is-null wrappers): anything else —
// FuncCall, RowExpr, CTIDExpr, TableOidExpr, MergeWholeRowRef, subquery and
// outer-ref shapes, and every future expression kind — returns false and the
// caller declines to full deform. A missed arm costs performance, never
// correctness.
func deformScanRefs(e optimizer.Expr, maxRef *int) bool {
	switch x := e.(type) {
	case nil:
		return false
	case *optimizer.ColumnRef:
		// Index<0 is the whole-row convention (see the NLI shift
		// paths): it cannot be bounded, so it declines.
		if x.Index < 0 {
			return false
		}
		if x.Index > *maxRef {
			*maxRef = x.Index
		}
		return true
	case *optimizer.IntegerConst, *optimizer.StringConst, *optimizer.NumericConst,
		*optimizer.TypedStringLit, *optimizer.IntervalLit, *optimizer.NullConst,
		*optimizer.BooleanConst:
		return true
	case *optimizer.BinaryOp:
		// LIKE...ESCAPE carries a LikeEscapePattern operand, which the
		// whitelist rejects, so those predicates decline — same as the
		// prefilter.
		return deformScanRefs(x.Left, maxRef) && deformScanRefs(x.Right, maxRef)
	case *optimizer.UnaryOp:
		return deformScanRefs(x.Operand, maxRef)
	case *optimizer.CastExpr:
		return deformScanRefs(x.Operand, maxRef)
	case *optimizer.IsNullExpr:
		return deformScanRefs(x.Operand, maxRef)
	case *optimizer.IsBoolExpr:
		return deformScanRefs(x.Operand, maxRef)
	case *optimizer.IsDistinctFromExpr:
		return deformScanRefs(x.Left, maxRef) && deformScanRefs(x.Right, maxRef)
	default:
		return false
	}
}

// deformFoldRefs folds exprs into the running bound, returning the updated
// bound. A nil expr is skipped (absent arm); any declined expression pins
// the bound to deformBoundFull. Folding MORE references can only widen the
// bound, so callers fold every genuine consumer; forgetting one would be a
// wrong answer, folding an extra one is only a missed narrowing.
func deformFoldRefs(bound int, exprs ...optimizer.Expr) int {
	if bound == deformBoundFull {
		return bound
	}
	maxRef := bound
	for _, e := range exprs {
		if e == nil {
			continue
		}
		if !deformScanRefs(e, &maxRef) {
			return deformBoundFull
		}
	}
	return maxRef
}

// deformSortKeyExprs extracts the expressions from SortKeys for folding.
func deformSortKeyExprs(keys []optimizer.SortKey) []optimizer.Expr {
	if len(keys) == 0 {
		return nil
	}
	out := make([]optimizer.Expr, 0, len(keys))
	for i := range keys {
		out = append(out, keys[i].Expr)
	}
	return out
}

// isIdentityProject reports whether p is exactly the identity projection:
// every target is ColumnRef(i)->i, checked syntactically. Only the position
// matters (names/types are ignored): such a node is a coordinate-preserving
// pass-through for the bound walk.
func isIdentityProject(p *optimizer.Project) bool {
	for i, t := range p.Targets {
		c, ok := t.(*optimizer.ColumnRef)
		if !ok || c.Index != i {
			return false
		}
	}
	return true
}

// isOrderedBareProject reports whether every target of p is a bare ColumnRef
// with a non-negative index and the indices are non-decreasing. Identity
// projections satisfy this too, but the identity arm runs first (for
// identity the output space coincides with the child space, so the incoming
// bound folds raw; here it must not). A reordered (decreasing) bare
// projection maps an above prefix to a non-prefix set, which a prefix bound
// cannot express, so only order-preserving projections map through.
func isOrderedBareProject(p *optimizer.Project) bool {
	prev := -1
	for _, t := range p.Targets {
		c, ok := t.(*optimizer.ColumnRef)
		if !ok || c.Index < 0 || c.Index < prev {
			return false
		}
		prev = c.Index
	}
	return true
}

// deformProjectThrough maps the incoming project-output-space bound through
// an order-preserving all-bare projection. The projection materialises every
// target, so each target is a genuine child-space consumer and folding all
// of them covers every column the projection can read — including the
// remapped above prefix (a subset for in-order targets), which is why the
// incoming bound is not folded raw (output coordinates ≠ child coordinates).
// Full stickiness is preserved: an incoming Full, or an incoming prefix
// reaching past the projection width (unmappable), fails to full.
func deformProjectThrough(p *optimizer.Project, incoming int) int {
	if incoming == deformBoundFull || incoming >= len(p.Targets) {
		return deformBoundFull
	}
	return deformFoldRefs(deformBoundNone, p.Targets...)
}

// deformBoundBelow computes the bound a parent threads to its child subtree
// from the incoming bound. Every node here has a single child subtree; Join
// and NestedLoopIndexJoin thread per-side bounds through deformJoinBounds /
// deformNLIOuterBound instead (see the buildNode/buildRec arms). Nodes not
// listed (WindowAgg, SetOp, CTE shapes, DistinctOn, DML, …) decline the whole
// subtree to full deform.
func deformBoundBelow(parent optimizer.Node, incoming int) int {
	switch p := parent.(type) {
	case *optimizer.Filter:
		return deformFoldRefs(incoming, p.Predicate)
	case *optimizer.Sort:
		return deformFoldRefs(incoming, deformSortKeyExprs(p.Keys)...)
	case *optimizer.Limit:
		bound := deformFoldRefs(incoming, p.Limit, p.Offset)
		return deformFoldRefs(bound, p.TiesKeys...)
	case *optimizer.LockRows:
		// filterPred (EPQ recheck) is extracted from the child Filter's
		// own predicate at Open, already folded while descending through
		// that Filter in the same coordinates — nothing extra to fold
		// here beyond the lifted LIMIT/OFFSET expressions.
		return deformFoldRefs(incoming, p.LimitCount, p.OffsetCount)
	case *optimizer.Result:
		bound := deformFoldRefs(incoming, p.Targets...)
		return deformFoldRefs(bound, p.OneTimeFilter)
	case *optimizer.Project:
		// EX1-02 project-through: identity targets are genuine consumers
		// in child-output space (a root SELECT a,b reads columns 0..1 of
		// the leaf row), so they fold like any other consumer — the
		// identity special case of the map-through rule. Folding can only
		// widen to len(Targets)-1; a full-width identity re-widens to
		// full, as it must. An all-bare order-preserving projection maps
		// through the same way (it materialises every target; for
		// in-order targets folding covers the remapped above prefix).
		// Anything else (expressions, funcalls, consts, or reordered bare
		// targets, whose above prefix would remap to a non-prefix set)
		// resets to full width below.
		if isIdentityProject(p) {
			// Identity targets are genuine consumers in child-output
			// space (a root SELECT a,b reads columns 0..1 of the leaf
			// row), so they fold like any other consumer. Folding can
			// only widen to len(Targets)-1; a full-width identity
			// re-widens to full, as it must.
			return deformFoldRefs(incoming, p.Targets...)
		}
		if isOrderedBareProject(p) {
			return deformProjectThrough(p, incoming)
		}
		return deformBoundFull
	case *optimizer.Aggregate:
		// Reshape boundary: the incoming bound is in aggregate-output
		// space and cannot map through it, so it is dropped. The arms
		// below ARE in child-output space, so they are read as consumers
		// first and re-narrow the fresh bound; the walk continues below.
		bound := deformFoldRefs(deformBoundNone, p.GroupExprs...)
		for i := range p.Aggs {
			a := &p.Aggs[i]
			bound = deformFoldRefs(bound, a.Arg, a.Arg2)
			bound = deformFoldRefs(bound, a.ExtraArgs...)
			bound = deformFoldRefs(bound, a.Filter)
			bound = deformFoldRefs(bound, deformSortKeyExprs(a.OrderBy)...)
			bound = deformFoldRefs(bound, deformSortKeyExprs(a.WithinGroupOrderBy)...)
		}
		return deformFoldRefs(bound, p.Passthrough...)
	case *optimizer.Gather:
		// No consumer expressions of its own; the caller captures the
		// returned bound into the worker buildChild closure.
		return incoming
	case *optimizer.GatherMerge:
		// The leader evaluates Keys against gathered worker rows, which
		// carry the scan's row width through the partial plan — genuine
		// consumers in child-output coordinates, so they fold.
		return deformFoldRefs(incoming, deformSortKeyExprs(p.Keys)...)
	case *optimizer.Join:
		// Unreachable via Build: joins thread per-side bounds through
		// deformJoinBounds (see the Join arms of buildNode/buildRec), so a
		// single bound cannot serve both children. Full is the safe
		// fallback for any other caller.
		return deformBoundFull
	case *optimizer.Distinct:
		// Whole-row consumer (dedup key + full-row sort comparator over
		// every column): no column may be excluded below it.
		return deformBoundFull
	default:
		return deformBoundFull
	}
}

// deformSideWidth reports the output width of a join side (or NLI arm).
// Planner-stamped Output() is authoritative and is what production plans
// carry. Hand-built unit-test nodes carry no schema, so the width is derived
// structurally instead: SeqScan (table columns), Project (target count), and
// the coordinate-preserving pass-throughs (recurse into the child). Anything
// else with no stamped schema reports 0 and the caller declines to full.
func deformSideWidth(n optimizer.Node) int {
	for n != nil {
		if w := len(n.Output()); w > 0 {
			return w
		}
		switch x := n.(type) {
		case *optimizer.SeqScan:
			if x.Table != nil {
				return len(x.Table.Columns)
			}
			return 0
		case *optimizer.Project:
			return len(x.Targets)
		case *optimizer.Filter:
			n = x.Child
		case *optimizer.Sort:
			n = x.Child
		case *optimizer.Limit:
			n = x.Child
		case *optimizer.LockRows:
			n = x.Child
		case *optimizer.Gather:
			n = x.Child
		case *optimizer.GatherMerge:
			n = x.Child
		case *optimizer.Result:
			n = x.Child
		case *optimizer.Distinct:
			n = x.Child
		default:
			return 0
		}
	}
	return 0
}

// deformUnionBound unions two prefix bounds (max-index consumers). Plain max
// carries the algebra: deformBoundNone (-1) absorbs, deformBoundFull
// (MaxInt) sticks.
func deformUnionBound(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// deformMergedAbove maps an incoming join-output-space prefix bound through a
// left++right output layout of the given side widths. The left share is the
// prefix clamped to the left width; the right share is the tail shifted down
// by leftWidth, or nothing when the prefix ends inside the left side. A tail
// reaching past the right width cannot map and fails the right side to full.
func deformMergedAbove(incoming, leftW, rightW int) (left, right int) {
	if incoming == deformBoundFull {
		return deformBoundFull, deformBoundFull
	}
	if incoming < 0 {
		return deformBoundNone, deformBoundNone
	}
	left = incoming
	if left > leftW-1 {
		left = leftW - 1
	}
	if incoming < leftW {
		return left, deformBoundNone
	}
	right = incoming - leftW
	if right >= rightW {
		right = deformBoundFull
	}
	return left, right
}

// deformSemiAbove maps an incoming prefix bound through a semi/anti output
// layout, which is the left side only. The right side narrows by
// keys+residual alone, so it takes no above contribution here. An incoming
// bound past the left width cannot map through this layout and fails the
// left side to full.
func deformSemiAbove(incoming, leftW int) (left, right int) {
	if incoming == deformBoundFull {
		return deformBoundFull, deformBoundNone
	}
	if incoming < 0 {
		return deformBoundNone, deformBoundNone
	}
	if incoming >= leftW {
		return deformBoundFull, deformBoundNone
	}
	return incoming, deformBoundNone
}

// deformSplitPredicate folds a merged-space (left++right) join predicate into
// per-side prefix bounds, splitting every ColumnRef by the exprSide cutoff
// (Index < leftWidth → left as-is, else right at Index-leftWidth). Constants
// and the transparent comparison/arithmetic/boolean/cast/is-null wrappers
// fold through like deformScanRefs; a bare nil arm is skipped. Every mapped
// index is range-checked against its side's output width: an out-of-range
// right index fails the right side only. Anything unattributable — a
// negative index or an expression the whitelist does not understand — fails
// BOTH sides to full, the safe direction (a missed consumer would be a wrong
// answer; an extra full is only a missed narrowing).
func deformSplitPredicate(pred optimizer.Expr, leftW, rightW int) (left, right int) {
	left, right = deformBoundNone, deformBoundNone
	if pred == nil {
		return left, right
	}
	var walk func(e optimizer.Expr) bool
	walk = func(e optimizer.Expr) bool {
		switch x := e.(type) {
		case nil:
			return true
		case *optimizer.ColumnRef:
			if x.Index < 0 {
				return false
			}
			if x.Index < leftW {
				if x.Index > left {
					left = x.Index
				}
				return true
			}
			if rix := x.Index - leftW; rix >= rightW {
				right = deformBoundFull
				return true
			} else if rix > right {
				right = rix
				return true
			}
			return true
		case *optimizer.IntegerConst, *optimizer.StringConst, *optimizer.NumericConst,
			*optimizer.TypedStringLit, *optimizer.IntervalLit, *optimizer.NullConst,
			*optimizer.BooleanConst:
			return true
		case *optimizer.BinaryOp:
			return walk(x.Left) && walk(x.Right)
		case *optimizer.UnaryOp:
			return walk(x.Operand)
		case *optimizer.CastExpr:
			return walk(x.Operand)
		case *optimizer.IsNullExpr:
			return walk(x.Operand)
		case *optimizer.IsBoolExpr:
			return walk(x.Operand)
		case *optimizer.IsDistinctFromExpr:
			return walk(x.Left) && walk(x.Right)
		default:
			return false
		}
	}
	if !walk(pred) {
		return deformBoundFull, deformBoundFull
	}
	return left, right
}

// deformJoinBounds computes the per-side EX1-02 bounds a Join threads to its
// Left and Right subtrees: the union of the remapped above-join refs and the
// remapped keys/residual, still prefix-truncated on full-width rows. The
// below-walk refs union in while descending further.
//
// Keys map POSITIONALLY, not by index space: the executor evaluates pair
// k.Left against left rows and k.Right against right rows
// (join_composite_key.go buildKeyExprs/probeKeyExprs), so key refs fold at
// face value per side. Decorrelated/semi keys may be unrebased (both
// operands displaying — and indexed — in outer space, e.g. Q16's
// `cs1.x = cs1.x` semi Hash Cond); an index-space split would file the
// right key on the left and starve the right scan (EX1-02 Q16 CKMISMATCH).
// The residual evaluates against merged rows (joinPredicateMatchSlot over
// the merged slot), so Join.Predicate keeps its index-space split for the
// residual half; the keys inside it double-cover by union, which is safe.
func deformJoinBounds(p *optimizer.Join, incoming int) (left, right int) {
	leftW, rightW := deformSideWidth(p.Left), deformSideWidth(p.Right)
	if leftW <= 0 || rightW <= 0 {
		return deformBoundFull, deformBoundFull
	}
	var leftAbove, rightAbove int
	switch p.Type {
	case optimizer.JoinTypeSemi, optimizer.JoinTypeAnti:
		leftAbove, rightAbove = deformSemiAbove(incoming, leftW)
	case optimizer.JoinTypeInner, optimizer.JoinTypeLeft, optimizer.JoinTypeRight,
		optimizer.JoinTypeFull, optimizer.JoinTypeCross:
		leftAbove, rightAbove = deformMergedAbove(incoming, leftW, rightW)
	default:
		return deformBoundFull, deformBoundFull
	}
	leftKeys, rightKeys := deformPositionalKeys(p, leftW, rightW)
	leftSplit, rightSplit := deformSplitPredicate(p.Predicate, leftW, rightW)
	return deformUnionBound(leftAbove, deformUnionBound(leftKeys, leftSplit)),
		deformUnionBound(rightAbove, deformUnionBound(rightKeys, rightSplit))
}

// deformPositionalKeys folds a join's equi-key pairs at face value per
// side: pair k.Left reads left-row columns, k.Right right-row columns,
// whatever Index space the operands were planned in. Canonical source is
// HashKeys (every usable equi-pair; HashKeys[0] is (LeftKey, RightKey));
// the LeftKey/RightKey fallback covers joins without a filled pair list.
// A declined operand fails its own side to full; face-value indices are
// range-checked against the side width (out-of-range fails that side).
func deformPositionalKeys(p *optimizer.Join, leftW, rightW int) (left, right int) {
	left, right = deformBoundNone, deformBoundNone
	face := func(e optimizer.Expr, w int) int {
		b := deformFoldRefs(deformBoundNone, e)
		if b != deformBoundFull && b >= w {
			return deformBoundFull
		}
		return b
	}
	if len(p.HashKeys) > 0 {
		for _, k := range p.HashKeys {
			left = deformUnionBound(left, face(k.Left, leftW))
			right = deformUnionBound(right, face(k.Right, rightW))
		}
		return left, right
	}
	return face(p.LeftKey, leftW), face(p.RightKey, rightW)
}

// deformNLIOuterBound computes the bound a NestedLoopIndexJoin threads to its
// Outer subtree: the outer follows the left-side rule (its share of the
// above prefix plus the Predicate refs below the outer-width cutoff), while
// the inner probe rescans stay out of this slice (EX1-02b) and always run
// full. Right-side (inner) Predicate refs are discarded; an unattributable
// Predicate fails the outer to full.
func deformNLIOuterBound(p *optimizer.NestedLoopIndexJoin, incoming int) int {
	outerW := deformSideWidth(p.Outer)
	if outerW <= 0 {
		return deformBoundFull
	}
	var above int
	switch {
	case incoming == deformBoundFull:
		above = deformBoundFull
	case incoming < 0:
		above = deformBoundNone
	case incoming >= outerW:
		// Outer++inner layout: the outer share of a prefix reaching past
		// the outer width is the whole outer width (the tail belongs to
		// the inner side, which stays full regardless).
		above = outerW - 1
	default:
		above = incoming
	}
	rightW := deformBoundFull
	if p.Inner != nil {
		if w := len(p.Inner.Output()); w > 0 {
			rightW = w
		}
	}
	outerKeys, _ := deformSplitPredicate(p.Predicate, outerW, rightW)
	return deformUnionBound(above, outerKeys)
}

// effectiveDeformBound resolves the threaded bound to the exclusive deform
// width for a leaf with ncols columns. deformBoundNone (no consumer recorded
// — e.g. a bare scan) and deformBoundFull both mean full width; anything
// else clamps to [1, ncols]. An effective bound equal to ncols must take the
// exact pre-EX1-01 decode path.
func effectiveDeformBound(bound, ncols int) int {
	if ncols <= 0 {
		return 0
	}
	if bound < 0 || bound == deformBoundFull {
		return ncols
	}
	if need := bound + 1; need < ncols {
		return need
	}
	return ncols
}

// ---------------------------------------------------------------------------
// Debug tail-poison (default off, zero release cost).
//
// When seqScanDeformPoison is set (tests only), the scan stamps every
// undeformed tail slot with the poison sentinel at deform time, and the
// ColumnRef evaluation sites panic on yielding a poisoned datum. A walk miss
// — any consumer reading past the bound — then fails loudly instead of
// returning a stale Datum. With the flag off no poison is ever written and
// the only cost is a predictable-not-taken branch at the evaluation sites.
//
// The sentinel rides the KindInt carrier (no new DatumKind, so no codec,
// spill, or exhaustive-switch churn): it is recognised by exact (Kind, Int)
// match, and every scan-internal Kind switch (detoast scan, enum injection,
// ACL renders) already skips KindInt. Pooled-row hygiene: the scan scrubs
// poison back to NULL before releasing scanRow, so a poisoned buffer never
// leaks into the row pool for a later flag-off test.
// ---------------------------------------------------------------------------

// seqScanDeformPoison arms the EX1-01 tail-poison. Tests set it for the
// poison run and restore false; it is never set in production.
var seqScanDeformPoison = false

// deformPoisonMagic is the Int payload of the poison sentinel. It is chosen
// to never collide with test data (tests avoid it); a production table that
// somehow held this exact int64 with the flag off is unaffected, since the
// checks are flag-gated.
const deformPoisonMagic = int64(0x1E601DF0AA700210)

// deformPoisonDatum stamps one undeformed tail slot.
func deformPoisonDatum() Datum {
	return Datum{Kind: KindInt, Int: deformPoisonMagic}
}

// isDeformPoison reports whether d is the tail-poison sentinel.
func isDeformPoison(d Datum) bool {
	return d.Kind == KindInt && d.Int == deformPoisonMagic
}

// checkDeformPoison panics when the evaluator yields a poisoned datum, i.e.
// some consumer read a scan column past the deform bound. Called at the
// ColumnRef evaluation sites (interpreted + compiled) and nowhere else, so
// bulk row transport (Row()/copy/clone) never trips it — poison rides along
// to the next genuine evaluation read instead.
func checkDeformPoison(d Datum) {
	if seqScanDeformPoison && isDeformPoison(d) {
		panic("executor: EX1-01 deform-bound violation: consumer read an undeformed scan tail slot")
	}
}

// poisonDeformTail stamps row[from:] at deform time. No-op unless armed.
func poisonDeformTail(row Row, from int) {
	if !seqScanDeformPoison {
		return
	}
	for i := from; i < len(row); i++ {
		row[i] = deformPoisonDatum()
	}
}

// scrubDeformPoison rewrites poisoned slots back to NULL before a pooled row
// is released. No-op unless armed.
func scrubDeformPoison(row Row) {
	if !seqScanDeformPoison {
		return
	}
	for i, d := range row {
		if isDeformPoison(d) {
			row[i] = NullDatum
		}
	}
}
