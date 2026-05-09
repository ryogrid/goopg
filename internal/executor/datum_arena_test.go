package executor

import (
	"bytes"
	"testing"
	"unsafe"
)

// TestM0073DatumStructSize pins the post-M0073-0001 Datum
// layout: 64 B exact (was 56 B with 8 B padding pre-M0073;
// the +arena *Arena field consumed all the headroom). Future
// field additions will trigger the M0074 packed-layout work.
func TestM0073DatumStructSize(t *testing.T) {
	const want = 64
	got := unsafe.Sizeof(Datum{})
	if got != want {
		t.Errorf("unsafe.Sizeof(Datum{}) = %d, want %d (M0073-0001 must keep struct exactly 64 B)", got, want)
	}
}

// TestM0073DatumStringArenaRoundTrip pins the new accessor
// contract: a KindStringArena Datum encoding (offset, length)
// in Int returns the original payload via StringValue() that
// resolves through arena.Bytes(...).
func TestM0073DatumStringArenaRoundTrip(t *testing.T) {
	a := NewArena(0)
	payload := "hello world"
	buf, offset := a.Allocate(len(payload))
	copy(buf, payload)

	d := newStringArenaDatum(a, offset, len(payload))
	if d.Kind != KindStringArena {
		t.Errorf("Kind = %d, want KindStringArena", d.Kind)
	}
	if got := d.StringValue(); got != payload {
		t.Errorf("StringValue() = %q, want %q", got, payload)
	}
}

// TestM0073DatumBytesArenaRoundTrip mirrors the StringArena
// test for KindBytesArena.
func TestM0073DatumBytesArenaRoundTrip(t *testing.T) {
	a := NewArena(0)
	payload := []byte{0x01, 0x02, 0x03, 0xFF, 0xFE}
	buf, offset := a.Allocate(len(payload))
	copy(buf, payload)

	d := newBytesArenaDatum(a, offset, len(payload))
	if d.Kind != KindBytesArena {
		t.Errorf("Kind = %d, want KindBytesArena", d.Kind)
	}
	if got := d.BytesValue(); !bytes.Equal(got, payload) {
		t.Errorf("BytesValue() = %v, want %v", got, payload)
	}
}

// TestM0073DatumNonArenaPathUnchanged pins the backward-compat
// invariant: KindString / KindBytes Datums with a Buf payload
// continue to work via the legacy unsafe.String / Buf path.
// arena field is nil for these; the dispatch in StringValue /
// BytesValue takes the non-arena branch.
func TestM0073DatumNonArenaPathUnchanged(t *testing.T) {
	d := NewStringDatum("legacy")
	if d.Kind != KindString {
		t.Fatalf("NewStringDatum: Kind = %d, want KindString", d.Kind)
	}
	if d.arena != nil {
		t.Errorf("NewStringDatum: arena should be nil; got %p", d.arena)
	}
	if got := d.StringValue(); got != "legacy" {
		t.Errorf("StringValue() = %q, want %q", got, "legacy")
	}

	bd := NewBytesDatum([]byte{0x10, 0x20})
	if bd.Kind != KindBytes {
		t.Fatalf("NewBytesDatum: Kind = %d, want KindBytes", bd.Kind)
	}
	if bd.arena != nil {
		t.Errorf("NewBytesDatum: arena should be nil")
	}
	if got := bd.BytesValue(); !bytes.Equal(got, []byte{0x10, 0x20}) {
		t.Errorf("BytesValue() = %v, want [16 32]", got)
	}
}

// TestM0073RowHasArena pins the fast-path skip predicate used
// by Materialize.
func TestM0073RowHasArena(t *testing.T) {
	a := NewArena(0)
	buf, off := a.Allocate(4)
	copy(buf, []byte("abcd"))

	plain := Row{NewIntDatum(1), NewStringDatum("x")}
	if rowHasArena(plain) {
		t.Errorf("rowHasArena(plain) = true, want false")
	}
	mixed := Row{
		NewIntDatum(1),
		newStringArenaDatum(a, off, 4),
		NewStringDatum("y"),
	}
	if !rowHasArena(mixed) {
		t.Errorf("rowHasArena(mixed) = false, want true")
	}
}

// TestM0073CloneRowOwnedPromotesArenaDatums pins the deep-copy
// contract: cloneRowOwned converts arena-backed Datums into
// regular KindString / KindBytes Datums whose Buf is independent
// of the source arena. After arena.Reset(), the cloned bytes
// must still read correctly.
func TestM0073CloneRowOwnedPromotesArenaDatums(t *testing.T) {
	a := NewArena(0)
	sBuf, sOff := a.Allocate(5)
	copy(sBuf, []byte("hello"))
	bBuf, bOff := a.Allocate(3)
	copy(bBuf, []byte{0xAA, 0xBB, 0xCC})

	src := Row{
		NewIntDatum(7),
		newStringArenaDatum(a, sOff, 5),
		newBytesArenaDatum(a, bOff, 3),
	}

	dst := cloneRowOwned(src)

	// Verify pre-Reset that values came through.
	if dst[0].Int != 7 {
		t.Errorf("non-arena passthrough: Int = %d", dst[0].Int)
	}
	if dst[1].Kind != KindString {
		t.Errorf("dst[1].Kind = %d, want KindString (promoted)", dst[1].Kind)
	}
	if dst[1].arena != nil {
		t.Errorf("dst[1].arena = %p, want nil after promotion", dst[1].arena)
	}
	if got := dst[1].StringValue(); got != "hello" {
		t.Errorf("dst[1].StringValue() = %q, want %q", got, "hello")
	}
	if dst[2].Kind != KindBytes {
		t.Errorf("dst[2].Kind = %d, want KindBytes (promoted)", dst[2].Kind)
	}
	if got := dst[2].BytesValue(); !bytes.Equal(got, []byte{0xAA, 0xBB, 0xCC}) {
		t.Errorf("dst[2].BytesValue() = %v, want [170 187 204]", got)
	}

	// Reset the arena. The cloned dst must keep working.
	a.Reset()
	// Re-allocate to overwrite the underlying page bytes (worst-
	// case scenario: post-Reset the same page is reused for new
	// data; the cloned Datum must NOT point into it).
	overwrite, _ := a.Allocate(8)
	copy(overwrite, []byte("OVERWRTE"))

	if got := dst[1].StringValue(); got != "hello" {
		t.Errorf("after Reset: dst[1].StringValue() = %q, want %q (cloneRowOwned must deep-copy)", got, "hello")
	}
	if got := dst[2].BytesValue(); !bytes.Equal(got, []byte{0xAA, 0xBB, 0xCC}) {
		t.Errorf("after Reset: dst[2].BytesValue() = %v, want [170 187 204]", got)
	}
}

// TestM0073MaterializePromotesArenaDatum pins the integration:
// a MaterializedSlot wrapping a row with arena Datums returns
// itself with promoted (Buf-backed) values when Materialize()
// is called. The arena's Reset cycle then doesn't corrupt the
// retained slot.
func TestM0073MaterializePromotesArenaDatum(t *testing.T) {
	a := NewArena(0)
	buf, off := a.Allocate(4)
	copy(buf, []byte("payl"))

	row := Row{newStringArenaDatum(a, off, 4)}
	slot := SlotFromRow(nil, row)

	mat := slot.Materialize()
	if mat.row[0].Kind != KindString {
		t.Errorf("after Materialize: Kind = %d, want KindString", mat.row[0].Kind)
	}

	a.Reset()
	overwrite, _ := a.Allocate(8)
	copy(overwrite, []byte("OVERWRTE"))

	if got := mat.row[0].StringValue(); got != "payl" {
		t.Errorf("after Reset: StringValue() = %q, want %q (Materialize must promote)", got, "payl")
	}
}

// TestM0073MaterializeNoArenaIsNoOp pins the fast-path
// invariant: when no Datums are arena-backed, Materialize is
// equivalent to returning self without any allocation.
func TestM0073MaterializeNoArenaIsNoOp(t *testing.T) {
	row := Row{NewIntDatum(1), NewStringDatum("x")}
	slot := SlotFromRow(nil, row)
	mat := slot.Materialize()
	if mat != slot {
		t.Errorf("no-arena fast path should return self; got %p, want %p", mat, slot)
	}
	// Also: the row backing slice should be the same identity.
	if &mat.row[0] != &row[0] {
		t.Errorf("no-arena Materialize should not reallocate row")
	}
}
