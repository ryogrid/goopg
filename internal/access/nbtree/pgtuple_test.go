package nbtree

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 1 guards for the upstream IndexTupleData layer
// (pgtuple.go). These pin the byte layout that a real PG 18.3 will read once
// slices 2/3 flip the writers; every expectation is derived from
// postgres/src/include/access/itup.h, .../access/nbtree.h and
// postgres/src/backend/access/common/{indextuple,heaptuple}.c.

// Common attribute shapes, matching pg_type's physical columns.
var (
	attOid   = PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}
	attInt2  = PGIndexAttr{Len: 2, ByVal: true, AlignBy: 2, Storage: 'p'}
	attInt8  = PGIndexAttr{Len: 8, ByVal: true, AlignBy: 8, Storage: 'd'}
	attName  = PGIndexAttr{Len: 64, ByVal: false, AlignBy: 1, Storage: 'p'}
	attText  = PGIndexAttr{Len: -1, ByVal: false, AlignBy: 4, Storage: 'x'}
	attPlain = PGIndexAttr{Len: -1, ByVal: false, AlignBy: 4, Storage: 'p'}
)

func TestPGIndexTupleLayoutMatchesUpstream(t *testing.T) {
	if SizeOfIndexTupleData != 8 {
		t.Errorf("sizeof(IndexTupleData) = %d, upstream is 8", SizeOfIndexTupleData)
	}
	if SizeOfIndexAttributeBitMapData != 4 {
		t.Errorf("sizeof(IndexAttributeBitMapData) = %d, upstream is 4 (INDEX_MAX_KEYS=32)",
			SizeOfIndexAttributeBitMapData)
	}
	if IndexAltTIDMask != IndexAMReservedBit {
		t.Errorf("INDEX_ALT_TID_MASK (%#x) must be the AM-reserved bit (%#x)", IndexAltTIDMask, IndexAMReservedBit)
	}
	// IndexInfoFindDataOffset: 8 without a bitmap, MAXALIGN(8+4)=16 with one.
	if got := PGIndexInfoFindDataOffset(0); got != 8 {
		t.Errorf("data offset without nulls = %d, want 8", got)
	}
	if got := PGIndexInfoFindDataOffset(IndexNullMask); got != 16 {
		t.Errorf("data offset with nulls = %d, want 16", got)
	}
}

// TestBTMaxItemSizeMatchesUpstream re-derives nbtree.h's ceilings from the same
// terms, so a BLCKSZ or opaque-size change cannot silently drift them.
func TestBTMaxItemSizeMatchesUpstream(t *testing.T) {
	want := MaxAlignDown((storage.BlockSize -
		MaxAlign(storage.SizeOfPageHeaderData+3*4) -
		MaxAlign(SizeOfBTPageOpaquePG)) / 3)
	if BTMaxItemSizeNoHeapTid != want {
		t.Errorf("BTMaxItemSizeNoHeapTid = %d, want %d", BTMaxItemSizeNoHeapTid, want)
	}
	if BTMaxItemSize != want-MaxAlign(6) {
		t.Errorf("BTMaxItemSize = %d, want %d", BTMaxItemSize, want-MaxAlign(6))
	}
	// The literal values for the only BLCKSZ goopg supports.
	if BTMaxItemSizeNoHeapTid != 2712 || BTMaxItemSize != 2704 {
		t.Errorf("with BLCKSZ=8192 upstream has 2712/2704, got %d/%d",
			BTMaxItemSizeNoHeapTid, BTMaxItemSize)
	}
	if err := CheckPGBTItemSize(BTMaxItemSize, true); err != nil {
		t.Errorf("size exactly at the heap-TID limit rejected: %v", err)
	}
	if err := CheckPGBTItemSize(BTMaxItemSize+1, true); err == nil {
		t.Error("size one past the heap-TID limit accepted")
	}
	if err := CheckPGBTItemSize(BTMaxItemSizeNoHeapTid, false); err != nil {
		t.Errorf("size exactly at the no-heap-TID limit rejected: %v", err)
	}
}

// TestPGItemPointerIsTwoHalves is the (bi_hi, bi_lo) trap: BlockIdData is two
// uint16 halves in struct order, NOT a flat little-endian uint32. Encoding
// block 3 as {3,0,0,0} makes PG read block 196608.
func TestPGItemPointerIsTwoHalves(t *testing.T) {
	var buf [6]byte
	PutPGItemPointer(buf[:], storage.ItemPointer{Block: 3, Offset: 1})
	if !bytes.Equal(buf[:], []byte{0, 0, 3, 0, 1, 0}) {
		t.Fatalf("block 3 encoded as %v, want [0 0 3 0 1 0]", buf)
	}
	for _, tid := range []storage.ItemPointer{
		{Block: 0, Offset: 1},
		{Block: 3, Offset: 7},
		{Block: 0x1234_5678, Offset: 0xFFFF},
		{Block: storage.InvalidBlockNumber, Offset: 0},
	} {
		PutPGItemPointer(buf[:], tid)
		if got := PGItemPointerAt(buf[:]); got != tid {
			t.Errorf("round trip %v -> %v", tid, got)
		}
	}
}

// TestFormPGIndexTupleFixedWidthGoldens pins the byte images of the fixed-width
// shapes goopg's already-PG-validated bootstrap encoders emit
// (internal/initdb/btree_index_bootstrap.go). Those encoders are known good —
// a real PG 18.3 reads the bootstrap catalog indexes they write — so matching
// them byte-for-byte is an oracle, not a self-consistency check.
// TestPGIndexTupleMatchesBootstrapEncoders in internal/initdb runs the same
// comparison against the encoders themselves.
func TestFormPGIndexTupleFixedWidthGoldens(t *testing.T) {
	tid := storage.ItemPointer{Block: 3, Offset: 5}

	// One oid key: hoff 8 + 4 bytes of data, MAXALIGN'd to 16.
	oid := u32(0x2A)
	got, err := FormPGIndexTuple([]PGIndexAttr{attOid}, [][]byte{oid}, []bool{false}, tid)
	if err != nil {
		t.Fatalf("oid key: %v", err)
	}
	want := make([]byte, 16)
	copy(want, []byte{0, 0, 3, 0, 5, 0, 16, 0})
	copy(want[8:], oid)
	if !bytes.Equal(got, want) {
		t.Errorf("oid key:\n got %v\nwant %v", got, want)
	}

	// (oid, int2): the int2 needs 2-byte alignment, which offset 12 already
	// satisfies, so size = MAXALIGN(8+4+2) = 16 with two pad bytes.
	got, err = FormPGIndexTuple([]PGIndexAttr{attOid, attInt2},
		[][]byte{u32(0x2A), u16(0xFFFE)}, []bool{false, false}, tid)
	if err != nil {
		t.Fatalf("(oid,int2) key: %v", err)
	}
	want = make([]byte, 16)
	copy(want, []byte{0, 0, 3, 0, 5, 0, 16, 0})
	copy(want[8:], u32(0x2A))
	copy(want[12:], u16(0xFFFE))
	if !bytes.Equal(got, want) {
		t.Errorf("(oid,int2) key:\n got %v\nwant %v", got, want)
	}

	// (name, oid): NameData is 64 bytes with attalign 'c', so the oid lands
	// at 8+64 = 72 (already 4-aligned) and size = MAXALIGN(76) = 80.
	nameBytes := make([]byte, 64)
	copy(nameBytes, "pg_class")
	got, err = FormPGIndexTuple([]PGIndexAttr{attName, attOid},
		[][]byte{nameBytes, u32(11)}, []bool{false, false}, tid)
	if err != nil {
		t.Fatalf("(name,oid) key: %v", err)
	}
	if len(got) != 80 {
		t.Fatalf("(name,oid) size = %d, want 80", len(got))
	}
	if PGIndexTupleSize(got) != 80 {
		t.Errorf("t_info size = %d, want 80", PGIndexTupleSize(got))
	}
	if !bytes.Equal(got[8:72], nameBytes) || !bytes.Equal(got[72:76], u32(11)) {
		t.Errorf("(name,oid) payload wrong: %v", got)
	}
	if PGIndexTupleHasVarwidths(got) {
		t.Error("a name/oid tuple must not set INDEX_VAR_MASK — neither column is varying-width")
	}
}

// TestFormPGIndexTupleAlignmentPadding checks att_nominal_alignby is applied
// per attribute, not just at the end: an int2 followed by an int8 must leave a
// 6-byte hole so the int8 lands on an 8-byte boundary.
func TestFormPGIndexTupleAlignmentPadding(t *testing.T) {
	got, err := FormPGIndexTuple([]PGIndexAttr{attInt2, attInt8},
		[][]byte{u16(7), u64(9)}, []bool{false, false}, storage.ItemPointer{})
	if err != nil {
		t.Fatal(err)
	}
	// 8 (header) + 2 (int2) -> pad to 16 -> +8 = 24.
	if len(got) != 24 {
		t.Fatalf("size = %d, want 24 (int2 at 8, int8 aligned to 16)", len(got))
	}
	if !bytes.Equal(got[10:16], make([]byte, 6)) {
		t.Errorf("alignment hole at [10:16] is not zero: %v", got[10:16])
	}
	if !bytes.Equal(got[16:24], u64(9)) {
		t.Errorf("int8 not at offset 16: %v", got[16:24])
	}

	vals, isnull, err := DeformPGIndexTuple(got, []PGIndexAttr{attInt2, attInt8}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if isnull[0] || isnull[1] {
		t.Fatal("no attribute is null")
	}
	if !bytes.Equal(vals[0], u16(7)) || !bytes.Equal(vals[1], u64(9)) {
		t.Errorf("deform gave %v / %v", vals[0], vals[1])
	}
}

// TestFormPGIndexTupleShortVarlenaConversion covers index_form_tuple's
// packable path: a 4-byte-header varlena short enough to fit the 1-byte header
// is re-headered AND loses its alignment padding entirely (fill_val's
// "convert to short varlena -- no alignment" branch).
func TestFormPGIndexTupleShortVarlenaConversion(t *testing.T) {
	// An int2 then a 3-character text. Without the conversion the text would
	// align to 4 and occupy 4+3 bytes; with it, it sits at offset 10 as
	// 1+3 bytes.
	text := varlena4B("abc")
	got, err := FormPGIndexTuple([]PGIndexAttr{attInt2, attText},
		[][]byte{u16(1), text}, []bool{false, false}, storage.ItemPointer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 16 { // MAXALIGN(8 + 2 + 4) = 16
		t.Fatalf("size = %d, want 16", len(got))
	}
	if got[10] != byte(4<<1)|0x01 {
		t.Errorf("short varlena header = %#x, want %#x", got[10], byte(4<<1)|0x01)
	}
	if string(got[11:14]) != "abc" {
		t.Errorf("short varlena payload = %q", got[11:14])
	}
	if !PGIndexTupleHasVarwidths(got) {
		t.Error("INDEX_VAR_MASK not set for a varlena attribute")
	}

	vals, _, err := DeformPGIndexTuple(got, []PGIndexAttr{attInt2, attText}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals[1]) != 4 || string(vals[1][1:]) != "abc" {
		t.Errorf("deformed short varlena = %v", vals[1])
	}

	// attstorage 'p' is NOT packable: the same value keeps its 4-byte header
	// and its 4-byte alignment.
	got, err = FormPGIndexTuple([]PGIndexAttr{attInt2, attPlain},
		[][]byte{u16(1), text}, []bool{false, false}, storage.ItemPointer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 24 { // 8 + 2 -> pad to 12 -> +7 = 19 -> MAXALIGN 24
		t.Fatalf("plain-storage size = %d, want 24", len(got))
	}
	if !bytes.Equal(got[12:19], text) {
		t.Errorf("plain-storage varlena not stored verbatim at 12: %v", got[12:19])
	}
}

// TestFormPGIndexTupleNullBitmap pins the two things that are easy to invert:
// the bitmap sits at offset 8 (NOT at hoff — the MAXALIGN pad between bitmap
// and data belongs to the data area), and a SET bit means the value is
// PRESENT.
func TestFormPGIndexTupleNullBitmap(t *testing.T) {
	attrs := []PGIndexAttr{attOid, attOid, attOid}
	vals := [][]byte{u32(1), nil, u32(3)}
	got, err := FormPGIndexTuple(attrs, vals, []bool{false, true, false}, storage.ItemPointer{})
	if err != nil {
		t.Fatal(err)
	}
	if !PGIndexTupleHasNulls(got) {
		t.Fatal("INDEX_NULL_MASK not set")
	}
	if got[8] != 0b101 {
		t.Errorf("null bitmap byte = %#b, want 0b101 (set = present, LSB = attribute 0)", got[8])
	}
	if got[9] != 0 || got[10] != 0 || got[11] != 0 {
		t.Errorf("bitmap bytes past the described attributes must be zero: %v", got[9:12])
	}
	// hoff = MAXALIGN(8+4) = 16, two live oids -> MAXALIGN(24) = 24.
	if len(got) != 24 {
		t.Fatalf("size = %d, want 24", len(got))
	}
	if !bytes.Equal(got[16:20], u32(1)) || !bytes.Equal(got[20:24], u32(3)) {
		t.Errorf("payload wrong (the null attribute must occupy no space): %v", got[16:])
	}

	rvals, isnull, err := DeformPGIndexTuple(got, attrs, 3)
	if err != nil {
		t.Fatal(err)
	}
	if isnull[0] || !isnull[1] || isnull[2] {
		t.Fatalf("null flags = %v, want [false true false]", isnull)
	}
	if !bytes.Equal(rvals[0], u32(1)) || !bytes.Equal(rvals[2], u32(3)) {
		t.Errorf("deformed values = %v / %v", rvals[0], rvals[2])
	}
}

// TestBTreePivotTupleAccessors covers nbtree.h's alternative-TID overlay: the
// pivot flag, the key-attribute count in t_tid's offset half, the downlink in
// its block half, and the tiebreaker heap TID at the tuple's tail.
func TestBTreePivotTupleAccessors(t *testing.T) {
	attrs := []PGIndexAttr{attOid, attOid}
	raw, err := FormPGIndexTuple(attrs, [][]byte{u32(1), u32(2)}, []bool{false, false},
		storage.ItemPointer{Block: 9, Offset: 4})
	if err != nil {
		t.Fatal(err)
	}
	if BTreeTupleIsPivot(raw) || BTreeTupleIsPosting(raw) {
		t.Fatal("a freshly formed tuple is neither pivot nor posting")
	}
	if got, _ := BTreeTupleGetHeapTID(raw); got != (storage.ItemPointer{Block: 9, Offset: 4}) {
		t.Errorf("non-pivot heap TID = %v", got)
	}
	if got := BTreeTupleGetNAtts(raw, 2); got != 2 {
		t.Errorf("non-pivot natts = %d, want the index's column count 2", got)
	}

	// Truncate to one key attribute, keeping no heap TID.
	if err := BTreeTupleSetNAtts(raw, 1, false); err != nil {
		t.Fatal(err)
	}
	if !BTreeTupleIsPivot(raw) {
		t.Fatal("BTreeTupleSetNAtts must set INDEX_ALT_TID_MASK")
	}
	if BTreeTupleIsPosting(raw) {
		t.Fatal("BT_IS_POSTING must be left unset")
	}
	if got := BTreeTupleGetNAtts(raw, 2); got != 1 {
		t.Errorf("pivot natts = %d, want 1", got)
	}
	if _, ok := BTreeTupleGetHeapTID(raw); ok {
		t.Error("a pivot without BT_PIVOT_HEAP_TID_ATTR has no heap TID")
	}

	// The downlink lives in the block half and survives the natts stamp.
	BTreeTupleSetDownLink(raw, 0x1234_5678)
	if got := BTreeTupleGetDownLink(raw); got != 0x1234_5678 {
		t.Errorf("downlink = %#x", got)
	}
	if got := BTreeTupleGetNAtts(raw, 2); got != 1 {
		t.Errorf("downlink write clobbered natts: %d", got)
	}

	// A pivot that kept its tiebreaker TID reads it from the last 6 bytes.
	tail, err := FormPGIndexTuple(attrs, [][]byte{u32(1), u32(2)}, []bool{false, false}, storage.ItemPointer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := BTreeTupleSetNAtts(tail, 2, true); err != nil {
		t.Fatal(err)
	}
	want := storage.ItemPointer{Block: 77, Offset: 3}
	PutPGItemPointer(tail[len(tail)-6:], want)
	if !BTreeTupleIsPivot(tail) {
		t.Fatal("heap-TID pivot not recognised as a pivot")
	}
	got, ok := BTreeTupleGetHeapTID(tail)
	if !ok || got != want {
		t.Errorf("tiebreaker heap TID = %v (ok=%v), want %v", got, ok, want)
	}

	// Guard rails.
	if err := BTreeTupleSetNAtts(raw, IndexMaxKeys+1, false); err == nil {
		t.Error("natts past INDEX_MAX_KEYS accepted")
	}
	if err := BTreeTupleSetNAtts(raw, 0, true); err == nil {
		t.Error("heap-TID pivot with zero key attributes accepted")
	}
}

func TestFormPGIndexTupleErrors(t *testing.T) {
	if _, err := FormPGIndexTuple([]PGIndexAttr{attOid}, [][]byte{u32(1)}, nil, storage.ItemPointer{}); err == nil {
		t.Error("arity mismatch accepted")
	}
	tooMany := make([]PGIndexAttr, IndexMaxKeys+1)
	if _, err := FormPGIndexTuple(tooMany, make([][]byte, IndexMaxKeys+1),
		make([]bool, IndexMaxKeys+1), storage.ItemPointer{}); err == nil {
		t.Error("more than INDEX_MAX_KEYS columns accepted")
	}
	// A single value that pushes the tuple past INDEX_SIZE_MASK must be
	// rejected by index_form_tuple's own ceiling, before any nbtree check.
	big := varlena4B(string(make([]byte, IndexSizeMask)))
	if _, err := FormPGIndexTuple([]PGIndexAttr{attText}, [][]byte{big},
		[]bool{false}, storage.ItemPointer{}); err == nil {
		t.Error("tuple larger than INDEX_SIZE_MASK accepted")
	}
	// An external TOAST pointer is never storable in an index tuple.
	ext := []byte{0x01, 18, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := FormPGIndexTuple([]PGIndexAttr{attText}, [][]byte{ext},
		[]bool{false}, storage.ItemPointer{}); err == nil {
		t.Error("external TOAST pointer accepted")
	}
	if _, err := PGAlignBy('z'); err == nil {
		t.Error("invalid attalign accepted")
	}
	for c, want := range map[byte]uint8{'c': 1, 's': 2, 'i': 4, 'd': 8} {
		if got, err := PGAlignBy(c); err != nil || got != want {
			t.Errorf("PGAlignBy(%q) = %d, %v; want %d", c, got, err, want)
		}
	}
}

func TestDeformPGIndexTupleRejectsOverruns(t *testing.T) {
	raw, err := FormPGIndexTuple([]PGIndexAttr{attOid}, [][]byte{u32(1)}, []bool{false}, storage.ItemPointer{})
	if err != nil {
		t.Fatal(err)
	}
	// Claim two attributes where only one was described.
	if _, _, err := DeformPGIndexTuple(raw, []PGIndexAttr{attOid}, 2); err == nil {
		t.Error("natts past the descriptor accepted")
	}
	// A t_info size larger than the supplied bytes is corruption, not a
	// short read to be tolerated.
	bad := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint16(bad[6:8], uint16(len(bad)+8))
	if _, _, err := DeformPGIndexTuple(bad, []PGIndexAttr{attOid}, 1); err == nil {
		t.Error("t_info size past the buffer accepted")
	}
	// Three oids described but only one stored. Note attribute 1 does NOT
	// error: it lands at offset 12, inside the tuple's own MAXALIGN pad, and
	// upstream's index_getattr would read that pad just the same — a tuple
	// cannot tell you its attribute count. Attribute 2 starts at 16, which is
	// past the 16-byte tuple, and that IS detectable.
	if _, _, err := DeformPGIndexTuple(raw, []PGIndexAttr{attOid, attOid, attOid}, 3); err == nil {
		t.Error("attribute past the tuple end accepted")
	}
}

// helpers

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

// varlena4B builds a long-form (4-byte header) uncompressed varlena.
func varlena4B(s string) []byte {
	b := make([]byte, 4+len(s))
	binary.LittleEndian.PutUint32(b[0:4], uint32(len(s)+4)<<2)
	copy(b[4:], s)
	return b
}
