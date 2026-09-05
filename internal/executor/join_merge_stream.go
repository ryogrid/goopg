package executor

// join_merge_stream.go — M0127-P4.1, design leftdeep-joins/07 §2.
//
// What this file replaces. Until P4.1 a goopg merge join ran entirely on
// materialised arrays: `joinOp.Open` drained BOTH children into `[]Row`,
// `buildMergeSide` keyed and `sort.SliceStable`-sorted each side into a second
// full-size array, and `runMergeJoin` appended EVERY joined row into `o.rows`
// before `Next` returned its first tuple. Three unbounded copies of a join's
// working set, none of them answerable to `work_mem`, for an operator whose
// upstream analogue (`nodeMergejoin.c`) holds exactly one tuple per side plus
// the current inner group.
//
// What it is now. Each side becomes a `mergeSortedSource`: a key-ordered
// STREAM whose resident set is bounded by `work_mem` — chunks that exceed the
// budget are sorted, written to a spill run and freed, and the runs are
// N-way merged back on the way out (the same shape `sortOp` has had since
// M0068-0006, keyed here on the merge key TUPLE rather than on a SortKey list,
// because the key expressions live in the MERGED left++right column space and
// only a `mergedKeySlot` can present a bare child row in it). The join itself
// is PG's state machine: pull one tuple per side, advance the lesser, and
// buffer only the INNER equal-key group — in memory while it fits `work_mem`,
// overflowing to a spill file past that. Output leaves through `Next`, one row
// at a time, and `o.rows` is never touched.
//
// Group buffering, not mark/restore. PG rewinds the inner side with
// `ExecMarkPos`/`ExecRestrPos` and so never materialises a group at all. goopg
// has no operator-level mark/restore, so v1 buffers the group (07 §2 states
// this explicitly). The bound is what matters: a group is at most one distinct
// key's worth of inner rows, where the old code held the whole side.
//
// Emission order is deliberately identical to the array implementation, so
// that neither the regress `.out` files nor the TPC-DS SF0.5 checksums can
// move under a change whose whole point is memory shape:
//
//	1. the merge proper, in key order; within an equal-key group, every
//	   left row against every buffered inner row, then that left row's
//	   null-extension if the residual rejected all of them, then the
//	   group's unmatched inner rows;
//	2. the tail: remaining left rows, then remaining right rows;
//	3. the NULL-keyed rows: left, then right.
//
// (3) is why a NULL key is carried as a per-row flag that sorts AFTER every
// real key rather than being filed in a side list as `buildMergeSide` did: a
// stream cannot come back for a side list, but it can be told that the null
// rows are last. The four-state tail below walks (2) and (3) in the old order
// while holding one row per side.

import (
	"fmt"
	"io"
	"sort"

	"github.com/goopg/goopg/internal/optimizer"
)

// errMergeKeyNil is the diagnosis buildMergeSide raised when a Join reached
// the merge executor without a usable key. Kept verbatim: initMergeKeys
// deliberately leaves the single-pair slot populated with the plan's (possibly
// nil) LeftKey/RightKey so this, and not a silent join on nothing, is what a
// keyless merge join produces.
func errMergeKeyNil() error { return fmt.Errorf("merge join key is nil") }

// mergeStreamRow is one row of a sorted merge side together with its evaluated
// key tuple. nullKey marks a row with a NULL in ANY key column: it can match
// nothing (the componentwise rule initMergeKeys documents) and sorts last.
type mergeStreamRow struct {
	row     Row
	keys    []Datum
	nullKey bool
}

// mergeKeyBlockRows is how many key tuples one backing block holds. The keys
// of a run are carved from blocks rather than allocated per row, so a side
// costs one allocation per this many rows; blocks are never appended to, so a
// carved sub-slice can never be invalidated by a growth reallocation (the
// property buildMergeSide got from its single exactly-sized store).
const mergeKeyBlockRows = 1024

// mergeSortedSource turns a child operator into a key-ordered stream of
// mergeStreamRow, spilling sorted runs once the resident chunk exceeds the
// budget. Constructed per side, drained during Open, streamed by next().
type mergeSortedSource struct {
	o        *joinOp
	child    Operator
	isLeft   bool
	keyExprs []optimizer.Expr
	nkeys    int

	// Widths of the merged column space this side's key expressions are
	// written against: selfWidth columns of real row, otherWidth of NULL.
	selfWidth  int
	otherWidth int

	keySlot  mergedKeySlotCache
	rowSlot  *MaterializedSlot
	keyBlock []Datum

	// limit is the resident-chunk budget in bytes (work_mem); 0 = unlimited.
	limit int64

	// primed holds the row pulled before the widths were known (see prime).
	primed    Row
	hasPrimed bool
	primedEOF bool

	// tail is the final in-memory run; cursors is non-empty only once a
	// chunk has spilled, in which case tail is the last cursor.
	tail    []mergeStreamRow
	tailIdx int
	cursors []*mergeRunCursor

	sortErr error
}

// mergeRunCursor is one sorted run — a spill file or the in-memory tail —
// positioned on its current row. Cursors are ordered by creation, which is the
// order the rows were pulled from the child, so breaking a comparison tie by
// cursor index makes the N-way merge as stable as the per-chunk sort.
type mergeRunCursor struct {
	src  *mergeSortedSource
	mem  []mergeStreamRow
	idx  int
	r    *spillReader
	path string

	cur  mergeStreamRow
	live bool
}

func newMergeSortedSource(o *joinOp, child Operator, isLeft bool) (*mergeSortedSource, error) {
	keyExprs := o.mergeSideKeyExprs(isLeft)
	if len(keyExprs) == 0 {
		return nil, errMergeKeyNil()
	}
	for _, e := range keyExprs {
		if e == nil {
			return nil, errMergeKeyNil()
		}
	}
	// E-05: compile the key nodes once per source (not per row in
	// keyRow) — same split, same moment as keyExprs, so the two can
	// never disagree.
	o.ensureMergeExprs()
	s := &mergeSortedSource{
		o:        o,
		child:    child,
		isLeft:   isLeft,
		keyExprs: keyExprs,
		nkeys:    len(keyExprs),
		rowSlot:  SlotFromRow(child.Schema(), nil),
	}
	if o.ctx != nil {
		s.limit = o.ctx.WorkMem
	}
	return s, nil
}

// prime pulls the first row so the caller can learn this side's width when the
// child's Schema() is empty (a Values node, a subplan built without one) —
// exactly the `width == 0 && len(rows) > 0` fallback the array path applied
// after its drain. The row is held and consumed by fill.
func (s *mergeSortedSource) prime() (Row, error) {
	slot, err := s.child.Next()
	if err == EOF {
		s.primedEOF = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.primed = slot.Materialize().Row()
	s.hasPrimed = true
	return s.primed, nil
}

// pull returns the next owned row from the child, primed row first.
func (s *mergeSortedSource) pull() (Row, error) {
	if s.hasPrimed {
		s.hasPrimed = false
		row := s.primed
		s.primed = nil
		return row, nil
	}
	if s.primedEOF {
		return nil, EOF
	}
	slot, err := s.child.Next()
	if err != nil {
		return nil, err
	}
	// Retention boundary: the run outlives the pull, so the row must be
	// owned (the sortOp contract, M0071-0010 Stage B).
	return slot.Materialize().Row(), nil
}

// fill drains the child into sorted runs. Resident bytes never exceed the
// budget: the chunk is sorted, written out and dropped each time it does.
func (s *mergeSortedSource) fill(ctx *Context) error {
	var chunkBytes int64
	pulled := 0
	for {
		if pulled&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		row, err := s.pull()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		pulled++
		keys, nullKey, err := s.keyRow(row)
		if err != nil {
			return err
		}
		s.tail = append(s.tail, mergeStreamRow{row: row, keys: keys, nullKey: nullKey})
		chunkBytes += estimatedRowBytes(row) + estimatedRowBytes(Row(keys))
		if s.limit > 0 && chunkBytes >= s.limit {
			if err := s.flushRun(); err != nil {
				return err
			}
			chunkBytes = 0
		}
	}
	s.sortChunk()
	if s.sortErr != nil {
		return s.sortErr
	}
	if len(s.cursors) > 0 && len(s.tail) > 0 {
		// The in-memory tail is the newest run, so it takes the highest
		// cursor index and loses every tie — which is what keeps the
		// N-way merge stable with respect to the child's row order.
		s.cursors = append(s.cursors, &mergeRunCursor{src: s, mem: s.tail})
	}
	for _, c := range s.cursors {
		if err := c.advance(); err != nil {
			return err
		}
	}
	return nil
}

// keyRow evaluates the key tuple for one raw child row. The expressions index
// the merged column space, so the row is presented through the P0.1 hoisted
// merged-key slot rather than the per-row concatRows padding buildMergeSide
// paid for every row of both sides.
func (s *mergeSortedSource) keyRow(row Row) ([]Datum, bool, error) {
	self := s.selfWidth
	if self == 0 {
		self = len(row)
	}
	s.rowSlot.row = row
	slot := s.keySlot.rebind(s.rowSlot, self, s.otherWidth, s.isLeft)
	if len(s.keyBlock) < s.nkeys {
		s.keyBlock = make([]Datum, mergeKeyBlockRows*s.nkeys)
	}
	keys := s.keyBlock[:s.nkeys:s.nkeys]
	s.keyBlock = s.keyBlock[s.nkeys:]
	// E-05: compiled twin of the key evaluators (nodes compiled at
	// source construction from the same keyExprs). Wrapper logic below
	// (NULL → nullKey) is untouched; only dispatch changes. The length
	// guard is a backstop for hand-built test sources only (production
	// lengths agree by construction) — never a per-site fallback path.
	nodes := s.o.mergeKeyNodesL
	if !s.isLeft {
		nodes = s.o.mergeKeyNodesR
	}
	useCompiled := len(nodes) == len(s.keyExprs)
	for i, e := range s.keyExprs {
		var v Datum
		var err error
		if useCompiled {
			v, err = evalFastExpr(s.o.mergeExprs, nodes[i], slot, s.o.ctx)
		} else {
			v, err = evalExprSlot(e, slot, s.o.ctx)
		}
		if err != nil {
			return nil, false, err
		}
		if v.IsNull() {
			// A NULL in any key column makes that column's equality NULL,
			// so the row matches nothing. It still has to be EMITTED for
			// an outer side, which is what the nullKey flag buys: the row
			// stays in the stream and sorts behind every real key.
			return keys, true, nil
		}
		keys[i] = v
	}
	return keys, false, nil
}

// less is the run comparator: real keys before NULL-keyed rows, then the key
// tuple lexicographically (compareMergeKeys — the same comparator the group
// boundary uses, so the order the sides merge on and the equality that closes
// a group cannot disagree).
func (s *mergeSortedSource) less(a, b mergeStreamRow) bool {
	if a.nullKey != b.nullKey {
		return !a.nullKey
	}
	if a.nullKey {
		return false
	}
	cmp, err := compareMergeKeys(a.keys, b.keys, s.o.plan.Pos())
	if err != nil {
		if s.sortErr == nil {
			s.sortErr = err
		}
		return false
	}
	return cmp < 0
}

func (s *mergeSortedSource) sortChunk() {
	sort.SliceStable(s.tail, func(i, j int) bool { return s.less(s.tail[i], s.tail[j]) })
}

// flushRun sorts the resident chunk, writes it to a spill run and frees it.
// The key tuple is written ahead of the row (a leading flag datum, then nkeys
// key datums, then the row) so a reloaded row never re-evaluates its keys —
// the same reasoning as WriteRowHashed's stored hash value (06 §2.2).
func (s *mergeSortedSource) flushRun() error {
	s.sortChunk()
	if s.sortErr != nil {
		return s.sortErr
	}
	w, err := newSpillWriter(s.o.ctx)
	if err != nil {
		return err
	}
	payload := make(Row, 0, 1+s.nkeys+8)
	for _, r := range s.tail {
		payload = payload[:0]
		if r.nullKey {
			payload = append(payload, NewIntDatum(1))
			for i := 0; i < s.nkeys; i++ {
				payload = append(payload, NullDatum)
			}
		} else {
			payload = append(payload, NewIntDatum(0))
			payload = append(payload, r.keys...)
		}
		payload = append(payload, r.row...)
		if err := w.WriteRow(payload); err != nil {
			w.Close()
			s.o.ctx.removeSpillFile(w.Path())
			return err
		}
	}
	if err := w.Close(); err != nil {
		s.o.ctx.removeSpillFile(w.Path())
		return err
	}
	rd, err := newSpillReader(w.Path())
	if err != nil {
		s.o.ctx.removeSpillFile(w.Path())
		return err
	}
	s.cursors = append(s.cursors, &mergeRunCursor{src: s, r: rd, path: w.Path()})
	s.tail = s.tail[:0]
	s.keyBlock = nil
	return nil
}

// next yields the smallest remaining row of the side, EOF when drained.
func (s *mergeSortedSource) next() (mergeStreamRow, error) {
	if len(s.cursors) == 0 {
		// The whole side fit the budget: no run merge, just the sorted tail.
		if s.tailIdx >= len(s.tail) {
			return mergeStreamRow{}, EOF
		}
		r := s.tail[s.tailIdx]
		s.tailIdx++
		return r, nil
	}
	best := -1
	for i, c := range s.cursors {
		if !c.live {
			continue
		}
		if best < 0 || s.less(c.cur, s.cursors[best].cur) {
			best = i
		}
	}
	if best < 0 {
		return mergeStreamRow{}, EOF
	}
	out := s.cursors[best].cur
	if err := s.cursors[best].advance(); err != nil {
		return mergeStreamRow{}, err
	}
	return out, nil
}

// advance loads the cursor's next row, clearing live at the run's end.
func (c *mergeRunCursor) advance() error {
	if c.r == nil {
		if c.idx >= len(c.mem) {
			c.live = false
			return nil
		}
		c.cur = c.mem[c.idx]
		c.idx++
		c.live = true
		return nil
	}
	payload, err := c.r.ReadRow()
	if err == io.EOF {
		c.live = false
		return nil
	}
	if err != nil {
		return err
	}
	n := c.src.nkeys
	nullKey := !payload[0].IsNull() && payload[0].Int == 1
	var keys []Datum
	if !nullKey {
		keys = payload[1 : 1+n : 1+n]
	}
	c.cur = mergeStreamRow{row: payload[1+n:], keys: keys, nullKey: nullKey}
	c.live = true
	return nil
}

func (c *mergeRunCursor) close(ctx *Context) {
	if c.r != nil {
		c.r.closeKeepFile()
		ctx.removeSpillFile(c.path)
		c.r = nil
	}
}

func (s *mergeSortedSource) close() {
	for _, c := range s.cursors {
		c.close(s.o.ctx)
	}
	s.cursors = nil
	s.tail = nil
	s.keyBlock = nil
	s.primed = nil
}

// ---------------------------------------------------------------------------
// The join state machine
// ---------------------------------------------------------------------------

// Phases. The first three are the merge proper; the last four walk the tail in
// the order the array implementation emitted it (see the file header).
const (
	mjPhaseMerge = iota
	mjPhaseGroup
	mjPhaseGroupFill
	mjPhaseTailLeftReal
	mjPhaseTailRightReal
	mjPhaseTailLeftNull
	mjPhaseTailRightNull
	mjPhaseDone
)

// mergeJoinStream is the streaming merge join: one row per side plus the
// current inner group, and nothing else.
type mergeJoinStream struct {
	o     *joinOp
	left  *mergeSortedSource
	right *mergeSortedSource

	nullLeft  Row
	nullRight Row
	fillLeft  bool
	fillRight bool

	phase int

	lr, rr       mergeStreamRow
	haveL, haveR bool

	// Current inner group. groupMem holds the prefix that fit work_mem;
	// anything past it lives in the overflow file at groupPath and is
	// replayed by groupReader (once per outer row) and by fillReader (once
	// per group, for the RIGHT/FULL sweep).
	groupKeys    []Datum
	groupMem     []mergeStreamRow
	groupBytes   int64
	groupWriter  *spillWriter
	groupPath    string
	groupReader  *spillReader
	groupCount   int
	groupMatched []bool

	groupIdx    int // position of the current outer row inside the group
	leftMatched bool
	fillIdx     int // position of the RIGHT/FULL sweep inside the group

	// steps counts state-machine iterations, not emitted rows: a join whose
	// residual rejects a whole large group makes progress without emitting
	// anything, and that is exactly the case a cancel has to interrupt.
	steps int
}

func newMergeJoinStream(o *joinOp, leftWidth, rightWidth int,
	left, right *mergeSortedSource) *mergeJoinStream {
	return &mergeJoinStream{
		o:         o,
		left:      left,
		right:     right,
		nullLeft:  nullRow(leftWidth),
		nullRight: nullRow(rightWidth),
		fillLeft:  o.plan.Type == optimizer.JoinTypeLeft || o.plan.Type == optimizer.JoinTypeFull,
		fillRight: o.plan.Type == optimizer.JoinTypeRight || o.plan.Type == optimizer.JoinTypeFull,
	}
}

// next returns the join's next output row, or EOF.
func (m *mergeJoinStream) next() (Row, error) {
	for {
		// M0058-0005: the array implementation checked cancellation every
		// 256 left rows; a streaming join checks every 256 state-machine
		// steps, and every step consumes an input row, a group member or a
		// tail row, so the bound is the same.
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
		case mjPhaseMerge:
			row, ok, err = m.stepMerge()
		case mjPhaseGroup:
			row, ok, err = m.stepGroup()
		case mjPhaseGroupFill:
			row, ok, err = m.stepGroupFill()
		case mjPhaseTailLeftReal, mjPhaseTailLeftNull:
			row, ok, err = m.stepTailLeft()
		case mjPhaseTailRightReal, mjPhaseTailRightNull:
			row, ok, err = m.stepTailRight()
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

// loadLeft / loadRight keep the one-tuple-per-side lookahead filled.
func (m *mergeJoinStream) loadLeft() error {
	if m.haveL {
		return nil
	}
	r, err := m.left.next()
	if err == EOF {
		return nil
	}
	if err != nil {
		return err
	}
	m.lr, m.haveL = r, true
	return nil
}

func (m *mergeJoinStream) loadRight() error {
	if m.haveR {
		return nil
	}
	r, err := m.right.next()
	if err == EOF {
		return nil
	}
	if err != nil {
		return err
	}
	m.rr, m.haveR = r, true
	return nil
}

// stepMerge is PG's EXEC_MJ_SKIP: compare the two current keys and advance the
// lesser, null-extending it when its side is an outer one. A NULL-keyed row on
// either side ends the merge, because both sides sort their NULL keys last and
// a NULL key matches nothing.
func (m *mergeJoinStream) stepMerge() (Row, bool, error) {
	if err := m.loadLeft(); err != nil {
		return nil, false, err
	}
	if err := m.loadRight(); err != nil {
		return nil, false, err
	}
	if !m.haveL || !m.haveR || m.lr.nullKey || m.rr.nullKey {
		m.phase = mjPhaseTailLeftReal
		return nil, false, nil
	}
	cmp, err := compareMergeKeys(m.lr.keys, m.rr.keys, m.o.plan.Pos())
	if err != nil {
		return nil, false, err
	}
	switch {
	case cmp < 0:
		m.haveL = false
		if m.fillLeft {
			return concatRows(m.lr.row, m.nullRight), true, nil
		}
		return nil, false, nil
	case cmp > 0:
		m.haveR = false
		if m.fillRight {
			merged := concatRows(m.nullLeft, m.rr.row)
			m.o.coalesceUsingRow(merged)
			return merged, true, nil
		}
		return nil, false, nil
	}
	if err := m.bufferGroup(); err != nil {
		return nil, false, err
	}
	m.phase = mjPhaseGroup
	m.groupIdx = 0
	m.leftMatched = false
	return nil, false, nil
}

// bufferGroup consumes the inner side's whole equal-key group. The prefix that
// fits work_mem stays resident; the rest goes to an overflow file. Leaves the
// lookahead on the first inner row PAST the group (or exhausted).
func (m *mergeJoinStream) bufferGroup() error {
	m.groupKeys = append(m.groupKeys[:0], m.rr.keys...)
	m.groupMem = m.groupMem[:0]
	m.groupBytes = 0
	m.groupCount = 0
	limit := int64(0)
	if m.o.ctx != nil {
		limit = m.o.ctx.WorkMem
	}
	for {
		if err := m.loadRight(); err != nil {
			return err
		}
		if !m.haveR || m.rr.nullKey {
			break
		}
		cmp, err := compareMergeKeys(m.groupKeys, m.rr.keys, m.o.plan.Pos())
		if err != nil {
			return err
		}
		if cmp != 0 {
			break
		}
		if err := m.appendGroupRow(m.rr.row, limit); err != nil {
			return err
		}
		m.haveR = false
	}
	if m.groupWriter != nil {
		if err := m.groupWriter.Close(); err != nil {
			return err
		}
		m.groupWriter = nil
		rd, err := newSpillReader(m.groupPath)
		if err != nil {
			return err
		}
		m.groupReader = rd
	}
	if m.fillRight {
		// One bit per inner row of the group — PG's HeapTupleHeaderSetMatch
		// analogue (07 §3). Sized by row COUNT, not by bytes, so it stays
		// small even for a group whose rows overflowed to disk.
		if cap(m.groupMatched) >= m.groupCount {
			m.groupMatched = m.groupMatched[:m.groupCount]
			for i := range m.groupMatched {
				m.groupMatched[i] = false
			}
		} else {
			m.groupMatched = make([]bool, m.groupCount)
		}
	}
	return nil
}

func (m *mergeJoinStream) appendGroupRow(row Row, limit int64) error {
	m.groupCount++
	size := estimatedRowBytes(row)
	// Always keep at least one row resident: a budget smaller than a single
	// row must still make progress, not spill every row of every group.
	if m.groupWriter == nil && (limit <= 0 || len(m.groupMem) == 0 || m.groupBytes+size <= limit) {
		m.groupMem = append(m.groupMem, mergeStreamRow{row: row})
		m.groupBytes += size
		return nil
	}
	if m.groupWriter == nil {
		w, err := newSpillWriter(m.o.ctx)
		if err != nil {
			return err
		}
		m.groupWriter = w
		m.groupPath = w.Path()
	}
	return m.groupWriter.WriteRow(row)
}

// groupRowAt returns the group's i-th inner row. Callers walk i upward, so the
// overflow portion is a sequential read of the replay file; i == len(groupMem)
// rewinds it (PG's ExecRestrPos).
func (m *mergeJoinStream) groupRowAt(i int) (Row, error) {
	if i < len(m.groupMem) {
		return m.groupMem[i].row, nil
	}
	if i == len(m.groupMem) {
		if err := m.groupReader.rewind(); err != nil {
			return nil, err
		}
	}
	row, err := m.groupReader.ReadRow()
	if err == io.EOF {
		// groupCount counts what bufferGroup wrote, so running out of file
		// early is a bookkeeping bug, not the end of anything. Say so
		// rather than letting io.EOF travel up as an opaque error (and
		// note it is NOT the executor's EOF sentinel, so it could never
		// have been mistaken for the end of the join's output).
		return nil, fmt.Errorf("internal error: merge join inner-group overflow file ended at row %d of %d",
			i, m.groupCount)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// stepGroup joins the current outer row against the buffered inner group —
// PG's EXEC_MJ_JOINTUPLES followed by MJFillOuter for an outer row that the
// joinqual rejected everywhere.
func (m *mergeJoinStream) stepGroup() (Row, bool, error) {
	if m.groupIdx < m.groupCount {
		g, err := m.groupRowAt(m.groupIdx)
		if err != nil {
			return nil, false, err
		}
		i := m.groupIdx
		m.groupIdx++
		joined := concatRows(m.lr.row, g)
		ok, err := m.o.mergeResidualMatch(joined)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		m.leftMatched = true
		if m.fillRight {
			m.groupMatched[i] = true
		}
		return joined, true, nil
	}
	// This outer row has seen the whole group.
	var out Row
	emitted := false
	if !m.leftMatched && m.fillLeft {
		// The key matched but the residual did not, which is still an
		// UNMATCHED row for outer-join purposes.
		out = concatRows(m.lr.row, m.nullRight)
		emitted = true
	}
	if err := m.advanceOuterInGroup(); err != nil {
		return nil, false, err
	}
	return out, emitted, nil
}

// advanceOuterInGroup pulls the next outer row and decides whether the group
// continues with it or is finished.
func (m *mergeJoinStream) advanceOuterInGroup() error {
	m.haveL = false
	if err := m.loadLeft(); err != nil {
		return err
	}
	if m.haveL && !m.lr.nullKey {
		cmp, err := compareMergeKeys(m.lr.keys, m.groupKeys, m.o.plan.Pos())
		if err != nil {
			return err
		}
		if cmp == 0 {
			m.groupIdx = 0
			m.leftMatched = false
			return nil
		}
	}
	m.phase = mjPhaseGroupFill
	m.fillIdx = 0
	return nil
}

// stepGroupFill is HJ_FILL_INNER's merge-join twin: after the whole outer group
// has been joined, the inner rows nothing matched are null-extended for a
// RIGHT/FULL join. Runs after the group so the emission order matches the
// array implementation's per-group ordering exactly.
func (m *mergeJoinStream) stepGroupFill() (Row, bool, error) {
	if !m.fillRight {
		return nil, false, m.endGroup()
	}
	for m.fillIdx < m.groupCount {
		i := m.fillIdx
		m.fillIdx++
		g, err := m.groupRowAt(i)
		if err != nil {
			return nil, false, err
		}
		if m.groupMatched[i] {
			continue
		}
		merged := concatRows(m.nullLeft, g)
		m.o.coalesceUsingRow(merged)
		return merged, true, nil
	}
	return nil, false, m.endGroup()
}

// endGroup releases the group's overflow file and returns to the merge.
func (m *mergeJoinStream) endGroup() error {
	m.releaseGroupFile()
	m.groupMem = m.groupMem[:0]
	m.groupCount = 0
	m.phase = mjPhaseMerge
	return nil
}

func (m *mergeJoinStream) releaseGroupFile() {
	if m.groupWriter != nil {
		m.groupWriter.Close()
		m.groupWriter = nil
	}
	if m.groupReader != nil {
		m.groupReader.closeKeepFile()
		m.groupReader = nil
	}
	if m.groupPath != "" {
		m.o.ctx.removeSpillFile(m.groupPath)
		m.groupPath = ""
	}
}

// stepTailLeft drains what is left of the outer side. It runs twice: once for
// the real-keyed remainder (before the right side's remainder) and once for the
// NULL-keyed rows (after it) — the order buildMergeSide's separate nullKey list
// produced.
func (m *mergeJoinStream) stepTailLeft() (Row, bool, error) {
	realPhase := m.phase == mjPhaseTailLeftReal
	if !m.fillLeft {
		m.phase = m.nextTailPhase()
		return nil, false, nil
	}
	if err := m.loadLeft(); err != nil {
		return nil, false, err
	}
	if !m.haveL {
		m.phase = m.nextTailPhase()
		return nil, false, nil
	}
	if realPhase && m.lr.nullKey {
		// Hold the row: the NULL-keyed remainder is emitted two phases
		// later, after the right side's real remainder.
		m.phase = mjPhaseTailRightReal
		return nil, false, nil
	}
	m.haveL = false
	return concatRows(m.lr.row, m.nullRight), true, nil
}

func (m *mergeJoinStream) stepTailRight() (Row, bool, error) {
	realPhase := m.phase == mjPhaseTailRightReal
	if !m.fillRight {
		m.phase = m.nextTailPhase()
		return nil, false, nil
	}
	if err := m.loadRight(); err != nil {
		return nil, false, err
	}
	if !m.haveR {
		m.phase = m.nextTailPhase()
		return nil, false, nil
	}
	if realPhase && m.rr.nullKey {
		m.phase = mjPhaseTailLeftNull
		return nil, false, nil
	}
	m.haveR = false
	merged := concatRows(m.nullLeft, m.rr.row)
	m.o.coalesceUsingRow(merged)
	return merged, true, nil
}

func (m *mergeJoinStream) nextTailPhase() int {
	switch m.phase {
	case mjPhaseTailLeftReal:
		return mjPhaseTailRightReal
	case mjPhaseTailRightReal:
		return mjPhaseTailLeftNull
	case mjPhaseTailLeftNull:
		return mjPhaseTailRightNull
	default:
		return mjPhaseDone
	}
}

func (m *mergeJoinStream) close() {
	m.releaseGroupFile()
	m.groupMem = nil
	m.groupMatched = nil
	if m.left != nil {
		m.left.close()
	}
	if m.right != nil {
		m.right.close()
	}
}
