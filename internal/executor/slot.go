package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
)

// TupleSlot is the goopg replacement for Row=[]Datum as the
// canonical pipeline carrier. Implementations represent rows in
// different storage forms (fully materialized, virtual reference
// across multiple sources, batch-relative offset). M0069-0001
// landed Stage A: the interface, MaterializedSlot, and adapters
// for use at the Row⇄slot boundary. Subsequent stages migrate
// individual operators to native slot consumption + remove the
// row-level BorrowSemantics contract; those land in M0070+.
//
// See `docs/design/0068-0002-tuple-slot-pipeline.md` for the
// authoritative design.
type TupleSlot interface {
	SlotView
	Schema() optimizer.Schema
	Width() int

	// Row exposes the slot as a Row=[]Datum view. For a
	// MaterializedSlot this is zero-copy. For a future
	// VirtualSlot this materializes lazily.
	Row() Row

	// Materialize returns a slot whose backing storage is
	// independent of any source state. Calling Materialize on
	// a MaterializedSlot is a no-op (returns self).
	Materialize() *MaterializedSlot

	// Release returns the slot to the pool. Caller must not
	// access any column afterwards. No-op for slots that don't
	// own backing storage (VirtualSlot, BatchRefSlot).
	Release()
}

// SlotView is the read-only column interface that evalExprSlot
// accepts. Both *MaterializedSlot and *VirtualSlot satisfy it
// via their Get/IsNull methods (TupleSlot embeds SlotView).
// Plain Row=[]Datum is wrapped via rowSlotView for legacy
// call sites that still pass Row to evalExpr.
type SlotView interface {
	Get(col int) Datum
	IsNull(col int) bool
}

// rowSlotView adapts a Row=[]Datum to SlotView. Zero-cost
// (type conversion, no alloc) — used at evalExpr's legacy
// entry where callers still pass Row.
type rowSlotView Row

func (r rowSlotView) Get(col int) Datum   { return r[col] }
func (r rowSlotView) IsNull(col int) bool { return r[col].IsNull() }

// MaterializedSlot owns its underlying Row=[]Datum slice. Used
// at hash-table storage, sort buffer, aggregate group key, and
// every other retention boundary.
type MaterializedSlot struct {
	schema    optimizer.Schema
	row       Row
	ctidBlock uint32 // injected by seqScanOp for CTIDExpr; valid when hasCTID=true. M0097-0038.
	ctidOff   uint16
	hasCTID   bool
}

// SlotFromRow wraps an existing Row in a MaterializedSlot. The
// slot does NOT take ownership of the underlying slice (no
// Release behaviour change); it's a zero-copy view. Used at
// the operator boundary during the M0069-0001..M0070
// migration window.
func SlotFromRow(s optimizer.Schema, r Row) *MaterializedSlot {
	return &MaterializedSlot{schema: s, row: r}
}

func (s *MaterializedSlot) Schema() optimizer.Schema { return s.schema }
func (s *MaterializedSlot) Width() int             { return len(s.row) }
func (s *MaterializedSlot) Get(col int) Datum      { return s.row[col] }
func (s *MaterializedSlot) IsNull(col int) bool    { return s.row[col].IsNull() }
func (s *MaterializedSlot) Row() Row               { return s.row }

// Materialize produces a slot whose Row is independent of the
// producer's internal buffers. Two cases:
//   - Any arena-backed Datum (KindStringArena / KindBytesArena)
//     gets its bytes promoted into a freshly-allocated owned
//     KindString / KindBytes Datum (via cloneRowOwned).
//   - Even without arena Datums, the Row slice itself is
//     deep-copied (M0092-0002 contract) — producers like
//     projectOp now return slots that ALIAS their internal
//     `o.out` buffer which is overwritten on the next Next()
//     call. Without the slice copy, consumers that retain the
//     slot past the next Next() would see corrupted data.
//
// Pre-M0092-0002, the no-arena fast path returned self because
// every producer's cloneRow already gave consumers independent
// rows. The new contract is "slot valid until next Next()
// unless materialized"; this method is the materialization
// boundary that honors it.
//
// Consumers that retain rows past next Next() MUST call
// Materialize. Callers that consume immediately (filter/limit
// pass-through, simple/extended-query result loops that format
// each cell synchronously) do not need to materialize.
func (s *MaterializedSlot) Materialize() *MaterializedSlot {
	s.row = cloneRowOwned(s.row)
	return s
}

// Release does nothing for MaterializedSlot in Stage A — the
// underlying Row is owned by the operator that produced it,
// not by the slot wrapper. Future stages (Stage B+) wire
// Release to releaseRow() at the slot's lifetime boundary.
func (s *MaterializedSlot) Release() {}

// VirtualSlot references column positions across one or more
// source slots. Stage A defines the type for forward
// compatibility; the actual operator wiring (filterOp /
// projectOp / NLI joinBuf / MHJ probe build) lands in
// subsequent stages.
type VirtualSlot struct {
	schema  optimizer.Schema
	sources []TupleSlot
	cols    []virtualCol
}

type virtualCol struct {
	sourceIdx, sourceCol int16
}

// NewVirtualSlot builds a slot whose column N is sources[cols[N].sourceIdx].Get(cols[N].sourceCol).
func NewVirtualSlot(schema optimizer.Schema, sources []TupleSlot, cols []virtualCol) *VirtualSlot {
	return &VirtualSlot{schema: schema, sources: sources, cols: cols}
}

func (s *VirtualSlot) Schema() optimizer.Schema { return s.schema }
func (s *VirtualSlot) Width() int             { return len(s.cols) }
func (s *VirtualSlot) Get(col int) Datum {
	c := s.cols[col]
	return s.sources[c.sourceIdx].Get(int(c.sourceCol))
}

// VirtualCol returns the runtime coordinate (sourceIdx,
// sourceCol) for the col-th output column. Used by
// chained-NLI diagnostics + future planner-side rebind
// (M0074-0002 forward-compat surface).
func (s *VirtualSlot) VirtualCol(col int) (sourceIdx, sourceCol int) {
	c := s.cols[col]
	return int(c.sourceIdx), int(c.sourceCol)
}
func (s *VirtualSlot) IsNull(col int) bool {
	c := s.cols[col]
	return s.sources[c.sourceIdx].IsNull(int(c.sourceCol))
}

// Row materialises the virtual slot into a fresh []Datum. Used
// at the slot⇄Row boundary while the pipeline still carries
// Row.
func (s *VirtualSlot) Row() Row {
	out := acquireRow(len(s.cols))
	for i := range s.cols {
		out[i] = s.Get(i)
	}
	return out
}

func (s *VirtualSlot) Materialize() *MaterializedSlot {
	// M0126-0001 Stage −1b: clone arena-backed Datums so the
	// materialised row owns its storage independently of any
	// resettable producer arena. Before this fix, VirtualSlot
	// skipped the MaterializeArena step that MaterializedSlot's
	// own Materialize and drainRowsBounded both perform
	// (datum.go:425-434 cloneRowOwned; spill.go:395).
	return &MaterializedSlot{schema: s.schema, row: cloneRowOwned(s.Row())}
}

// Release on a VirtualSlot is a no-op — backing storage lives in
// the source slots. The operator pipeline contract (M0070+) will
// require sources to outlive their VirtualSlot consumers.
func (s *VirtualSlot) Release() {}

// asSlot wraps a Row in a MaterializedSlot for return at the
// Operator.Next() boundary (M0071-0010 Stage B). nil row →
// nil slot, so EOF returns stay clean.
func asSlot(s optimizer.Schema, r Row) TupleSlot {
	if r == nil {
		return nil
	}
	return SlotFromRow(s, r)
}

// slotRow extracts the Row view from a slot. nil slot → nil row.
// Used at consumer sites during the M0071-0010 Stage B migration
// while internal operator logic still works on Row.
func slotRow(slot TupleSlot) Row {
	if slot == nil {
		return nil
	}
	return slot.Row()
}

// slotToRow extracts a Row from a SlotView for legacy helpers
// (evalSubquery / evalInExpr / evalExistsExpr / evalExtract /
// evalFuncCall / evalCaseExpr) that still take Row. Type-asserts
// to known concrete impls for zero-copy when possible; falls back
// to materialization for unrecognized SlotView shapes.
//
// nil view → nil row (preserves the "operators with no input"
// contract documented at evalExpr).
func slotToRow(view SlotView) Row {
	switch v := view.(type) {
	case nil:
		return nil
	case rowSlotView:
		return Row(v)
	case *MaterializedSlot:
		if v == nil {
			return nil
		}
		return v.row
	case *VirtualSlot:
		if v == nil {
			return nil
		}
		return v.Row()
	case *Slot:
		// Phase C concrete slot (M0107-0003): used by evalFastExpr's ExprAdapter
		// path when evalExprSlot is called with a *Slot from projectOpNext /
		// filterOpNext. Without this case, expressions that convert the slot to a
		// Row (InExpr, CaseExpr, SubqueryExpr, ExistsExpr, ExtractExpr, FuncCall)
		// fall to default and return nil, causing spurious "nil slot" errors.
		if v == nil {
			return nil
		}
		return v.Row()
	default:
		return nil
	}
}
