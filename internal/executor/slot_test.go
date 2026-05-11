package executor

import (
	"testing"
)

// TestM0069MaterializedSlot exercises the basic Row-backed slot
// path: construct, read, materialize (always deep-copies per
// M0092-0002), release.
func TestM0069MaterializedSlot(t *testing.T) {
	r := Row{NewIntDatum(10), NewStringDatum("alpha"), Datum{}}
	s := SlotFromRow(nil, r)
	if s.Width() != 3 {
		t.Fatalf("Width: got %d, want 3", s.Width())
	}
	if s.Get(0).Int != 10 {
		t.Errorf("Get(0).Int = %d, want 10", s.Get(0).Int)
	}
	if s.Get(1).StringValue() != "alpha" {
		t.Errorf("Get(1).StringValue() = %q, want %q", s.Get(1).StringValue(), "alpha")
	}
	if !s.IsNull(2) {
		t.Errorf("IsNull(2) = false, want true")
	}
	// M0092-0002: Materialize always deep-copies the row slice so
	// consumers retaining past producer's next Next() see
	// independent storage. The returned slot may be the same
	// pointer (in-place update of s.row); the row contents must
	// match.
	m := s.Materialize()
	if m != s {
		t.Errorf("Materialize on MaterializedSlot must return self")
	}
	got := s.Row()
	if got[0].Int != 10 || got[1].StringValue() != "alpha" || !got[2].IsNull() {
		t.Errorf("Materialize() corrupted row content: %+v", got)
	}
	s.Release() // no-op, must not panic
}

// TestM0069VirtualSlot exercises a virtual slot referencing two
// source MaterializedSlots — the canonical NLI joinBuf / MHJ
// probe shape.
func TestM0069VirtualSlot(t *testing.T) {
	left := SlotFromRow(nil, Row{NewIntDatum(100), NewStringDatum("L")})
	right := SlotFromRow(nil, Row{NewIntDatum(200), NewStringDatum("R"), NewIntDatum(300)})
	v := NewVirtualSlot(
		nil,
		[]TupleSlot{left, right},
		[]virtualCol{
			{sourceIdx: 0, sourceCol: 0}, // left[0] = 100
			{sourceIdx: 1, sourceCol: 1}, // right[1] = "R"
			{sourceIdx: 0, sourceCol: 1}, // left[1] = "L"
			{sourceIdx: 1, sourceCol: 2}, // right[2] = 300
		},
	)
	if v.Width() != 4 {
		t.Fatalf("Width: got %d, want 4", v.Width())
	}
	if v.Get(0).Int != 100 {
		t.Errorf("Get(0) = %d, want 100", v.Get(0).Int)
	}
	if v.Get(1).StringValue() != "R" {
		t.Errorf("Get(1) = %q, want %q", v.Get(1).StringValue(), "R")
	}
	if v.Get(2).StringValue() != "L" {
		t.Errorf("Get(2) = %q, want %q", v.Get(2).StringValue(), "L")
	}
	if v.Get(3).Int != 300 {
		t.Errorf("Get(3) = %d, want 300", v.Get(3).Int)
	}
	// Row() materialises into a fresh slice.
	mat := v.Materialize()
	if mat.Width() != 4 {
		t.Errorf("Materialize Width: got %d, want 4", mat.Width())
	}
	if mat.Get(2).StringValue() != "L" {
		t.Errorf("Materialize Get(2) = %q, want %q", mat.Get(2).StringValue(), "L")
	}
}
