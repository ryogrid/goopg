package executor

import "github.com/goopg/goopg/internal/planner"

// Streaming LATERAL join — M0127-P4.4 (design leftdeep-joins/07 §4, the last
// row of the P4 table).
//
// The lateral join was the final operator in `joinOp` that still built its
// whole answer during `Open`: it drained the outer side into a `[]Row`, then
// for every outer row re-ran the right subtree, drained THAT, and appended
// every surviving concatenation into `o.rows` for `Next` to hand back one at a
// time. Peak memory was therefore |outer| + |inner for one outer row| + |output|,
// and the first row reached the caller only after the last one had been
// computed — so a `LIMIT 1` over a lateral join paid for the entire product.
//
// PG has no "lateral join" node: a LATERAL RHS is an ordinary parameterised
// inner side of `nodeNestloop.c`, re-scanned per outer tuple with the outer
// tuple's values installed as PARAM_EXEC parameters. This file keeps goopg's
// re-execution mechanics (there is no parameter machinery here — the right
// subtree is Open/Close'd per outer tuple, and correlation resolves through
// `ctx.OuterRows` or `lateralBindable`) and changes only WHEN the work happens:
// the outer side streams one tuple at a time, the inner side streams one tuple
// at a time under it, and nothing accumulates anywhere. Peak memory is now one
// outer tuple plus whatever the right subtree itself holds.
//
// The one thing streaming forces that the eager form got for free is
// correlation-context HYGIENE. The eager loop could push the outer row onto
// `ctx.OuterRows` for the whole duration of one iteration, because that
// iteration ran to completion inside `Open` and popped before returning to the
// caller. A streaming inner side yields control back to the PARENT operator
// between inner tuples, and a parent that evaluates its own `OuterColumnRef`
// (level=1) must not see this join's outer tuple sitting on top of the stack.
// So the binding is installed around every individual right-side call
// (`Open`, each `Next`, `Close`) and removed the moment it returns —
// `bindOuter`/`unbindOuter` below. The same window carries the per-outer-tuple
// `CTERowCache`, which is what makes an outer-dependent CTE inside the LATERAL
// re-materialise per outer row while leaving the enclosing scope's cache alone.

// lateralPhase* are the states of lateralJoinStream.
const (
	latPhaseOuter = iota // pull the next outer tuple and (re)open the right side
	latPhaseInner        // walk the right side for the current outer tuple
	latPhaseDone
)

// lateralJoinStream is the streaming replacement for the old eager
// `openLateral` loop. It mirrors nlJoinStream's shape (phase machine,
// reusable pair buffer, lazily resolved widths) but keeps the per-outer-tuple
// re-execution of the right subtree that LATERAL requires.
type lateralJoinStream struct {
	o *joinOp

	// Exactly one correlation mechanism is live per stream:
	//   - bindable != nil: the right child is a set-returning function that
	//     takes the outer tuple directly (pg_get_publication_tables & co.,
	//     M0103-0008). outerSlot is the slot it was handed at Open; its `row`
	//     field is repointed per outer tuple.
	//   - bindable == nil: the general path, which pushes the outer tuple onto
	//     ctx.OuterRows so OuterColumnRef (level=1) resolves against it.
	bindable  lateralBindable
	outerSlot *MaterializedSlot

	rw        int // right width, resolved from Schema() or the first inner tuple
	nullRight Row
	pair      Row // reusable outer++inner buffer for predicate evaluation

	fillOuter bool // LEFT lateral: null-extend an outer tuple nothing matched

	phase        int
	outerRow     Row
	outerMatched bool
	rightOpen    bool

	// innerCTE is the CTERowCache belonging to the CURRENT outer tuple. It is
	// swapped in for the duration of each right-side call and swapped back out
	// again, so the enclosing query's cache never sees the lateral's entries
	// and vice versa. Cleared when a new outer tuple arrives — that clearing is
	// the whole point (a CTE whose body reads the outer row must recompute).
	innerCTE map[string][]Row
	savedCTE map[string][]Row

	steps int
}

// openLateral handles `Join.Lateral == true`. The left side streams; for each
// left tuple the right subtree is re-opened with that tuple in scope and
// streamed in turn. Nothing is drained and nothing accumulates: see the file
// header for why the correlation binding is per-call rather than per-iteration.
//
// LEFT lateral joins emit a null-padded row when no right tuple satisfied the
// join predicate; CROSS / INNER drop the outer row.
func (o *joinOp) openLateral(ctx *Context) error {
	o.closeLateralStream()
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	m := &lateralJoinStream{
		o:         o,
		rw:        len(o.right.Schema()),
		fillOuter: o.plan.Type == planner.JoinTypeLeft,
	}
	if bindable, ok := o.right.(lateralBindable); ok {
		m.bindable = bindable
		m.outerSlot = SlotFromRow(o.left.Schema(), nil)
		bindable.BindLateralOuter(m.outerSlot)
	}
	o.latStream = m
	return nil
}

// closeLateralStream tears the correlation binding down. The children
// themselves belong to joinOp.Close, which runs right after this — including
// the right child this stream may have left open mid-flight (a LIMIT above the
// join closes us between inner tuples).
func (o *joinOp) closeLateralStream() {
	if o.latStream == nil {
		return
	}
	if o.latStream.bindable != nil {
		o.latStream.bindable.BindLateralOuter(nil)
	}
	o.latStream = nil
}

// nextLateral is joinOp.Next's arm for the streaming lateral join. Unlike the
// nested loop there is no ctid side-channel here: the eager path never captured
// one either (it used drainRowsCtx, not drainRowsCtxCTID), so a LockRows above
// a LATERAL join has always had to find its tuple another way.
func (o *joinOp) nextLateral() (TupleSlot, error) { //nolint:ireturn
	row, err := o.latStream.next()
	if err != nil {
		return nil, err
	}
	return asSlot(o.Schema(), row), nil
}

// next returns the join's next output row, or EOF.
func (m *lateralJoinStream) next() (Row, error) {
	for {
		m.steps++
		if m.steps&0xFF == 1 && m.o.ctx != nil && m.o.ctx.Ctx != nil {
			if err := m.o.ctx.Ctx.Err(); err != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		var (
			row Row
			ok  bool
			err error
		)
		switch m.phase {
		case latPhaseOuter:
			row, ok, err = m.stepOuter()
		case latPhaseInner:
			row, ok, err = m.stepInner()
		default:
			return nil, EOF
		}
		if err != nil {
			return nil, err
		}
		if ok {
			return row, nil
		}
	}
}

// stepOuter pulls the next outer tuple and re-opens the right subtree under it.
func (m *lateralJoinStream) stepOuter() (Row, bool, error) {
	slot, err := m.o.left.Next()
	if err == EOF {
		m.phase = latPhaseDone
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	row := slotRow(slot)
	// The outer tuple stays in scope for the whole right-side re-execution, so
	// it must survive the left child's next step.
	if rowHasArena(row) {
		m.outerRow = cloneRowOwned(row)
	} else {
		m.outerRow = append(m.outerRow[:0], row...)
	}
	m.outerMatched = false
	// Per-outer-tuple CTE cache: a LATERAL CTE whose body reads the outer row
	// must be re-materialised, not served from the previous tuple's result.
	m.innerCTE = nil

	m.bindOuter()
	openErr := m.o.right.Open(m.o.ctx)
	m.unbindOuter()
	if openErr != nil {
		return nil, false, openErr
	}
	m.rightOpen = true
	m.phase = latPhaseInner
	return nil, false, nil
}

// stepInner advances the right subtree by one tuple for the current outer
// tuple.
func (m *lateralJoinStream) stepInner() (Row, bool, error) {
	m.bindOuter()
	slot, err := m.o.right.Next()
	m.unbindOuter()
	if err == EOF {
		return m.finishOuter()
	}
	if err != nil {
		m.closeRight()
		m.phase = latPhaseDone
		return nil, false, err
	}
	inner := slotRow(slot)
	if m.rw == 0 && len(inner) > 0 {
		// A right child with an empty Schema() only reveals its width when its
		// first row arrives — the streaming form of the array path's
		// `width == 0 && len(rows) > 0` fallback.
		m.rw = len(inner)
	}
	n := len(m.outerRow) + len(inner)
	if cap(m.pair) < n {
		m.pair = make(Row, n)
	}
	m.pair = m.pair[:n]
	copy(m.pair, m.outerRow)
	copy(m.pair[len(m.outerRow):], inner)
	match, err := m.o.joinPredicateMatch(m.pair)
	if err != nil {
		m.closeRight()
		m.phase = latPhaseDone
		return nil, false, err
	}
	if !match {
		// A rejected pair costs zero allocations — the buffer is reused.
		return nil, false, nil
	}
	m.outerMatched = true
	return cloneRow(m.pair), true, nil
}

// finishOuter closes the right subtree for the current outer tuple and, for a
// LEFT lateral join, null-extends an outer tuple nothing matched.
func (m *lateralJoinStream) finishOuter() (Row, bool, error) {
	m.closeRight()
	m.phase = latPhaseOuter
	if !m.outerMatched && m.fillOuter {
		if len(m.nullRight) != m.rw {
			m.nullRight = nullRow(m.rw)
		}
		return concatRows(m.outerRow, m.nullRight), true, nil
	}
	return nil, false, nil
}

// closeRight ends the current outer tuple's right-side re-execution. The Close
// runs inside the correlation window, exactly as it did in the eager loop —
// an operator that flushes on Close can still resolve the outer row.
func (m *lateralJoinStream) closeRight() {
	if !m.rightOpen {
		return
	}
	m.bindOuter()
	_ = m.o.right.Close()
	m.unbindOuter()
	m.rightOpen = false
}

// bindOuter installs the current outer tuple's correlation context for the
// duration of ONE right-side call. See the file header: the window has to be
// this narrow because a streaming inner side hands control back to the parent
// operator between tuples, and the parent's own OuterColumnRefs must not
// resolve against this join's outer tuple.
func (m *lateralJoinStream) bindOuter() {
	if m.bindable != nil {
		// The SRF path never touches ctx: the outer tuple travels through the
		// slot the child was handed at Open. Repointing per call (rather than
		// per outer tuple) is required because outerRow's backing array moves
		// when it grows.
		m.outerSlot.row = m.outerRow
		return
	}
	ctx := m.o.ctx
	if ctx == nil {
		return
	}
	m.savedCTE = ctx.CTERowCache
	ctx.OuterRows = append(ctx.OuterRows, m.outerRow)
	ctx.CTERowCache = m.innerCTE
}

// unbindOuter removes what bindOuter installed, carrying the right side's CTE
// materialisations forward to the next call for the SAME outer tuple.
func (m *lateralJoinStream) unbindOuter() {
	if m.bindable != nil {
		return
	}
	ctx := m.o.ctx
	if ctx == nil {
		return
	}
	m.innerCTE = ctx.CTERowCache
	ctx.CTERowCache = m.savedCTE
	m.savedCTE = nil
	if n := len(ctx.OuterRows); n > 0 {
		ctx.OuterRows = ctx.OuterRows[:n-1]
	}
}
