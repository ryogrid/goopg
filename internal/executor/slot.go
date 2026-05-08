package executor

import (
	"github.com/goopg/goopg/internal/planner"
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
	Schema() planner.Schema
	Width() int
	Get(col int) Datum
	IsNull(col int) bool

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

// MaterializedSlot owns its underlying Row=[]Datum slice. Used
// at hash-table storage, sort buffer, aggregate group key, and
// every other retention boundary.
type MaterializedSlot struct {
	schema planner.Schema
	row    Row
}

// SlotFromRow wraps an existing Row in a MaterializedSlot. The
// slot does NOT take ownership of the underlying slice (no
// Release behaviour change); it's a zero-copy view. Used at
// the operator boundary during the M0069-0001..M0070
// migration window.
func SlotFromRow(s planner.Schema, r Row) *MaterializedSlot {
	return &MaterializedSlot{schema: s, row: r}
}

func (s *MaterializedSlot) Schema() planner.Schema { return s.schema }
func (s *MaterializedSlot) Width() int             { return len(s.row) }
func (s *MaterializedSlot) Get(col int) Datum      { return s.row[col] }
func (s *MaterializedSlot) IsNull(col int) bool    { return s.row[col].IsNull() }
func (s *MaterializedSlot) Row() Row               { return s.row }
func (s *MaterializedSlot) Materialize() *MaterializedSlot {
	return s
}

// Release does nothing for MaterializedSlot in Stage A — the
// underlying Row is owned by the operator that produced it,
// not by the slot wrapper. Future stages (Stage B+) wire
// Release to releaseRow() at the slot's lifetime boundary.
func (s *MaterializedSlot) Release() {}

// set is the per-operator-slot M0069-0001 Stage B emit helper.
// Each producer keeps a `outSlot MaterializedSlot` field as a
// VALUE inside its struct (no heap allocation per call); on
// each Next() it sets the row and returns &o.outSlot. A
// receiver-pointer method on MaterializedSlot lets `&o.outSlot`
// be addressed naturally:
//
//	func (o *T) Next() (TupleSlot, error) {
//	    // ... compute row ...
//	    return o.outSlot.set(row), nil
//	}
//
// The slot pointer is stable per-operator; its `row` field is
// invalidated by the next `Next()` call on the same operator.
// Retention consumers (sortOp drain, hash-build, etc.) must
// call slot.Materialize() (or cloneRow at the slot.Row())
// before the next Next() call.
func (s *MaterializedSlot) set(row Row) *MaterializedSlot {
	s.row = row
	return s
}

// withSchema records the schema on this slot — typically called
// once at operator Open() time.
func (s *MaterializedSlot) withSchema(sch planner.Schema) {
	s.schema = sch
}

// NextRow is a transitional helper for the M0069-0001 Stage B
// migration: it advances `op` and materialises the slot back to
// a Row. Operators / call sites that haven't been migrated to
// consume TupleSlot natively use NextRow to keep reading rows.
func NextRow(op Operator) (Row, error) {
	s, err := op.Next()
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	return s.Row(), nil
}

// VirtualSlot references column positions across one or more
// source slots. Stage A defines the type for forward
// compatibility; the actual operator wiring (filterOp /
// projectOp / NLI joinBuf / MHJ probe build) lands in
// subsequent stages.
type VirtualSlot struct {
	schema  planner.Schema
	sources []TupleSlot
	cols    []virtualCol
}

type virtualCol struct {
	sourceIdx, sourceCol int16
}

// NewVirtualSlot builds a slot whose column N is sources[cols[N].sourceIdx].Get(cols[N].sourceCol).
func NewVirtualSlot(schema planner.Schema, sources []TupleSlot, cols []virtualCol) *VirtualSlot {
	return &VirtualSlot{schema: schema, sources: sources, cols: cols}
}

func (s *VirtualSlot) Schema() planner.Schema { return s.schema }
func (s *VirtualSlot) Width() int             { return len(s.cols) }
func (s *VirtualSlot) Get(col int) Datum {
	c := s.cols[col]
	return s.sources[c.sourceIdx].Get(int(c.sourceCol))
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
	return &MaterializedSlot{schema: s.schema, row: s.Row()}
}

// Release on a VirtualSlot is a no-op — backing storage lives in
// the source slots. The operator pipeline contract (M0070+) will
// require sources to outlive their VirtualSlot consumers.
func (s *VirtualSlot) Release() {}
