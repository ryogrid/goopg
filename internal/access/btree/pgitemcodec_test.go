package btree

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B1 guards. Like the comparison seam before it,
// the item codec has to be an EXACT no-op in the blob format and a complete
// switch in the tuple format, so both halves are pinned here.

func int4Desc() *PGIndexKeyDesc {
	attr := int4Attr()
	attr.Compare = PGCompareInt4
	return &PGIndexKeyDesc{Attrs: []PGKeyAttr{attr}}
}

// TestItemCodecBlobFormatUnchanged pins the no-op half against the pre-seam
// encoders directly: whatever route the engine now takes, a descriptor-less
// index must still produce `PGBTItemRaw`/`PGBTPivotRaw` bytes and the same
// space budget, because that is what is on disk.
func TestItemCodecBlobFormatUnchanged(t *testing.T) {
	tid := storage.ItemPointer{Block: 77, Offset: 3}
	for _, it := range []item{
		{ptr: tid, key: EncodeInt4(42)},
		{ptr: tid, key: nil},
		{ptr: storage.ItemPointer{Block: 9}, key: EncodeInt4(-1), pivot: true},
		{ptr: storage.ItemPointer{Block: 9}, key: nil, pivot: true},
	} {
		got := blobFormat.marshal(it)
		if want := it.marshal(); !bytes.Equal(got, want) {
			t.Fatalf("marshal(%+v) = %x, want %x", it, got, want)
		}
		if want := SizeOfIndexTupleData + len(it.key); blobFormat.bodySize(it.key) != want {
			t.Fatalf("bodySize = %d, want %d", blobFormat.bodySize(it.key), want)
		}
		// The BODY is unchanged; the BUDGET is MAXALIGNed since 3b-3a,
		// because that is what PageAddItemRaw now allocates. The blob bytes
		// on disk are still the same bytes — only the padding between items
		// (which no reader ever addresses) is new.
		if want := 4 + MaxAlign(SizeOfIndexTupleData+len(it.key)); blobFormat.itemEncodedSize(it) != want {
			t.Fatalf("itemEncodedSize = %d, want %d", blobFormat.itemEncodedSize(it), want)
		}
		back, err := blobFormat.parse(got)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		// The blob format strips the header: the parsed key is the payload.
		if !bytes.Equal(back.key, it.key) || back.pivot != it.pivot {
			t.Fatalf("parse round trip = %+v, want key %x pivot %v", back, it.key, it.pivot)
		}
	}
}

// TestItemCodecTupleFormatKeepsWholeTuple pins the switch half for a plain
// leaf item: the key IS the tuple, so marshalling adds no second header and
// parsing strips none — the exact confusion that would make a flipped tree
// unreadable while every unit test still passed.
func TestItemCodecTupleFormatKeepsWholeTuple(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	heapTID := storage.ItemPointer{Block: 12, Offset: 4}
	raw := tup(t, f.desc.Attrs, [][]byte{int4Val(42)}, heapTID)

	it := item{ptr: heapTID, key: raw}
	got := f.marshal(it)
	if !bytes.Equal(got, raw) {
		t.Fatalf("marshal = %x, want the tuple itself %x", got, raw)
	}
	if f.bodySize(raw) != len(raw) {
		t.Fatalf("bodySize = %d, want %d (no added header)", f.bodySize(raw), len(raw))
	}
	if f.itemEncodedSize(it) != 4+len(raw) {
		t.Fatalf("itemEncodedSize = %d, want %d", f.itemEncodedSize(it), 4+len(raw))
	}

	back, err := f.parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !bytes.Equal(back.key, raw) {
		t.Fatalf("parse key = %x, want the whole tuple %x", back.key, raw)
	}
	if back.ptr != heapTID || back.pivot {
		t.Fatalf("parse = %+v, want ptr %+v and pivot=false", back, heapTID)
	}

	// item.ptr stays authoritative: a rebuilt item (dedup recovery, posting
	// split) must re-stamp t_tid rather than keep the tuple's stale one.
	moved := storage.ItemPointer{Block: 500, Offset: 9}
	if got := PGIndexTupleTID(f.marshal(item{ptr: moved, key: raw})); got != moved {
		t.Fatalf("t_tid = %+v, want the item's ptr %+v", got, moved)
	}
}

// TestItemCodecTupleFormatPivot pins the pivot half: a downlink's t_tid is the
// child block plus nbtree status bits, never a heap TID, and a parse →
// re-marshal round trip (the split redistribution and both vacuum page
// rewrites do exactly that) must not un-truncate the tuple.
func TestItemCodecTupleFormatPivot(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	raw := tup(t, f.desc.Attrs, [][]byte{int4Val(7)}, storage.ItemPointer{Block: 1, Offset: 1})

	sep := f.marshal(item{ptr: storage.ItemPointer{Block: 314}, key: raw, pivot: true})
	if !PGIndexTupleIsAltTID(sep) {
		t.Fatalf("pivot tuple is not INDEX_ALT_TID: t_info=%#x", pgTInfo(sep))
	}
	if got := BTreeTupleGetDownLink(sep); got != 314 {
		t.Fatalf("downlink = %d, want 314", got)
	}
	if got := BTreeTupleGetNAtts(sep, 1); got != 1 {
		t.Fatalf("natts = %d, want 1 (the descriptor's key count)", got)
	}
	if len(sep) != len(raw) {
		t.Fatalf("pivot len = %d, want %d (no suffix truncation yet — slice 3b-3)", len(sep), len(raw))
	}

	back, err := f.parse(sep)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !back.pivot || back.ptr.Block != 314 || !bytes.Equal(back.key, sep) {
		t.Fatalf("parse = %+v, want pivot with downlink 314 and the whole tuple as key", back)
	}
	if again := f.marshal(back); !bytes.Equal(again, sep) {
		t.Fatalf("re-marshal = %x, want %x (round trip must not change the tuple)", again, sep)
	}

	// A zero-attribute minus-infinity downlink: a bare header, nothing else.
	neg := f.marshal(item{ptr: storage.ItemPointer{Block: 8}, key: make([]byte, SizeOfIndexTupleData), pivot: true})
	if got := BTreeTupleGetNAtts(neg, 1); got != 0 {
		t.Fatalf("minus-infinity natts = %d, want 0", got)
	}
}

// TestTupleFormatTreeOrdersByDatum is the end-to-end guard, and the reason the
// codec and the comparer are ONE object: a tree opened with a descriptor must
// store tuple-shaped keys AND order them with btint4cmp. int4 is the sharp
// case — -5 is bytewise the largest of these values and numerically the
// smallest, so a tree that kept either half of the old behaviour fails here.
func TestTupleFormatTreeOrdersByDatum(t *testing.T) {
	desc := int4Desc()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9100, Fork: storage.MainFork}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	if bt.KeyDesc() != desc {
		t.Fatalf("tree did not keep the descriptor")
	}

	values := []int32{42, -5, 7, 1000, 0, -1}
	keys := make(map[int32][]byte, len(values))
	for i, v := range values {
		tid := storage.ItemPointer{Block: storage.BlockNumber(100 + i), Offset: uint16(i + 1)}
		raw := tup(t, desc.Attrs, [][]byte{int4Val(v)}, tid)
		keys[v] = raw
		if err := bt.Insert(raw, tid); err != nil {
			t.Fatalf("Insert(%d): %v", v, err)
		}
	}
	for i, v := range values {
		got, ok, err := bt.Search(keys[v])
		if err != nil || !ok {
			t.Fatalf("Search(%d) = (%+v, %v, %v), want found", v, got, ok, err)
		}
		if want := (storage.ItemPointer{Block: storage.BlockNumber(100 + i), Offset: uint16(i + 1)}); got != want {
			t.Fatalf("Search(%d) = %+v, want %+v", v, got, want)
		}
	}

	// The ON-PAGE bytes must be the tuple itself, with no second header
	// wrapped around it. Ordering alone does not catch a layout slip: the blob
	// layout would nest a valid tuple inside its payload and compare it just
	// as correctly, while a real PG (and amcheck) would read the outer header
	// and see garbage.
	slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 1})
	if err != nil {
		t.Fatalf("Pin leaf: %v", err)
	}
	raw, err := pgGetItemRaw(slot.Page(), 1)
	pool.Unpin(slot)
	if err != nil {
		t.Fatalf("pgGetItemRaw: %v", err)
	}
	if PGIndexTupleSize(raw) != len(raw) {
		t.Fatalf("on-page item %x: t_info size %d != length %d", raw, PGIndexTupleSize(raw), len(raw))
	}
	if want := len(keys[values[0]]); len(raw) != want {
		t.Fatalf("on-page item is %d bytes, want %d — the key was wrapped in a second header", len(raw), want)
	}

	// A full scan must come back in int4 order, not byte order.
	var seen []int32
	if err := bt.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		vals, isnull, derr := DeformPGIndexTuple(key, desc.Physical(), 1)
		if derr != nil {
			return false, derr
		}
		if isnull[0] {
			t.Fatalf("unexpected NULL key")
		}
		seen = append(seen, int32(decodeLEUint32(vals[0])))
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	want := []int32{-5, -1, 0, 7, 42, 1000}
	if len(seen) != len(want) {
		t.Fatalf("scan returned %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("scan order %v, want %v", seen, want)
		}
	}
}

func decodeLEUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// TestTupleFormatTreeSplits drives the tuple format through the paths a
// four-value tree never touches: page splits, pivot/high-key construction,
// multi-level descent and right-link recovery. Those are the paths where a
// layout slip is invisible in the item bytes and visible only as a lost row,
// so the assertion is the complete key set, in int4 order, after enough
// inserts to build a tree several levels deep.
func TestTupleFormatTreeSplits(t *testing.T) {
	desc := int4Desc()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9101, Fork: storage.MainFork}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}

	// Insert in an order unrelated to the key order, spanning the sign
	// boundary so the bytewise and int4 orders disagree throughout.
	const n = 3000
	for i := 0; i < n; i++ {
		v := int32((i*7919)%n) - n/2
		tid := storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)}
		raw := tup(t, desc.Attrs, [][]byte{int4Val(v)}, tid)
		if err := bt.Insert(raw, tid); err != nil {
			t.Fatalf("Insert(%d): %v", v, err)
		}
	}

	var seen []int32
	if err := bt.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		vals, isnull, derr := DeformPGIndexTuple(key, desc.Physical(), 1)
		if derr != nil {
			return false, derr
		}
		if isnull[0] {
			t.Fatalf("unexpected NULL key")
		}
		seen = append(seen, int32(decodeLEUint32(vals[0])))
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if len(seen) != n {
		t.Fatalf("scan returned %d keys, want %d", len(seen), n)
	}
	for i, v := range seen {
		if want := int32(i - n/2); v != want {
			t.Fatalf("scan[%d] = %d, want %d (int4 order across the sign boundary)", i, v, want)
		}
	}
}

// TestTupleFormatMinusInfinitySearchKey pins the empty-operand rule the split
// test discovered: a descent with no lower bound compares an EMPTY key against
// real tuples, which upstream models as a zero-length scankey. It must sort
// first, and it must not panic reading a header that is not there.
func TestTupleFormatMinusInfinitySearchKey(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	real := tup(t, f.desc.Attrs, [][]byte{int4Val(-1)}, storage.ItemPointer{Block: 1, Offset: 1})
	for _, empty := range [][]byte{nil, {}, {0x00}} {
		if got := f.compare(empty, real); got >= 0 {
			t.Fatalf("compare(%x, tuple) = %d, want < 0 (minus infinity)", empty, got)
		}
		if got := f.compare(real, empty); got <= 0 {
			t.Fatalf("compare(tuple, %x) = %d, want > 0", empty, got)
		}
	}
}
