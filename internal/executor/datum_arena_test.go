package executor

import (
	"bytes"
	"testing"
	"unsafe"

	"github.com/goopg/goopg/internal/mctx"
)

// TestM0073DatumStructSize pins the post-M0073-0001 Datum
// layout: 64 B exact (was 56 B with 8 B padding pre-M0073;
// the +arena *Arena field consumed all the headroom). Future
// field additions will trigger the M0074 packed-layout work.
// M0107-0001: arena *Arena → mctx *mctx.Context (same pointer size).
func TestM0073DatumStructSize(t *testing.T) {
	const want = 64
	got := unsafe.Sizeof(Datum{})
	if got != want {
		t.Errorf("unsafe.Sizeof(Datum{}) = %d, want %d (M0073-0001 must keep struct exactly 64 B)", got, want)
	}
}

// TestM0073DatumStringArenaRoundTrip pins the accessor contract:
// a KindStringArena Datum encoding (offset, length) in Int returns
// the original payload via StringValue() through mctx.Bytes().
func TestM0073DatumStringArenaRoundTrip(t *testing.T) {
	a := mctx.Acquire(nil, mctx.KindStmt)
	defer a.Release()
	payload := "hello world"
	offset, length := a.AllocString(payload)

	d := newStringArenaDatum(a, offset, length)
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
	a := mctx.Acquire(nil, mctx.KindStmt)
	defer a.Release()
	payload := []byte{0x01, 0x02, 0x03, 0xFF, 0xFE}
	offset, length := a.AllocBytes(payload)

	d := newBytesArenaDatum(a, offset, length)
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
// mctx field is nil for these; the dispatch in StringValue /
// BytesValue takes the non-arena branch.
func TestM0073DatumNonArenaPathUnchanged(t *testing.T) {
	d := NewStringDatum("legacy")
	if d.Kind != KindString {
		t.Fatalf("NewStringDatum: Kind = %d, want KindString", d.Kind)
	}
	if d.mctx != nil {
		t.Errorf("NewStringDatum: mctx should be nil; got %p", d.mctx)
	}
	if got := d.StringValue(); got != "legacy" {
		t.Errorf("StringValue() = %q, want %q", got, "legacy")
	}

	bd := NewBytesDatum([]byte{0x10, 0x20})
	if bd.Kind != KindBytes {
		t.Fatalf("NewBytesDatum: Kind = %d, want KindBytes", bd.Kind)
	}
	if bd.mctx != nil {
		t.Errorf("NewBytesDatum: mctx should be nil")
	}
	if got := bd.BytesValue(); !bytes.Equal(got, []byte{0x10, 0x20}) {
		t.Errorf("BytesValue() = %v, want [16 32]", got)
	}
}

// TestM0073RowHasArena pins the fast-path skip predicate used
// by Materialize.
func TestM0073RowHasArena(t *testing.T) {
	a := mctx.Acquire(nil, mctx.KindStmt)
	defer a.Release()
	off, l := a.AllocBytes([]byte("abcd"))

	plain := Row{NewIntDatum(1), NewStringDatum("x")}
	if rowHasArena(plain) {
		t.Errorf("rowHasArena(plain) = true, want false")
	}
	mixed := Row{
		NewIntDatum(1),
		newStringArenaDatum(a, off, l),
		NewStringDatum("y"),
	}
	if !rowHasArena(mixed) {
		t.Errorf("rowHasArena(mixed) = false, want true")
	}
}

// TestM0073CloneRowOwnedPromotesArenaDatums pins the deep-copy
// contract: cloneRowOwned converts mctx-backed Datums into
// regular KindString / KindBytes Datums whose Buf is independent
// of the source context. After mctx.Reset(), the cloned bytes
// must still read correctly.
func TestM0073CloneRowOwnedPromotesArenaDatums(t *testing.T) {
	a := mctx.Acquire(nil, mctx.KindStmt)
	defer a.Release()
	sOff, sLen := a.AllocBytes([]byte("hello"))
	bOff, bLen := a.AllocBytes([]byte{0xAA, 0xBB, 0xCC})

	src := Row{
		NewIntDatum(7),
		newStringArenaDatum(a, sOff, sLen),
		newBytesArenaDatum(a, bOff, bLen),
	}

	dst := cloneRowOwned(src)

	// Verify pre-Reset that values came through.
	if dst[0].Int != 7 {
		t.Errorf("non-arena passthrough: Int = %d", dst[0].Int)
	}
	if dst[1].Kind != KindString {
		t.Errorf("dst[1].Kind = %d, want KindString (promoted)", dst[1].Kind)
	}
	if dst[1].mctx != nil {
		t.Errorf("dst[1].mctx = %p, want nil after promotion", dst[1].mctx)
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

	// Reset the context. The cloned dst must keep working.
	a.Reset()
	// Re-allocate to overwrite the underlying chunk bytes (worst-
	// case scenario: post-Reset the same chunk is reused for new
	// data; the cloned Datum must NOT point into it).
	overOff, overLen := a.AllocBytes([]byte("OVERWRTE"))
	_ = overOff
	_ = overLen

	if got := dst[1].StringValue(); got != "hello" {
		t.Errorf("after Reset: dst[1].StringValue() = %q, want %q (cloneRowOwned must deep-copy)", got, "hello")
	}
	if got := dst[2].BytesValue(); !bytes.Equal(got, []byte{0xAA, 0xBB, 0xCC}) {
		t.Errorf("after Reset: dst[2].BytesValue() = %v, want [170 187 204]", got)
	}
}

// TestM0073MaterializePromotesArenaDatum pins the integration:
// a MaterializedSlot wrapping a row with mctx Datums returns
// itself with promoted (Buf-backed) values when Materialize()
// is called. The mctx Reset cycle then doesn't corrupt the
// retained slot.
func TestM0073MaterializePromotesArenaDatum(t *testing.T) {
	a := mctx.Acquire(nil, mctx.KindStmt)
	defer a.Release()
	off, l := a.AllocBytes([]byte("payl"))

	row := Row{newStringArenaDatum(a, off, l)}
	slot := SlotFromRow(nil, row)

	mat := slot.Materialize()
	if mat.row[0].Kind != KindString {
		t.Errorf("after Materialize: Kind = %d, want KindString", mat.row[0].Kind)
	}

	a.Reset()
	overOff, overLen := a.AllocBytes([]byte("OVERWRTE"))
	_ = overOff
	_ = overLen

	if got := mat.row[0].StringValue(); got != "payl" {
		t.Errorf("after Reset: StringValue() = %q, want %q (Materialize must promote)", got, "payl")
	}
}

// TestM0092MaterializeAlwaysDeepCopies pins the M0092-0002
// contract change: Materialize must ALWAYS produce a row
// independent of the producer's internal buffers, including
// when no Datums are mctx-backed. Pre-M0092 the no-arena
// path was a no-op; post-M0092 producers (projectOp,
// indexScanOp) may return slots aliasing internal buffers
// that get overwritten on the next Next() call, so the slice
// itself must be copied even without arena Datums.
func TestM0092MaterializeAlwaysDeepCopies(t *testing.T) {
	row := Row{NewIntDatum(1), NewStringDatum("x")}
	slot := SlotFromRow(nil, row)
	mat := slot.Materialize()
	if mat != slot {
		t.Errorf("Materialize must return the same slot pointer; got %p, want %p", mat, slot)
	}
	// The row backing slice MUST be a fresh allocation now.
	if &mat.row[0] == &row[0] {
		t.Errorf("Materialize must deep-copy the row slice (M0092-0002 contract)")
	}
	// Content must match.
	if mat.row[0].Int != 1 || mat.row[1].StringValue() != "x" {
		t.Errorf("Materialize corrupted row content: %+v", mat.row)
	}
}
