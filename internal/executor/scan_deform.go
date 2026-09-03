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
//   - Join examines the side-local keys first (whitelist discipline, same
//     decline direction) and then resets to full width below on both sides:
//     below a join the coordinates are side-local while the accumulated
//     bound is merged-schema space, and this slice does not do join-key
//     narrowing (explicit follow-up; join-heavy shapes keep bound=ncols).
//   - A non-identity Project (identity = Targets exactly ColumnRef(i)->i,
//     checked syntactically) resets to full width below.
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

// deformBoundBelow computes the bound a parent threads to its child subtree
// from the incoming bound. For Join the returned bound is full on both sides
// (the caller threads it to Left and Right); every other node here has a
// single child subtree. Nodes not listed (WindowAgg, SetOp, CTE shapes,
// DistinctOn, DML, …) decline the whole subtree to full deform.
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
		if isIdentityProject(p) {
			// Identity targets are genuine consumers in child-output
			// space (a root SELECT a,b reads columns 0..1 of the leaf
			// row), so they fold like any other consumer. Folding can
			// only widen to len(Targets)-1; a full-width identity
			// re-widens to full, as it must.
			return deformFoldRefs(incoming, p.Targets...)
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
		// Terminator. The side-local keys are examined first (whitelist
		// discipline — a violation fails toward full deform, the same
		// direction as the reset); the walk then resets to full width
		// below on both sides. Join-key narrowing is the explicit
		// follow-up, not this slice.
		leftKeys := append([]optimizer.Expr{p.LeftKey}, joinDeformKeys(p, true)...)
		rightKeys := append([]optimizer.Expr{p.RightKey}, joinDeformKeys(p, false)...)
		deformFoldRefs(deformBoundNone, leftKeys...)
		deformFoldRefs(deformBoundNone, rightKeys...)
		return deformBoundFull
	case *optimizer.Distinct:
		// Whole-row consumer (dedup key + full-row sort comparator over
		// every column): no column may be excluded below it.
		return deformBoundFull
	default:
		return deformBoundFull
	}
}

// joinDeformKeys extracts the per-side key expressions from a Join's HashKeys
// list for the whitelist examination in deformBoundBelow.
func joinDeformKeys(p *optimizer.Join, left bool) []optimizer.Expr {
	if len(p.HashKeys) == 0 {
		return nil
	}
	out := make([]optimizer.Expr, 0, len(p.HashKeys))
	for i := range p.HashKeys {
		if left {
			out = append(out, p.HashKeys[i].Left)
		} else {
			out = append(out, p.HashKeys[i].Right)
		}
	}
	return out
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
