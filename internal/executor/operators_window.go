package executor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// windowOp is the Stage-A WindowAgg executor skeleton. It drains
// child rows, sorts by PARTITION BY/ORDER BY, and appends one
// placeholder column per planned window function.
type windowOp struct {
	plan   *optimizer.WindowAgg
	child  Operator
	schema optimizer.Schema

	ctx  *Context
	rows []Row
	idx  int

	// frameStartOff/frameEndOff are o.plan.Frame's offset expressions
	// evaluated once (mirroring LIMIT/OFFSET's once-per-query
	// evaluation, not per-row — PG disallows column/aggregate/window
	// references in a frame offset for exactly this reason). Only
	// meaningful when the corresponding bound kind is
	// FrameBoundOffsetPreceding/Following.
	frameStartOff int64
	frameEndOff   int64

	// frameStartOffDatum/frameEndOffDatum are the RANGE-mode value offsets
	// (numeric/float/interval, not just int), evaluated once alongside
	// frameStartOff/frameEndOff. Only meaningful when Frame.Mode is
	// FrameModeRange and the corresponding bound is
	// FrameBoundOffsetPreceding/Following; see frameBoundsRange.
	frameStartOffDatum Datum
	frameEndOffDatum   Datum
}

func newWindowOp(plan *optimizer.WindowAgg, child Operator) *windowOp {
	return &windowOp{plan: plan, child: child, schema: plan.Output()}
}

func (o *windowOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	for {
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		// Materialize at retention boundary: windowOp holds rows
		// across many Next() calls. (M0071-0010 Stage B.)
		row := slot.Materialize().Row()
		dup := make(Row, len(row))
		copy(dup, row)
		o.rows = append(o.rows, dup)
	}

	if len(o.plan.PartitionBy) > 0 || len(o.plan.OrderBy) > 0 {
		var sortErr error
		sort.SliceStable(o.rows, func(i, j int) bool {
			if sortErr != nil {
				return false
			}
			for _, pe := range o.plan.PartitionBy {
				a, err := evalExpr(pe, o.rows[i], ctx)
				if err != nil {
					sortErr = err
					return false
				}
				b, err := evalExpr(pe, o.rows[j], ctx)
				if err != nil {
					sortErr = err
					return false
				}
				cmp, decided, err := compareSortDatums(a, b, pe.Pos(), false, false)
				if err != nil {
					sortErr = err
					return false
				}
				if decided {
					return cmp < 0
				}
			}
			for _, ok := range o.plan.OrderBy {
				a, err := evalExpr(ok.Expr, o.rows[i], ctx)
				if err != nil {
					sortErr = err
					return false
				}
				b, err := evalExpr(ok.Expr, o.rows[j], ctx)
				if err != nil {
					sortErr = err
					return false
				}
				cmp, decided, err := compareSortDatums(a, b, ok.Expr.Pos(), ok.Desc, ok.NullsFirst)
				if err != nil {
					sortErr = err
					return false
				}
				if decided {
					if ok.Desc {
						return cmp > 0
					}
					return cmp < 0
				}
			}
			return false
		})
		if sortErr != nil {
			return sortErr
		}
	}

	if n := len(o.plan.Funcs); n > 0 {
		for i := range o.rows {
			out := make(Row, 0, len(o.rows[i])+n)
			out = append(out, o.rows[i]...)
			for j := 0; j < n; j++ {
				out = append(out, NullDatum)
			}
			o.rows[i] = out
		}
		if err := o.evalWindowFuncs(); err != nil {
			return err
		}
	}

	return nil
}

func (o *windowOp) evalWindowFuncs() error {
	if len(o.plan.Funcs) == 0 {
		return nil
	}
	colBase := 0
	if len(o.rows) > 0 {
		colBase = len(o.rows[0]) - len(o.plan.Funcs)
	}

	// Find partition start indices (rows are already sorted by partition key).
	pStarts := []int{0}
	if len(o.rows) > 0 {
		prevKey, err := o.partitionKey(o.rows[0])
		if err != nil {
			return err
		}
		for i := 1; i < len(o.rows); i++ {
			key, err := o.partitionKey(o.rows[i])
			if err != nil {
				return err
			}
			if key != prevKey {
				pStarts = append(pStarts, i)
				prevKey = key
			}
		}
	}
	pStarts = append(pStarts, len(o.rows)) // sentinel

	// aggHelper reuses the ordinary GROUP BY aggregate accumulator
	// (numeric-exact sums, float4/float8 formatting, NULL-skipping,
	// FILTER support) for frame-consuming window aggregates. Its
	// methods only touch o.ctx, so a bare instance is safe to share.
	aggHelper := &aggregateOp{ctx: o.ctx}

	// A frame's offset expressions are evaluated once per query, not
	// per row or per partition (PG forbids column/aggregate/window
	// references in them for exactly this reason) — mirrors
	// limitOp.Open's once-per-query LIMIT/OFFSET evaluation.
	if fr := o.plan.Frame; fr != nil {
		if fr.Mode == parser.FrameModeRange {
			// RANGE value offsets are compared against the ORDER BY column
			// value (value±offset), so the offset keeps its native type
			// (numeric/float/interval) rather than being coerced to an int
			// row count. resolveRangeOffset null/negative-checks it.
			if fr.StartOffset != nil {
				v, err := o.resolveRangeOffset(fr.StartOffset, "starting")
				if err != nil {
					return err
				}
				o.frameStartOffDatum = v
			}
			if fr.EndOffset != nil {
				v, err := o.resolveRangeOffset(fr.EndOffset, "ending")
				if err != nil {
					return err
				}
				o.frameEndOffDatum = v
			}
		} else {
			if fr.StartOffset != nil {
				v, err := o.resolveFrameOffset(fr.StartOffset, "starting")
				if err != nil {
					return err
				}
				o.frameStartOff = v
			}
			if fr.EndOffset != nil {
				v, err := o.resolveFrameOffset(fr.EndOffset, "ending")
				if err != nil {
					return err
				}
				o.frameEndOff = v
			}
		}
	}

	// Evaluate each partition independently.
	for p := 0; p < len(pStarts)-1; p++ {
		pStart := pStarts[p]
		pEnd := pStarts[p+1]
		rowNum := int64(0)
		rank := int64(1)
		denseRank := int64(1)

		if err := o.evalFrameAggFuncs(aggHelper, colBase, pStart, pEnd); err != nil {
			return err
		}
		if err := o.evalNtileFuncs(colBase, pStart, pEnd); err != nil {
			return err
		}

		// frameEnd[i-pStart] is the exclusive end of the peer group
		// containing row i — the default-frame end (RANGE UNBOUNDED
		// PRECEDING AND CURRENT ROW) that first_value/last_value/
		// nth_value/cume_dist fall back to. cume_dist always uses this
		// array even under an explicit frame clause: per PG spec,
		// cume_dist (like row_number/rank/lag/lead/ntile/percent_rank)
		// is frame-independent — only aggregates and first_value/
		// last_value/nth_value respect an explicit frame. Only
		// computed when one of those functions is actually present.
		var frameEnd []int
		var valueGroupBounds []int
		needsValueGroupBounds := o.plan.Frame != nil &&
			(o.plan.Frame.Exclusion != parser.FrameExcludeNone ||
				o.plan.Frame.Mode == parser.FrameModeGroups ||
				o.plan.Frame.Mode == parser.FrameModeRange) &&
			hasFrameValueWindowFunc(o.plan.Funcs)
		if hasFrameValueWindowFunc(o.plan.Funcs) {
			groupBounds, err := o.peerGroupBounds(pStart, pEnd)
			if err != nil {
				return err
			}
			frameEnd = make([]int, pEnd-pStart)
			for g := 0; g < len(groupBounds)-1; g++ {
				gStart, gEnd := groupBounds[g], groupBounds[g+1]
				for i := gStart; i < gEnd; i++ {
					frameEnd[i-pStart] = gEnd
				}
			}
			if needsValueGroupBounds {
				valueGroupBounds = groupBounds
			}
		}

		for i := pStart; i < pEnd; i++ {
			rowNum++
			if i > pStart {
				peer, err := o.samePeer(o.rows[i-1], o.rows[i])
				if err != nil {
					return err
				}
				if !peer {
					rank = rowNum
					denseRank++
				}
			}
			localIdx := i - pStart // 0-based position within this partition

			for j, fn := range o.plan.Funcs {
				colIdx := colBase + j
				switch strings.ToLower(fn.Name) {
				case "row_number":
					o.rows[i][colIdx] = Datum{Kind: KindInt, Int: rowNum}
				case "rank":
					o.rows[i][colIdx] = Datum{Kind: KindInt, Int: rank}
				case "dense_rank":
					o.rows[i][colIdx] = Datum{Kind: KindInt, Int: denseRank}
				case "lag", "lead":
					offset := int64(1)
					if len(fn.Args) >= 2 {
						v, err := evalExpr(fn.Args[1], o.rows[i], o.ctx)
						if err != nil {
							return err
						}
						if v.Kind == KindInt {
							offset = v.Int
						}
					}
					var targetLocal int
					if strings.ToLower(fn.Name) == "lag" {
						targetLocal = localIdx - int(offset)
					} else {
						targetLocal = localIdx + int(offset)
					}
					targetGlobal := pStart + targetLocal
					if targetGlobal < pStart || targetGlobal >= pEnd {
						// Out of partition — return default or NULL.
						if len(fn.Args) >= 3 {
							v, err := evalExpr(fn.Args[2], o.rows[i], o.ctx)
							if err != nil {
								return err
							}
							o.rows[i][colIdx] = v
						} else {
							o.rows[i][colIdx] = NullDatum
						}
					} else {
						v, err := evalExpr(fn.Args[0], o.rows[targetGlobal], o.ctx)
						if err != nil {
							return err
						}
						o.rows[i][colIdx] = v
					}
				case "sum", "count", "avg", "min", "max":
					// Already computed per-frame in evalFrameAggFuncs above.
				case "ntile":
					// Already computed per-partition in evalNtileFuncs above.
				case "percent_rank":
					total := int64(pEnd - pStart)
					if total <= 1 {
						o.rows[i][colIdx] = NewStringDatum("0")
					} else {
						result := float64(rank-1) / float64(total-1)
						o.rows[i][colIdx] = NewStringDatum(strconv.FormatFloat(result, 'g', 15, 64))
					}
				case "cume_dist":
					total := int64(pEnd - pStart)
					np := int64(frameEnd[localIdx] - pStart)
					result := float64(np) / float64(total)
					o.rows[i][colIdx] = NewStringDatum(strconv.FormatFloat(result, 'g', 15, 64))
				case "first_value":
					start, end := pStart, frameEnd[localIdx]
					if o.plan.Frame != nil {
						var err error
						start, end, err = o.frameBounds(pStart, pEnd, i, valueGroupBounds)
						if err != nil {
							return err
						}
					}
					idx := start
					if o.plan.Frame != nil && o.plan.Frame.Exclusion != parser.FrameExcludeNone {
						peerStart, peerEnd := peerBoundsOf(valueGroupBounds, i)
						idx = firstInFrame(start, end, i, peerStart, peerEnd, o.plan.Frame.Exclusion)
					}
					if idx < 0 || idx >= end {
						o.rows[i][colIdx] = NullDatum
						continue
					}
					v, err := evalExpr(fn.Args[0], o.rows[idx], o.ctx)
					if err != nil {
						return err
					}
					o.rows[i][colIdx] = v
				case "last_value":
					start, end := pStart, frameEnd[localIdx]
					if o.plan.Frame != nil {
						var err error
						start, end, err = o.frameBounds(pStart, pEnd, i, valueGroupBounds)
						if err != nil {
							return err
						}
					}
					idx := end - 1
					if o.plan.Frame != nil && o.plan.Frame.Exclusion != parser.FrameExcludeNone {
						peerStart, peerEnd := peerBoundsOf(valueGroupBounds, i)
						idx = lastInFrame(start, end, i, peerStart, peerEnd, o.plan.Frame.Exclusion)
					}
					if idx < start {
						o.rows[i][colIdx] = NullDatum
						continue
					}
					v, err := evalExpr(fn.Args[0], o.rows[idx], o.ctx)
					if err != nil {
						return err
					}
					o.rows[i][colIdx] = v
				case "nth_value":
					nVal, err := evalExpr(fn.Args[1], o.rows[i], o.ctx)
					if err != nil {
						return err
					}
					if nVal.IsNull() {
						o.rows[i][colIdx] = NullDatum
						continue
					}
					if nVal.Kind != KindInt || nVal.Int <= 0 {
						return &ExecError{Code: "22016", Pos: fn.Pos(), Message: "argument of nth_value must be greater than zero"}
					}
					start, end := pStart, frameEnd[localIdx]
					if o.plan.Frame != nil {
						var err error
						start, end, err = o.frameBounds(pStart, pEnd, i, valueGroupBounds)
						if err != nil {
							return err
						}
					}
					var target int
					if o.plan.Frame != nil && o.plan.Frame.Exclusion != parser.FrameExcludeNone {
						peerStart, peerEnd := peerBoundsOf(valueGroupBounds, i)
						target = nthInFrame(start, end, i, peerStart, peerEnd, o.plan.Frame.Exclusion, int(nVal.Int))
					} else {
						target = start + int(nVal.Int) - 1 // offset from frame head
						if target >= end {
							target = -1
						}
					}
					if target < 0 {
						o.rows[i][colIdx] = NullDatum
						continue
					}
					v, err := evalExpr(fn.Args[0], o.rows[target], o.ctx)
					if err != nil {
						return err
					}
					o.rows[i][colIdx] = v
				default:
					return &ExecError{Code: "0A000", Pos: fn.Pos(), Message: "window function is not supported in v0 executor"}
				}
			}
		}
	}
	return nil
}

// isFrameAggWindowFunc reports whether name is one of the ordinary
// aggregates usable as a window function (sum/count/avg/min/max).
// These consume a frame rather than a fixed row offset like
// row_number/rank/lag/lead.
func isFrameAggWindowFunc(name string) bool {
	switch strings.ToLower(name) {
	case "sum", "count", "avg", "min", "max":
		return true
	}
	return false
}

// hasFrameValueWindowFunc reports whether fns contains first_value,
// last_value, nth_value, or cume_dist — functions that need the
// default-frame end (peer-group boundary) computed per row.
func hasFrameValueWindowFunc(fns []optimizer.WindowFunc) bool {
	for _, fn := range fns {
		switch strings.ToLower(fn.Name) {
		case "first_value", "last_value", "nth_value", "cume_dist":
			return true
		}
	}
	return false
}

// evalNtileFuncs computes ntile() bucket numbers for every ntile() call in
// o.plan.Funcs across one partition, writing results directly into o.rows —
// mirroring how evalFrameAggFuncs pre-computes sum/count/avg/min/max so the
// per-row switch in evalWindowFuncs only has to skip already-filled columns.
func (o *windowOp) evalNtileFuncs(colBase, pStart, pEnd int) error {
	for j, fn := range o.plan.Funcs {
		if strings.ToLower(fn.Name) != "ntile" {
			continue
		}
		if err := o.evalNtileFunc(fn, colBase+j, pStart, pEnd); err != nil {
			return err
		}
	}
	return nil
}

// evalNtileFunc computes ntile()'s bucket assignment for one partition,
// reproducing window_ntile's exact bucket-sizing algorithm
// (postgres/src/backend/utils/adt/windowfuncs.c): the first `total %
// nbuckets` buckets get one extra row so the remainder isn't concentrated
// in the last bucket. The bucket-count argument is evaluated once, from the
// partition's first row, matching PostgreSQL's WinGetFuncArgCurrent-on-
// first-call semantics.
func (o *windowOp) evalNtileFunc(fn optimizer.WindowFunc, colIdx, pStart, pEnd int) error {
	total := int64(pEnd - pStart)
	if total == 0 {
		return nil
	}
	nArg, err := evalExpr(fn.Args[0], o.rows[pStart], o.ctx)
	if err != nil {
		return err
	}
	if nArg.IsNull() {
		for i := pStart; i < pEnd; i++ {
			o.rows[i][colIdx] = NullDatum
		}
		return nil
	}
	if nArg.Kind != KindInt || nArg.Int <= 0 {
		return &ExecError{Code: "22014", Pos: fn.Pos(), Message: "argument of ntile must be greater than zero"}
	}
	nbuckets := nArg.Int

	ntile := int64(1)
	rowsPerBucket := int64(0)
	boundary := total / nbuckets
	remainder := int64(0)
	if boundary <= 0 {
		boundary = 1
	} else {
		remainder = total % nbuckets
		if remainder != 0 {
			boundary++
		}
	}
	for i := pStart; i < pEnd; i++ {
		rowsPerBucket++
		if boundary < rowsPerBucket {
			if remainder != 0 && ntile == remainder {
				remainder = 0
				boundary--
			}
			ntile++
			rowsPerBucket = 1
		}
		o.rows[i][colIdx] = NewIntDatum(ntile)
	}
	return nil
}

// evalFrameAggFuncs computes sum/count/avg/min/max window functions for
// one partition ([pStart, pEnd) of o.rows) and writes the results
// directly into o.rows. It reuses aggHelper (a bare *aggregateOp) to
// share the ordinary GROUP BY accumulator, so formatting/NULL handling
// matches non-window aggregates exactly.
//
// Frame semantics match PostgreSQL's default when no frame clause is
// given: RANGE UNBOUNDED PRECEDING (cumulative, peer-inclusive) when
// ORDER BY is present, otherwise the entire partition. Peer-group
// boundaries are the same ones rank() uses, so peerGroupBounds
// naturally collapses to a single group (the whole partition) when
// there is no ORDER BY, since samePeer always returns true in that case.
func (o *windowOp) evalFrameAggFuncs(aggHelper *aggregateOp, colBase, pStart, pEnd int) error {
	hasAggFunc := false
	for _, fn := range o.plan.Funcs {
		if isFrameAggWindowFunc(fn.Name) {
			hasAggFunc = true
			break
		}
	}
	if !hasAggFunc {
		return nil
	}

	if o.plan.Frame != nil {
		return o.evalExplicitFrameAggFuncs(aggHelper, colBase, pStart, pEnd)
	}

	groupBounds, err := o.peerGroupBounds(pStart, pEnd)
	if err != nil {
		return err
	}

	for j, fn := range o.plan.Funcs {
		if !isFrameAggWindowFunc(fn.Name) {
			continue
		}
		colIdx := colBase + j
		call := windowFuncToAggregateCall(fn)
		var running aggRuntime
		for g := 0; g < len(groupBounds)-1; g++ {
			gStart, gEnd := groupBounds[g], groupBounds[g+1]
			for i := gStart; i < gEnd; i++ {
				if err := aggHelper.applyAgg(&running, call, asSlot(o.schema, o.rows[i])); err != nil {
					return err
				}
			}
			val, ferr := aggHelper.finishAgg(running, call)
			if ferr != nil {
				return ferr
			}
			for i := gStart; i < gEnd; i++ {
				o.rows[i][colIdx] = val
			}
		}
	}
	return nil
}

// evalExplicitFrameAggFuncs computes sum/count/avg/min/max for an
// explicit ROWS or GROUPS frame clause. Unlike the default-frame path
// above (one running total per peer group, since the default frame
// only ever grows), an explicit frame's bounds can both grow and
// shrink from one row to the next (e.g. `ROWS BETWEEN 1 PRECEDING AND
// 1 FOLLOWING` slides rather than accumulates), so there is no valid
// single running total to share across rows — each row's frame is
// recomputed from scratch via the same aggregate accumulator. This is
// a new feature with no prior consumer, so correctness-first O(partition
// size × average frame size) is the deliberate v0 tradeoff; TPC-H
// doesn't use frame clauses, so this doesn't touch the spot-check gate.
func (o *windowOp) evalExplicitFrameAggFuncs(aggHelper *aggregateOp, colBase, pStart, pEnd int) error {
	var groupBounds []int
	if o.plan.Frame.Exclusion != parser.FrameExcludeNone ||
		o.plan.Frame.Mode == parser.FrameModeGroups ||
		o.plan.Frame.Mode == parser.FrameModeRange {
		gb, err := o.peerGroupBounds(pStart, pEnd)
		if err != nil {
			return err
		}
		groupBounds = gb
	}

	for j, fn := range o.plan.Funcs {
		if !isFrameAggWindowFunc(fn.Name) {
			continue
		}
		colIdx := colBase + j
		call := windowFuncToAggregateCall(fn)
		for i := pStart; i < pEnd; i++ {
			start, end, err := o.frameBounds(pStart, pEnd, i, groupBounds)
			if err != nil {
				return err
			}
			peerStart, peerEnd := 0, 0
			if o.plan.Frame.Exclusion != parser.FrameExcludeNone {
				peerStart, peerEnd = peerBoundsOf(groupBounds, i)
			}
			var running aggRuntime
			for k := start; k < end; k++ {
				if frameRowExcluded(o.plan.Frame.Exclusion, k, i, peerStart, peerEnd) {
					continue
				}
				if err := aggHelper.applyAgg(&running, call, asSlot(o.schema, o.rows[k])); err != nil {
					return err
				}
			}
			val, ferr := aggHelper.finishAgg(running, call)
			if ferr != nil {
				return ferr
			}
			o.rows[i][colIdx] = val
		}
	}
	return nil
}

// windowFuncToAggregateCall adapts a planner.WindowFunc (sum/count/avg/
// min/max only) into the planner.AggregateCall shape applyAgg/finishAgg
// expect, so window aggregates share the exact ordinary-aggregate
// accumulator instead of a second implementation that could drift.
func windowFuncToAggregateCall(fn optimizer.WindowFunc) optimizer.AggregateCall {
	call := optimizer.AggregateCall{
		Name:            fn.Name,
		Star:            fn.Star,
		Type:            fn.Type,
		InputType:       fn.InputType,
		Filter:          fn.Filter,
		SharedStateSlot: -1,
	}
	if !fn.Star && len(fn.Args) > 0 {
		call.Arg = fn.Args[0]
	}
	return call
}

// peerGroupBounds returns the peer-group start indices within
// [pStart, pEnd), plus the pEnd sentinel — mirroring the transitions
// evalWindowFuncs already tracks inline for rank(). When there is no
// ORDER BY, samePeer always returns true, so this collapses to a
// single group spanning the whole partition.
func (o *windowOp) peerGroupBounds(pStart, pEnd int) ([]int, error) {
	bounds := []int{pStart}
	for i := pStart + 1; i < pEnd; i++ {
		peer, err := o.samePeer(o.rows[i-1], o.rows[i])
		if err != nil {
			return nil, err
		}
		if !peer {
			bounds = append(bounds, i)
		}
	}
	bounds = append(bounds, pEnd)
	return bounds, nil
}

// resolveFrameOffset evaluates a frame's PRECEDING/FOLLOWING offset
// expression once (mirrors limitOp.Open's LIMIT/OFFSET evaluation —
// PG disallows column/aggregate/window-function references in a frame
// offset for exactly this reason: it's a constant for the whole
// query, not a per-row value). label is "starting" or "ending" to
// match nodeWindowAgg.c's exact error wording.
func (o *windowOp) resolveFrameOffset(expr optimizer.Expr, label string) (int64, error) {
	v, err := evalExpr(expr, nil, o.ctx)
	if err != nil {
		return 0, err
	}
	if v.IsNull() {
		return 0, &ExecError{Code: "22004", Pos: expr.Pos(), Message: fmt.Sprintf("frame %s offset must not be null", label)}
	}
	if v.Kind != KindInt {
		return 0, &ExecError{Code: "42804", Pos: expr.Pos(), Message: fmt.Sprintf("frame %s offset must be integer", label)}
	}
	if v.Int < 0 {
		return 0, &ExecError{Code: "22013", Pos: expr.Pos(), Message: fmt.Sprintf("frame %s offset must not be negative", label)}
	}
	return v.Int, nil
}

// frameBounds returns the absolute [start, end) row-index bounds of
// row i's window frame within partition [pStart, pEnd). groupBounds is
// the partition's peer-group boundary array (as returned by
// peerGroupBounds) and is only consulted for GROUPS mode — ROWS-mode
// callers may pass nil. Dispatches to frameBoundsGroups for GROUPS
// mode; otherwise reproduces update_frameheadpos/update_frametailpos's
// ROWS-mode arithmetic (postgres/src/backend/executor/nodeWindowAgg.c)
// exactly: both bounds are clamped to the partition, and an
// out-of-order result (e.g. a FOLLOWING start past a PRECEDING end)
// collapses to an empty frame rather than erroring, matching upstream.
func (o *windowOp) frameBounds(pStart, pEnd, i int, groupBounds []int) (int, int, error) {
	fr := o.plan.Frame
	if fr.Mode == parser.FrameModeRange && frameHasValueOffset(fr) {
		// RANGE with a value offset bound compares each row's ORDER BY
		// value against currentValue±offset (PostgreSQL's in_range), so it
		// cannot use the physical-index arithmetic below.
		return o.frameBoundsRange(pStart, pEnd, i)
	}
	if fr.Mode == parser.FrameModeGroups || fr.Mode == parser.FrameModeRange {
		// RANGE mode reaches here only with UNBOUNDED/CURRENT ROW bounds.
		// For those bound kinds RANGE is peer-based — CURRENT ROW means
		// "the current row's whole ORDER BY peer group" — which is exactly
		// what frameBoundsGroups computes for the non-offset bound kinds,
		// so the two modes share this path.
		s, e := o.frameBoundsGroups(i, groupBounds)
		return s, e, nil
	}
	local := i - pStart
	n := pEnd - pStart
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > n {
			return n
		}
		return v
	}
	var start, end int
	switch fr.StartKind {
	case parser.FrameBoundUnboundedPreceding:
		start = 0
	case parser.FrameBoundCurrentRow:
		start = local
	case parser.FrameBoundOffsetPreceding:
		start = clamp(local - int(o.frameStartOff))
	case parser.FrameBoundOffsetFollowing:
		start = clamp(local + int(o.frameStartOff))
	}
	switch fr.EndKind {
	case parser.FrameBoundUnboundedFollowing:
		end = n
	case parser.FrameBoundCurrentRow:
		end = local + 1
	case parser.FrameBoundOffsetPreceding:
		end = clamp(local - int(o.frameEndOff) + 1)
	case parser.FrameBoundOffsetFollowing:
		end = clamp(local + int(o.frameEndOff) + 1)
	}
	if end < start {
		end = start
	}
	return pStart + start, pStart + end, nil
}

// frameHasValueOffset reports whether either frame bound is a value
// offset (n PRECEDING / n FOLLOWING).
func frameHasValueOffset(fr *optimizer.WindowFrame) bool {
	return fr.StartKind == parser.FrameBoundOffsetPreceding ||
		fr.StartKind == parser.FrameBoundOffsetFollowing ||
		fr.EndKind == parser.FrameBoundOffsetPreceding ||
		fr.EndKind == parser.FrameBoundOffsetFollowing
}

// frameBoundsRange computes the [start, end) frame for row i in RANGE mode
// with a value offset bound. It mirrors nodeWindowAgg.c's update_frameheadpos
// / update_frametailpos RANGE START_OFFSET / END_OFFSET scans: rows are
// already sorted by the single ORDER BY column, so the frame head is the
// first row satisfying the start in_range predicate and the frame tail is
// the first row that fails the end predicate. The boundary value
// currentValue±offset reuses evalBinary (numeric/interval/date arithmetic)
// and the value comparison reuses compareDatum, so a column/offset type
// combination evalBinary cannot add/subtract surfaces as 0A000 here (goopg
// has no per-type in_range catalog). NULL handling matches PG: a non-null
// current row never frames a null-valued row; a null current row frames
// exactly its null peer block (UNBOUNDED extends that side to the edge).
func (o *windowOp) frameBoundsRange(pStart, pEnd, i int) (int, int, error) {
	ok := o.plan.OrderBy[0]
	desc := ok.Desc
	nullsFirst := ok.NullsFirst
	cur, err := evalExpr(ok.Expr, o.rows[i], o.ctx)
	if err != nil {
		return 0, 0, err
	}
	fr := o.plan.Frame

	// head: first index that is the frame start.
	head := pStart
	if fr.StartKind != parser.FrameBoundUnboundedPreceding {
		for head < pEnd {
			val, err := evalExpr(ok.Expr, o.rows[head], o.ctx)
			if err != nil {
				return 0, 0, err
			}
			stop, err := o.rangeStartReached(val, cur, desc, nullsFirst)
			if err != nil {
				return 0, 0, err
			}
			if stop {
				break
			}
			head++
		}
	}

	// tail: the exclusive frame end — the first row that no longer
	// satisfies the end predicate.
	tail := pStart
	if fr.EndKind == parser.FrameBoundUnboundedFollowing {
		tail = pEnd
	} else {
		for tail < pEnd {
			val, err := evalExpr(ok.Expr, o.rows[tail], o.ctx)
			if err != nil {
				return 0, 0, err
			}
			stop, err := o.rangeEndReached(val, cur, desc, nullsFirst)
			if err != nil {
				return 0, 0, err
			}
			if stop {
				break
			}
			tail++
		}
	}
	if tail < head {
		tail = head
	}
	return head, tail, nil
}

// rangeStartReached reports whether the candidate row's ORDER BY value val
// is the frame head for the current row's value cur, mirroring the break
// condition of nodeWindowAgg.c's update_frameheadpos RANGE loop.
func (o *windowOp) rangeStartReached(val, cur Datum, desc, nullsFirst bool) (bool, error) {
	if val.IsNull() || cur.IsNull() {
		if nullsFirst {
			// stop (frame head) unless head is null while cur is not
			return !val.IsNull() || cur.IsNull(), nil
		}
		return val.IsNull() || !cur.IsNull(), nil
	}
	// sub = START_OFFSET_PRECEDING (CURRENT ROW == offset 0 preceding);
	// less = false; flip both if DESC.
	sub := o.plan.Frame.StartKind != parser.FrameBoundOffsetFollowing
	less := false
	if desc {
		sub = !sub
		less = true
	}
	return o.inRange(val, cur, o.frameStartOffDatum, sub, less)
}

// rangeEndReached reports whether the candidate row's ORDER BY value val is
// the frame tail (first row past the frame end) for the current row's value
// cur, mirroring update_frametailpos's RANGE loop (which breaks when the
// in_range predicate first turns false).
func (o *windowOp) rangeEndReached(val, cur Datum, desc, nullsFirst bool) (bool, error) {
	if val.IsNull() || cur.IsNull() {
		if nullsFirst {
			return !val.IsNull(), nil
		}
		return !cur.IsNull(), nil
	}
	// sub = END_OFFSET_PRECEDING (CURRENT ROW == offset 0 preceding);
	// less = true; flip both if DESC.
	sub := o.plan.Frame.EndKind != parser.FrameBoundOffsetFollowing
	less := true
	if desc {
		sub = !sub
		less = false
	}
	in, err := o.inRange(val, cur, o.frameEndOffDatum, sub, less)
	if err != nil {
		return false, err
	}
	// The scan advances while the row is still in range; it stops at the
	// first row that is NOT in range (the exclusive frame tail).
	return !in, nil
}

// inRange implements PostgreSQL's in_range(val, base, offset, sub, less):
// sum = base∓offset (sub subtracts), result = val<=sum (less) or val>=sum.
// offset is nil for a CURRENT ROW bound, where sum degenerates to base.
func (o *windowOp) inRange(val, base, offset Datum, sub, less bool) (bool, error) {
	sum := base
	if !offset.IsNull() {
		op := parser.OpAdd
		if sub {
			op = parser.OpSub
		}
		var err error
		sum, err = evalBinary(op, base, offset, o.rangePos(), nil)
		if err != nil {
			return false, err
		}
	}
	cmp, err := compareDatum(val, sum, o.rangePos())
	if err != nil {
		return false, err
	}
	if less {
		return cmp <= 0, nil
	}
	return cmp >= 0, nil
}

// rangePos returns a parse position for RANGE arithmetic/comparison error
// messages: the offset expression's position when present, else the single
// ORDER BY expression's, else 0.
func (o *windowOp) rangePos() int {
	if fr := o.plan.Frame; fr != nil {
		if fr.StartOffset != nil {
			return fr.StartOffset.Pos()
		}
		if fr.EndOffset != nil {
			return fr.EndOffset.Pos()
		}
	}
	if len(o.plan.OrderBy) > 0 {
		return o.plan.OrderBy[0].Expr.Pos()
	}
	return 0
}

// resolveRangeOffset evaluates a RANGE frame offset once, keeping its native
// type (numeric/float/interval), and enforces PG's non-null (22004) and
// non-negative (22013) checks.
func (o *windowOp) resolveRangeOffset(expr optimizer.Expr, label string) (Datum, error) {
	v, err := evalExpr(expr, nil, o.ctx)
	if err != nil {
		return Datum{}, err
	}
	if v.IsNull() {
		return Datum{}, &ExecError{Code: "22004", Pos: expr.Pos(), Message: fmt.Sprintf("frame %s offset must not be null", label)}
	}
	if rangeOffsetNegative(v) {
		return Datum{}, &ExecError{Code: "22013", Pos: expr.Pos(), Message: "invalid preceding or following size in window function"}
	}
	return v, nil
}

// rangeOffsetNegative reports whether a RANGE offset value is negative,
// which PostgreSQL rejects in every type's in_range function.
func rangeOffsetNegative(v Datum) bool {
	switch v.Kind {
	case KindInt:
		return v.Int < 0
	case KindNumeric:
		// float8/float4 offsets are also carried as KindNumeric in v0.
		z := numericFromInt(0)
		cmp, err := compareDatum(v, z, 0)
		return err == nil && cmp < 0
	case KindInterval:
		// PostgreSQL's in_range_interval_interval rejects the offset when
		// interval_sign(offset) < 0 — the sign of the *linear span*
		// span = time_micros + (months*30 + days)*USECS_PER_DAY — NOT the sign
		// of any individual component (timestamp.c: interval_cmp_value). So
		// '1 mon -10 days' (a +20-day span) is a valid, positive offset even
		// though its day field is negative. Mirror interval_cmp_value via the
		// same overflow-safe day/frac decomposition compareDatum already uses
		// for interval ordering: days carries the whole-day part, frac the
		// sub-day microsecond remainder, and the sign of the whole equals the
		// sign of days when nonzero (|frac| < usecsPerDay) else the sign of frac.
		days := int64(v.IntervalMonthsValue())*30 + int64(v.IntervalDaysValue()) + v.IntervalMicrosValue()/usecsPerDay
		frac := v.IntervalMicrosValue() % usecsPerDay
		if days != 0 {
			return days < 0
		}
		return frac < 0
	}
	return false
}

// frameBoundsGroups returns the absolute [start, end) row-index bounds
// of row i's GROUPS-mode window frame, given groupBounds — the
// partition's peer-group boundary array (ascending absolute
// group-start row indices plus a trailing partition-end sentinel, as
// returned by peerGroupBounds). GROUPS-mode bounds are counted in
// peer groups rather than rows (an offset PRECEDING/FOLLOWING moves N
// whole peer groups, and CURRENT ROW means the current row's own peer
// group), mirroring update_frameheadpos/update_frametailpos's GROUPS
// branch exactly — the row-index arithmetic above is otherwise
// identical, just performed on group indices and then translated back
// to row indices via groupBounds.
func (o *windowOp) frameBoundsGroups(i int, groupBounds []int) (int, int) {
	fr := o.plan.Frame
	numGroups := len(groupBounds) - 1
	curGroup := groupIndexOf(groupBounds, i)
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > numGroups {
			return numGroups
		}
		return v
	}
	var startG, endG int
	switch fr.StartKind {
	case parser.FrameBoundUnboundedPreceding:
		startG = 0
	case parser.FrameBoundCurrentRow:
		startG = curGroup
	case parser.FrameBoundOffsetPreceding:
		startG = clamp(curGroup - int(o.frameStartOff))
	case parser.FrameBoundOffsetFollowing:
		startG = clamp(curGroup + int(o.frameStartOff))
	}
	switch fr.EndKind {
	case parser.FrameBoundUnboundedFollowing:
		endG = numGroups
	case parser.FrameBoundCurrentRow:
		endG = curGroup + 1
	case parser.FrameBoundOffsetPreceding:
		endG = clamp(curGroup - int(o.frameEndOff) + 1)
	case parser.FrameBoundOffsetFollowing:
		endG = clamp(curGroup + int(o.frameEndOff) + 1)
	}
	if endG < startG {
		endG = startG
	}
	return groupBounds[startG], groupBounds[endG]
}

// groupIndexOf returns the index k of the peer group containing
// absolute row index i, given groupBounds as returned by
// peerGroupBounds (ascending group-start indices plus a trailing
// partition-end sentinel) — i.e. the k such that
// groupBounds[k] <= i < groupBounds[k+1].
func groupIndexOf(groupBounds []int, i int) int {
	return sort.Search(len(groupBounds)-1, func(k int) bool { return groupBounds[k+1] > i })
}

// peerBoundsOf returns the [start, end) peer-group bounds containing
// absolute row index i, given groupBounds as returned by
// peerGroupBounds (ascending group-start indices plus a trailing pEnd
// sentinel).
func peerBoundsOf(groupBounds []int, i int) (int, int) {
	k := groupIndexOf(groupBounds, i)
	return groupBounds[k], groupBounds[k+1]
}

// frameRowExcluded reports whether row k is excluded from row i's
// frame by an EXCLUDE clause, given i's peer-group bounds
// [peerStart, peerEnd) — mirrors nodeWindowAgg.c's row_is_in_frame
// exclusion check (frame bounds themselves are computed without
// regard to EXCLUDE; only membership within those bounds is filtered
// here).
func frameRowExcluded(excl parser.FrameExclusion, k, i, peerStart, peerEnd int) bool {
	switch excl {
	case parser.FrameExcludeCurrentRow:
		return k == i
	case parser.FrameExcludeGroup:
		return k >= peerStart && k < peerEnd
	case parser.FrameExcludeTies:
		return k != i && k >= peerStart && k < peerEnd
	default:
		return false
	}
}

// firstInFrame/lastInFrame/nthInFrame return the absolute row index of
// the first/last/nth row in [start, end) that isn't excluded by excl
// relative to current row i (peer bounds peerStart/peerEnd), or -1 if
// there is no such row (an all-excluded or empty frame — first_value/
// last_value/nth_value return NULL in that case).
func firstInFrame(start, end, i, peerStart, peerEnd int, excl parser.FrameExclusion) int {
	for k := start; k < end; k++ {
		if !frameRowExcluded(excl, k, i, peerStart, peerEnd) {
			return k
		}
	}
	return -1
}

func lastInFrame(start, end, i, peerStart, peerEnd int, excl parser.FrameExclusion) int {
	for k := end - 1; k >= start; k-- {
		if !frameRowExcluded(excl, k, i, peerStart, peerEnd) {
			return k
		}
	}
	return -1
}

func nthInFrame(start, end, i, peerStart, peerEnd int, excl parser.FrameExclusion, n int) int {
	count := 0
	for k := start; k < end; k++ {
		if frameRowExcluded(excl, k, i, peerStart, peerEnd) {
			continue
		}
		count++
		if count == n {
			return k
		}
	}
	return -1
}

func (o *windowOp) partitionKey(row Row) (string, error) {
	if len(o.plan.PartitionBy) == 0 {
		return "__all__", nil
	}
	parts := make([]string, 0, len(o.plan.PartitionBy))
	for _, pe := range o.plan.PartitionBy {
		v, err := evalExpr(pe, row, o.ctx)
		if err != nil {
			return "", err
		}
		parts = append(parts, datumKey(v))
	}
	return strings.Join(parts, "|"), nil
}

func (o *windowOp) samePeer(prev, cur Row) (bool, error) {
	if len(o.plan.OrderBy) == 0 {
		return true, nil
	}
	for _, ok := range o.plan.OrderBy {
		a, err := evalExpr(ok.Expr, prev, o.ctx)
		if err != nil {
			return false, err
		}
		b, err := evalExpr(ok.Expr, cur, o.ctx)
		if err != nil {
			return false, err
		}
		if a.IsNull() || b.IsNull() {
			if a.IsNull() && b.IsNull() {
				continue
			}
			return false, nil
		}
		cmp, err := compareDatum(a, b, ok.Expr.Pos())
		if err != nil {
			return false, err
		}
		if cmp != 0 {
			return false, nil
		}
	}
	return true, nil
}

// compareSortDatums compares two datums for sort purposes.
// nullsFirst determines whether NULLs sort before or after non-NULLs.
// The returned cmp is in "natural" order; the caller applies direction (desc).
// NULLs vs non-NULLs: the sign is chosen so that after applying the desc
// direction test (cmp < 0 for ASC, cmp > 0 for DESC) we get the correct
// "comes first" answer.
//
// Formula for NULL vs non-null:
//   cmp = 1  when nullsFirst == desc  (both true or both false)
//   cmp = -1 when nullsFirst != desc
func compareSortDatums(a, b Datum, pos int, desc bool, nullsFirst bool) (cmp int, decided bool, err error) {
	if a.IsNull() && !b.IsNull() {
		if nullsFirst == desc {
			return 1, true, nil
		}
		return -1, true, nil
	}
	if !a.IsNull() && b.IsNull() {
		if nullsFirst == desc {
			return -1, true, nil
		}
		return 1, true, nil
	}
	if a.IsNull() && b.IsNull() {
		return 0, false, nil
	}
	c, err := compareDatum(a, b, pos)
	if err != nil {
		return 0, false, err
	}
	if c == 0 {
		return 0, false, nil
	}
	return c, true, nil
}

func (o *windowOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return asSlot(o.schema, row), nil
}

func (o *windowOp) Close() error {
	o.rows = nil
	o.ctx = nil
	o.idx = 0
	return o.child.Close()
}
func (o *windowOp) Schema() optimizer.Schema { return o.schema }
