package executor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

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
			o.rows = append(o.rows, concatRows(nullLeft, r))
		}
	}
	return nil
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
				o.rows = append(o.rows, concatRows(nullLeft, rightKeyed[j].row))
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
			o.rows = append(o.rows, concatRows(nullLeft, rightKeyed[j].row))
		}
	}

	if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
		for _, l := range leftNull {
			o.rows = append(o.rows, concatRows(l, nullRight))
		}
	}
	if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
		for _, r := range rightNull {
			o.rows = append(o.rows, concatRows(nullLeft, r))
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
}

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
	distinct   map[string]struct{}
	// Extended aggregate accumulators (M0097-0007).
	boolResult bool   // for bool_and / bool_or / every
	intResult  int64  // for bit_and / bit_or / bit_xor
	strResult  string // for string_agg
}

func newAggregateOp(plan *planner.Aggregate, child Operator) *aggregateOp {
	return &aggregateOp{plan: plan, child: child, schema: plan.Output()}
}

func (o *aggregateOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}

	type groupRuntime struct {
		groupValues Row
		aggs        []aggRuntime
	}

	groups := map[string]*groupRuntime{}
	order := make([]string, 0)

	if len(o.plan.GroupExprs) == 0 {
		groups["__all__"] = &groupRuntime{groupValues: nil, aggs: make([]aggRuntime, len(o.plan.Aggs))}
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
		row := slotRow(slot)

		key, groupValues, err := o.evalGroupKey(row)
		if err != nil {
			return err
		}
		gr, ok := groups[key]
		if !ok {
			gr = &groupRuntime{groupValues: groupValues, aggs: make([]aggRuntime, len(o.plan.Aggs))}
			groups[key] = gr
			order = append(order, key)
		}

		for i, call := range o.plan.Aggs {
			if err := o.applyAgg(&gr.aggs[i], call, row); err != nil {
				return err
			}
		}
	}

	o.rows = make([]Row, 0, len(order))
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
		out := make(Row, 0, len(gr.groupValues)+len(o.plan.Aggs))
		out = append(out, gr.groupValues...)
		for i, call := range o.plan.Aggs {
			out = append(out, o.finishAgg(gr.aggs[i], call))
		}
		o.rows = append(o.rows, out)
	}
	return nil
}

func (o *aggregateOp) evalGroupKey(row Row) (string, Row, error) {
	if len(o.plan.GroupExprs) == 0 {
		return "__all__", nil, nil
	}
	vals := make(Row, 0, len(o.plan.GroupExprs))
	parts := make([]string, 0, len(o.plan.GroupExprs))
	for _, g := range o.plan.GroupExprs {
		v, err := evalExpr(g, row, o.ctx)
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

func (o *aggregateOp) applyAgg(st *aggRuntime, call planner.AggregateCall, row Row) error {
	// FILTER (WHERE condition): skip this row if the condition is false/null.
	// M0097-0007.
	if call.Filter != nil {
		fv, ferr := evalExpr(call.Filter, row, o.ctx)
		if ferr != nil || fv.IsNull() || fv.Kind != KindBool || !fv.BoolValue() {
			return nil // skip row — filter not satisfied
		}
	}

	name := strings.ToLower(call.Name)
	if call.Star {
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

	if call.Arg == nil {
		// Zero-arg extended aggregate stub — just count rows.
		st.count++
		return nil
	}
	arg, err := evalExpr(call.Arg, row, o.ctx)
	if err != nil {
		return err
	}
	if arg.IsNull() {
		return nil
	}

	if call.Distinct {
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
		}
	case "bit_or":
		if arg.Kind == KindInt {
			if !st.hasValue {
				st.intResult = arg.Int
				st.hasValue = true
			} else {
				st.intResult |= arg.Int
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
		}
	case "string_agg":
		// string_agg(expr, delimiter) — accumulate in strResult with delimiter.
		sv := arg.Format()
		if !st.hasValue {
			st.strResult = sv
			st.hasValue = true
		} else {
			// Use comma as default delimiter; second arg would refine this.
			st.strResult += "," + sv
		}
	case "any_value":
		// any_value(x) — return the first non-null value seen.
		if !st.hasValue && !arg.IsNull() {
			st.value = arg.MaterializeArena()
			st.hasValue = true
		}
	default:
		// Extended aggregates (var_pop, stddev, percentile, etc.):
		// stub — accumulate into sum/count for numeric, ignore otherwise.
		// M0097-0007.
		st.count++
		if arg.Kind == KindInt {
			st.sum += arg.Int
		}
	}
	return nil
}

func (o *aggregateOp) finishAgg(st aggRuntime, call planner.AggregateCall) Datum {
	switch strings.ToLower(call.Name) {
	case "count":
		return Datum{Kind: KindInt, Int: st.count}
	case "sum":
		if !st.hasValue {
			return NullDatum
		}
		if st.numericSum.Kind == KindNumeric {
			return st.numericSum
		}
		return Datum{Kind: KindInt, Int: st.sum}
	case "avg":
		if st.count == 0 {
			return NullDatum
		}
		if st.numericSum.Kind == KindNumeric {
			d, err := numericDiv(st.numericSum, numericFromInt(st.count), call.Pos())
			if err != nil {
				return NullDatum
			}
			return d
		}
		return Datum{Kind: KindInt, Int: st.sum / st.count}
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
		return Datum{Kind: KindInt, Int: st.intResult}
	case "string_agg":
		if !st.hasValue {
			return NullDatum
		}
		return NewStringDatum(st.strResult)
	case "any_value":
		if !st.hasValue {
			return NullDatum
		}
		return st.value
	}
	// Extended aggregates (var_pop, stddev, etc.) — stub: return NULL.
	// M0097-0007.
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
	case KindString, KindStringArena:
		return "s:" + d.StringValue()
	case KindBytes, KindBytesArena:
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
