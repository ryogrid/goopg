package executor

import (
	"github.com/goopg/goopg/internal/catalog"
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
//     scans (Aggregate→Filter→SeqScan). The fresh bound starts at
//     deformBoundZero, not deformBoundNone: an Aggregate whose arms fold
//     nothing (count(*)) consumes no child column, and the scan below it
//     deforms none (M0145-0008p). A Finalize aggregate reads its child's
//     partial-state columns without an arm, so it starts at None instead.
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
	// At the plan root that means the rows go to the client whole, so a leaf
	// resolves it to full width.
	deformBoundNone = -1
	// deformBoundZero is a consumer boundary that reads no child column: an
	// Aggregate whose arms fold no reference (count(*)). PG deforms nothing
	// there; the aggregate never calls slot_getsomeattrs (execTuples.c). It
	// sorts below deformBoundNone, so plain max keeps the algebra: a column
	// reference folded later raises it to that index, and meeting None
	// (every `incoming < 0` mapping arm below) widens it to full, the safe
	// direction. Only a SeqScan leaf resolves it to width 0 (M0145-0008p).
	deformBoundZero = -2
	// deformBoundFull is sticky full width: max() with it never narrows.
	deformBoundFull = int(^uint(0) >> 1)
	// deformWidthZero is the stamp effectiveDeformBound gives a leaf for
	// deformBoundZero: deform no column. Stamps are otherwise widths, and 0
	// already means "unset, full width" for scans built outside Build. The
	// index and bitmap heap leaves read any stamp <= 0 as full width; the
	// SeqScan resolves this one through seqScanSurvivorWidth.
	deformWidthZero = -1
)

// deformScanRefs records the highest ColumnRef.Index e reads in *maxRef,
// reporting whether e is positively understood as reading only indexed
// columns. This is deliberately a whitelist mirroring prefilterSafeExpr's
// understood set (plain column reads, constants, and the transparent
// comparison/arithmetic/boolean/cast/is-null wrappers, value-list IN):
// anything else —
// FuncCall, RowExpr, CTIDExpr, TableOidExpr, MergeWholeRowRef, subquery and
// outer-ref shapes, and every future expression kind — returns false and the
// caller declines to full deform. A missed arm costs performance, never
// correctness.
func deformScanRefs(e optimizer.Expr, maxRef *int) bool {
	return deformScanVisit(e, func(idx int) {
		if idx > *maxRef {
			*maxRef = idx
		}
	})
}

// deformScanVisit is deformScanRefs' walk: it calls visit for every
// ColumnRef.Index e reads and reports whether e is in the understood set
// (deformScanRefs' whitelist). A false return means the reads are unknown.
func deformScanVisit(e optimizer.Expr, visit func(idx int)) bool {
	switch x := e.(type) {
	case nil:
		return false
	case *optimizer.ColumnRef:
		// Index<0 is the whole-row convention (see the NLI shift
		// paths): it cannot be bounded, so it declines.
		if x.Index < 0 {
			return false
		}
		visit(x.Index)
		return true
	case *optimizer.IntegerConst, *optimizer.StringConst, *optimizer.NumericConst,
		*optimizer.TypedStringLit, *optimizer.IntervalLit, *optimizer.NullConst,
		*optimizer.BooleanConst:
		return true
	case *optimizer.BinaryOp:
		// LIKE...ESCAPE carries a LikeEscapePattern operand, which the
		// whitelist rejects, so those predicates decline — same as the
		// prefilter.
		return deformScanVisit(x.Left, visit) && deformScanVisit(x.Right, visit)
	case *optimizer.UnaryOp:
		return deformScanVisit(x.Operand, visit)
	case *optimizer.CastExpr:
		return deformScanVisit(x.Operand, visit)
	case *optimizer.IsNullExpr:
		return deformScanVisit(x.Operand, visit)
	case *optimizer.IsBoolExpr:
		return deformScanVisit(x.Operand, visit)
	case *optimizer.IsDistinctFromExpr:
		return deformScanVisit(x.Left, visit) && deformScanVisit(x.Right, visit)
	case *optimizer.InExpr:
		// A value list (`x IN (a, b)`, `x = ANY('{…}'::t[])`) reads its
		// operand and items and nothing else: evalInExprScalarRow evaluates
		// exactly those against the row (slice 25; PlanScanQual admits the
		// same shape, slice 21). The sublink form declines.
		if x.Plan != nil || x.Subquery != nil || len(x.Args) > 0 || len(x.ParParam) > 0 {
			return false
		}
		if !deformScanVisit(x.Operand, visit) {
			return false
		}
		for _, it := range x.List {
			if !deformScanVisit(it, visit) {
				return false
			}
		}
		return true
	case *optimizer.SubqueryExpr:
		args, ok := deformSublinkArgs(x)
		if !ok {
			return false
		}
		for _, a := range args {
			if !deformScanVisit(a, visit) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// deformScanReadSet reports which of a scan's width columns exprs read, or
// nil when any expression falls outside deformScanVisit's understood set (the
// reads are then unknown and every column must be decoded).
func deformScanReadSet(width int, exprs ...optimizer.Expr) []bool {
	read := make([]bool, width)
	for _, e := range exprs {
		if e == nil {
			continue
		}
		ok := deformScanVisit(e, func(idx int) {
			if idx < width {
				read[idx] = true
			}
		})
		if !ok {
			return nil
		}
	}
	return read
}

// deformSublinkArgs reports the outer-row expressions a scalar sublink reads
// (M0122-0012 slice 17). A param-lowered sublink (ParParam set, D4.1) binds
// its correlation params from Args (bindSubPlanParams). Its inner plan reads
// those ParamExec slots, nothing is pushed on OuterRows, and the result cache
// is keyed by the bound values. So Args are its only consumers of the outer
// row, as a PG SubPlan's parParam/args are. An unlowered sublink pushes the
// whole row and keys its cache by every column (subqueryCacheKey), so it
// cannot be bounded and declines.
//
// A non-correlated sublink (IsNonCorrelated: its plan reads no outer value at
// all, planHasOuterRef) reads nothing of the row either: evalSubquery keys its
// result by the sublink alone (nonCorrelatedCacheKey, InitPlan semantics) and
// the row it pushes on OuterRows is never read — PG plans it as an InitPlan,
// whose param references no column of the scan (M0122-0012 slice 53; TPC-DS
// Q14's `d_week_seq = (SELECT d_week_seq FROM date_dim …)` probe filter
// otherwise deformed every date_dim, store_sales and item column).
func deformSublinkArgs(x *optimizer.SubqueryExpr) ([]optimizer.Expr, bool) {
	if x == nil {
		return nil, false
	}
	if len(x.ParParam) == 0 {
		return nil, x.IsNonCorrelated
	}
	return x.Args, true
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
// from the incoming bound. Every node here has a single child subtree; Join
// and NestedLoopIndexJoin thread per-side bounds through deformJoinBounds /
// deformNLIOuterBound instead (see the buildNode/buildRec arms). Nodes not
// listed (WindowAgg, SetOp, CTE shapes, DistinctOn, DML, …) decline the whole
// subtree to full deform.
func deformBoundBelow(parent optimizer.Node, incoming int) int {
	switch p := parent.(type) {
	case *optimizer.SubqueryScan:
		// M0146-0005w: labelling pass-through — output column i is child
		// column i, so the bound crosses unchanged.
		return incoming
	case *optimizer.Materialize:
		// M0146-0010: same pass-through — the buffer adds no exprs.
		return incoming
	case *optimizer.Filter:
		return deformFoldRefs(incoming, p.Predicate)
	case *optimizer.Sort:
		return deformFoldRefs(incoming, deformSortKeyExprs(p.Keys)...)
	case *optimizer.IncrementalSort:
		// M0141-S7-exec-b: same consumer set as Sort — the whole Keys list,
		// prefix and trailing alike (the prefix keys are still evaluated per
		// row by sortPrefixEqual's group split).
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
		// full, as it must. Every other projection folds its targets
		// alone (deformProjectBelow). Every target is live here: whether
		// the incoming bound covers every reader of the projection's
		// output is known only to a consumer-complete parent, which builds
		// the projection itself (buildProjectNode, M0122-0012 slice 35).
		return deformProjectBelow(p, incoming, nil)
	case *optimizer.Aggregate:
		// Reshape boundary: the incoming bound is in aggregate-output
		// space and cannot map through it, so it is dropped. The arms
		// below ARE in child-output space, so they are read as consumers
		// first and re-narrow the fresh bound; the walk continues below.
		// With no arm folding a reference the bound stays deformBoundZero
		// and the scan deforms nothing. A Finalize aggregate consumes its
		// child's state columns without an arm, so it starts at None.
		fresh := deformBoundZero
		if p.Mode == optimizer.AggModeFinal {
			fresh = deformBoundNone
		}
		bound := deformFoldRefs(fresh, p.GroupExprs...)
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

// deformProjectBelow is the bound a projection threads to its child when it
// evaluates only the targets live marks (nil = every target).
func deformProjectBelow(p *optimizer.Project, incoming int, live []bool) int {
	targets := p.Targets
	if live != nil {
		targets = make([]optimizer.Expr, 0, len(p.Targets))
		for i, t := range p.Targets {
			if live[i] {
				targets = append(targets, t)
			}
		}
	}
	if isIdentityProject(p) {
		// Identity targets are genuine consumers in child-output
		// space (a root SELECT a,b reads columns 0..1 of the leaf
		// row), so they fold like any other consumer. Folding can
		// only widen to len(Targets)-1; a full-width identity
		// re-widens to full, as it must.
		return deformFoldRefs(incoming, targets...)
	}
	// Any other projection (M0122-0012 slice 17): projectOp and the
	// op-tree OpProject evaluate p.Targets against the child slot and
	// read nothing else of it. So the targets are the child's only
	// consumers, whatever the parent reads of the projection's output,
	// and the bound is their fold. This is PG's rule:
	// ExecBuildProjectionInfo → ExecCreateExprSetupSteps
	// (execExpr.c) sets last_scan to the highest attnum the target
	// list references and emits one EEOP_SCAN_FETCHSOME for it. A
	// target the whitelist does not understand still declines to
	// full. Reordered bare targets used to reset to full here: HammerDB's
	// lineitem column order makes TPC-H Q17's probe projection
	// reordered, so all 16 columns were deformed and cloned per row.
	return deformFoldRefs(deformBoundNone, targets...)
}

// projectLiveSet marks the targets of p that its consumers read, or returns
// nil when every target must be evaluated (M0122-0012 slice 35). consumers
// are every expression that reads p's output, in p's output coordinates —
// which only a consumer-complete parent can supply (buildCompleteChild): a
// plain or partial Aggregate's arms, plus the keys of any sort between them.
//
// It exists for the re-expansion Project the planner puts between an
// aggregate and a narrowed join: it maps the join's narrowed row back to the
// aggregate's arm coordinates, so it lists every join column, and evaluating
// (and folding) all of them deformed the whole heap tuple of every inner
// probe. PG never builds the dead targets — a node's targetlist holds only
// what its parent needs — so skipping them is the PG-shaped behaviour.
//
// A dead target is skipped only when evaluating it can neither fail nor have
// an effect (projectTargetSkippable); any other is kept live. A consumer that
// may read the row beyond its cells (consumerLiveColumns), or a reference
// outside the target list, keeps every target.
//
// The question is only which targets a consumer reads, so the consumers are
// walked by consumerLiveColumns, through any expression kind. The narrowing
// whitelist (deformScanVisit) it used before declined every CASE, so the
// aggregates of TPC-DS Q43 / Q59 (`sum(CASE WHEN d_day_name = 'Sunday' THEN
// ss_sales_price ...)`) kept all 80 targets of their re-expansion Project
// live — every join column evaluated per row (M0122-0012 slice 79).
func projectLiveSet(p *optimizer.Project, consumers []optimizer.Expr) []bool {
	n := len(p.Targets)
	read := consumerLiveColumns(consumers)
	if read == nil {
		return nil
	}
	for i := n; i < len(read); i++ {
		if read[i] {
			return nil
		}
	}
	live := make([]bool, n)
	copy(live, read)
	all := true
	for i, t := range p.Targets {
		if !live[i] && !projectTargetSkippable(t) {
			live[i] = true
		}
		all = all && live[i]
	}
	if all {
		return nil
	}
	return live
}

// consumerLiveColumns marks the row columns consumers read, or returns nil
// (every column) when it cannot be sure. It answers only "which cells may a
// consumer read", not "may this expression be narrowed past", so unlike
// deformScanVisit it walks through function calls and operators of any kind
// (optimizer.WalkExprHostScope, fail-closed on an expression kind it does not
// enumerate) and collects their column references. It gives up on anything
// that can read the row as a whole or outside its cells: a whole-row or
// negative ColumnRef, a merge whole-row reference, a ctid / tableoid read, or
// a sublink (an unlowered one pushes the entire outer row). Columns past the
// highest reference are left out of the mask, which reads them as dead.
func consumerLiveColumns(consumers []optimizer.Expr) []bool {
	live := []bool{}
	for _, e := range consumers {
		if e == nil {
			continue
		}
		known := true
		ok := optimizer.WalkExprHostScope(e, func(x optimizer.Expr) {
			switch r := x.(type) {
			case *optimizer.ColumnRef:
				if r.Index < 0 {
					known = false
					return
				}
				for len(live) <= r.Index {
					live = append(live, false)
				}
				live[r.Index] = true
			case *optimizer.SubqueryExpr, *optimizer.MergeWholeRowRef, *optimizer.CTIDExpr, *optimizer.TableOidExpr:
				known = false
			}
		})
		if !ok || !known {
			return nil
		}
	}
	return live
}

// aggConsumerExprs is every expression a plain or partial Aggregate evaluates
// against its child's rows — the arms deformBoundBelow folds.
func aggConsumerExprs(p *optimizer.Aggregate) []optimizer.Expr {
	out := append([]optimizer.Expr(nil), p.GroupExprs...)
	for i := range p.Aggs {
		a := &p.Aggs[i]
		out = append(out, a.Arg, a.Arg2, a.Filter)
		out = append(out, a.ExtraArgs...)
		out = append(out, deformSortKeyExprs(a.OrderBy)...)
		out = append(out, deformSortKeyExprs(a.WithinGroupOrderBy)...)
	}
	return append(out, p.Passthrough...)
}

// projectTargetSkippable reports whether a target may be left unevaluated:
// its evaluation cannot raise an error or have a side effect.
func projectTargetSkippable(e optimizer.Expr) bool {
	switch e.(type) {
	case *optimizer.ColumnRef, *optimizer.NullConst, *optimizer.IntegerConst,
		*optimizer.BooleanConst, *optimizer.StringConst, *optimizer.NumericConst:
		return true
	}
	return false
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
		case *optimizer.IncrementalSort:
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
		case *optimizer.SubqueryExpr:
			// Same rule as deformScanRefs: a lowered sublink reads the
			// merged row only through its Args.
			args, ok := deformSublinkArgs(x)
			if !ok {
				return false
			}
			for _, a := range args {
				if !walk(a) {
					return false
				}
			}
			return true
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
	left = deformUnionBound(leftAbove, deformUnionBound(leftKeys, leftSplit))
	if p.Lateral {
		left = deformUnionBound(left, deformLateralOuterRefs(p.Right))
	}
	return left, deformUnionBound(rightAbove, deformUnionBound(rightKeys, rightSplit))
}

// joinLeftConsumers translates the consumers of a join's merged (left++right)
// output into the expressions that read its LEFT rows, for buildCompleteChild:
// one ColumnRef per live left column, plus the join's own readers of the left
// row — the merged-space predicate (folded into the live set) and the left
// halves of its equi-key pairs, which evaluate positionally against left rows
// (deformPositionalKeys). ok is false — build the join normally — for a join
// type whose output is not that layout, a LATERAL or null-aware join, a
// layout the side widths do not account for, or consumers whose reads
// consumerLiveColumns cannot enumerate.
func joinLeftConsumers(p *optimizer.Join, consumers []optimizer.Expr) ([]optimizer.Expr, bool) {
	switch p.Type {
	case optimizer.JoinTypeInner, optimizer.JoinTypeLeft, optimizer.JoinTypeCross:
	default:
		return nil, false
	}
	if p.Lateral || p.NullAware || len(p.UsingLeftCols) > 0 {
		return nil, false
	}
	leftW, rightW := deformSideWidth(p.Left), deformSideWidth(p.Right)
	if leftW <= 0 || rightW <= 0 || len(p.Output()) != leftW+rightW {
		return nil, false
	}
	live := consumerLiveColumns(append(append([]optimizer.Expr(nil), consumers...), p.Predicate))
	if live == nil {
		return nil, false
	}
	var out []optimizer.Expr
	for i := range min(len(live), leftW) {
		if live[i] {
			out = append(out, &optimizer.ColumnRef{Index: i})
		}
	}
	if len(p.HashKeys) > 0 {
		for _, k := range p.HashKeys {
			out = append(out, k.Left)
		}
	} else {
		out = append(out, p.LeftKey)
	}
	return out, true
}

// deformLateralOuterRefs is the left-side bound a LATERAL join's inner reads
// of the outer row: lateralJoinStream binds every outer row into the inner's
// outer slot, and the probe evaluates its key expressions — and any outer
// reference in its Cond — against it. Those reads are OuterColumnRef{Level:
// 1} (Index = the outer row's column); a ColumnRef among the keys is taken at
// face value, which can only widen. It is the generic-Join twin of
// NestedLoopIndexJoin's deformNLIOuterKeyExprs fold. An inner the walk does
// not positively understand, or an expression it cannot enumerate (a
// sublink, an unknown kind), reads the outer row in ways it cannot
// inventory, so the outer deforms in full (M0122-0012 slice 35 — before it,
// the omission was masked by the full-width projection that always sat
// above such a join's outer side).
func deformLateralOuterRefs(right optimizer.Node) int {
	var keys []optimizer.Expr
	var cond optimizer.Expr
	switch in := right.(type) {
	case *optimizer.Memoize:
		return deformLateralOuterRefs(in.Child)
	case *optimizer.IndexScan:
		keys = append(deformNLIOuterKeyExprs(in), in.SAOPKeys...)
		keys = append(keys, in.RangePrefix...)
		cond = in.Cond
	case *optimizer.IndexOnlyScan:
		keys, cond = deformNLIOuterKeyExprs(in), in.Cond
	default:
		return deformBoundFull
	}
	bound := deformBoundNone
	visit := func(e optimizer.Expr, isKey bool) bool {
		if e == nil {
			return true
		}
		known := true
		ok := optimizer.WalkExprHostScope(e, func(x optimizer.Expr) {
			switch r := x.(type) {
			case *optimizer.OuterColumnRef:
				if r.Level == 1 {
					if r.Index < 0 {
						known = false
					} else if r.Index > bound {
						bound = r.Index
					}
				}
			case *optimizer.ColumnRef:
				// A key's ColumnRef indexes the outer row (as on an NLI
				// inner); a Cond's indexes the inner row and reads nothing
				// of the outer one.
				if isKey {
					if r.Index < 0 {
						known = false
					} else if r.Index > bound {
						bound = r.Index
					}
				}
			case *optimizer.SubqueryExpr:
				// A non-correlated sublink reads no outer value
				// (deformSublinkArgs); any other may read the outer row.
				if !r.IsNonCorrelated || len(r.ParParam) > 0 {
					known = false
				}
			}
		})
		return ok && known
	}
	for _, k := range keys {
		if !visit(k, true) {
			return deformBoundFull
		}
	}
	if !visit(cond, false) {
		return deformBoundFull
	}
	return bound
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
	// EX1-02b fix (NLI probe-key hole): the inner probe's Key/Keys/
	// LowKey/HighKey evaluate against the OUTER slot
	// (lookupKey/lookupKeys/lookupRangeBounds via o.outerSlot), so their
	// outer-space refs are genuine outer consumers. Without this fold, an
	// outer bound narrowed past a probe-key column starves the per-row
	// re-probe (stale tail read; poison-caught on Q4-class shapes).
	outerKeys = deformFoldRefs(outerKeys, deformNLIOuterKeyExprs(p.Inner)...)
	return deformUnionBound(above, outerKeys)
}

// deformNLIOuterKeyExprs collects the inner probe's key exprs, which read
// outer-row columns (never inner deform). Both closed inner kinds carry
// the same four slots. Non-ColumnRef shapes (OuterRef indirection, CASE)
// decline via deformScanRefs → full outer, the safe direction.
func deformNLIOuterKeyExprs(inner optimizer.Node) []optimizer.Expr {
	switch in := inner.(type) {
	case *optimizer.IndexScan:
		out := make([]optimizer.Expr, 0, len(in.Keys)+3)
		out = append(out, in.Key, in.LowKey, in.HighKey)
		return append(out, in.Keys...)
	case *optimizer.IndexOnlyScan:
		out := make([]optimizer.Expr, 0, len(in.Keys)+3)
		out = append(out, in.Key, in.LowKey, in.HighKey)
		return append(out, in.Keys...)
	default:
		return nil
	}
}

// deformIndexLeafBound computes the EX1-02b bound an IndexScan leaf deforms:
// the union of the incoming parent-walk bound and the leaf-local Cond refs.
// Only when no consumer bound is recorded (None, or the count(*)-style Zero
// an index leaf would otherwise read as full width) is it widened with the
// index's heap key-column ordinals.
//
// The key PROBE exprs (Key/Keys/LowKey/HighKey) are NEVER folded here: on an
// NLI inner they reference OUTER columns (outer-space indexes), so folding
// them at face value files outer coordinates into the inner bound —
// widening-only but wrong. The key COLUMNS (heap ordinals of Index.Columns)
// are safe to union instead: the descent reads them before the heap fetch,
// and unioning can only widen the consumer-derived bound (when no consumer
// is recorded at all, any bound is safe).
func deformIndexLeafBound(incoming int, cond optimizer.Expr, index *catalog.Index, table *catalog.Table) int {
	bound := deformFoldRefs(incoming, cond)
	if bound == deformBoundFull {
		return bound
	}
	// M0146-0171: a consumer-derived bound is the whole answer. PG deforms a
	// fetched heap tuple only up to the highest attribute a consumer reads
	// (slot_getsomeattrs from the quals and target list); a btree index scan
	// never re-reads its key columns from the heap tuple — the descent reads
	// the index tuple, and every recheck is a Cond already folded above. The
	// key-column widening only stood in for a missing consumer set: on TPC-DS
	// Q17's catalog_sales_pkey probe it deformed through cs_order_number
	// (18 of 34 columns) for a Filter that reads cs_bill_customer_sk (4).
	if bound >= 0 {
		return bound
	}
	if index != nil && table != nil {
		for _, kc := range index.Columns {
			for j, tc := range table.Columns {
				if tc.Name == kc {
					if j > bound {
						bound = j
					}
					break
				}
			}
		}
	}
	return bound
}

// deformBitmapLeafBound computes the EX1-02b bound a BitmapHeapScan leaf
// deforms. The BitmapQual recheck readers and the Cond readers are genuine
// consumers — folded FIRST — then unioned with the incoming parent bound; a
// decline in either fails the whole bound (re-widen, never narrow past a
// recheck reader).
func deformBitmapLeafBound(incoming int, bitmapQual []optimizer.Expr, cond optimizer.Expr) int {
	bound := deformFoldRefs(deformBoundNone, bitmapQual...)
	bound = deformFoldRefs(bound, cond)
	return deformUnionBound(bound, incoming)
}

// deformNLIMappedInner maps an incoming NLI-output-space (outer++inner)
// prefix bound to the inner-output share: the tail past the outer width. A
// prefix ending inside the outer side contributes nothing; a tail reaching
// past the inner width cannot map and fails to full.
func deformNLIMappedInner(incoming, outerW, innerW int) int {
	if incoming == deformBoundFull {
		return deformBoundFull
	}
	if incoming < 0 {
		return deformBoundNone
	}
	if outerW <= 0 || innerW <= 0 {
		return deformBoundFull
	}
	if incoming < outerW {
		return deformBoundNone
	}
	if r := incoming - outerW; r < innerW {
		return r
	}
	return deformBoundFull
}

// deformNLIInnerWidth reports the output width of an NLI inner probe.
// Planner-stamped Output() is authoritative; hand-built nodes carry no
// schema, so the width falls back to the probe's table shape (an IndexScan
// or BitmapHeapScan emits full heap rows, an IndexOnlyScan its Covered
// list). Anything else reports 0 and the caller declines to full.
func deformNLIInnerWidth(n optimizer.Node) int {
	if w := len(n.Output()); w > 0 {
		return w
	}
	switch x := n.(type) {
	case *optimizer.IndexScan:
		if x.Table != nil {
			return len(x.Table.Columns)
		}
	case *optimizer.IndexOnlyScan:
		if len(x.Covered) > 0 {
			return len(x.Covered)
		}
		if x.Table != nil {
			return len(x.Table.Columns)
		}
	case *optimizer.BitmapHeapScan:
		if x.Table != nil {
			return len(x.Table.Columns)
		}
	}
	return 0
}

// effectiveDeformBound resolves the threaded bound to the exclusive deform
// width for a leaf with ncols columns. deformBoundNone (no consumer recorded
// — e.g. a bare scan) and deformBoundFull both mean full width;
// deformBoundZero stamps deformWidthZero; anything else clamps to
// [1, ncols]. An effective bound equal to ncols must take the exact
// pre-EX1-01 decode path.
func effectiveDeformBound(bound, ncols int) int {
	if ncols <= 0 {
		return 0
	}
	if bound == deformBoundZero {
		return deformWidthZero
	}
	if bound < 0 || bound == deformBoundFull {
		return ncols
	}
	if need := bound + 1; need < ncols {
		return need
	}
	return ncols
}

// seqScanSurvivorWidth resolves a SeqScan's stamp to the number of leading
// columns its survivor path deforms: 0 for deformWidthZero, the stamp for a
// narrowing stamp, and ncols for unset (0) or full. The absorbed qual's
// columns are always inside it: the walk folds the Filter predicate before
// the scan is built, so a qual that reads a column never leaves the bound
// at deformBoundZero.
func seqScanSurvivorWidth(stamp, ncols int) int {
	if stamp == deformWidthZero {
		return 0
	}
	if stamp > 0 && stamp < ncols {
		return stamp
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

// prefilterSkipInfo returns a copy of info whose columns in [0, maxCols) that
// qual does not read are marked skip (M0122-0012 slice 25), or nil when the
// qual's reads cannot be bounded or no column would be skipped. PG's
// slot_getsomeattrs deforms the qual's prefix too, but a by-reference value
// costs it a pointer store; goopg's decode converts every value, so on TPC-H
// Q10's `l_returnflag = 'R'` the 75 % of lineitem rows the qual rejects paid
// for four numerics and two dates nobody read.
func prefilterSkipInfo(info []colTypeInfo, maxCols int, qual optimizer.Expr) []colTypeInfo {
	if maxCols > len(info) {
		return nil
	}
	read := deformScanReadSet(len(info), qual)
	if read == nil {
		return nil
	}
	out := append([]colTypeInfo(nil), info...)
	skipped := false
	for i := 0; i < maxCols; i++ {
		if !read[i] {
			out[i].skip = true
			skipped = true
		}
	}
	if !skipped {
		return nil
	}
	return out
}
