package executor

import (
	"bytes"
	"math/big"
	"testing"
	"unsafe"

	"github.com/goopg/goopg/internal/mctx"
)

// TestM0107DatumStructSize pins the post-M0107-0002 Datum layout:
// 48 B exact (was 64 B / 3 GC pointers). DatumKind → uint8,
// mctx *mctx.Context → ArenaID uint16, Big *big.Int removed.
func TestM0107DatumStructSize(t *testing.T) {
	const want = 48
	got := unsafe.Sizeof(Datum{})
	if got != want {
		t.Errorf("unsafe.Sizeof(Datum{}) = %d, want %d (M0107-0002 must be exactly 48 B)", got, want)
	}
}

// TestM0073DatumStringArenaRoundTrip pins the accessor contract:
// a mctx-backed KindString Datum (ArenaID≠0) returns the original
// payload via StringValue() through mctx.Bytes(). M0107-0002: merged
// KindStringArena into KindString (ArenaID field distinguishes).
func TestM0073DatumStringArenaRoundTrip(t *testing.T) {
	a := mctx.Acquire(nil, mctx.KindStmt)
	defer a.Release()
	payload := "hello world"
	offset, length := a.AllocString(payload)

	d := newStringArenaDatum(a, offset, length)
	if d.Kind != KindString {
		t.Errorf("Kind = %d, want KindString (arena-backed, M0107-0002)", d.Kind)
	}
	if d.ArenaID == 0 {
		t.Errorf("ArenaID = 0, want non-zero for arena-backed Datum")
	}
	if got := d.StringValue(); got != payload {
		t.Errorf("StringValue() = %q, want %q", got, payload)
	}
}

// TestM0073DatumBytesArenaRoundTrip mirrors the string arena
// test for bytes (M0107-0002: KindBytesArena merged into KindBytes).
func TestM0073DatumBytesArenaRoundTrip(t *testing.T) {
	a := mctx.Acquire(nil, mctx.KindStmt)
	defer a.Release()
	payload := []byte{0x01, 0x02, 0x03, 0xFF, 0xFE}
	offset, length := a.AllocBytes(payload)

	d := newBytesArenaDatum(a, offset, length)
	if d.Kind != KindBytes {
		t.Errorf("Kind = %d, want KindBytes (arena-backed, M0107-0002)", d.Kind)
	}
	if d.ArenaID == 0 {
		t.Errorf("ArenaID = 0, want non-zero for arena-backed Datum")
	}
	if got := d.BytesValue(); !bytes.Equal(got, payload) {
		t.Errorf("BytesValue() = %v, want %v", got, payload)
	}
}

// TestM0073DatumNonArenaPathUnchanged pins the backward-compat
// invariant: KindString / KindBytes Datums with a Buf payload
// continue to work via the unsafe.String / Buf path.
// ArenaID=0 for these; the dispatch in StringValue/BytesValue
// takes the Buf branch. M0107-0002: mctx field removed; use ArenaID.
func TestM0073DatumNonArenaPathUnchanged(t *testing.T) {
	d := NewStringDatum("legacy")
	if d.Kind != KindString {
		t.Fatalf("NewStringDatum: Kind = %d, want KindString", d.Kind)
	}
	if d.ArenaID != 0 {
		t.Errorf("NewStringDatum: ArenaID should be 0 (Buf-backed); got %d", d.ArenaID)
	}
	if got := d.StringValue(); got != "legacy" {
		t.Errorf("StringValue() = %q, want %q", got, "legacy")
	}

	bd := NewBytesDatum([]byte{0x10, 0x20})
	if bd.Kind != KindBytes {
		t.Fatalf("NewBytesDatum: Kind = %d, want KindBytes", bd.Kind)
	}
	if bd.ArenaID != 0 {
		t.Errorf("NewBytesDatum: ArenaID should be 0 (Buf-backed)")
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
	if dst[1].ArenaID != 0 {
		t.Errorf("dst[1].ArenaID = %d, want 0 after promotion (Buf-backed)", dst[1].ArenaID)
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

// TestBigNumericArenaSurvivesReset pins the sibling-path fix: a
// mctx-backed big-numeric (flagBigNumeric) must be fully detached by
// cloneRowOwned / MaterializeArena at the retention boundary, so a
// producer arena Reset cannot corrupt it. Before the fix, rowHasArena /
// cloneRowOwned only handled KindString/KindBytes, leaving big-numeric
// build rows aliasing an arena that resets at scale (the class of bug
// behind wrong join results on large builds).
func TestBigNumericArenaSurvivesReset(t *testing.T) {
	a := mctx.Acquire(nil, mctx.KindStmt)
	// A value beyond int64 range forces the flagBigNumeric lane.
	bi, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	src := newBigNumericInCtx(a, bi, 5)
	if src.Flags&flagBigNumeric == 0 || src.ArenaID == 0 {
		t.Fatalf("setup: expected arena-backed big-numeric, got Flags=%d ArenaID=%d", src.Flags, src.ArenaID)
	}

	// rowHasArena must now SEE the big-numeric (not just String/Bytes).
	if !rowHasArena(Row{src}) {
		t.Fatalf("rowHasArena did not detect an arena-backed big-numeric")
	}

	// Detach at the retention boundary, THEN destroy the source arena.
	owned := cloneRowOwned(Row{src})
	a.Reset()
	a.Release()

	got := owned[0].NumericBigValue()
	if got.Cmp(bi) != 0 {
		t.Errorf("after source-arena Reset, cloned big-numeric = %s, want %s (shallow copy corrupted it)", got, bi)
	}
	if owned[0].NumericScaleValue() != 5 {
		t.Errorf("scale = %d, want 5", owned[0].NumericScaleValue())
	}
}
