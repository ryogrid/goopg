package executor

// Streaming nested-loop join (M0127-P4.3; design leftdeep-joins/07 §4).
//
// What this replaces: `runNestedLoop`, which ran inside Open, drained BOTH
// children into `[]Row`, built every candidate pair with `concatRows`, and
// pushed every surviving pair into `o.rows` for Next to hand out later. Three
// full copies of the join's data — the outer array, the inner array, and the
// output array — for an operator PG runs with one tuple per side plus a
// rescannable inner.
//
// The shape here is PG's nodeNestloop.c:
//
//   - the OUTER side streams, one tuple at a time (ENL_OUTERTUPLE);
//   - the INNER side sits under a Materialize (`operators_material.go`) whose
//     first pass caches it and whose Rescan replays the cache, so the loop
//     re-reads it per outer tuple without re-executing the subtree;
//   - the join emits as Next asks. Nothing accumulates.
//
// `concatRows`-per-candidate-pair dies with it: the predicate is evaluated
// against ONE reusable merged buffer (`pair`) and a fresh Row is allocated
// only for a pair that is actually emitted. Allocation is now proportional to
// output, not to N×M.
//
// Semantics preserved verbatim from runNestedLoop, including the cases that
// are not obvious:
//
//   - keyless Semi/Anti (S4a D3.2) emit the OUTER row only, once, and break
//     out of the inner scan on the first qualifying inner row. The Materialize
//     is what makes that early-out safe: PG's `eof_underlying` rule lets the
//     next outer row resume reading the child past the break point.
//   - RIGHT/FULL sweep unmatched inner rows at the end, with M0097-0060's
//     FULL JOIN USING coalescing copying each USING column from the right
//     position to the left one.
//   - M0100-0010's left-side ctid side-channel: the outer scan leaf is read
//     per outer tuple instead of per drained array element.
//   - M0058-0005 / M0062-followup cancellation: checked per state-machine
//     step, and every step consumes an outer tuple, an inner tuple or a sweep
//     tuple, so the bound is at least as tight as the two old loop counters.

import (
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/optimizer"
)

// nlInnerWorkMemEnabled gates the work_mem bound on the nested loop's inner
// Materialize. See openNestedLoop for why the default is off.
var nlInnerWorkMemEnabled = os.Getenv("GOOPG_NL_MATERIALIZE_WORK_MEM") == "1"

// nlBareReexec selects the STRICT bare-inner semantics (M0146-0010): when the
// plan elects no `*optimizer.Materialize` over a nested-loop inner, the rescan
// re-executes the subtree — `cost_rescan`'s default arm, and what PG's
// ExecReScan does. The default is off: the inner is still wrapped in an
// attach-time materializeOp exactly as before this task, because the strict
// semantics exposed a real executor cliff that is not this task's to fix —
// TPC-DS Q14's second statement joins two aggregate subqueries under a
// Nested Loop whose inner is a self-contained (unparameterised) subtree.
// The planner elects the bare inner faithfully (outer est. rows=1 makes the
// matpath lose by epsilon; PG's own plan shows no Materialize there either),
// but goopg's inner subtree is a hash-semi-join pipeline costed ~18825 where
// PG's is a ~2730 correlated NL/index-probe chain — a pre-existing join-order
// gap for CTE-backed semi inners (the un-wired `joinIsLegal` unique_ified arm,
// M0142-0008c-2), invisible while every inner was cached. Under strict
// semantics the ~830 actual outer rows re-execute that subtree wholesale —
// measured 11 s → >300 s on the SF0.25 sweep, a deterministic gate regression.
// Until that join-order fix lands, the compat wrap keeps the runtime; the
// plan/executor disagreement it preserves is a stated debt, narrower than
// before (the wrap now fires only where the plan did NOT elect Materialize,
// never duplicating an elected one). GOOPG_NL_BARE_REEXEC=1 flips to strict
// for diagnostics and for the eventual default flip.
var nlBareReexec = os.Getenv("GOOPG_NL_BARE_REEXEC") == "1"

const (
	nlPhaseOuter = iota // need the next outer tuple
	nlPhaseInner        // scanning the inner side for the current outer tuple
	nlPhaseSweep        // RIGHT/FULL: emitting unmatched inner tuples
	nlPhaseDone
)

// nlJoinStream is the state machine. One instance per Open.
type nlJoinStream struct {
	o *joinOp
	// inner is whichever rescannable serves the inner side (M0146-0010): the
	// materializeOp a `*optimizer.Materialize` plan node built, the compat
	// attach-time cache over a bare inner (default), or — under
	// GOOPG_NL_BARE_REEXEC — a rescanByReexec re-executing a bare inner.
	inner rescannable
	// mat is the *materializeOp half of inner, kept beside it so Close can
	// release the buffer without a type assertion at the close site.
	mat *materializeOp

	lw, rw    int
	nullLeft  Row
	nullRight Row
	pair      Row // reusable lw+rw merge buffer for predicate evaluation

	semiAnti  bool
	anti      bool
	fillOuter bool // LEFT / FULL: null-extend an outer tuple that matched nothing
	fillInner bool // RIGHT / FULL: sweep inner tuples that matched nothing

	phase int

	outerRow     Row
	outerCTID    joinRowCTID
	outerMatched bool

	// innerMatched is one bit per inner tuple ORDINAL. The Materialize replays
	// in insertion order, so the ordinal is just the count of tuples returned
	// since the last Rescan — no key, no map. Sized by row count, so it stays
	// small even when the cache overflowed to disk.
	innerMatched []bool
	innerIdx     int

	sweepIdx int

	scanLeaf currentTIDProvider
	steps    int
}

// openNestedLoop opens both children and prepares the streaming join. When
// the plan carries a `*optimizer.Materialize` inner, buildNode's arm already
// built it as o.right — a real operator owning its child's lifecycle through
// its own Open/Close. A bare inner gets the compat attach-time cache by
// default (or rescanByReexec under GOOPG_NL_BARE_REEXEC) — see nlBareReexec.
//
// captureCTID selects M0100-0010's outer-side ctid side-channel. It is on for
// the general join path (which is what lockRowsOp sits above) and off for the
// keyless semi/anti path, matching which of the two old drains captured TIDs.
func (o *joinOp) openNestedLoop(ctx *Context, captureCTID bool) error {
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		_ = o.left.Close()
		return err
	}
	// M0146-0010: the wrap is conditional on the PLAN now. PG elects
	// materialisation at the path level (joinpath.c:1890-1901 files the
	// matpath beside the bare inner; addPath elects on cost), so a
	// `*optimizer.Materialize` node is present exactly when the planner
	// priced and chose it — the executor follows the plan rather than
	// deciding for it.
	var inner rescannable
	var mat *materializeOp
	if _, planned := o.plan.Right.(*optimizer.Materialize); planned {
		// buildNode's Materialize arm already constructed the cache: o.right
		// fronts it — directly (legacy), under an instrumentedOp (ANALYZE), or
		// through the op-tree adapter (BuildFast). Rescan/Next flow through
		// the wrapper so ANALYZE counters stay live; the concrete
		// materializeOp is still dug out for the unbounded/default and the
		// Close-time cache release.
		mat = planMaterializeOp(o.right)
		rs, ok := o.right.(rescannable)
		if mat == nil || !ok {
			// A plan that says Materialize over an inner that built no
			// materializeOp is a producer/executor disagreement — fail closed
			// rather than wrap a second buffer and hide it.
			_ = o.left.Close()
			_ = o.right.Close()
			return fmt.Errorf("nested loop: plan elected Materialize over the inner but no materializeOp was built")
		}
		if !nlInnerWorkMemEnabled {
			// The inner cache runs UNBOUNDED by default, which is exactly
			// what the pre-P4.3 `drainRowsCtx` did. The bound is not declined
			// out of caution: measured on TPC-DS SF0.5 Q54, whose plan is a
			// nested loop over a 1.44M-row `store_sales` seq scan (~1.6 GB as
			// `[]Datum`), the work_mem-bounded cache spills and then every
			// outer tuple replays the whole file with full datum decoding —
			// 144 s → >400 s, the sweep's only regression. PG never meets
			// that wall because `cost_rescan` prices exactly this case and
			// the planner picks another path — which is now true here too
			// (costMaterial + materialRescanCost, optimizer/materialize.go);
			// re-timing the default is slice 4 of the same task.
			// `GOOPG_NL_MATERIALIZE_WORK_MEM=1` turns the bound on for the
			// A/B, exactly as P4.2's gate does for the hash outer fill.
			mat.setUnbounded()
		}
		inner = rs
	} else if nlBareReexec {
		// The strict bare-inner arm: no Materialize node means PG's cost
		// model priced (and chose) a rescan that RE-EXECUTES the inner —
		// the cost_rescan default arm. rescanByReexec performs exactly
		// that. Default-off pending the Q14 join-order fix; see
		// nlBareReexec's comment.
		inner = &rescanByReexec{op: o.right, ctx: ctx}
	} else {
		// Compat arm (pre-M0146-0010 semantics, kept): attach a cache over
		// the ALREADY-OPEN bare inner — joinOp owns the child's lifecycle,
		// so the cache is attached, not opened. Correct because a
		// non-parameterised inner is deterministic across rescans; the
		// plan/executor disagreement is the documented debt.
		m := newMaterializeOp(o.right)
		m.openCached(ctx)
		if !nlInnerWorkMemEnabled {
			m.setUnbounded()
		}
		mat = m
		inner = m
	}

	m := &nlJoinStream{
		o:         o,
		inner:     inner,
		mat:       mat,
		lw:        len(o.left.Schema()),
		rw:        len(o.right.Schema()),
		semiAnti:  o.plan.Type == optimizer.JoinTypeSemi || o.plan.Type == optimizer.JoinTypeAnti,
		anti:      o.plan.Type == optimizer.JoinTypeAnti,
		fillOuter: o.plan.Type == optimizer.JoinTypeLeft || o.plan.Type == optimizer.JoinTypeFull,
		fillInner: o.plan.Type == optimizer.JoinTypeRight || o.plan.Type == optimizer.JoinTypeFull,
	}
	if captureCTID {
		// The ctid side-channel is an optimisation for LockRows;
		// if an unrecognised operator sits between the join and its
		// scan leaf, skip the capture rather than failing the join.
		// LockRows will diagnose the unsupported shape through its
		// own findScanLeaf walker at Open time.
		if sl, err := findScanLeaf(o.left); err == nil {
			m.scanLeaf = sl
			// The capture reads sl.currentTID after each outer row.
			disableFilterReadAhead(o.left)
		}
	}
	o.nlStream = m
	return nil
}

// closeNLStream releases the inner cache (and its spill file). The children
// themselves belong to joinOp.Close.
func (o *joinOp) closeNLStream() {
	if o.nlStream == nil {
		return
	}
	// A bare inner has no cache to release — rescanByReexec owns nothing.
	if o.nlStream.mat != nil {
		o.nlStream.mat.releaseCache()
	}
	o.nlStream = nil
}

// nextNL is joinOp.Next's arm for the streaming nested loop.
func (o *joinOp) nextNL() (TupleSlot, error) { //nolint:ireturn
	row, ctid, err := o.nlStream.next()
	if err != nil {
		return nil, err
	}
	if ctid.hasCTID {
		// M0100-0010: propagate the outer-side ctid so a downstream LockRows
		// can stamp tuple locks even though the scan is buried under the join.
		ms := SlotFromRow(o.Schema(), row)
		ms.hasCTID = true
		ms.ctidBlock = uint32(ctid.ptr.Block)
		ms.ctidOff = ctid.ptr.Offset
		return ms, nil
	}
	// NOT asSlot: the stream signals absence with EOF, never with a nil Row, so
	// every (row, nil) return here is a real tuple. A join of two zero-column
	// relations (`SELECT * FROM nocols a, nocols b` — PG emits one 0-column row)
	// builds its pair as cloneRow(m.pair) with n == 0, which is nil; asSlot maps
	// that back to a nil TupleSlot, and the caller reads a nil error as "row
	// present" and dereferences it. The ctid arm above has always built the slot
	// unconditionally — this arm is its sibling and must agree.
	return SlotFromRow(o.Schema(), row), nil
}

// next returns the join's next output row, its outer-side ctid, or EOF.
func (m *nlJoinStream) next() (Row, joinRowCTID, error) {
	for {
		m.steps++
		if m.steps&0xFF == 1 && m.o.ctx != nil && m.o.ctx.Ctx != nil {
			if err := m.o.ctx.Ctx.Err(); err != nil {
				return nil, joinRowCTID{}, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		var (
			row Row
			ok  bool
			err error
		)
		switch m.phase {
		case nlPhaseOuter:
			row, ok, err = m.stepOuter()
		case nlPhaseInner:
			row, ok, err = m.stepInner()
		case nlPhaseSweep:
			row, ok, err = m.stepSweep()
		default:
			return nil, joinRowCTID{}, EOF
		}
		if err != nil {
			return nil, joinRowCTID{}, err
		}
		if ok {
			// Only rows produced from the current outer tuple carry its ctid;
			// a sweep row has no outer source (the old path wrote -1 into
			// rowSourceLeft for exactly this case).
			if m.phase == nlPhaseSweep {
				return row, joinRowCTID{}, nil
			}
			return row, m.outerCTID, nil
		}
	}
}

// stepOuter pulls the next outer tuple and rewinds the inner cache for it.
func (m *nlJoinStream) stepOuter() (Row, bool, error) {
	slot, err := m.o.left.Next()
	if err == EOF {
		// Outer exhausted. RIGHT/FULL still owe their unmatched inner tuples;
		// everything else is done.
		if !m.fillInner {
			m.phase = nlPhaseDone
			return nil, false, nil
		}
		// The inner may never have been read at all (an empty outer side), in
		// which case the cache is empty and the sweep has to fill it first —
		// draining it here is what makes `SELECT ... RIGHT JOIN` over an empty
		// left side still emit every right row.
		if err := m.drainInner(); err != nil {
			return nil, false, err
		}
		if err := m.inner.Rescan(); err != nil {
			return nil, false, err
		}
		m.innerIdx = 0
		m.sweepIdx = 0
		m.phase = nlPhaseSweep
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	row := slotRow(slot)
	// The outer tuple is read once and revisited for every inner tuple, so it
	// must survive the child's next step.
	if rowHasArena(row) {
		m.outerRow = cloneRowOwned(row)
	} else {
		m.outerRow = append(m.outerRow[:0], row...)
	}
	if m.lw == 0 && len(m.outerRow) > 0 {
		// A child with an empty Schema() (a Values node, a subplan built
		// without one) only reveals its width when its first row arrives —
		// the streaming form of the array path's `width == 0 && len(rows) > 0`
		// fallback.
		m.lw = len(m.outerRow)
	}
	if m.scanLeaf != nil {
		rel, ptr, ok := m.scanLeaf.currentTID()
		m.outerCTID = joinRowCTID{rel: rel, ptr: ptr, hasCTID: ok}
	}
	if err := m.inner.Rescan(); err != nil {
		return nil, false, err
	}
	m.innerIdx = 0
	m.outerMatched = false
	m.phase = nlPhaseInner
	return nil, false, nil
}

// stepInner advances the inner scan by one tuple for the current outer tuple.
func (m *nlJoinStream) stepInner() (Row, bool, error) {
	slot, err := m.inner.Next()
	if err == EOF {
		return m.finishOuter()
	}
	if err != nil {
		return nil, false, err
	}
	j := m.innerIdx
	m.innerIdx++
	inner := slotRow(slot)
	if m.rw == 0 && len(inner) > 0 {
		m.rw = len(inner)
	}
	match, err := m.pairMatches(inner)
	if err != nil {
		return nil, false, err
	}
	if !match {
		// A NULL predicate result counts as no-match (joinPredicateMatch),
		// which is exactly the semi/anti contract too.
		return nil, false, nil
	}
	m.outerMatched = true
	if m.semiAnti {
		// S4a (D3.2): one qualifying inner tuple decides this outer tuple.
		// Never emit the joined row (the join's schema is outer-only) and
		// never scan further inner tuples.
		return m.finishOuter()
	}
	if m.fillInner {
		m.markInner(j)
	}
	return cloneRow(m.pair), true, nil
}

// finishOuter closes out the current outer tuple: semi/anti decide here, and
// LEFT/FULL null-extend an outer tuple nothing matched.
func (m *nlJoinStream) finishOuter() (Row, bool, error) {
	m.phase = nlPhaseOuter
	if m.semiAnti {
		hit := m.outerMatched
		if m.anti {
			hit = !m.outerMatched
		}
		if hit {
			return append(Row(nil), m.outerRow...), true, nil
		}
		return nil, false, nil
	}
	if !m.outerMatched && m.fillOuter {
		m.ensurePads()
		return concatRows(m.outerRow, m.nullRight), true, nil
	}
	return nil, false, nil
}

// stepSweep emits the inner tuples no outer tuple matched (RIGHT / FULL).
func (m *nlJoinStream) stepSweep() (Row, bool, error) {
	slot, err := m.inner.Next()
	if err == EOF {
		m.phase = nlPhaseDone
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	j := m.sweepIdx
	m.sweepIdx++
	if j < len(m.innerMatched) && m.innerMatched[j] {
		return nil, false, nil
	}
	inner := slotRow(slot)
	if m.rw == 0 && len(inner) > 0 {
		m.rw = len(inner)
	}
	m.ensurePads()
	merged := concatRows(m.nullLeft, inner)
	// M0097-0060: FULL JOIN USING coalescing. For an unmatched inner tuple the
	// outer side is all NULL, so each USING column value is copied from the
	// right position to the left one, making `SELECT *` see
	// COALESCE(left.col, right.col) = right.col.
	m.o.coalesceUsingRow(merged)
	return merged, true, nil
}

// drainInner reads the inner side to completion. Only the sweep needs this:
// every other path has already walked the cache at least once, and a RIGHT or
// FULL join over an empty outer side would otherwise never touch it.
func (m *nlJoinStream) drainInner() error {
	for {
		_, err := m.inner.Next()
		if err == EOF {
			return nil
		}
		if err != nil {
			return err
		}
		m.steps++
		if m.steps&0xFFF == 1 && m.o.ctx != nil && m.o.ctx.Ctx != nil {
			if err := m.o.ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
	}
}

// pairMatches composes the current outer tuple with `inner` into the reusable
// buffer and evaluates the join predicate against it. The buffer is what makes
// a rejected candidate pair cost zero allocations.
func (m *nlJoinStream) pairMatches(inner Row) (bool, error) {
	n := len(m.outerRow) + len(inner)
	if cap(m.pair) < n {
		m.pair = make(Row, n)
	}
	m.pair = m.pair[:n]
	copy(m.pair, m.outerRow)
	copy(m.pair[len(m.outerRow):], inner)
	return m.o.joinPredicateMatch(m.pair)
}

// markInner records that inner ordinal j matched. Ordinals arrive in order on
// the first pass, so the bitmap grows by append and is indexed afterwards.
func (m *nlJoinStream) markInner(j int) {
	for len(m.innerMatched) <= j {
		m.innerMatched = append(m.innerMatched, false)
	}
	m.innerMatched[j] = true
}

// ensurePads (re)builds the null padding rows once the two widths are known.
// Widths can only grow (0 → resolved from the first tuple), never change.
func (m *nlJoinStream) ensurePads() {
	if len(m.nullLeft) != m.lw {
		m.nullLeft = nullRow(m.lw)
	}
	if len(m.nullRight) != m.rw {
		m.nullRight = nullRow(m.rw)
	}
}
