package executor

// packedslot.go — D-03 (MD-03): the lazy-deform slot over a PackedTuple.
//
// 04 §2.2. PackedSlot implements TupleSlot by deforming its tuple on demand
// under PG's watermark, `tts_nvalid` + `HeapTupleTableSlot.off` (01 §4).
//
// THIS FILE HAS NO PRODUCER (TODO_ALL D-03). Nothing in the pipeline
// constructs a PackedSlot; the six type-switch arms R-0 requires exist so that
// the first slice that DOES produce one cannot produce the silent wrong
// answers 04 §9.1 catalogues.

import (
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/adt/array"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// PackedSlot deforms a PackedTuple on demand. It implements TupleSlot.
//
// The (nvalid, off) pair is PG's tts_nvalid + HeapTupleTableSlot.off (01 §4):
// `nvalid` columns are valid in `values`, and `off` is the byte offset in the
// tuple's data area at which column `nvalid` starts. Two invariants come with
// that, both restated from the decoder's own doc comment (codec.go:1338-1346):
//
//   - A SUFFIX MAY BE SKIPPED; A PREFIX MAY NOT. Get(5) on a fresh slot
//     deforms columns 0..5, not column 5 alone. A physical tuple has no column
//     offset array; PG has attcacheoff and goopg does not (04 §6).
//   - A PARTIALLY-FILLED `values` MUST NEVER ESCAPE. Entries past nvalid hold
//     the PREVIOUS tuple's values. Row() deforms fully first, Materialize()
//     goes through Row(), and nothing indexes `values` directly — including
//     inside this file.
type PackedSlot struct {
	schema optimizer.Schema
	desc   *TupleDesc
	tup    PackedTuple

	// values is scratch, len == descriptor width, REUSED across tuples. Its
	// contents past nvalid are stale by construction; see the escape rule.
	values Row
	nvalid int
	off    int

	// mctx is the slot's PRIVATE deform arena. See the R-8 note on
	// newPackedSlotArena below — its privacy is what makes the reset point
	// safe, so it is created here and never handed in.
	mctx  *mmgr.Context
	style array.OutputStyle

	// err records a deform failure. Get(col) has no error return and SlotView
	// cannot grow one without touching every evaluator call site (04 §9.3), so
	// a failure is latched here, the undeformed tail is poisoned rather than
	// filled with NULL — "never a NullDatum fallback; that is the shape of a
	// silent wrong-answer bug" — and Err() surfaces it at the next
	// error-returning boundary the producer offers.
	err error

	// TID (04 §7). MaterializedSlot carries these three fields for CTIDExpr
	// and WHERE CURRENT OF; PackedSlot must carry them too, or the two
	// ctid-propagation type switches (R-0 sites 4 and 5) silently set
	// hasCTID = false.
	ctidBlock uint32
	ctidOff   uint16
	hasCTID   bool
}

// NewPackedSlot builds a slot over desc. parent is the context the slot's
// private deform arena is parented to (the operator's, or nil).
//
// R-8 — WHERE THE DEFORM SCRATCH IS RESET (04 §9.9, "the most consequential
// unstated detail in the design"; MD-03 may not land without answering it).
//
// The question 04 §7 D-5 leaves open: decode-into-arena keeps the pointer-free
// Datum property, but if the arena never resets, deforming N rows accumulates
// N rows of varlena bytes and gives back the memory this bundle removes; if it
// resets too eagerly, every reload pays churn.
//
// Decision, in three parts:
//
//  1. THE RESET POINT IS THE TUPLE LOAD. Load() resets the arena before the
//     new tuple's first deform. Arena bytes are therefore bounded by ONE
//     tuple's varlena payload no matter how many rows are scanned, which is
//     the property R-8 demands and what
//     TestPackedSlotArenaGrowthIsBoundedAcrossAScan asserts.
//
//  2. THE CHURN THE DESIGN FEARED IS NOT ALLOCATION. mmgr.Context.Reset
//     (mctx.go:267) rewinds each chunk's length to 0 and RETAINS the backing
//     array, so steady state is zero allocation and the cost is a pointer
//     store per chunk. The precedent is seqScanOp, which resets its scan arena
//     once per PAGE for exactly this reason (operators_storage.go:2328).
//     Per-page is not available here: a retained tuple has no page to hang a
//     reset on, so per-load is the finest boundary that exists and also the
//     coarsest one that stays bounded.
//
//  3. THE ARENA IS PRIVATE TO THE SLOT, AND THAT IS WHAT MAKES (1) LEGAL. A
//     reset invalidates every arena-backed Datum in the context, so a slot
//     that reset a context it SHARED with its operator would silently
//     invalidate the operator's own decoded values. Hence the constructor
//     acquires a child context rather than accepting one; there is deliberately
//     no way to pass an arena in.
//
// The consequence for consumers is the contract that already exists: values
// are valid until the next Load, and anything retained past it must go through
// Materialize(), whose cloneRowOwned promotes arena-backed Datums to owned
// bytes (datum.go:493, the M0092-0002 rule slot.go:95-110 states).
//
// parent == nil is legal and means "no arena": the decoder then allocates
// owned Go memory per value and the GC bounds it. Tests and any caller without
// a session context use that form; it is slower and allocates, but it can
// never be wrong.
func NewPackedSlot(desc *TupleDesc, parent *mmgr.Context, style array.OutputStyle) *PackedSlot {
	s := &PackedSlot{
		desc:   desc,
		values: make(Row, desc.Width()),
		style:  style,
	}
	s.schema = make(optimizer.Schema, desc.Width())
	for i, c := range desc.cols {
		s.schema[i] = optimizer.SchemaColumn{Name: c.Name, Type: c.Type}
	}
	if parent != nil {
		s.mctx = mmgr.Acquire(parent, mmgr.KindExpr)
	}
	return s
}

// NewPackedSlotForSchema is NewPackedSlot preserving the caller's schema
// (including SourceTableIdx, which the descriptor does not carry and which
// findColumnIndexByNameAndSource needs to disambiguate self-joins).
func NewPackedSlotForSchema(schema optimizer.Schema, desc *TupleDesc, parent *mmgr.Context, style array.OutputStyle) *PackedSlot {
	s := NewPackedSlot(desc, parent, style)
	if len(schema) == desc.Width() {
		s.schema = schema
	}
	return s
}

// Load installs a tuple and resets the watermark. See R-8 on NewPackedSlot for
// why the arena is reset here and nowhere else.
func (s *PackedSlot) Load(t PackedTuple) {
	if s.mctx != nil {
		s.mctx.Reset()
	}
	s.tup = t
	s.nvalid = 0
	s.off = 0
	s.err = nil
	s.hasCTID = false
	// EX1-01 tail poison, armed only under the debug flag: every entry is
	// stale until deformed, so stamp them all. A consumer that reaches past
	// the watermark then panics at the ColumnRef evaluation sites instead of
	// silently reading the previous tuple's value. This is the escape rule's
	// enforcement, not a substitute for it.
	poisonDeformTail(s.values, 0)
}

// LoadWithTID is Load carrying the row's self-tid, for the CTIDExpr and
// WHERE CURRENT OF paths (04 §7).
func (s *PackedSlot) LoadWithTID(t PackedTuple, block uint32, off uint16) {
	s.Load(t)
	s.ctidBlock, s.ctidOff, s.hasCTID = block, off, true
}

// Tuple returns the loaded tuple.
func (s *PackedSlot) Tuple() PackedTuple { return s.tup }

// Err returns a latched deform failure, if any. A producer surfaces it from
// its own error-returning boundary (04 §9.3).
func (s *PackedSlot) Err() error { return s.err }

// Desc returns the slot's descriptor.
func (s *PackedSlot) Desc() *TupleDesc { return s.desc }

// deformTo advances the watermark to n columns.
//
// It calls the UNEXPORTED decodeRowRangeInfo, NOT the exported
// DecodeRowRangeIntoMctxPGTupleStyled (codec.go:1347). That wrapper's entire
// body is `return decodeRowRangeInfo(dst, cols, nil, …)` — it HARDCODES
// info = nil, which would discard the descriptor D-01 built and re-derive
// every column's alignment from its type NAME, per column per row, which is
// the string work D-01 measured at 4.64 % of Q14's CPU. REVIEW M-goopg-3.
func (s *PackedSlot) deformTo(n int) {
	if n > len(s.values) {
		n = len(s.values)
	}
	if n <= s.nvalid || s.err != nil {
		return
	}
	off, err := decodeRowRangeInfo(
		s.values, s.desc.cols, s.desc.info,
		s.tup.data(), s.tup.bitmap(), s.tup.natts(),
		s.mctx, s.style, s.nvalid, n, s.off)
	if err != nil {
		// 04 §9.3 (R-2): encode-side validation makes decode total, so this
		// is an invariant violation, not a data-dependent error. Latch it and
		// leave the tail poisoned; do NOT publish NULLs over the range, which
		// would turn a broken tuple into a plausible answer.
		s.err = err
		// The tail is NOT marked valid. An earlier draft set
		// `nvalid = len(s.values)` here, reasoning that a latched error
		// stops every later deform anyway. It does not: `Row()` calls
		// `deformTo(width)`, which then early-returns on `n <= s.nvalid`
		// and hands back a slice whose entries past the failing column are
		// the PREVIOUS tuple's Datums on a reused slot — or the zero Datum
		// on a fresh one, and `KindNull` is iota 0, so the zero Datum IS
		// NullDatum. That is exactly the "turn a broken tuple into a
		// plausible answer" outcome the rule below forbids, and
		// `poisonDeformTail` cannot prevent it because it is gated on
		// `seqScanDeformPoison`, which is false in production
		// (scan_deform.go).
		//
		// So the watermark stays where the deform actually reached, and the
		// escape paths fail closed on `s.err` instead (see Row /
		// Materialize). Found by review of the D-03 slice.
		poisonDeformTail(s.values, s.nvalid)
		s.off = off
		return
	}
	s.off, s.nvalid = off, n
	poisonDeformTail(s.values, n)
}

// ----- SlotView -------------------------------------------------------------

// Get implements SlotView. A suffix may be skipped, a prefix may not: this
// deforms columns 0..col, not column col alone.
func (s *PackedSlot) Get(col int) Datum {
	if col >= s.nvalid {
		s.deformTo(col + 1)
	}
	return s.values[col]
}

// IsNull implements SlotView. It goes through Get so the watermark rule holds
// for the null test too — reading `values[col]` directly here would be the
// escape-rule violation in its smallest form.
func (s *PackedSlot) IsNull(col int) bool { return s.Get(col).IsNull() }

// ----- TupleSlot ------------------------------------------------------------

// Schema implements TupleSlot.
func (s *PackedSlot) Schema() optimizer.Schema { return s.schema }

// Width implements TupleSlot: the descriptor's width, NOT the watermark. A
// slot's width is a property of its schema and must not change as columns are
// deformed — every bounds guard in the two evaluators compares against it.
func (s *PackedSlot) Width() int { return len(s.values) }

// Row implements TupleSlot. It deforms FULLY first — this is the escape rule,
// and the reason it is a method rather than a field read.
//
// The returned Row ALIASES the slot's scratch and is therefore valid only
// until the next Load. That is the same contract MaterializedSlot acquired at
// M0092-0002 ("slot valid until next Next() unless materialized",
// slot.go:95-110), so it is a new instance of a known hazard class, not a new
// class.
func (s *PackedSlot) Row() Row {
	s.deformTo(len(s.values))
	if s.err != nil {
		// FAIL CLOSED. A latched deform error means `values` holds a prefix
		// of this tuple and a tail belonging to the previous one; returning
		// it would publish stale values as this row's. nil is the loud
		// answer: `slotToRow`'s callers already turn a nil Row into an
		// explicit "nil slot" error rather than a wrong row (slot.go), which
		// is the precedent this follows.
		return nil
	}
	return s.values
}

// Materialize implements TupleSlot: a slot whose storage is independent of
// this one's scratch AND of its arena, preserving today's contract exactly.
func (s *PackedSlot) Materialize() *MaterializedSlot {
	r := s.Row()
	if r == nil {
		// Row() failed closed on a latched deform error. Materialising here
		// would be worse than returning it live: the mixed prefix/stale-tail
		// row would be CLONED into a retained slot and outlive the tuple that
		// exposed the problem.
		return &MaterializedSlot{schema: s.schema}
	}
	ms := &MaterializedSlot{schema: s.schema, row: cloneRowOwned(r)}
	ms.hasCTID = s.hasCTID
	ms.ctidBlock = s.ctidBlock
	ms.ctidOff = s.ctidOff
	return ms
}

// Release implements TupleSlot: clear the slot for reuse. It drops the tuple
// and rewinds the arena, so every Datum previously handed out is invalid
// afterwards — the interface's stated contract ("Caller must not access any
// column afterwards").
//
// It does NOT release the arena context; a pooled slot is Loaded again. Close
// is the end-of-life call.
func (s *PackedSlot) Release() {
	if s.mctx != nil {
		s.mctx.Reset()
	}
	s.tup = PackedTuple{}
	s.nvalid = 0
	s.off = 0
	s.err = nil
	s.hasCTID = false
	scrubDeformPoison(s.values)
}

// Close returns the slot's private arena to the pool. After Close the slot
// must not be used. Operators call it from their own Close, next to the
// releases of every other arena they acquired.
func (s *PackedSlot) Close() {
	if s.mctx != nil {
		s.mctx.Release()
		s.mctx = nil
	}
}

// TID implements TupleSlot (04 §7).
func (s *PackedSlot) TID() (block uint32, off uint16, ok bool) {
	return s.ctidBlock, s.ctidOff, s.hasCTID
}
