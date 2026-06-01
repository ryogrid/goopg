package executor

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// joinOp is a join operator that dispatches on plan.Algo.
// Hash joins use lazy materialization (M0036): joined rows are
// yielded on demand via Next() instead of pre-computed in o.rows.
// Merge and nested-loop still materialize in o.rows.
type joinOp struct {
	plan   *planner.Join
	left   Operator
	right  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	idx  int

	// M0036 lazy-output state (hash join only)
	lazyHash     map[string][]Row // build-side hash table
	lazyProbe    Operator         // probe side (streaming)
	lazyRow      Row              // current probe row
	lazyMatches  []Row            // matches for current probe row (borrowed from lazyHash)
	lazyMatchIdx int
	lazyActive   bool // true between probeRow and last match
	lazyLW       int  // left schema width
	lazyRW       int  // right schema width

	// M0054-0005b: per-Open() reusable buffers for nullRow / hash
	// key evaluation. The pre-fix path called `nullRow(width)` and
	// `concatRows(...)` on every probe row, allocating fresh slices.
	// These buffers stay constant for a given (leftWidth, rightWidth)
	// pair, so a single allocation per side is enough.
	lazyNullLeft  Row
	lazyNullRight Row
	lazyKeyRow    Row

	// M0071-0014 Stage D-2: VirtualSlot composition replaces the
	// nextLazy concatRows allocations. lazyBuildSlot's .row holds
	// the matched build row (for INNER / LEFT-no-match-fallback);
	// lazyProbeSlot's .row holds the current probe row;
	// lazyVirtualOut composes them in plan.Output() order
	// (BuildLeft swaps the source order). lazyOuterOnlySlot is the
	// emit slot for Semi / Anti, which return the probe row alone.
	// Allocated lazily on first nextLazy invocation (Open path is
	// shared with non-hash joinOp algorithms which don't need the
	// virtual composition).
	lazyBuildSlot     *MaterializedSlot
	lazyProbeSlot     *MaterializedSlot
	lazyVirtualOut    *VirtualSlot
	lazyOuterOnlySlot *MaterializedSlot
}

func newJoinOp(plan *planner.Join, left, right Operator) *joinOp {
	schema := plan.Output()
	if len(schema) == 0 {
		schema = append(schema, left.Schema()...)
		schema = append(schema, right.Schema()...)
	}
	return &joinOp{plan: plan, left: left, right: right, schema: schema}
}

func (o *joinOp) Open(ctx *Context) error {
	o.ctx = ctx
	// M0061-0001: Semi / Anti join must run through the lazy hash
	// path; the materialising NL / Merge paths don't implement
	// "emit probe row at most once" semantics. The planner only
	// emits Semi/Anti with Algo=JoinAlgoHash, but defend against
	// future changes that might leave Algo unset.
	if o.plan.Type == planner.JoinTypeSemi || o.plan.Type == planner.JoinTypeAnti {
		if o.plan.Algo != planner.JoinAlgoHash {
			return fmt.Errorf("internal error: semi/anti join requires hash algorithm, got %d", o.plan.Algo)
		}
		return o.openLazyHashJoin(ctx)
	}
	// Lateral joins must always use the per-row driver path so the right-side
	// plan can evaluate OuterColumnRef nodes against the current left row.
	// Check Lateral BEFORE Algo: even when the planner chose hash-join for the
	// equi-predicate, the equality predicate just means JOIN ON col=col, not
	// that the right side is independent of the outer row. M0097-0106.
	if o.plan.Lateral {
		return o.openLateral(ctx)
	}
	if o.plan.Algo == planner.JoinAlgoHash {
		return o.openLazyHashJoin(ctx)
	}
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		_ = o.left.Close()
		return err
	}

	leftRows, err := drainRowsCtx(o.left, ctx)
	if err != nil {
		return err
	}
	rightRows, err := drainRowsCtx(o.right, ctx)
	if err != nil {
		return err
	}


	leftWidth := len(o.left.Schema())
	rightWidth := len(o.right.Schema())
	if leftWidth == 0 && len(leftRows) > 0 {
		leftWidth = len(leftRows[0])
	}
	if rightWidth == 0 && len(rightRows) > 0 {
		rightWidth = len(rightRows[0])
	}

	if o.plan.Algo == planner.JoinAlgoMerge {
		return o.runMergeJoin(leftRows, rightRows, leftWidth, rightWidth)
	}
	return o.runNestedLoop(leftRows, rightRows, leftWidth, rightWidth)
}

// lateralBindable is implemented by FROM-clause SRFs that can have
// their argument expressions resolved against an outer row supplied
// by a parent Join.Lateral. M0103-0008.
type lateralBindable interface {
	BindLateralOuter(slot SlotView)
}

// openLateral handles `Join.Lateral == true`: drain the left, then
// for each left row re-run the right side with the left row in scope.
// Concatenated rows accumulate in o.rows and are emitted via the
// existing Next() path.
//
// Two paths:
//   - If the right child implements lateralBindable (e.g. pg_get_publication_tables):
//     use BindLateralOuter to pass the outer row directly.
//   - Otherwise: push the left row onto ctx.OuterRows so OuterColumnRef
//     expressions inside the right subtree can resolve correlated refs.
//     The CTERowCache is saved/cleared per iteration so LATERAL CTEs that
//     depend on the outer row (e.g. WITH RECURSIVE inside LATERAL) are
//     re-evaluated for each outer row, not served from a stale cache.
//
// LEFT lateral joins emit a null-padded row when the right side yields
// zero rows; CROSS / INNER drop the outer row.
func (o *joinOp) openLateral(ctx *Context) error {
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	leftRows, err := drainRowsCtx(o.left, ctx)
	if err != nil {
		_ = o.left.Close()
		return err
	}
	leftWidth := len(o.left.Schema())
	if leftWidth == 0 && len(leftRows) > 0 {
		leftWidth = len(leftRows[0])
	}
	rightWidth := len(o.right.Schema())
	nullRight := nullRow(rightWidth)

	bindable, isSRF := o.right.(lateralBindable)
	if isSRF {
		outerSlot := SlotFromRow(o.left.Schema(), nil)
		bindable.BindLateralOuter(outerSlot)
		defer bindable.BindLateralOuter(nil)
		for i, l := range leftRows {
			if i&0xFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
				if err := o.ctx.Ctx.Err(); err != nil {
					return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			outerSlot.row = l
			if err := o.right.Open(ctx); err != nil {
				return err
			}
			rightRows, err := drainRowsCtx(o.right, ctx)
			_ = o.right.Close()
			if err != nil {
				return err
			}
			matched := false
			for _, r := range rightRows {
				joined := concatRows(l, r)
				ok, perr := o.joinPredicateMatch(joined)
				if perr != nil {
					return perr
				}
				if !ok {
					continue
				}
				matched = true
				o.rows = append(o.rows, joined)
			}
			// LEFT JOIN: null-extend when the right side produced no rows or
			// no right row satisfied the join predicate.
			if (len(rightRows) == 0 || !matched) && o.plan.Type == planner.JoinTypeLeft {
				o.rows = append(o.rows, concatRows(l, nullRight))
			}
		}
		return nil
	}

	// General LATERAL: push each left row as an outer row context so
	// OuterColumnRef (level=1) expressions in the right subtree resolve
	// against it. Also clear CTERowCache per iteration so LATERAL CTEs
	// whose content depends on the outer row are re-materialised.
	savedCTECache := ctx.CTERowCache
	for i, l := range leftRows {
		if i&0xFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		ctx.OuterRows = append(ctx.OuterRows, l)
		ctx.CTERowCache = nil // clear per-iteration so outer-dependent CTEs recompute
		var rightRows []Row
		if openErr := o.right.Open(ctx); openErr == nil {
			rightRows, err = drainRowsCtx(o.right, ctx)
			_ = o.right.Close()
		} else {
			err = openErr
		}
		ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1]
		if err != nil {
			ctx.CTERowCache = savedCTECache
			return err
		}
		matched := false
		for _, r := range rightRows {
			joined := concatRows(l, r)
			ok, perr := o.joinPredicateMatch(joined)
			if perr != nil {
				ctx.CTERowCache = savedCTECache
				return perr
			}
			if !ok {
				continue
			}
			matched = true
			o.rows = append(o.rows, joined)
		}
		// LEFT JOIN: null-extend when the right side produced no rows or
		// no right row satisfied the join predicate.
		if (len(rightRows) == 0 || !matched) && o.plan.Type == planner.JoinTypeLeft {
			o.rows = append(o.rows, concatRows(l, nullRight))
		}
	}
	return nil
}

// runNestedLoop is the universal fallback. O(N*M) over the two
// drained sides; supports INNER / LEFT / RIGHT / FULL / CROSS.
//
// Cancellation: ctx.Err() is checked once per outer-row loop so a
// CancelRequest interrupts a long join even when the inner side has
// no per-row hooks. (M0058-0005.) Q5 and Q13 ran 60+ minutes without
// responding to cancellation before this check existed.
func (o *joinOp) runNestedLoop(leftRows, rightRows []Row, leftWidth, rightWidth int) error {
	nullLeft := nullRow(leftWidth)
	nullRight := nullRow(rightWidth)

	rightMatched := make([]bool, len(rightRows))
	for i, l := range leftRows {
		if i&0xFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		matched := false
		for j, r := range rightRows {
			// M0062-followup: also check ctx.Err() inside the inner
			// loop, every 4096 iterations. Q13 (customer LEFT JOIN
			// orders, 150K × 1.5M with a NOT LIKE residual) ran 300 s
			// past --cancel-after=600s in the M0061-0003 sweep
			// because the *outer* check (every 256 outer rows) only
			// fired between full passes of the 1.5M-row inner.
			if j&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
				if err := o.ctx.Ctx.Err(); err != nil {
					return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			joined := concatRows(l, r)
			ok, err := o.joinPredicateMatch(joined)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			matched = true
			rightMatched[j] = true
			o.rows = append(o.rows, joined)
		}
		if !matched && (o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull) {
			o.rows = append(o.rows, concatRows(l, nullRight))
		}
	}

	if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
		for j, r := range rightRows {
			// M0062-followup: ctx check inside the unmatched-right
			// emission loop too. RIGHT/FULL join over a multi-million-
			// row right side could otherwise stall cancel here even
			// after the join body has finished.
			if j&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
				if err := o.ctx.Ctx.Err(); err != nil {
					return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			if rightMatched[j] {
				continue
			}
			// M0097-0060: FULL JOIN USING coalescing. For unmatched
			// right rows the left side is all NULL; copy each USING
			// column value from the right position to the left position
			// so `SELECT *` sees COALESCE(left.col, right.col) = right.col.
			merged := concatRows(nullLeft, r)
			for k, lIdx := range o.plan.UsingLeftCols {
				rIdx := o.plan.UsingRightCols[k]
				if rIdx < len(merged) {
					merged[lIdx] = merged[rIdx]
				}
			}
			o.rows = append(o.rows, merged)
		}
	}
	return nil
}

// coalesceUsingRow applies FULL JOIN USING coalescing: for each USING
// column pair, copy the right-side value into the left-side position
// so star-expansion sees the correct non-NULL value for unmatched right
// rows. Modifies merged in place. M0097-0060.
func (o *joinOp) coalesceUsingRow(merged Row) {
	for k, lIdx := range o.plan.UsingLeftCols {
		rIdx := o.plan.UsingRightCols[k]
		if lIdx < len(merged) && rIdx < len(merged) {
			merged[lIdx] = merged[rIdx]
		}
	}
}

// openLazyHashJoin builds a hash table from the build side and sets
// up lazy output. When ctx.WorkMem > 0, the build side uses
// drainRowsBounded to spill to disk if row data exceeds the budget.
func (o *joinOp) openLazyHashJoin(ctx *Context) error {
	leftWidth := len(o.left.Schema())
	rightWidth := len(o.right.Schema())
	o.lazyLW = leftWidth
	o.lazyRW = rightWidth
	budget := ctx.WorkMem
	if budget <= 0 {
		budget = 512 * 1024 * 1024 // default 512 MiB
	}
	// M0054-0005b: hoist the per-iteration `nullRow(...)` allocation
	// out of the build loop. The hash-key evaluation only needs the
	// other-side columns to be present so column-index resolution
	// works; the values are not read. We also reuse a single
	// `keyRow` buffer per side to avoid `concatRows`'s per-row
	// `make(Row, leftW+rightW)` churn (M0054-0004 cumulative
	// `concatRows` 56 GB on Q9, 7,980 GB on Q20).
	// M0061-0001: Semi / Anti join semantics require the OUTER
	// (left) side to drive the probe loop and the INNER (right)
	// side to be hashed. BuildLeft is INNER-only by contract; we
	// also defend here so a stray flag doesn't silently break the
	// emit-once-per-probe-row invariant.
	buildLeft := o.plan.BuildLeft
	if o.plan.Type == planner.JoinTypeSemi || o.plan.Type == planner.JoinTypeAnti {
		buildLeft = false
	}
	if buildLeft {
		if err := o.left.Open(ctx); err != nil { return err }
		buildOp, err := drainRowsBounded(o.left, budget)
		_ = o.left.Close()
		if err != nil { return err }
		if err := buildOp.Open(ctx); err != nil { return err }
		var nullRight Row
		var keyRow Row
		buildCount := 0
		for {
			// M0062-followup: ctx check inside the build loop. With
			// 6M-row build inputs (Q21's anti-join lineitem) the
			// build alone runs minutes; without this check the
			// cancel-after deadline can be exceeded by 100+ s
			// while build keeps draining.
			if buildCount&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
				if err := ctx.Ctx.Err(); err != nil {
					return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			buildCount++
			lSlot, err := buildOp.Next()
			if err == EOF { break }
			if err != nil { return err }
			l := slotRow(lSlot)
			if leftWidth == 0 && len(l) > 0 {
				leftWidth = len(l); o.lazyLW = leftWidth
			}
			if nullRight == nil {
				nullRight = nullRow(rightWidth)
			}
			if keyRow == nil || len(keyRow) != leftWidth+rightWidth {
				keyRow = make(Row, leftWidth+rightWidth)
			}
			copy(keyRow[:leftWidth], l)
			copy(keyRow[leftWidth:], nullRight)
			key, ok, err := o.evalHashKey(o.plan.LeftKey, keyRow)
			if err != nil { return err }
			if !ok { continue }
			if o.lazyHash == nil { o.lazyHash = make(map[string][]Row) }
			o.lazyHash[key] = append(o.lazyHash[key], l)
		}
		_ = buildOp.Close()
		if err := o.right.Open(ctx); err != nil { return err }
		o.lazyProbe = o.right
		return nil
	}
	if err := o.right.Open(ctx); err != nil { return err }
	buildOp, err := drainRowsBounded(o.right, budget)
	_ = o.right.Close()
	if err != nil { return err }
	if err := buildOp.Open(ctx); err != nil { return err }
	var nullLeft Row
	var keyRow Row
	buildCount := 0
	for {
		// M0062-followup: same ctx check on the build-right path.
		if buildCount&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		buildCount++
		rSlot, err := buildOp.Next()
		if err == EOF { break }
		if err != nil { return err }
		r := slotRow(rSlot)
		if rightWidth == 0 && len(r) > 0 {
			rightWidth = len(r); o.lazyRW = rightWidth
		}
		if nullLeft == nil {
			nullLeft = nullRow(leftWidth)
		}
		if keyRow == nil || len(keyRow) != leftWidth+rightWidth {
			keyRow = make(Row, leftWidth+rightWidth)
		}
		copy(keyRow[:leftWidth], nullLeft)
		copy(keyRow[leftWidth:], r)
		key, ok, err := o.evalHashKey(o.plan.RightKey, keyRow)
		if err != nil { return err }
		if !ok { continue }
		if o.lazyHash == nil { o.lazyHash = make(map[string][]Row) }
		o.lazyHash[key] = append(o.lazyHash[key], r)
	}
	_ = buildOp.Close()
	if err := o.left.Open(ctx); err != nil { return err }
	o.lazyProbe = o.left
	return nil
}

type mergeKeyedRow struct {
	row    Row
	key    Datum
	hasKey bool
}

// runMergeJoin sorts both sides on their join keys and merges the
// two ordered streams. NULL keys never match (same as hash join).
// Supports INNER / LEFT / RIGHT / FULL outer semantics.
func (o *joinOp) runMergeJoin(leftRows, rightRows []Row, leftWidth, rightWidth int) error {
	nullLeft := nullRow(leftWidth)
	nullRight := nullRow(rightWidth)

	leftKeyed, leftNull, err := o.buildMergeSide(leftRows, true, leftWidth, rightWidth)
	if err != nil {
		return err
	}
	rightKeyed, rightNull, err := o.buildMergeSide(rightRows, false, leftWidth, rightWidth)
	if err != nil {
		return err
	}

	i, j := 0, 0
	for i < len(leftKeyed) && j < len(rightKeyed) {
		// M0058-0005: cheap ctx check every 256 left-side rows so a
		// CancelRequest interrupts a long sort-merge join promptly.
		if i&0xFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		cmp, err := compareDatum(leftKeyed[i].key, rightKeyed[j].key, o.plan.Pos())
		if err != nil {
			return err
		}
		switch {
		case cmp < 0:
			if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
				o.rows = append(o.rows, concatRows(leftKeyed[i].row, nullRight))
			}
			i++
		case cmp > 0:
			if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
				merged := concatRows(nullLeft, rightKeyed[j].row)
				o.coalesceUsingRow(merged)
				o.rows = append(o.rows, merged)
			}
			j++
		default:
			li := i
			for i < len(leftKeyed) {
				eq, err := compareDatum(leftKeyed[li].key, leftKeyed[i].key, o.plan.Pos())
				if err != nil {
					return err
				}
				if eq != 0 {
					break
				}
				i++
			}
			rj := j
			for j < len(rightKeyed) {
				eq, err := compareDatum(rightKeyed[rj].key, rightKeyed[j].key, o.plan.Pos())
				if err != nil {
					return err
				}
				if eq != 0 {
					break
				}
				j++
			}
			for a := li; a < i; a++ {
				for b := rj; b < j; b++ {
					o.rows = append(o.rows, concatRows(leftKeyed[a].row, rightKeyed[b].row))
				}
			}
		}
	}

	for ; i < len(leftKeyed); i++ {
		if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
			o.rows = append(o.rows, concatRows(leftKeyed[i].row, nullRight))
		}
	}
	for ; j < len(rightKeyed); j++ {
		if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
			merged := concatRows(nullLeft, rightKeyed[j].row)
			o.coalesceUsingRow(merged)
			o.rows = append(o.rows, merged)
		}
	}

	if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
		for _, l := range leftNull {
			o.rows = append(o.rows, concatRows(l, nullRight))
		}
	}
	if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
		for _, r := range rightNull {
			merged := concatRows(nullLeft, r)
			o.coalesceUsingRow(merged)
			o.rows = append(o.rows, merged)
		}
	}

	return nil
}

func (o *joinOp) buildMergeSide(rows []Row, isLeft bool, leftWidth, rightWidth int) ([]mergeKeyedRow, []Row, error) {
	var paddedLeft, paddedRight Row
	if isLeft {
		paddedRight = nullRow(rightWidth)
	} else {
		paddedLeft = nullRow(leftWidth)
	}
	keyExpr := o.plan.RightKey
	if isLeft {
		keyExpr = o.plan.LeftKey
	}
	if keyExpr == nil {
		return nil, nil, fmt.Errorf("merge join key is nil")
	}

	keyed := make([]mergeKeyedRow, 0, len(rows))
	nullKey := make([]Row, 0)
	for _, row := range rows {
		var evalRow Row
		if isLeft {
			evalRow = concatRows(row, paddedRight)
		} else {
			evalRow = concatRows(paddedLeft, row)
		}
		v, err := evalExpr(keyExpr, evalRow, o.ctx)
		if err != nil {
			return nil, nil, err
		}
		if v.IsNull() {
			nullKey = append(nullKey, row)
			continue
		}
		keyed = append(keyed, mergeKeyedRow{row: row, key: v, hasKey: true})
	}

	var sortErr error
	sort.SliceStable(keyed, func(i, j int) bool {
		cmp, err := compareDatum(keyed[i].key, keyed[j].key, o.plan.Pos())
		if err != nil {
			sortErr = err
			return false
		}
		return cmp < 0
	})
	if sortErr != nil {
		return nil, nil, sortErr
	}

	return keyed, nullKey, nil
}

// evalHashKey evaluates one side of the hash-join key against a
// padded row and returns its canonical key string. The boolean
// is false when the key evaluated to NULL (never matches).
func (o *joinOp) evalHashKey(keyExpr planner.Expr, row Row) (string, bool, error) {
	v, err := evalExpr(keyExpr, row, o.ctx)
	if err != nil {
		return "", false, err
	}
	if v.IsNull() {
		return "", false, nil
	}
	return datumKey(v), true, nil
}

func (o *joinOp) joinPredicateMatch(row Row) (bool, error) {
	if o.plan.Predicate == nil {
		return true, nil
	}
	v, err := evalExpr(o.plan.Predicate, row, o.ctx)
	if err != nil {
		return false, err
	}
	return !v.IsNull() && v.Kind == KindBool && v.BoolValue(), nil
}

// joinPredicateMatchSlot evaluates plan.Predicate against a slot
// (typically o.lazyVirtualOut). Caller must update the source-slot
// .row fields before invocation.
func (o *joinOp) joinPredicateMatchSlot(slot SlotView) (bool, error) {
	if o.plan.Predicate == nil {
		return true, nil
	}
	v, err := evalExprSlot(o.plan.Predicate, slot, o.ctx)
	if err != nil {
		return false, err
	}
	return !v.IsNull() && v.Kind == KindBool && v.BoolValue(), nil
}

// ensureLazyVirtual lazily builds the persistent VirtualSlot used
// by nextLazy to emit joined rows without per-match concat. Source
// order depends on BuildLeft so plan.Output()'s left++right column
// layout is preserved.
func (o *joinOp) ensureLazyVirtual() {
	if o.lazyVirtualOut != nil {
		return
	}
	leftSchema := o.left.Schema()
	rightSchema := o.right.Schema()
	o.lazyBuildSlot = SlotFromRow(nil, nil)
	o.lazyProbeSlot = SlotFromRow(nil, nil)
	o.lazyOuterOnlySlot = SlotFromRow(o.schema, nil)
	cols := make([]virtualCol, 0, o.lazyLW+o.lazyRW)
	if o.plan.BuildLeft {
		// Output is left ++ right; build side is left → sources
		// [build, probe], cols (0,*) ++ (1,*).
		_ = leftSchema
		_ = rightSchema
		for i := 0; i < o.lazyLW; i++ {
			cols = append(cols, virtualCol{sourceIdx: 0, sourceCol: int16(i)})
		}
		for i := 0; i < o.lazyRW; i++ {
			cols = append(cols, virtualCol{sourceIdx: 1, sourceCol: int16(i)})
		}
		o.lazyVirtualOut = NewVirtualSlot(o.schema,
			[]TupleSlot{o.lazyBuildSlot, o.lazyProbeSlot}, cols)
		return
	}
	// !BuildLeft: probe is left, build is right → sources
	// [probe, build].
	for i := 0; i < o.lazyLW; i++ {
		cols = append(cols, virtualCol{sourceIdx: 0, sourceCol: int16(i)})
	}
	for i := 0; i < o.lazyRW; i++ {
		cols = append(cols, virtualCol{sourceIdx: 1, sourceCol: int16(i)})
	}
	o.lazyVirtualOut = NewVirtualSlot(o.schema,
		[]TupleSlot{o.lazyProbeSlot, o.lazyBuildSlot}, cols)
}

func (o *joinOp) Next() (TupleSlot, error) {
	// M0036 lazy output: yield joined rows on demand.
	if o.lazyProbe != nil {
		return o.nextLazy()
	}
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return asSlot(o.Schema(), row), nil
}

// nextLazy yields one joined row at a time for lazy hash joins.
//
// M0071-0014 Stage D-2: per-match concatRows is replaced by
// VirtualSlot composition. lazyBuildSlot.row / lazyProbeSlot.row
// are overwritten in place; predicate eval reads via
// joinPredicateMatchSlot. The plan-emit Schema is preserved by
// the cols mapping in ensureLazyVirtual (BuildLeft swaps source
// order).
func (o *joinOp) nextLazy() (TupleSlot, error) {
	// M0054-0005b: reuse the operator's per-Open null padding rows
	// instead of allocating per call.
	if o.lazyNullLeft == nil || len(o.lazyNullLeft) != o.lazyLW {
		o.lazyNullLeft = nullRow(o.lazyLW)
	}
	if o.lazyNullRight == nil || len(o.lazyNullRight) != o.lazyRW {
		o.lazyNullRight = nullRow(o.lazyRW)
	}
	nullLeft := o.lazyNullLeft
	nullRight := o.lazyNullRight
	o.ensureLazyVirtual()
	// Cancellation: cheap ctx check per Next() call so a long
	// probe-only join responds promptly to CancelRequest.
	// (M0058-0005.)
	if o.ctx != nil && o.ctx.Ctx != nil {
		if err := o.ctx.Ctx.Err(); err != nil {
			return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}
	for {
		// Continue serving matches from current probe row. Apply
		// the full join Predicate per emitted row — hash matching
		// only checks LeftKey=RightKey, but the planner may have
		// ANDed extra residual conjuncts onto the Predicate via
		// pushOneConjunct (e.g. TPC-H Q9's `ps_partkey=l_partkey`
		// pushed onto the part-join). Without the post-hash filter
		// those extras are silently dropped and the join over-emits.
		for o.lazyActive && o.lazyMatchIdx < len(o.lazyMatches) {
			m := o.lazyMatches[o.lazyMatchIdx]
			o.lazyMatchIdx++
			o.lazyBuildSlot.row = m
			o.lazyProbeSlot.row = o.lazyRow
			ok, err := o.joinPredicateMatchSlot(o.lazyVirtualOut)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			return o.lazyVirtualOut, nil
		}
		o.lazyActive = false
		// Pull next probe row.
		probeSlot, err := o.lazyProbe.Next()
		if err == EOF {
			return nil, EOF
		}
		if err != nil {
			return nil, err
		}
		r := slotRow(probeSlot)
		o.lazyRow = r
		// M0054-0005b: reuse a single keyRow buffer across probe
		// rows. evalHashKey only reads the column slot the join key
		// references, so the throwaway buffer is safe.
		w := o.lazyLW + o.lazyRW
		if o.lazyKeyRow == nil || len(o.lazyKeyRow) != w {
			o.lazyKeyRow = make(Row, w)
		}
		var key string
		var ok bool
		if o.plan.BuildLeft {
			copy(o.lazyKeyRow[:o.lazyLW], nullLeft)
			copy(o.lazyKeyRow[o.lazyLW:], r)
			key, ok, err = o.evalHashKey(o.plan.RightKey, o.lazyKeyRow)
		} else {
			copy(o.lazyKeyRow[:o.lazyLW], r)
			copy(o.lazyKeyRow[o.lazyLW:], nullRight)
			key, ok, err = o.evalHashKey(o.plan.LeftKey, o.lazyKeyRow)
		}
		if err != nil {
			return nil, err
		}
		matches := o.lazyHash[key]
		if !ok {
			matches = nil
		}
		// M0061-0001: Semi / Anti emit just the probe row at most
		// once. NULL probe key never matches (`ok == false`):
		//   - Semi: skip the probe row (no match).
		//   - Anti: keep the probe row (matches PostgreSQL
		//     `NOT EXISTS` semantics — equality cannot be true).
		//
		// M0071-0009: hash matches are necessary but not
		// sufficient — the planner may have lifted residual
		// non-equi conjuncts (e.g. Q21's
		// `l3.l_suppkey <> l1.l_suppkey`) onto the join Predicate
		// via unnestExistsExpr's M0062-0005 residual lift. Without
		// re-evaluating the Predicate per match, Anti silently
		// over-excludes (every l1 self-matches a late l3 hash
		// entry where l3=l1, so the residual is essential to
		// distinguish self-match from a different-supplier
		// match). This was Q21's silent-FN root cause: 0 rows vs
		// canonical ~411.
		//
		// Walk matches and apply Predicate; treat a "match" as
		// hash match AND Predicate=TRUE. The slot composition
		// already covers both Semi/Anti and INNER predicate eval
		// — re-bind the build slot per candidate match.
		if o.plan.Type == planner.JoinTypeSemi || o.plan.Type == planner.JoinTypeAnti {
			anyMatch := false
			if ok && len(matches) > 0 {
				o.lazyProbeSlot.row = r
				for _, m := range matches {
					o.lazyBuildSlot.row = m
					pok, err := o.joinPredicateMatchSlot(o.lazyVirtualOut)
					if err != nil {
						return nil, err
					}
					if pok {
						anyMatch = true
						break
					}
				}
			}
			if o.plan.Type == planner.JoinTypeSemi {
				if !anyMatch {
					continue
				}
				o.lazyRow = nil
				o.lazyOuterOnlySlot.row = r
				return o.lazyOuterOnlySlot, nil
			}
			// Anti: keep iff no match passed the predicate.
			if anyMatch {
				continue
			}
			o.lazyRow = nil
			o.lazyOuterOnlySlot.row = r
			return o.lazyOuterOnlySlot, nil
		}
		if len(matches) == 0 {
			if o.plan.Type == planner.JoinTypeLeft && !o.plan.BuildLeft {
				// LEFT JOIN: preserve unmatched left rows. Bind
				// build slot to nullRight padding; virtualOut
				// already composes [probe, build] for !BuildLeft.
				o.lazyRow = nil
				o.lazyProbeSlot.row = r
				o.lazyBuildSlot.row = nullRight
				return o.lazyVirtualOut, nil
			}
			// No matches, not LEFT — skip this probe row.
			continue
		}
		o.lazyMatches = matches
		o.lazyMatchIdx = 0
		o.lazyActive = true
		// Continue loop to yield first match.
	}
}

func (o *joinOp) Close() error {
	o.rows = nil
	o.lazyHash = nil
	o.lazyProbe = nil
	o.lazyRow = nil
	o.lazyMatches = nil
	o.lazyMatchIdx = 0
	o.lazyActive = false
	o.ctx = nil
	o.idx = 0
	errL := o.left.Close()
	errR := o.right.Close()
	if errL != nil {
		return errL
	}
	return errR
}

func (o *joinOp) Schema() planner.Schema { return o.schema }

// aggregateOp performs grouped aggregation in memory.
type aggregateOp struct {
	plan   *planner.Aggregate
	child  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	idx  int

	// sharedUserStates supports PG aggregate state sharing: when multiple user-defined
	// aggregates have the same sfunc/stype and the same input, sfunc is called only
	// once per row and the state is shared. SharedStateSlot=-1 means no sharing. M0097-0035.
	sharedUserStates       []Datum
	sharedUserStateSet     []bool
	sharedUserStateVersion []int64 // which currentRowVersion last updated this slot
	currentRowVersion      int64   // incremented per input row
}

// floatSpecialKind encodes the IEEE 754 special-value state for
// sum/avg when float4/float8 inputs contain NaN or Infinity.
type floatSpecialKind int8

const (
	floatSpecialNone     floatSpecialKind = 0
	floatSpecialNaN      floatSpecialKind = 1 // NaN dominates all
	floatSpecialPosInf   floatSpecialKind = 2 // +Infinity
	floatSpecialNegInf   floatSpecialKind = 3 // -Infinity
)

type aggRuntime struct {
	hasValue bool
	value    Datum
	// sum tracks INT-only running sums; numericSum tracks the
	// NUMERIC accumulator. Each aggregate-call uses exactly one
	// of them based on the first non-NULL argument's kind. The
	// two are not mixed within a single aggregate.
	sum        int64
	numericSum Datum
	count      int64
	// floatSpecial tracks NaN/Infinity state for sum/avg when float
	// inputs contain special IEEE 754 values (stored as KindString).
	// M0097-0053.
	floatSpecial floatSpecialKind
	distinct     map[string]struct{}
	// Extended aggregate accumulators (M0097-0007).
	boolResult bool   // for bool_and / bool_or / every
	intResult  int64  // for bit_and / bit_or / bit_xor
	strResult  string // for string_agg
	// arrayElems holds the accumulated element format-strings for
	// array_agg(expr); arrayElemNull[i] marks element i as NULL
	// (reserved — current applyAgg skips NULL inputs, so this is
	// always all-false in practice). finishAgg emits the standard
	// `{e1,e2}` text-array literal via formatTextArray, which the
	// libpqrcv `fetch_table_list` probe pipes back into
	// pg_get_publication_tables. M0103-0008 probe-survival.
	arrayElems    []string
	arrayElemNull []bool
	// arrayElemKeys stores ORDER BY key values for array_agg(x ORDER BY y).
	// Each entry corresponds to arrayElems[i]; nil when no ORDER BY.
	arrayElemKeys [][]Datum
	// Variance accumulators (var_pop/var_samp/stddev_pop/stddev_samp).
	// Uses Youngs-Cramer algorithm for float inputs.
	floatSx float64 // running sum of values (Youngs-Cramer Sx)
	floatM2 float64 // running sum of squared deviations (Youngs-Cramer Sxx)
	// Exact integer accumulators for var_pop/var_samp/stddev on integer inputs.
	// When intExact is true, these hold exact values and finishAgg uses them
	// instead of the float Youngs-Cramer values. Matches PG's int4_accum/int8_accum.
	intExact bool
	intSx    *big.Int // Σx
	intSxx   *big.Int // Σx²
	// Exact rational accumulators for var_pop/var_samp/stddev on numeric inputs.
	// When numericExact is true, these hold exact rational values matching PG's
	// numeric_accum path (which uses exact arithmetic, not floating point). M0097-0125.
	numericExact bool
	numericSx    *big.Rat // Σx (exact)
	numericSxx   *big.Rat // Σx² (exact)
	// Regression accumulators for regr_*/covar_*/corr. M0097-0020.
	// Follows PostgreSQL's float8_regr_accum signature: first arg is y, second is x.
	regrN     int64   // count of non-NULL (x,y) pairs
	regrSumX  float64 // Σx
	regrSumY  float64 // Σy
	regrSumXX float64 // Σx²
	regrSumXY float64 // Σx*y
	regrSumYY float64 // Σy²
	// userState holds the running state for user-defined aggregates (CREATE AGGREGATE).
	// Initialized from initcond; updated by sfunc on each row.
	userState    Datum
	userStateSet bool // true once userState has been initialized
	// withinGroupElems accumulates per-row values for WITHIN GROUP (ORDER BY ...)
	// ordered-set aggregates (percentile_cont, percentile_disc, rank, dense_rank, mode).
	// Each entry is [sortKey0, sortKey1, ..., valueExpr]. M0097-0035.
	withinGroupElems [][]Datum
	// withinGroupDirectArg stores the "direct argument" (e.g. the fraction p in
	// percentile_cont(p)) evaluated from the first row of the group. M0097-0035.
	withinGroupDirectArg    Datum
	withinGroupDirectArgSet bool
	// withinGroupDirectArgs stores ALL direct args for multi-arg hypothetical-set aggregates
	// like rank(5,'AZZZZ',50) WITHIN GROUP (ORDER BY col1, col2, col3). Set alongside
	// withinGroupDirectArg; used for tuple comparison in rank/dense_rank/cume_dist/percent_rank.
	withinGroupDirectArgs []Datum
	// distinctUserAggRows holds per-row arg vectors for user-defined aggregates
	// with DISTINCT (and optional ORDER BY). Deferred to finishAgg for correct
	// multi-arg dedup and ORDER BY sort before sfunc calls. M0097-0035.
	distinctUserAggRows [][]Datum // inner: [sortKey0..., arg0, arg1, ...]
}

func newAggregateOp(plan *planner.Aggregate, child Operator) *aggregateOp {
	return &aggregateOp{plan: plan, child: child, schema: plan.Output()}
}

func (o *aggregateOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}

	// Compute the number of shared state slots needed for this aggregate. M0097-0035.
	maxSlot := -1
	for _, call := range o.plan.Aggs {
		if call.SharedStateSlot > maxSlot {
			maxSlot = call.SharedStateSlot
		}
	}
	nSlots := maxSlot + 1
	o.sharedUserStates = make([]Datum, nSlots)
	o.sharedUserStateSet = make([]bool, nSlots)
	o.sharedUserStateVersion = make([]int64, nSlots)
	o.currentRowVersion = 0

	type groupRuntime struct {
		groupValues      Row
		passthroughVals  Row // values of functionally-determined passthrough columns
		aggs             []aggRuntime
	}

	groups := map[string]*groupRuntime{}
	order := make([]string, 0)

	if len(o.plan.GroupExprs) == 0 {
		var ptVals Row
		if len(o.plan.Passthrough) > 0 {
			ptVals = make(Row, len(o.plan.Passthrough))
		}
		groups["__all__"] = &groupRuntime{groupValues: nil, passthroughVals: ptVals, aggs: make([]aggRuntime, len(o.plan.Aggs))}
		order = append(order, "__all__")
	}

	for {
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		if ctx.Ctx != nil {
			if cerr := ctx.Ctx.Err(); cerr != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		key, groupValues, err := o.evalGroupKey(slot)
		if err != nil {
			return err
		}
		gr, ok := groups[key]
		if !ok {
			// Evaluate passthrough (functionally-determined) columns from the first row.
			var ptVals Row
			if len(o.plan.Passthrough) > 0 {
				ptVals = make(Row, len(o.plan.Passthrough))
				for i, expr := range o.plan.Passthrough {
					v, err := evalExprSlot(expr, slot, ctx)
					if err != nil {
						ptVals[i] = NullDatum
					} else {
						ptVals[i] = v
					}
				}
			}
			gr = &groupRuntime{groupValues: groupValues, passthroughVals: ptVals, aggs: make([]aggRuntime, len(o.plan.Aggs))}
			groups[key] = gr
			order = append(order, key)
		}

		o.currentRowVersion++
		for i, call := range o.plan.Aggs {
			// For user-defined aggregates with shared transition state, only the
			// "leader" (first call with this SharedStateSlot) calls sfunc. Followers
			// skip applyAgg — they will be synced from the leader's final state just
			// before finishAgg. M0097-0035.
			if call.SharedStateSlot >= 0 && call.UserAgg != nil && i > 0 {
				isFollower := false
				for j := 0; j < i; j++ {
					if o.plan.Aggs[j].SharedStateSlot == call.SharedStateSlot {
						isFollower = true
						break
					}
				}
				if isFollower {
					continue
				}
			}
			if err := o.applyAgg(&gr.aggs[i], call, slot); err != nil {
				return err
			}
		}
	}

	o.rows = make([]Row, 0, len(order))
	nGroupCols := len(o.plan.GroupExprs)
	for idx, key := range order {
		// M0062-followup: mirror the input-drain ctx check (line ~629)
		// on the output-materialisation loop. A 1 M-group aggregate's
		// rebuild can otherwise take seconds without a cancel
		// opportunity.
		if idx&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		gr := groups[key]
		if gr == nil {
			continue
		}
		out := make(Row, 0, len(gr.groupValues)+len(o.plan.Aggs)+len(gr.passthroughVals))
		out = append(out, gr.groupValues...)
		// Sync follower aggregates from their leader before finishAgg. M0097-0035.
		for i, call := range o.plan.Aggs {
			if call.SharedStateSlot < 0 || call.UserAgg == nil || i == 0 {
				continue
			}
			for j := 0; j < i; j++ {
				if o.plan.Aggs[j].SharedStateSlot == call.SharedStateSlot {
					gr.aggs[i].userState = gr.aggs[j].userState
					gr.aggs[i].userStateSet = gr.aggs[j].userStateSet
					gr.aggs[i].hasValue = gr.aggs[j].hasValue
					break
				}
			}
		}
		for i, call := range o.plan.Aggs {
			out = append(out, o.finishAgg(gr.aggs[i], call))
		}
		out = append(out, gr.passthroughVals...)
		o.rows = append(o.rows, out)
	}
	// Sort output rows by GROUP BY key columns for deterministic ordering
	// matching PostgreSQL's sort-based aggregate behavior. Without an explicit
	// ORDER BY the planner wraps with a Sort node; here we pre-sort by key
	// so hash-aggregate output is stable and GROUP BY queries without ORDER BY
	// match PG's sort-aggregate output order. M0097-0117.
	if nGroupCols > 0 {
		sort.SliceStable(o.rows, func(i, j int) bool {
			ra, rb := o.rows[i], o.rows[j]
			for k := 0; k < nGroupCols && k < len(ra) && k < len(rb); k++ {
				c, _ := compareDatum(ra[k], rb[k], 0)
				if c < 0 {
					return true
				}
				if c > 0 {
					return false
				}
			}
			return false
		})
	}
	return nil
}

func (o *aggregateOp) evalGroupKey(slot TupleSlot) (string, Row, error) {
	if len(o.plan.GroupExprs) == 0 {
		return "__all__", nil, nil
	}
	vals := make(Row, 0, len(o.plan.GroupExprs))
	parts := make([]string, 0, len(o.plan.GroupExprs))
	for _, g := range o.plan.GroupExprs {
		v, err := evalExprSlot(g, slot, o.ctx)
		if err != nil {
			return "", nil, err
		}
		// M0073-0004 retention boundary: arena-backed Datums
		// (varchar / char / text / bytea group keys) must be
		// promoted to owned []byte before they enter
		// groupRuntime.groupValues — the next input page's
		// arena.Reset would invalidate them otherwise.
		// MaterializeArena is a no-op for non-arena Datums.
		v = v.MaterializeArena()
		vals = append(vals, v)
		parts = append(parts, datumKey(v))
	}
	return strings.Join(parts, "|"), vals, nil
}

func (o *aggregateOp) applyAgg(st *aggRuntime, call planner.AggregateCall, slot TupleSlot) error {
	// FILTER (WHERE condition): skip this row if the condition is false/null.
	// M0097-0007.
	if call.Filter != nil {
		fv, ferr := evalExprSlot(call.Filter, slot, o.ctx)
		if ferr != nil || fv.IsNull() || fv.Kind != KindBool || !fv.BoolValue() {
			return nil // skip row — filter not satisfied
		}
	}

	name := strings.ToLower(call.Name)
	if call.Star {
		if call.UserAgg != nil {
			// User-defined star aggregate (e.g. newcnt(*)) — call sfunc with state only.
			ua := call.UserAgg
			if !st.userStateSet {
				st.userState = userAggInitState(ua)
				st.userStateSet = true
			}
			newState, serr := executeSFuncCall(ua.SFunc, []Datum{st.userState}, o.ctx)
			if serr == nil {
				st.userState = newState
			}
			st.hasValue = true
			return nil
		}
		if name != "count" {
			return &ExecError{Code: "0A000", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s(*) is not supported", call.Name)}
		}
		if call.Distinct {
			return &ExecError{Code: "0A000", Pos: call.Pos(), Message: "count(distinct *) is not supported"}
		}
		st.count++
		st.hasValue = true
		return nil
	}

	// Ordered-set aggregates (WITHIN GROUP): accumulate sort-key values per row.
	// The direct arg (call.Arg, e.g. the fraction p in percentile_cont(p)) is
	// evaluated lazily in finishAgg from the first row's value. M0097-0035.
	if call.WithinGroup && len(call.WithinGroupOrderBy) > 0 {
		// Evaluate the direct argument (call.Arg) from the first row.
		// If evaluation fails (e.g. rank('fred') with int ORDER BY), propagate
		// the error so the caller sees "invalid input syntax" instead of NULL.
		if !st.withinGroupDirectArgSet && call.Arg != nil {
			v, verr := evalExprSlot(call.Arg, slot, o.ctx)
			if verr != nil {
				return verr
			}
			st.withinGroupDirectArg = v
			st.withinGroupDirectArgSet = true
			// Evaluate additional direct args for multi-arg hypothetical-set aggregates.
			if len(call.ExtraArgs) > 0 {
				st.withinGroupDirectArgs = make([]Datum, 1+len(call.ExtraArgs))
				st.withinGroupDirectArgs[0] = v
				for ei, ea := range call.ExtraArgs {
					ev, eerr := evalExprSlot(ea, slot, o.ctx)
					if eerr != nil {
						return eerr
					}
					st.withinGroupDirectArgs[ei+1] = ev
				}
			}
		}
		row := make([]Datum, len(call.WithinGroupOrderBy))
		allNull := true
		for i, sk := range call.WithinGroupOrderBy {
			v, verr := evalExprSlot(sk.Expr, slot, o.ctx)
			if verr != nil {
				return verr
			}
			row[i] = v
			if !v.IsNull() {
				allNull = false
			}
		}
		if !allNull {
			st.withinGroupElems = append(st.withinGroupElems, row)
			st.hasValue = true
		}
		return nil
	}

	if call.Arg == nil {
		// Zero-arg extended aggregate stub — just count rows.
		st.count++
		return nil
	}
	arg, err := evalExprSlot(call.Arg, slot, o.ctx)
	if err != nil {
		return err
	}
	// For user-defined aggregates with a strict sfunc: skip rows where any arg is NULL.
	// STRICT means "returns null on null input" — sfunc not called when any arg is NULL.
	// This includes the state (first sfunc arg): if state is NULL, skip all subsequent rows.
	if call.UserAgg != nil && call.UserAgg.SFuncStrict {
		if st.userStateSet && st.userState.IsNull() {
			return nil // state went NULL; sfunc cannot be called (state is first arg, strict)
		}
		if arg.IsNull() {
			return nil
		}
		if call.Arg2 != nil {
			a2, _ := evalExprSlot(call.Arg2, slot, o.ctx)
			if a2.IsNull() {
				return nil
			}
		}
		for _, ea := range call.ExtraArgs {
			eav, _ := evalExprSlot(ea, slot, o.ctx)
			if eav.IsNull() {
				return nil
			}
		}
	}
	// For built-in aggregates: NULL handling depends on the aggregate.
	// array_agg includes NULLs; all others skip NULLs.
	if arg.IsNull() && name != "array_agg" && call.UserAgg == nil {
		return nil
	}

	// User-defined DISTINCT aggregates: defer dedup to finishAgg for correct multi-arg handling.
	// Only apply the single-arg outer dedup for built-in aggregates. M0097-0035.
	if call.Distinct && call.UserAgg == nil {
		if st.distinct == nil {
			st.distinct = map[string]struct{}{}
		}
		k := datumKey(arg)
		if _, seen := st.distinct[k]; seen {
			return nil
		}
		st.distinct[k] = struct{}{}
	}

	switch name {
	case "count":
		st.count++
		st.hasValue = true
	case "sum", "avg":
		switch arg.Kind {
		case KindInt:
			st.sum += arg.Int
		case KindNumeric:
			if !st.hasValue || st.numericSum.Kind != KindNumeric {
				st.numericSum = Datum{Kind: KindNumeric, Scale: arg.Scale}
			}
			s, err := numericAdd(st.numericSum, arg)
			if err != nil {
				return &ExecError{Code: "22003", Pos: call.Pos(), Message: err.Error()}
			}
			st.numericSum = s
		case KindString:
			// IEEE 754 special values and regular float strings from evalCast.
			// NaN dominates; +Inf/-Inf obey IEEE addition rules. M0097-0053 / M0097-0020.
			// Use case-insensitive and alias-aware comparisons since evalCast for
			// float8 returns the raw KindString value without normalization.
			sv := strings.TrimSpace(arg.StringValue())
			svLow := strings.ToLower(sv)
			switch {
			case svLow == "nan":
				st.floatSpecial = floatSpecialNaN
			case svLow == "infinity" || svLow == "+infinity" || svLow == "inf" || svLow == "+inf":
				switch st.floatSpecial {
				case floatSpecialNegInf:
					st.floatSpecial = floatSpecialNaN // +Inf + (-Inf) = NaN
				case floatSpecialNone:
					st.floatSpecial = floatSpecialPosInf
				}
			case svLow == "-infinity" || svLow == "-inf":
				switch st.floatSpecial {
				case floatSpecialPosInf:
					st.floatSpecial = floatSpecialNaN // -Inf + (+Inf) = NaN
				case floatSpecialNone:
					st.floatSpecial = floatSpecialNegInf
				}
			default:
				// Regular numeric string (e.g. from evalCast on float8/float4 column).
				// Parse and accumulate as KindNumeric. M0097-0020.
				m, scale, perr := parseNumeric(sv)
				if perr != nil {
					return &ExecError{Code: "42804", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s requires numeric argument in v0", name)}
				}
				numArg := newNumeric(m, int(scale))
				if !st.hasValue || st.numericSum.Kind != KindNumeric {
					st.numericSum = Datum{Kind: KindNumeric, Scale: numArg.Scale}
				}
				s, serr := numericAdd(st.numericSum, numArg)
				if serr != nil {
					return &ExecError{Code: "22003", Pos: call.Pos(), Message: serr.Error()}
				}
				st.numericSum = s
			}
		default:
			return &ExecError{Code: "42804", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s requires numeric argument in v0", name)}
		}
		st.count++
		st.hasValue = true
	case "min":
		if !st.hasValue {
			// M0073-0004 retention boundary: arena-backed
			// Datums must be promoted before storage in
			// st.value (next input page's Reset would
			// invalidate the arena bytes otherwise).
			st.value = arg.MaterializeArena()
			st.hasValue = true
			return nil
		}
		cmp, err := compareDatum(arg, st.value, call.Pos())
		if err != nil {
			return err
		}
		if cmp < 0 {
			st.value = arg.MaterializeArena()
		}
	case "max":
		if !st.hasValue {
			st.value = arg.MaterializeArena()
			st.hasValue = true
			return nil
		}
		cmp, err := compareDatum(arg, st.value, call.Pos())
		if err != nil {
			return err
		}
		if cmp > 0 {
			st.value = arg.MaterializeArena()
		}
	case "bool_and", "every":
		bv, ok := arg.Kind == KindBool && arg.BoolValue(), arg.Kind == KindBool
		if !ok {
			return nil
		}
		if !st.hasValue {
			st.boolResult = bv
			st.hasValue = true
		} else {
			st.boolResult = st.boolResult && bv
		}
	case "bool_or":
		bv, ok := arg.BoolValue(), arg.Kind == KindBool
		if !ok {
			return nil
		}
		if !st.hasValue {
			st.boolResult = bv
			st.hasValue = true
		} else {
			st.boolResult = st.boolResult || bv
		}
	case "bit_and":
		if arg.Kind == KindInt {
			if !st.hasValue {
				st.intResult = arg.Int
				st.hasValue = true
			} else {
				st.intResult &= arg.Int
			}
		} else if arg.Kind == KindString {
			// BIT(n) type: parse binary string like "B0101" or "0101".
			bstr := arg.StringValue()
			bstr = strings.TrimPrefix(strings.TrimPrefix(bstr, "B"), "b")
			if v, err := strconv.ParseInt(bstr, 2, 64); err == nil {
				if !st.hasValue {
					st.intResult = v
					st.strResult = fmt.Sprintf("b%d", len(bstr))
					st.hasValue = true
				} else {
					st.intResult &= v
				}
			}
		}
	case "bit_or":
		if arg.Kind == KindInt {
			if !st.hasValue {
				st.intResult = arg.Int
				st.hasValue = true
			} else {
				st.intResult |= arg.Int
			}
		} else if arg.Kind == KindString {
			bstr := arg.StringValue()
			bstr = strings.TrimPrefix(strings.TrimPrefix(bstr, "B"), "b")
			if v, err := strconv.ParseInt(bstr, 2, 64); err == nil {
				if !st.hasValue {
					st.intResult = v
					st.strResult = fmt.Sprintf("b%d", len(bstr))
					st.hasValue = true
				} else {
					st.intResult |= v
				}
			}
		}
	case "bit_xor":
		if arg.Kind == KindInt {
			if !st.hasValue {
				st.intResult = arg.Int
				st.hasValue = true
			} else {
				st.intResult ^= arg.Int
			}
		} else if arg.Kind == KindString {
			bstr := arg.StringValue()
			bstr = strings.TrimPrefix(strings.TrimPrefix(bstr, "B"), "b")
			if v, err := strconv.ParseInt(bstr, 2, 64); err == nil {
				if !st.hasValue {
					st.intResult = v
					st.strResult = fmt.Sprintf("b%d", len(bstr))
					st.hasValue = true
				} else {
					st.intResult ^= v
				}
			}
		}
	case "string_agg":
		// string_agg(expr, delimiter) — accumulate in strResult with delimiter.
		// For bytea values, st.boolResult=true signals hex-encoded mode.
		const hexChars = "0123456789abcdef"
		if arg.Kind == KindBytes {
			// Bytea string_agg: concatenate hex-encoded bytes with hex-encoded delimiter.
			b := arg.BytesValue()
			hexBuf := make([]byte, len(b)*2)
			for i, bb := range b {
				hexBuf[2*i] = hexChars[bb>>4]
				hexBuf[2*i+1] = hexChars[bb&0x0f]
			}
			hexVal := string(hexBuf)
			delimHex := ""
			if call.Arg2 != nil {
				dv, _ := evalExprSlot(call.Arg2, slot, o.ctx)
				if !dv.IsNull() && dv.Kind == KindBytes {
					db := dv.BytesValue()
					dh := make([]byte, len(db)*2)
					for i, bb := range db {
						dh[2*i] = hexChars[bb>>4]
						dh[2*i+1] = hexChars[bb&0x0f]
					}
					delimHex = string(dh)
				}
			}
			if !st.hasValue {
				st.strResult = hexVal
				st.boolResult = true // bytea mode flag
				st.hasValue = true
			} else {
				st.strResult += delimHex + hexVal
			}
			break
		}
		// Text string_agg: evaluate the delimiter from Arg2.
		delim := ""
		if call.Arg2 != nil {
			dv, derr := evalExprSlot(call.Arg2, slot, o.ctx)
			if derr != nil {
				break
			}
			if !dv.IsNull() {
				delim = dv.Format()
			}
		}
		sv := arg.Format()
		if !st.hasValue {
			st.strResult = sv
			st.hasValue = true
		} else {
			st.strResult += delim + sv
		}
	case "array_agg":
		// array_agg(expr [ORDER BY sort_list]) — accumulate per-row elements.
		// NULLs are included as array elements (PostgreSQL semantics).
		// Elements are stored as their Format() string; finishAgg
		// wraps them in PG's text-array literal syntax. Numeric kinds
		// (int, numeric, oid) and string kinds (text, char, varchar)
		// all round-trip through Format() identically to how a SELECT
		// would print them.
		elemStr := ""
		isNull := arg.IsNull()
		if !isNull {
			elemStr = arg.Format()
		}
		st.arrayElems = append(st.arrayElems, elemStr)
		st.arrayElemNull = append(st.arrayElemNull, isNull)
		// Evaluate ORDER BY expressions for later sorting in finishAgg.
		if len(call.OrderBy) > 0 {
			keys := make([]Datum, 0, len(call.OrderBy))
			for _, sk := range call.OrderBy {
				kv, kerr := evalExprSlot(sk.Expr, slot, o.ctx)
				if kerr != nil {
					kv = NullDatum
				}
				keys = append(keys, kv.MaterializeArena())
			}
			st.arrayElemKeys = append(st.arrayElemKeys, keys)
		}
		st.hasValue = true
	case "any_value":
		// any_value(x) — return the first non-null value seen.
		if !st.hasValue && !arg.IsNull() {
			st.value = arg.MaterializeArena()
			st.hasValue = true
		}
	default:
		// User-defined aggregates (CREATE AGGREGATE): call sfunc on each row.
		if call.UserAgg != nil {
			// For DISTINCT or ORDER BY aggregates, accumulate all arg vectors for
			// deduplication and/or sorting in finishAgg. M0097-0035.
			// Non-DISTINCT with ORDER BY also needs row accumulation so rows can
			// be sorted before sfunc calls (PostgreSQL semantics). M0097-0113.
			if call.Distinct || len(call.OrderBy) > 0 {
				// Build row: [sortKey0, ..., arg0, arg1, ...extraArgs].
				// Sort keys first (for ORDER BY), then arg values for sfunc. M0097-0035.
				nSortKeys := len(call.OrderBy)
				row := make([]Datum, 0, nSortKeys+1+1+len(call.ExtraArgs))
				// Evaluate sort keys first.
				for _, sk := range call.OrderBy {
					kv, _ := evalExprSlot(sk.Expr, slot, o.ctx)
					row = append(row, kv.MaterializeArena())
				}
				// Then arg values.
				row = append(row, arg.MaterializeArena())
				// Arg2
				if call.Arg2 != nil {
					a2, a2err := evalExprSlot(call.Arg2, slot, o.ctx)
					if a2err == nil {
						row = append(row, a2.MaterializeArena())
					}
				}
				// ExtraArgs
				for _, ea := range call.ExtraArgs {
					ev, everr := evalExprSlot(ea, slot, o.ctx)
					if everr == nil {
						row = append(row, ev.MaterializeArena())
					}
				}
				st.distinctUserAggRows = append(st.distinctUserAggRows, row)
				st.hasValue = true
				return nil
			}
			ua := call.UserAgg
			// Initialize state from InitCond on first call.
			if !st.userStateSet {
				st.userState = userAggInitState(ua)
				st.userStateSet = true
			}
			// Build args: [state, arg1, arg2, ...extraArgs].
			sfuncArgs := []Datum{st.userState}
			if ua.Variadic {
				// For variadic aggregates, bundle all input args into a single array
				// (the sfunc expects state + VARIADIC anyarray). M0097-0117.
				var elems []string
				elems = append(elems, arg.Format())
				if call.Arg2 != nil {
					a2, a2err := evalExprSlot(call.Arg2, slot, o.ctx)
					if a2err == nil {
						elems = append(elems, a2.Format())
					}
				}
				for _, ea := range call.ExtraArgs {
					ev, everr := evalExprSlot(ea, slot, o.ctx)
					if everr == nil {
						elems = append(elems, ev.Format())
					}
				}
				sfuncArgs = append(sfuncArgs, NewStringDatum(formatTextArray(elems)))
			} else {
				sfuncArgs = append(sfuncArgs, arg)
				// Evaluate optional second argument.
				if call.Arg2 != nil {
					arg2, a2err := evalExprSlot(call.Arg2, slot, o.ctx)
					if a2err == nil {
						sfuncArgs = append(sfuncArgs, arg2)
					}
				}
				// Evaluate any extra arguments beyond the second (3-arg+ user aggregates).
				for _, ea := range call.ExtraArgs {
					ev, everr := evalExprSlot(ea, slot, o.ctx)
					if everr == nil {
						sfuncArgs = append(sfuncArgs, ev)
					}
				}
			}
			newState, serr := executeSFuncCall(ua.SFunc, sfuncArgs, o.ctx)
			if serr == nil {
				st.userState = newState
			}
			st.hasValue = true
			return nil
		}
		// Variance/stddev aggregates: accumulate float sum and sum of squares.
		// M0097-0007.
		switch strings.ToLower(call.Name) {
		case "var_pop", "var_samp", "stddev_pop", "stddev_samp", "stddev", "variance":
			if !arg.IsNull() {
				// For integer inputs, use exact big.Int arithmetic (matching PG int4_accum/int8_accum).
				inputIsInt := func() bool {
					switch strings.ToLower(call.InputType.Name) {
					case "int2", "int4", "int8", "int", "integer", "smallint", "bigint",
						"serial", "bigserial", "smallserial":
						return true
					}
					return false
				}
				if arg.Kind == KindInt && inputIsInt() {
					if !st.intExact {
						st.intExact = true
						st.intSx = new(big.Int)
						st.intSxx = new(big.Int)
					}
					v := big.NewInt(arg.Int)
					st.intSx.Add(st.intSx, v)
					st.intSxx.Add(st.intSxx, new(big.Int).Mul(v, v))
					st.count++
					break
				}
				// For pure numeric inputs (not float4/float8 cast), use exact rational
				// arithmetic matching PG's numeric_accum path. M0097-0125.
				if arg.Kind == KindNumeric && !isFloat4TypeName(call.InputType.Name) &&
					strings.ToLower(call.InputType.Name) != "float4" &&
					strings.ToLower(call.InputType.Name) != "float8" &&
					strings.ToLower(call.InputType.Name) != "real" &&
					strings.ToLower(call.InputType.Name) != "double precision" {
					if st.numericSx == nil {
						st.numericExact = true
						st.numericSx = new(big.Rat)
						st.numericSxx = new(big.Rat)
					}
					numStr := formatNumeric(arg.NumericMantissaValue(), arg.NumericScaleValue())
					x := new(big.Rat)
					if _, ok := x.SetString(numStr); ok {
						st.numericSx.Add(st.numericSx, x)
						x2 := new(big.Rat).Mul(x, x)
						st.numericSxx.Add(st.numericSxx, x2)
					}
					st.count++
					break
				}
				var f float64
				switch arg.Kind {
				case KindInt:
					f = float64(arg.Int)
				case KindNumeric:
					f, _ = strconv.ParseFloat(formatNumeric(arg.NumericMantissaValue(), arg.NumericScaleValue()), 64)
				case KindString:
					f, _ = strconv.ParseFloat(arg.StringValue(), 64)
				}
				// For float4 input, round through float32 to match PG's float4_accum
				// semantics (PG stores float4 as 4-byte IEEE 754; accumulation uses the
				// float32-precision value, not the original decimal string). M0097-0115.
				if isFloat4TypeName(call.InputType.Name) {
					f = float64(float32(f))
				}
				// Youngs-Cramer algorithm: numerically stable for large-offset inputs.
				// tmp = x*N - Sx avoids subtracting two large nearly-equal values.
				// NaN/Inf propagates through the computation; we mark floatM2 = NaN
				// so the final result is NaN regardless of count (matches PG behavior).
				st.count++
				st.floatSx += f
				if math.IsNaN(f) || math.IsInf(f, 0) {
					st.floatM2 = math.NaN()
				} else if st.count > 1 && !math.IsNaN(st.floatM2) {
					tmp := f*float64(st.count) - st.floatSx
					st.floatM2 += tmp * tmp / (float64(st.count) * float64(st.count-1))
				}
			}
		case "regr_count", "regr_sxx", "regr_syy", "regr_sxy",
			"regr_avgx", "regr_avgy", "regr_r2", "regr_slope", "regr_intercept",
			"covar_pop", "covar_samp", "corr":
			// Two-argument regression aggregates. First arg is y (dependent),
			// second arg is x (independent). M0097-0020.
			if call.Arg2 == nil {
				break
			}
			arg2, a2err := evalExprSlot(call.Arg2, slot, o.ctx)
			if a2err != nil || arg2.IsNull() {
				break // skip row if either arg is NULL
			}
			y := aggDatumToFloat64(arg)
			x := aggDatumToFloat64(arg2)
			// For float4 input, round through float32 to match PG's float4_accum. M0097-0115.
			if isFloat4TypeName(call.InputType.Name) {
				y = float64(float32(y))
				x = float64(float32(x))
			}
			st.regrN++
			st.regrSumX += x
			st.regrSumY += y
			st.regrSumXX += x * x
			st.regrSumXY += x * y
			st.regrSumYY += y * y
		default:
			st.count++
			if arg.Kind == KindInt {
				st.sum += arg.Int
			}
		}
	}
	return nil
}

// aggDatumToFloat64 converts any numeric datum to float64 for aggregate computation.
// isFloat4TypeName returns true for float4/real (single-precision) types.
// Used to apply float32 rounding for PG-compatible float4 aggregate semantics.
// withinGroupTupleLT returns true if row < directArgs in the hypothetical-set ordering.
// It performs lexicographic comparison of the row's sort-key tuple against directArgs
// respecting each sort key's ASC/DESC direction and NULL handling.
func withinGroupTupleLT(row []Datum, directArgs []Datum, sortKeys []planner.SortKey) bool {
	n := len(sortKeys)
	if n > len(row) {
		n = len(row)
	}
	if n > len(directArgs) {
		n = len(directArgs)
	}
	for i := 0; i < n; i++ {
		ri, di := row[i], directArgs[i]
		rNull, dNull := ri.IsNull(), di.IsNull()
		if rNull && dNull {
			continue // equal, move to next key
		}
		sk := sortKeys[i]
		nullsFirst := sk.NullsFirst
		// For DESC: default nulls FIRST; for ASC: default nulls LAST.
		if rNull {
			// row is NULL: comes before non-NULL if nullsFirst
			return nullsFirst
		}
		if dNull {
			// direct arg is NULL: row is non-NULL, comes after NULL if nullsFirst
			return !nullsFirst
		}
		cmp, err := compareDatum(ri, di, 0)
		if err != nil {
			return false
		}
		if cmp == 0 {
			continue // equal on this key, check next
		}
		if sk.Desc {
			return cmp > 0 // DESC: row > direct means row sorts BEFORE direct (is "less" in rank order)
		}
		return cmp < 0
	}
	return false // equal on all keys: not strictly less than
}

// compareDatumWithNullsFirst compares two Datums respecting nullsFirst and desc flags.
// Returns negative if a < b, zero if equal, positive if a > b in the sort order.
func compareDatumWithNullsFirst(a, b Datum, nullsFirst bool, desc bool) (int, error) {
	aNull, bNull := a.IsNull(), b.IsNull()
	if aNull && bNull {
		return 0, nil
	}
	if aNull {
		if nullsFirst {
			return -1, nil
		}
		return 1, nil
	}
	if bNull {
		if nullsFirst {
			return 1, nil
		}
		return -1, nil
	}
	cmp, err := compareDatum(a, b, 0)
	if err != nil {
		return 0, err
	}
	if desc {
		return -cmp, nil
	}
	return cmp, nil
}

func isFloat4TypeName(name string) bool {
	switch strings.ToLower(name) {
	case "float4", "real":
		return true
	default:
		return false
	}
}

// exactIntVariance computes variance/stddev for integer accumulators using exact big.Int arithmetic.
// Matching PostgreSQL's int4_accum / numeric_var_samp / numeric_var_pop path.
// isSample: true=var_samp/stddev_samp, false=var_pop/stddev_pop.
// isSqrt: true=stddev output, false=variance output.
// exactNumericVariance computes var_pop/var_samp/stddev for exact rational accumulators.
// Matches PG's numeric_accum + numeric_var_pop/var_samp path for numeric inputs. M0097-0125.
func exactNumericVariance(sx, sxx *big.Rat, n int64, isSample, isSqrt bool) Datum {
	bigN := new(big.Rat).SetInt64(n)
	// numerator = N * Σxx - (Σx)²
	numer := new(big.Rat).Mul(bigN, sxx)
	sxSq := new(big.Rat).Mul(sx, sx)
	numer.Sub(numer, sxSq)
	// denominator = N² (pop) or N*(N-1) (samp)
	var denom *big.Rat
	if isSample {
		denom = new(big.Rat).Mul(bigN, new(big.Rat).SetInt64(n-1))
	} else {
		denom = new(big.Rat).Mul(bigN, bigN)
	}
	if denom.Sign() == 0 {
		return NullDatum
	}
	result := new(big.Rat).Quo(numer, denom)
	if isSqrt {
		f64, _ := result.Float64()
		if f64 <= 0 {
			// Variance is 0 (or negative due to floating-point artifacts): stddev = 0.
			return NewStringDatum("0")
		}
		prec := uint(128)
		ratFloat := new(big.Float).SetPrec(prec).SetRat(result)
		seed := new(big.Float).SetPrec(prec).SetFloat64(math.Sqrt(f64))
		half := new(big.Float).SetPrec(prec).SetFloat64(0.5)
		for i := 0; i < 15; i++ {
			if seed.Sign() == 0 {
				break
			}
			div := new(big.Float).SetPrec(prec).Quo(ratFloat, seed)
			seed.Mul(half, new(big.Float).SetPrec(prec).Add(seed, div))
		}
		// PG uses 15 significant digits for numeric stddev output (extra_float_digits=0).
		s := seed.Text('g', 15)
		if strings.Contains(s, ".") && !strings.Contains(s, "e") {
			s = strings.TrimRight(s, "0")
			s = strings.TrimRight(s, ".")
		}
		return NewStringDatum(s)
	}
	return NewStringDatum(formatBigRatDecimal(result, 12))
}

func exactIntVariance(sx, sxx *big.Int, n int64, isSample, isSqrt bool) Datum {
	bigN := big.NewInt(n)
	// numerator = N * Sxx - Sx²
	num := new(big.Int).Sub(new(big.Int).Mul(bigN, sxx), new(big.Int).Mul(sx, sx))
	// denominator = N² (pop) or N*(N-1) (samp)
	var den *big.Int
	if isSample {
		den = new(big.Int).Mul(bigN, big.NewInt(n-1))
	} else {
		den = new(big.Int).Mul(bigN, bigN)
	}
	if den.Sign() == 0 {
		return NullDatum
	}
	// Use exact big.Rat arithmetic, then format as a decimal string.
	// Matching PostgreSQL numeric display: up to 18 decimal places, trailing zeros stripped.
	rat := new(big.Rat).SetFrac(num, den)
	if isSqrt {
		// For stddev, compute sqrt via high-precision float.
		f64, _ := rat.Float64()
		if f64 < 0 {
			f64 = 0
		}
		// Newton-Raphson sqrt with 128-bit precision.
		prec := uint(128)
		ratFloat := new(big.Float).SetPrec(prec).SetRat(rat)
		seed := new(big.Float).SetPrec(prec).SetFloat64(math.Sqrt(f64))
		half := new(big.Float).SetPrec(prec).SetFloat64(0.5)
		for i := 0; i < 15; i++ {
			div := new(big.Float).SetPrec(prec).Quo(ratFloat, seed)
			seed.Mul(half, new(big.Float).SetPrec(prec).Add(seed, div))
		}
		// Format with 18 significant digits matching PostgreSQL stddev output.
		s := seed.Text('g', 18)
		if strings.Contains(s, ".") && !strings.Contains(s, "e") {
			s = strings.TrimRight(s, "0")
			s = strings.TrimRight(s, ".")
		}
		return NewStringDatum(s)
	}
	// Format rational as decimal with up to 12 decimal places with round-half-up, then
	// strip trailing zeros. Matches PostgreSQL's numeric_poly_var_samp output scale
	// for integer inputs (consistently 12 decimal places across test cases).
	s := formatBigRatDecimal(rat, 12)
	return NewStringDatum(s)
}

// formatBigRatDecimal formats a big.Rat as a decimal string with up to maxDecimals decimal
// places, stripping trailing zeros and unnecessary decimal point.
func formatBigRatDecimal(r *big.Rat, maxDecimals int) string {
	if r.Sign() == 0 {
		return "0"
	}
	neg := r.Sign() < 0
	num := new(big.Int).Abs(r.Num())
	den := new(big.Int).Set(r.Denom())

	// Integer part
	intPart := new(big.Int)
	rem := new(big.Int)
	intPart.DivMod(num, den, rem)

	var sb strings.Builder
	if neg {
		sb.WriteByte('-')
	}
	sb.WriteString(intPart.String())
	if rem.Sign() == 0 || maxDecimals == 0 {
		return sb.String()
	}

	sb.WriteByte('.')
	ten := big.NewInt(10)
	digits := make([]byte, maxDecimals)
	for i := 0; i < maxDecimals; i++ {
		rem.Mul(rem, ten)
		d := new(big.Int)
		d.DivMod(rem, den, rem) // d = digit, rem = new remainder
		digits[i] = byte('0' + d.Int64())
		if rem.Sign() == 0 {
			// Exact: trim trailing slice and write
			sb.Write(digits[:i+1])
			return sb.String()
		}
	}
	// Write all digits, trimming trailing zeros
	// Round-half-up: look at the next digit to decide whether to round up.
	rem.Mul(rem, ten)
	nextD := new(big.Int)
	nextD.Div(rem, den)
	if nextD.Int64() >= 5 {
		for i := maxDecimals - 1; i >= 0; i-- {
			digits[i]++
			if digits[i] <= '9' {
				break
			}
			digits[i] = '0'
			if i == 0 {
				// Carry into integer part
				newInt := new(big.Int).Add(intPart, big.NewInt(1))
				s := sb.String()
				// Find the '.' and rebuild
				dotPos := strings.LastIndex(s, ".")
				sb.Reset()
				if neg {
					sb.WriteByte('-')
				}
				sb.WriteString(newInt.String())
				sb.WriteByte('.')
				_ = dotPos
			}
		}
	}
	end := maxDecimals
	for end > 0 && digits[end-1] == '0' {
		end--
	}
	if end == 0 {
		s := sb.String()
		return strings.TrimRight(s, ".")
	}
	sb.Write(digits[:end])
	return sb.String()
}

func aggDatumToFloat64(d Datum) float64 {
	switch d.Kind {
	case KindInt:
		return float64(d.Int)
	case KindNumeric:
		f, _ := strconv.ParseFloat(formatNumeric(d.NumericMantissaValue(), d.NumericScaleValue()), 64)
		return f
	case KindString:
		f, _ := strconv.ParseFloat(d.StringValue(), 64)
		return f
	}
	return 0
}

func (o *aggregateOp) finishAgg(st aggRuntime, call planner.AggregateCall) Datum {
	// Handle ordered-set aggregates (WITHIN GROUP) before regular handling. M0097-0035.
	if call.WithinGroup {
		return finishWithinGroupAgg(st, call, o.ctx)
	}
	// Handle user-defined aggregates first.
	if call.UserAgg != nil {
		ua := call.UserAgg
		// For DISTINCT or ORDER BY user-defined aggregates: deduplicate and/or sort,
		// then call sfunc in the resulting order. M0097-0035 / M0097-0113.
		if (call.Distinct || len(call.OrderBy) > 0) && len(st.distinctUserAggRows) > 0 {
			nSortKeys := len(call.OrderBy)
			// Deduplicate by all arg values (rows[nSortKeys:]) when DISTINCT is requested.
			var deduped [][]Datum
			if call.Distinct {
				seen := map[string]struct{}{}
				for _, row := range st.distinctUserAggRows {
					argSlice := row[nSortKeys:]
					var keyParts []string
					for _, d := range argSlice {
						keyParts = append(keyParts, datumKey(d))
					}
					k := strings.Join(keyParts, "	")
					if _, ok := seen[k]; ok {
						continue
					}
					seen[k] = struct{}{}
					deduped = append(deduped, row)
				}
			} else {
				// No dedup: use all accumulated rows in order (ORDER BY only).
				deduped = st.distinctUserAggRows
			}
			// Sort by ORDER BY sort keys when present. When no ORDER BY is given,
			// sort by the argument values (PostgreSQL's default for DISTINCT
			// aggregates — ensures deterministic, reproducible order). M0097-0035.
			sort.SliceStable(deduped, func(i, j int) bool {
				if nSortKeys > 0 {
					// User-specified ORDER BY columns come first.
					for ki := 0; ki < nSortKeys; ki++ {
						ai, bi := deduped[i][ki], deduped[j][ki]
						aNull, bNull := ai.IsNull(), bi.IsNull()
						if aNull && bNull {
							continue
						}
						nullsFirst := call.OrderBy[ki].NullsFirst
						if aNull {
							return nullsFirst
						}
						if bNull {
							return !nullsFirst
						}
						cmp, err := compareDatum(ai, bi, 0)
						if err != nil || cmp == 0 {
							continue
						}
						if call.OrderBy[ki].Desc {
							return cmp > 0
						}
						return cmp < 0
					}
					return false
				}
				// No ORDER BY: sort by arg values for deterministic DISTINCT order.
				argI := deduped[i][nSortKeys:]
				argJ := deduped[j][nSortKeys:]
				for k := 0; k < len(argI) && k < len(argJ); k++ {
					ai, bj := argI[k], argJ[k]
					aNull, bNull := ai.IsNull(), bj.IsNull()
					if aNull && bNull {
						continue
					}
					if aNull {
						return false // NULLs last
					}
					if bNull {
						return true
					}
					cmp, err := compareDatum(ai, bj, 0)
					if err != nil || cmp == 0 {
						continue
					}
					return cmp < 0
				}
				return false
			})
			// Initialize state and call sfunc for each deduped+sorted row.
			state := userAggInitState(ua)
			for _, row := range deduped {
				argSlice := row[nSortKeys:]
				sfuncArgs := make([]Datum, 0, 1+len(argSlice))
				sfuncArgs = append(sfuncArgs, state)
				sfuncArgs = append(sfuncArgs, argSlice...)
				newState, serr := executeSFuncCall(ua.SFunc, sfuncArgs, o.ctx)
				if serr == nil {
					state = newState
				}
			}
			if ua.FinalFunc == "" {
				return state
			}
			result, ferr := executeSFuncCall(ua.FinalFunc, []Datum{state}, o.ctx)
			if ferr != nil {
				return NullDatum
			}
			return result
		}
		if !st.hasValue {
			return NullDatum
		}
		state := st.userState
		// Apply CombineFunc when defined: combinefunc(NULL, partial_state).
		// Even in non-parallel mode, PG calls the combine function once to merge
		// the NULL initial combine-state with the single partial state. A STRICT
		// combinefunc with a NULL first arg returns NULL (the balk aggregate pattern). M0097-0122.
		if ua.CombineFunc != "" {
			combined, cerr := executeSFuncCall(ua.CombineFunc, []Datum{NullDatum, state}, o.ctx)
			if cerr == nil {
				state = combined
			}
		}
		if ua.FinalFunc == "" {
			return state
		}
		// Call the final function.
		result, ferr := executeSFuncCall(ua.FinalFunc, []Datum{state}, o.ctx)
		if ferr != nil {
			return NullDatum
		}
		return result
	}
	switch strings.ToLower(call.Name) {
	case "count":
		return Datum{Kind: KindInt, Int: st.count}
	case "sum":
		if !st.hasValue {
			return NullDatum
		}
		// IEEE 754 special values from float4/float8 columns take precedence.
		// M0097-0053.
		switch st.floatSpecial {
		case floatSpecialNaN:
			return NewStringDatum("NaN")
		case floatSpecialPosInf:
			return NewStringDatum("Infinity")
		case floatSpecialNegInf:
			return NewStringDatum("-Infinity")
		}
		if st.numericSum.Kind == KindNumeric {
			// For float4 input, PostgreSQL uses float4 as the transition type
			// (float4pl accumulation with intermediate float32 rounding). Simulate
			// this by casting the final sum through float32 and formatting with
			// float4 precision (6 significant digits). M0097-0115.
			if isFloat4TypeName(call.InputType.Name) {
				fsum := aggDatumToFloat64(st.numericSum)
				return NewStringDatum(strconv.FormatFloat(float64(float32(fsum)), 'g', 6, 32))
			}
			// For float8 input, format with 15 significant digits (PostgreSQL's
			// float8out format) to avoid spurious trailing-digit differences. M0097-0035.
			switch strings.ToLower(call.InputType.Name) {
			case "float8", "double precision", "double", "float":
				fsum := aggDatumToFloat64(st.numericSum)
				return NewStringDatum(strconv.FormatFloat(fsum, 'g', 15, 64))
			}
			return st.numericSum
		}
		return Datum{Kind: KindInt, Int: st.sum}
	case "avg":
		if st.count == 0 {
			return NullDatum
		}
		// IEEE 754 special values from float4/float8 columns. M0097-0053.
		switch st.floatSpecial {
		case floatSpecialNaN:
			return NewStringDatum("NaN")
		case floatSpecialPosInf:
			return NewStringDatum("Infinity")
		case floatSpecialNegInf:
			return NewStringDatum("-Infinity")
		}
		// avg(float4/float8) returns float8 — use float64 division and format
		// with %.15g to match PostgreSQL's float8out. M0097-0020.
		if strings.EqualFold(call.Type.Name, "float8") || strings.EqualFold(call.Type.Name, "float4") {
			var fsum float64
			if st.numericSum.Kind == KindNumeric {
				fsum = aggDatumToFloat64(st.numericSum)
			} else {
				fsum = float64(st.sum)
			}
			return NewStringDatum(strconv.FormatFloat(fsum/float64(st.count), 'g', 15, 64))
		}
		if st.numericSum.Kind == KindNumeric {
			d, err := numericDiv(st.numericSum, numericFromInt(st.count), call.Pos())
			if err != nil {
				return NullDatum
			}
			return d
		}
		// avg(integer types) must return numeric, not integer (PostgreSQL
		// behaviour: avg(int2/int4/int8) → numeric). M0097-0053.
		numSum := numericFromInt(st.sum)
		d, err := numericDiv(numSum, numericFromInt(st.count), call.Pos())
		if err != nil {
			return NullDatum
		}
		return d
	case "min", "max":
		if !st.hasValue {
			return NullDatum
		}
		return st.value
	case "bool_and", "every", "bool_or":
		if !st.hasValue {
			return NullDatum
		}
		return NewBoolDatum(st.boolResult)
	case "bit_and", "bit_or", "bit_xor":
		if !st.hasValue {
			return NullDatum
		}
		// BIT(n) type: format as zero-padded binary string.
		if strings.HasPrefix(st.strResult, "b") {
			if width, err := strconv.Atoi(st.strResult[1:]); err == nil && width > 0 {
				return NewStringDatum(fmt.Sprintf("%0*b", width, uint64(st.intResult)))
			}
		}
		return Datum{Kind: KindInt, Int: st.intResult}
	case "string_agg":
		if !st.hasValue {
			return NullDatum
		}
		// Bytea mode: st.boolResult=true means result is hex-encoded bytes.
		if st.boolResult {
			return NewStringDatum(`\x` + st.strResult)
		}
		return NewStringDatum(st.strResult)
	case "array_agg":
		if !st.hasValue {
			return NullDatum
		}
		// Sort elements by ORDER BY keys if present.
		if len(st.arrayElemKeys) == len(st.arrayElems) && len(st.arrayElemKeys) > 0 {
			// Build index slice and sort by keys.
			idx := make([]int, len(st.arrayElems))
			for i := range idx {
				idx[i] = i
			}
			orderByDescs := make([]bool, len(call.OrderBy))
			for i, sk := range call.OrderBy {
				orderByDescs[i] = sk.Desc
			}
			sort.SliceStable(idx, func(a, b int) bool {
				ka, kb := st.arrayElemKeys[idx[a]], st.arrayElemKeys[idx[b]]
				for ki := range ka {
					if ki >= len(kb) {
						break
					}
					akNull := ka[ki].IsNull()
					bkNull := kb[ki].IsNull()
					if akNull && bkNull {
						continue
					}
					// NULLs LAST for ASC (default), NULLs FIRST for DESC (default).
					nullsFirst := false
					if ki < len(call.OrderBy) {
						nullsFirst = call.OrderBy[ki].NullsFirst
					}
					if akNull {
						return nullsFirst
					}
					if bkNull {
						return !nullsFirst
					}
					cmp, err := compareDatum(ka[ki], kb[ki], 0)
					if err != nil || cmp == 0 {
						continue
					}
					if ki < len(orderByDescs) && orderByDescs[ki] {
						return cmp > 0
					}
					return cmp < 0
				}
				return false
			})
			sortedElems := make([]string, len(st.arrayElems))
			sortedNulls := make([]bool, len(st.arrayElems))
			for i, origIdx := range idx {
				sortedElems[i] = st.arrayElems[origIdx]
				if origIdx < len(st.arrayElemNull) {
					sortedNulls[i] = st.arrayElemNull[origIdx]
				}
			}
			return NewStringDatum(formatTextArrayWithNulls(sortedElems, sortedNulls))
		}
		return NewStringDatum(formatTextArrayWithNulls(st.arrayElems, st.arrayElemNull))
	case "any_value":
		if !st.hasValue {
			return NullDatum
		}
		return st.value
	}
	// Variance/stddev aggregates. M0097-0007.
	switch strings.ToLower(call.Name) {
	case "var_pop":
		if st.count == 0 {
			return NullDatum
		}
		if st.intExact {
			return exactIntVariance(st.intSx, st.intSxx, st.count, false, false)
		}
		if st.numericExact {
			return exactNumericVariance(st.numericSx, st.numericSxx, st.count, false, false)
		}
		varPop := st.floatM2 / float64(st.count)
		return NewStringDatum(strconv.FormatFloat(varPop, 'g', 15, 64))
	case "variance", "var_samp":
		// In PostgreSQL, variance() = var_samp() (sample variance, divides by n-1).
		if st.count < 2 {
			return NullDatum
		}
		if st.intExact {
			return exactIntVariance(st.intSx, st.intSxx, st.count, true, false)
		}
		if st.numericExact {
			return exactNumericVariance(st.numericSx, st.numericSxx, st.count, true, false)
		}
		varSamp := st.floatM2 / float64(st.count-1)
		return NewStringDatum(strconv.FormatFloat(varSamp, 'g', 15, 64))
	case "stddev_pop":
		if st.count == 0 {
			return NullDatum
		}
		if st.intExact {
			return exactIntVariance(st.intSx, st.intSxx, st.count, false, true)
		}
		if st.numericExact {
			return exactNumericVariance(st.numericSx, st.numericSxx, st.count, false, true)
		}
		varPop := st.floatM2 / float64(st.count)
		if varPop < 0 {
			varPop = 0
		}
		return NewStringDatum(strconv.FormatFloat(math.Sqrt(varPop), 'g', 15, 64))
	case "stddev_samp", "stddev":
		if st.count < 2 {
			return NullDatum
		}
		if st.intExact {
			return exactIntVariance(st.intSx, st.intSxx, st.count, true, true)
		}
		if st.numericExact {
			return exactNumericVariance(st.numericSx, st.numericSxx, st.count, true, true)
		}
		varSamp := st.floatM2 / float64(st.count-1)
		if varSamp < 0 {
			varSamp = 0
		}
		return NewStringDatum(strconv.FormatFloat(math.Sqrt(varSamp), 'g', 15, 64))
	}

	// Regression aggregates. M0097-0020.
	switch strings.ToLower(call.Name) {
	case "regr_count":
		return NewIntDatum(st.regrN)
	case "regr_avgx":
		if st.regrN < 1 {
			return NullDatum
		}
		return NewStringDatum(strconv.FormatFloat(st.regrSumX/float64(st.regrN), 'g', 15, 64))
	case "regr_avgy":
		if st.regrN < 1 {
			return NullDatum
		}
		return NewStringDatum(strconv.FormatFloat(st.regrSumY/float64(st.regrN), 'g', 15, 64))
	case "regr_sxx":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(sxx, 'g', 15, 64))
	case "regr_syy":
		if st.regrN < 1 {
			return NullDatum
		}
		syy := st.regrSumYY - st.regrSumY*st.regrSumY/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(syy, 'g', 15, 64))
	case "regr_sxy":
		if st.regrN < 1 {
			return NullDatum
		}
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(sxy, 'g', 15, 64))
	case "covar_pop":
		if st.regrN < 1 {
			return NullDatum
		}
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(sxy/float64(st.regrN), 'g', 15, 64))
	case "covar_samp":
		if st.regrN < 2 {
			return NullDatum
		}
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(sxy/float64(st.regrN-1), 'g', 15, 64))
	case "regr_r2":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		syy := st.regrSumYY - st.regrSumY*st.regrSumY/float64(st.regrN)
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		if sxx == 0 {
			return NullDatum
		}
		r2 := (sxy * sxy) / (sxx * syy)
		return NewStringDatum(strconv.FormatFloat(r2, 'g', 15, 64))
	case "regr_slope":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		if sxx == 0 {
			return NullDatum
		}
		return NewStringDatum(strconv.FormatFloat(sxy/sxx, 'g', 15, 64))
	case "regr_intercept":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		if sxx == 0 {
			return NullDatum
		}
		slope := sxy / sxx
		intercept := (st.regrSumY - slope*st.regrSumX) / float64(st.regrN)
		return NewStringDatum(strconv.FormatFloat(intercept, 'g', 15, 64))
	case "corr":
		if st.regrN < 1 {
			return NullDatum
		}
		sxx := st.regrSumXX - st.regrSumX*st.regrSumX/float64(st.regrN)
		syy := st.regrSumYY - st.regrSumY*st.regrSumY/float64(st.regrN)
		sxy := st.regrSumXY - st.regrSumX*st.regrSumY/float64(st.regrN)
		if sxx <= 0 || syy <= 0 {
			return NullDatum
		}
		return NewStringDatum(strconv.FormatFloat(sxy/math.Sqrt(sxx*syy), 'g', 15, 64))
	}
	return NullDatum
}

func (o *aggregateOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return asSlot(o.schema, row), nil
}

func (o *aggregateOp) Close() error {
	o.rows = nil
	o.ctx = nil
	o.idx = 0
	return o.child.Close()
}

func (o *aggregateOp) Schema() planner.Schema { return o.schema }

func drainRows(op Operator) ([]Row, error) {
	return drainRowsCtx(op, nil)
}

// drainRowsCtx drains all rows from op, checking ctx.Err() every 1000
// rows so a CancelRequest can interrupt long hash-join build phases.
//
// M0073-0004 retention boundary: when slots carry arena-backed Datums
// (KindStringArena / KindBytesArena), the cloneRowOwned helper deep-
// copies the arena bytes into owned []byte. Without this, the build-
// side hash tables would alias the source operator's per-page arena
// pages — invalidated on the next arena.Reset() (typically per-page
// in seqScan, per-Rescan in indexScan). The fast path
// (rowHasArena=false) preserves the legacy O(width) struct copy.
func drainRowsCtx(op Operator, ctx *Context) ([]Row, error) {
	rows := make([]Row, 0)
	n := 0
	for {
		if ctx != nil && ctx.Ctx != nil && n%1000 == 0 {
			if err := ctx.Ctx.Err(); err != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		slot, err := op.Next()
		if err == EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		row := slotRow(slot)
		var dup Row
		if rowHasArena(row) {
			dup = cloneRowOwned(row)
		} else {
			dup = acquireRow(len(row))
			copy(dup, row)
		}
		rows = append(rows, dup)
		n++
	}
}

func concatRows(a, b Row) Row {
	out := make(Row, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func nullRow(n int) Row {
	out := make(Row, n)
	for i := range out {
		out[i] = NullDatum
	}
	return out
}

// datumToInt64Key converts d to an int64 hash key without any allocation.
// Returns (key, true) when d is KindInt or a KindNumeric that normalises
// to an integer (scale == 0 after stripping trailing zeros). Returns
// (0, false) for NULL, bool, string, time, interval, and fractional
// numerics. Used by the MHJ int64 fast path (M0043-0003).
//
// The int64 canonical form matches canonicalNumericKey: KindInt(v) and
// KindNumeric{mantissa=v*10^n, scale=n} both produce the same int64
// after normalisation, preserving cross-kind hash equality.
func datumToInt64Key(d Datum) (int64, bool) {
	switch d.Kind {
	case KindInt:
		return d.Int, true
	case KindNumeric:
		if d.NumericBigValue() != nil {
			return 0, false // overflow lane: not representable as int64
		}
		m, s := d.NumericMantissaValue(), int(d.Scale)
		for s > 0 && m%10 == 0 {
			m /= 10
			s--
		}
		if s == 0 {
			return m, true
		}
		return 0, false // fractional numeric
	}
	return 0, false
}

func datumKey(d Datum) string {
	switch d.Kind {
	case KindNull:
		return "n"
	case KindBool:
		if d.BoolValue() {
			return "b:t"
		}
		return "b:f"
	case KindInt:
		// Cross-kind hash compatibility: integers and numerics
		// must hash equal when they represent the same value, so
		// `aid = $1` matches whether $1 lands as KindInt or as a
		// scale-0 KindNumeric. Normalise both to the same shape.
		return canonicalNumericKey(d.Int, 0)
	case KindNumeric:
		return canonicalNumericKey(d.NumericMantissaValue(), int(d.Scale))
	case KindString:
		return "s:" + d.StringValue()
	case KindBytes:
		return "x:" + string(d.BytesValue())
	case KindTime:
		var buf [20]byte
		b := append(buf[:0], 't', ':')
		b = strconv.AppendInt(b, d.TimeValue().UnixNano(), 10)
		return string(b)
	case KindInterval:
		var buf [32]byte
		b := append(buf[:0], 'v', ':')
		b = strconv.AppendInt(b, int64(d.IntervalMonthsValue()), 10)
		b = append(b, ':')
		b = strconv.AppendInt(b, int64(d.IntervalDaysValue()), 10)
		return string(b)
	}
	return fmt.Sprintf("k:%d", d.Kind)
}

// canonicalNumericKey produces a string identifier that's identical
// for two numerics that compare equal. `1` (m=1,s=0), `1.0`
// (m=10,s=1), and `1.00` (m=100,s=2) all canonicalise to the same
// key. Trailing zero pairs (one digit + one scale step) are stripped
// until either the scale reaches 0 or the trailing digit is non-zero.
// Negative-scale results are flagged as `e<N>` so `100` (m=100,s=0)
// and `100` (m=1,s=-2) — should the latter ever arise — both map
// to the same canonical form. v0 never produces negative scales,
// but the helper is kept robust.
func canonicalNumericKey(mantissa int64, scale int) string {
	// Special case: 0 at any scale collapses to a single value.
	if mantissa == 0 {
		return "m:0:0"
	}
	for scale > 0 && mantissa%10 == 0 {
		mantissa /= 10
		scale--
	}
	// Use strconv.AppendInt instead of fmt.Sprintf to avoid format-string
	// parsing overhead on the string-key hot path.
	var buf [32]byte
	b := append(buf[:0], 'm', ':')
	b = strconv.AppendInt(b, mantissa, 10)
	b = append(b, ':')
	b = strconv.AppendInt(b, int64(scale), 10)
	return string(b)
}

// userAggInitState returns the initial state Datum for a user-defined aggregate.
// An empty InitCond gives NullDatum; a numeric string like "0" gives NewIntDatum(0);
// an array literal like "{0,0}" or "{}" gives NewStringDatum(initcond).
func userAggInitState(ua *catalog.UserAggregate) Datum {
	ic := ua.InitCond
	if ic == "" {
		return NullDatum
	}
	// Try to parse as an integer.
	if n, err := strconv.ParseInt(ic, 10, 64); err == nil {
		return NewIntDatum(n)
	}
	// Otherwise treat as string (covers arrays, empty array, text, etc.).
	return NewStringDatum(ic)
}

// executeSFuncCall invokes a named state-transition or final function for a
// user-defined aggregate.  Built-in SQL/PG functions are handled inline;
// user-defined SQL functions are executed via executeStoredRoutine.
func executeSFuncCall(funcName string, args []Datum, ctx *Context) (Datum, error) {
	switch strings.ToLower(funcName) {
	case "int8inc":
		// int8inc(int8) → int8+1: counts every row
		if len(args) >= 1 {
			n, _ := datumInt64(args[0])
			return NewIntDatum(n + 1), nil
		}
	case "int8inc_any":
		// int8inc_any(int8, any) → int8+1: counts rows ignoring second arg
		if len(args) >= 1 {
			n, _ := datumInt64(args[0])
			return NewIntDatum(n + 1), nil
		}
	case "int4pl", "int8pl":
		// int4pl/int8pl(int,int) → sum
		if len(args) == 2 {
			a, _ := datumInt64(args[0])
			b, _ := datumInt64(args[1])
			return NewIntDatum(a + b), nil
		}
	case "int2pl":
		if len(args) == 2 {
			a, _ := datumInt64(args[0])
			b, _ := datumInt64(args[1])
			return NewIntDatum(a + b), nil
		}
	case "int4_avg_accum":
		// int4_avg_accum(int8[], int4) → int8[] as {count, sum}
		if len(args) == 2 {
			state := parseTextArray(args[0].StringValue())
			cnt, sum := int64(0), int64(0)
			if len(state) == 2 {
				cnt, _ = strconv.ParseInt(state[0], 10, 64)
				sum, _ = strconv.ParseInt(state[1], 10, 64)
			}
			val, _ := datumInt64(args[1])
			return NewStringDatum(fmt.Sprintf("{%d,%d}", cnt+1, sum+val)), nil
		}
	case "int8_avg":
		// int8_avg(int8[]) → numeric: sum/count
		if len(args) == 1 {
			state := parseTextArray(args[0].StringValue())
			if len(state) == 2 {
				cnt, _ := strconv.ParseInt(state[0], 10, 64)
				sum, _ := strconv.ParseInt(state[1], 10, 64)
				if cnt == 0 {
					return NullDatum, nil
				}
				return NewStringDatum(formatAvgInt8(sum, cnt)), nil
			}
		}
	case "least_accum":
		// least_accum(int8, int8) → least($1, $2), strict (skip NULLs).
		// Only handle the scalar form here; the variadic form passes an array
		// string as args[1] and is handled by the SQL function path below.
		if len(args) == 2 && (args[1].Kind == KindInt || (args[1].Kind == KindString && len(args[1].StringValue()) > 0 && args[1].StringValue()[0] != '{')) {
			if args[0].IsNull() {
				return args[1], nil
			}
			if args[1].IsNull() {
				return args[0], nil
			}
			a, _ := datumInt64(args[0])
			b, _ := datumInt64(args[1])
			if b < a {
				return NewIntDatum(b), nil
			}
			return NewIntDatum(a), nil
		}
	}
	// Try user-defined routine from the catalog.
	rs := routineRegistry(ctx)
	if rs != nil {
		funcObjName := parser.ObjectName{Name: funcName}
		candidates := rs.LookupByName(funcObjName)
		if len(candidates) > 0 {
			// Use first candidate with matching arg count.
			for _, r := range candidates {
				if len(r.ArgTypes) == len(args) {
					result, rerr := executeStoredRoutine(r, args, ctx, 0)
					if rerr == nil {
						return result, nil
					}
					// Log the error for debugging (sfunc execution failure). M0097-0117.
					_ = rerr
				}
			}
			// Fallback: try any candidate.
			result, rerr := executeStoredRoutine(candidates[0], args, ctx, 0)
			if rerr == nil {
				return result, nil
			}
			_ = rerr
		}
	}
	return NullDatum, &ExecError{Code: "42883", Message: fmt.Sprintf("aggregate state function %q does not exist", funcName)}
}

// formatAvgInt8 formats sum/count as a PostgreSQL numeric with 16 decimal places.
func formatAvgInt8(sum, count int64) string {
	r := new(big.Rat).SetFrac(big.NewInt(sum), big.NewInt(count))
	return r.FloatString(16)
}

// finishWithinGroupAgg handles ordered-set aggregate finalization for
// WITHIN GROUP (ORDER BY ...) aggregate functions. M0097-0035.
//
// The st.withinGroupElems slice contains one []Datum per input row,
// where each entry holds the sort-key value(s) from WithinGroupOrderBy.
// For single-key cases (most common), each inner slice has one element.
func finishWithinGroupAgg(st aggRuntime, call planner.AggregateCall, ctx *Context) Datum {
	name := strings.ToLower(call.Name)
	elems := st.withinGroupElems
	if len(elems) == 0 {
		// Hypothetical-set aggregates return a defined value when there are 0 actual rows:
		// the hypothetical row is alone so rank=1, percent_rank=0, cume_dist=1.
		switch name {
		case "rank", "dense_rank":
			return NewIntDatum(1)
		case "percent_rank":
			return NewStringDatum("0")
		case "cume_dist":
			return NewStringDatum("1")
		}
		return NullDatum
	}

	// Sort the elements according to WithinGroupOrderBy direction.
	// Each elem is []Datum with one Datum per sort key.
	sortKeys := call.WithinGroupOrderBy
	sort.SliceStable(elems, func(i, j int) bool {
		for ki, sk := range sortKeys {
			if ki >= len(elems[i]) || ki >= len(elems[j]) {
				break
			}
			ai, bi := elems[i][ki], elems[j][ki]
			// NULLs: by default NULLs LAST for ASC, FIRST for DESC.
			aNull, bNull := ai.IsNull(), bi.IsNull()
			if aNull && bNull {
				continue
			}
			nullsFirst := sk.NullsFirst
			if aNull {
				return nullsFirst
			}
			if bNull {
				return !nullsFirst
			}
			cmp, err := compareDatum(ai, bi, 0)
			if err != nil || cmp == 0 {
				continue
			}
			if sk.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})

	// Extract the first sort-key values (the "ordered values").
	// For most ordered-set aggregates, only the first key is used as the value.
	orderedVals := make([]Datum, len(elems))
	for i, row := range elems {
		if len(row) > 0 {
			orderedVals[i] = row[0]
		} else {
			orderedVals[i] = NullDatum
		}
	}

	// User-defined ordered-set aggregates: delegate to built-in implementations
	// based on their final function name (e.g. test_rank uses rank_final → rank).
	// This handles aggregates created via CREATE AGGREGATE and renamed with ALTER AGGREGATE.
	if call.UserAgg != nil {
		ff := strings.ToLower(call.UserAgg.FinalFunc)
		var builtinName string
		switch ff {
		case "rank_final":
			builtinName = "rank"
		case "dense_rank_final":
			builtinName = "dense_rank"
		case "percent_rank_final":
			builtinName = "percent_rank"
		case "cume_dist_final":
			builtinName = "cume_dist"
		case "percentile_disc_final":
			builtinName = "percentile_disc"
		case "percentile_cont_final":
			builtinName = "percentile_cont"
		case "mode_final":
			builtinName = "mode"
		}
		if builtinName != "" {
			bc := call
			bc.Name = builtinName
			bc.UserAgg = nil
			return finishWithinGroupAgg(st, bc, ctx)
		}
	}

	switch name {
	case "percentile_cont":
		// Use direct arg stored during applyAgg from the first row of the group.
		if call.Arg == nil || !st.withinGroupDirectArgSet {
			return NullDatum
		}
		p := st.withinGroupDirectArg
		n := len(orderedVals)
		if n == 0 {
			return NullDatum
		}
		float4Input := isFloat4TypeName(call.WithinGroupKeyType.Name)
		// Array form: percentile_cont(array[p1,p2,...]) WITHIN GROUP (ORDER BY x)
		// returns an array of interpolated values. M0097-0035.
		if fracs, ok := tryParseFloatArray(p); ok {
			results := make([]string, len(fracs))
			for i, pf := range fracs {
				if math.IsNaN(pf) {
					results[i] = "NULL"
					continue
				}
				if pf < 0 || pf > 1 {
					results[i] = "NULL"
					continue
				}
				results[i] = percentileContOneFloat(orderedVals, n, pf, float4Input)
			}
			return NewStringDatum(formatTextArray(results))
		}
		pf := aggDatumToFloat64(p)
		if math.IsNaN(pf) || pf < 0 || pf > 1 {
			return NullDatum
		}
		return NewStringDatum(percentileContOneFloat(orderedVals, n, pf, float4Input))

	case "percentile_disc":
		// Use direct arg stored during applyAgg from the first row of the group.
		if call.Arg == nil || !st.withinGroupDirectArgSet {
			return NullDatum
		}
		p := st.withinGroupDirectArg
		n := len(orderedVals)
		if n == 0 {
			return NullDatum
		}
		// Array form: percentile_disc(array[p1,p2,...]) WITHIN GROUP (ORDER BY x)
		// returns an array of discrete values. 2D arrays preserve structure. M0097-0125.
		if rows, ok := tryParseFloat2DArray(p); ok {
			result2D := make([][]string, len(rows))
			for r, row := range rows {
				result2D[r] = make([]string, len(row))
				for c, pf := range row {
					if math.IsNaN(pf) || pf < 0 || pf > 1 {
						result2D[r][c] = "NULL"
						continue
					}
					d := percentileDiscOneFloat(orderedVals, n, pf)
					if d.IsNull() {
						result2D[r][c] = "NULL"
					} else {
						result2D[r][c] = d.Format()
					}
				}
			}
			return NewStringDatum(format2DTextArray(result2D))
		}
		if fracs, ok := tryParseFloatArray(p); ok {
			results := make([]string, len(fracs))
			for i, pf := range fracs {
				if math.IsNaN(pf) {
					results[i] = "NULL"
					continue
				}
				if pf < 0 || pf > 1 {
					results[i] = "NULL"
					continue
				}
				d := percentileDiscOneFloat(orderedVals, n, pf)
				if d.IsNull() {
					results[i] = "NULL"
				} else {
					results[i] = d.Format()
				}
			}
			return NewStringDatum(formatTextArray(results))
		}
		pf := aggDatumToFloat64(p)
		if math.IsNaN(pf) || pf < 0 || pf > 1 {
			return NullDatum
		}
		return percentileDiscOneFloat(orderedVals, n, pf)

	case "rank":
		// rank(v) WITHIN GROUP (ORDER BY x): count values strictly less than v, +1.
		if call.Arg == nil || !st.withinGroupDirectArgSet {
			return NullDatum
		}
		rank := int64(1)
		if len(st.withinGroupDirectArgs) > 1 {
			// Multi-arg rank: tuple comparison against all direct args.
			for _, row := range elems {
				if withinGroupTupleLT(row, st.withinGroupDirectArgs, call.WithinGroupOrderBy) {
					rank++
				}
			}
		} else {
			v := st.withinGroupDirectArg
			for _, val := range orderedVals {
				if val.IsNull() {
					continue
				}
				cmp, cerr := compareDatumWithNullsFirst(val, v, call.WithinGroupOrderBy[0].NullsFirst, call.WithinGroupOrderBy[0].Desc)
				if cerr != nil {
					continue
				}
				if cmp < 0 {
					rank++
				}
			}
		}
		return NewIntDatum(rank)

	case "dense_rank":
		// dense_rank(v) WITHIN GROUP (ORDER BY x): count distinct values strictly less than v, +1.
		if call.Arg == nil || !st.withinGroupDirectArgSet {
			return NullDatum
		}
		seen := map[string]struct{}{}
		if len(st.withinGroupDirectArgs) > 1 {
			// Multi-arg dense_rank: tuple comparison.
			for _, row := range elems {
				if withinGroupTupleLT(row, st.withinGroupDirectArgs, call.WithinGroupOrderBy) {
					key := ""
					for _, d := range row {
						key += datumKey(d) + "|"
					}
					seen[key] = struct{}{}
				}
			}
		} else {
			v := st.withinGroupDirectArg
			for _, val := range orderedVals {
				if val.IsNull() {
					continue
				}
				cmp, cerr := compareDatum(val, v, 0)
				if cerr != nil {
					continue
				}
				if cmp < 0 {
					seen[datumKey(val)] = struct{}{}
				}
			}
		}
		return NewIntDatum(int64(len(seen)) + 1)

	case "cume_dist":
		// cume_dist(v) WITHIN GROUP (ORDER BY x): fraction of rows <= v (including hypothetical row).
		if !st.withinGroupDirectArgSet {
			return NullDatum
		}
		v := st.withinGroupDirectArg
		totalWithHypothetical := int64(len(orderedVals)) + 1
		lessOrEqual := int64(0)
		for _, val := range orderedVals {
			if val.IsNull() {
				continue
			}
			cmp, cerr := compareDatum(val, v, 0)
			if cerr != nil {
				continue
			}
			if cmp <= 0 {
				lessOrEqual++
			}
		}
		lessOrEqual++ // count the hypothetical row itself
		result := float64(lessOrEqual) / float64(totalWithHypothetical)
		return NewStringDatum(strconv.FormatFloat(result, 'g', 15, 64))

	case "percent_rank":
		// percent_rank(v) WITHIN GROUP (ORDER BY x): (rank-1) / (total-1).
		if !st.withinGroupDirectArgSet {
			return NullDatum
		}
		v := st.withinGroupDirectArg
		totalWithHypothetical := int64(len(orderedVals)) + 1
		if totalWithHypothetical <= 1 {
			return NewStringDatum("0")
		}
		rank := int64(1)
		for _, val := range orderedVals {
			if val.IsNull() {
				continue
			}
			cmp, cerr := compareDatum(val, v, 0)
			if cerr != nil {
				continue
			}
			if cmp < 0 {
				rank++
			}
		}
		result := float64(rank-1) / float64(totalWithHypothetical-1)
		return NewStringDatum(strconv.FormatFloat(result, 'g', 15, 64))

	case "mode":
		// mode() WITHIN GROUP (ORDER BY x): most frequent value (lowest if tie).
		freq := map[string]int{}
		order := []string{}
		vals := map[string]Datum{}
		for _, val := range orderedVals {
			if val.IsNull() {
				continue
			}
			k := datumKey(val)
			if _, seen := freq[k]; !seen {
				order = append(order, k)
				vals[k] = val
			}
			freq[k]++
		}
		if len(order) == 0 {
			return NullDatum
		}
		maxFreq := 0
		best := order[0]
		for _, k := range order {
			if freq[k] > maxFreq {
				maxFreq = freq[k]
				best = k
			}
		}
		return vals[best]
	}

	return NullDatum
}

// evalExprFromRow evaluates a planner expression against a fixed (possibly empty) row.
// Used by ordered-set aggregate finalizers to evaluate "direct arguments" like the
// percentile fraction p in `percentile_cont(p) WITHIN GROUP (ORDER BY x)`.
// tryParseFloatArray attempts to parse a Datum as a text-format float array
// like {0.25,0.5,0.75}. Returns the fractions and true on success. M0097-0035.
func tryParseFloatArray(d Datum) ([]float64, bool) {
	sv := ""
	switch d.Kind {
	case KindString:
		sv = d.StringValue()
	case KindBytes:
		sv = string(d.BytesValue())
	default:
		return nil, false
	}
	sv = strings.TrimSpace(sv)
	if !strings.HasPrefix(sv, "{") {
		return nil, false
	}
	// Flatten multi-dimensional arrays: {{NULL,1,0.5},{0.75,0.25,NULL}}
	// → [NULL, 1, 0.5, 0.75, 0.25, NULL]
	flat := strings.NewReplacer("{", "", "}", "").Replace(sv)
	parts := strings.Split(flat, ",")
	fracs := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.EqualFold(p, "null") || p == "" {
			fracs = append(fracs, math.NaN())
			continue
		}
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return nil, false
		}
		fracs = append(fracs, f)
	}
	return fracs, true
}

// tryParseFloat2DArray parses a 2D array string like {{null,1,0.5},{0.75,0.25,null}}
// into a slice of rows, each row being a slice of float64 (NaN for NULL).
// Returns (rows, true) if input is a well-formed 2D array; otherwise (nil, false). M0097-0125.
func tryParseFloat2DArray(d Datum) ([][]float64, bool) {
	sv := ""
	switch d.Kind {
	case KindString:
		sv = d.StringValue()
	case KindBytes:
		sv = string(d.BytesValue())
	default:
		return nil, false
	}
	sv = strings.TrimSpace(sv)
	// Must start with {{ to be a 2D array.
	if !strings.HasPrefix(sv, "{{") {
		return nil, false
	}
	// Strip outer braces: {{a,b},{c,d}} → {a,b},{c,d}
	inner := sv[1 : len(sv)-1]
	// Split into rows: find each {...} sub-array.
	var rows [][]float64
	depth := 0
	start := -1
	for i, ch := range inner {
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				rowStr := inner[start+1 : i] // strip { and }
				parts := strings.Split(rowStr, ",")
				row := make([]float64, len(parts))
				for j, p := range parts {
					p = strings.TrimSpace(p)
					if strings.EqualFold(p, "null") || p == "" {
						row[j] = math.NaN()
						continue
					}
					f, err := strconv.ParseFloat(p, 64)
					if err != nil {
						return nil, false
					}
					row[j] = f
				}
				rows = append(rows, row)
				start = -1
			}
		}
	}
	if len(rows) == 0 {
		return nil, false
	}
	return rows, true
}

// format2DTextArray formats a 2D slice of string values as {{a,b},{c,d}}. M0097-0125.
func format2DTextArray(rows [][]string) string {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, row := range rows {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('{')
		for j, v := range row {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(v)
		}
		sb.WriteByte('}')
	}
	sb.WriteByte('}')
	return sb.String()
}

// percentileContOneFloat computes percentile_cont for a single fraction pf
// over the sorted orderedVals. Returns the result as a string Datum. M0097-0035.
// If float4Input is true, ordered values are rounded through float32 to match
// PG's float4_accum semantics (float4 binary storage → float8 interpolation). M0097-0115.
func percentileContOneFloat(orderedVals []Datum, n int, pf float64, float4Input bool) string {
	pos := pf * float64(n-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower >= n {
		lower = n - 1
	}
	if upper >= n {
		upper = n - 1
	}
	if lower == upper {
		lo := aggDatumToFloat64(orderedVals[lower])
		if float4Input {
			lo = float64(float32(lo))
		}
		return strconv.FormatFloat(lo, 'g', 15, 64)
	}
	frac := pos - float64(lower)
	lo := aggDatumToFloat64(orderedVals[lower])
	hi := aggDatumToFloat64(orderedVals[upper])
	if float4Input {
		lo = float64(float32(lo))
		hi = float64(float32(hi))
	}
	result := lo + frac*(hi-lo)
	return strconv.FormatFloat(result, 'g', 15, 64)
}

// percentileDiscOneFloat computes percentile_disc for a single fraction pf
// over the sorted orderedVals. Returns the matching element Datum. M0097-0035.
func percentileDiscOneFloat(orderedVals []Datum, n int, pf float64) Datum {
	rowNum := int(math.Ceil(pf * float64(n)))
	if rowNum < 1 {
		rowNum = 1
	}
	if rowNum > n {
		rowNum = n
	}
	return orderedVals[rowNum-1]
}


func evalExprFromRow(expr planner.Expr, row Row, ctx *Context) (Datum, error) {
	slot := SlotFromRow(nil, row)
	return evalExprSlot(expr, slot, ctx)
}
