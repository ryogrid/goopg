package nbtree

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 2 guards: the on-page item is now an upstream
// IndexTupleData, so these tests pin the header layout, the alternative-TID
// discriminators, and the two properties the old goopg-private layout used to
// provide implicitly (recoverable key length, posting/plain separation).

// TestItemMarshalIsIndexTupleData pins the byte layout of a plain item against
// the S11.4-slice-1 codec, which is itself byte-validated against the
// hand-rolled encoders a real PG 18.3 reads (see
// TestPGIndexTupleMatchesBootstrapEncoders). Treating the opaque key as one
// fixed-width pass-by-reference attribute is exactly what the current writer
// does; slice 3 replaces it with the real index descriptor.
func TestItemMarshalIsIndexTupleData(t *testing.T) {
	key := []byte("abcdefghij")
	tid := storage.ItemPointer{Block: 0x0001_2345, Offset: 7}

	got := item{ptr: tid, key: key}.marshal()

	want, err := FormPGIndexTuple(
		[]PGIndexAttr{{Len: int16(len(key)), ByVal: false, AlignBy: 1, Storage: 'p'}},
		[][]byte{key}, []bool{false}, tid)
	if err != nil {
		t.Fatalf("FormPGIndexTuple: %v", err)
	}
	// index_form_tuple MAXALIGNs the tuple size ("be conservative"); goopg
	// cannot yet, because the opaque key's length is only recoverable as
	// size - SizeOfIndexTupleData and padding would destroy it. So the
	// comparison is: identical up to the padding, and the padding is the only
	// difference. Slice 3 (per-attribute datums) removes the exception — see
	// the M0130-S11.4 deferral-ledger row.
	if MaxAlign(len(got)) != len(want) {
		t.Fatalf("size relationship changed: len(got)=%d MaxAlign=%d len(want)=%d",
			len(got), MaxAlign(len(got)), len(want))
	}
	if !bytes.Equal(got[:6], want[:6]) {
		t.Fatalf("t_tid differs:\n got %x\nwant %x", got[:6], want[:6])
	}
	if !bytes.Equal(got[SizeOfIndexTupleData:], want[SizeOfIndexTupleData:len(got)]) {
		t.Fatalf("data area differs:\n got %x\nwant %x", got[SizeOfIndexTupleData:], want[SizeOfIndexTupleData:len(got)])
	}
	for i, b := range want[len(got):] {
		if b != 0 {
			t.Fatalf("index_form_tuple padding byte %d is %#x, not zero", i, b)
		}
	}
	if PGIndexTupleSize(want) != len(want) || PGIndexTupleSize(got) != len(got) {
		t.Fatalf("t_info size must equal the encoded length in both forms")
	}

	// Header field-by-field, so a change to BOTH encoders at once still trips.
	if n := PGIndexTupleSize(got); n != len(got) {
		t.Errorf("t_info size = %d, want %d", n, len(got))
	}
	if PGIndexTupleHasNulls(got) || PGIndexTupleHasVarwidths(got) || PGIndexTupleIsAltTID(got) {
		t.Errorf("t_info = %#x: no flag bit may be set for a plain item", binary.LittleEndian.Uint16(got[6:8]))
	}
	if p := PGIndexTupleTID(got); p != tid {
		t.Errorf("t_tid = %+v, want %+v", p, tid)
	}
}

// TestParseItemRoundTrip covers the key-length recovery that used to come from
// an explicit keyLen field and now comes from t_info's size, including the
// zero-length key of an internal page's negative-infinity downlink.
func TestParseItemRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
		ptr  storage.ItemPointer
	}{
		{"empty key (neg infinity downlink)", nil, storage.ItemPointer{Block: 9}},
		{"4-byte key", EncodeInt4(42), storage.ItemPointer{Block: 3, Offset: 5}},
		{"max block/offset", []byte("k"), storage.ItemPointer{Block: 0xFFFF_FFFE, Offset: 0xFFFE}},
		{"long key", bytes.Repeat([]byte("x"), 1000), storage.ItemPointer{Block: 1, Offset: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := item{ptr: tc.ptr, key: tc.key}.marshal()
			got, err := parseItem(raw)
			if err != nil {
				t.Fatalf("parseItem: %v", err)
			}
			if got.ptr != tc.ptr || !bytes.Equal(got.key, tc.key) {
				t.Fatalf("round trip: got {ptr=%+v key=%x}, want {ptr=%+v key=%x}",
					got.ptr, got.key, tc.ptr, tc.key)
			}
			// parseItemNoCopy must agree with parseItem on every field; it
			// differs only in whether key aliases raw.
			nc, err := parseItemNoCopy(raw)
			if err != nil {
				t.Fatalf("parseItemNoCopy: %v", err)
			}
			if nc.ptr != got.ptr || !bytes.Equal(nc.key, got.key) {
				t.Fatalf("parseItemNoCopy disagrees with parseItem: %+v vs %+v", nc, got)
			}
		})
	}
}

// TestParseItemRejectsPosting: a posting tuple decoded as a plain item would
// hand the caller nbtree status bits (TID count, array offset) as a heap TID.
// The size check alone does not catch it — a posting tuple's t_info size is
// correct — so parseItemBody tests BT_IS_POSTING explicitly. (A PIVOT tuple by
// contrast is decoded, not rejected: see TestParseItemDecodesPivot.)
func TestParseItemRejectsPosting(t *testing.T) {
	post := blobFormat.marshalPosting(EncodeInt4(1), []storage.ItemPointer{{Block: 1, Offset: 1}, {Block: 1, Offset: 2}})
	if _, err := parseItem(post); err == nil {
		t.Fatal("parseItem accepted a posting tuple")
	}
}

// TestParseItemDecodesPivot pins the slice-3a decode contract: a pivot tuple
// round-trips through parseItem/marshal with its downlink, its key and its
// pivot status intact. The re-marshal half is the one that matters — the vacuum
// and LP_DEAD page rewrites parse every item and write it back, and a pivot
// demoted to a plain tuple on that path would publish nbtree status bits to a
// real PG as a heap TID.
func TestParseItemDecodesPivot(t *testing.T) {
	raw := downlinkItem(EncodeInt4(1), 12).marshal()
	if !BTreeTupleIsPivot(raw) {
		t.Fatal("downlinkItem did not produce a pivot tuple")
	}
	if got := BTreeTupleGetDownLink(raw); got != 12 {
		t.Errorf("downlink = %d, want 12", got)
	}
	if got := BTreeTupleGetNAtts(raw, 1); got != 1 {
		t.Errorf("natts = %d, want 1", got)
	}

	it, err := parseItem(raw)
	if err != nil {
		t.Fatalf("parseItem: %v", err)
	}
	if !it.pivot {
		t.Error("parseItem lost the pivot flag")
	}
	// t_tid's offset half is status data, never a line pointer.
	if it.ptr != (storage.ItemPointer{Block: 12}) {
		t.Errorf("ptr = %+v, want {Block:12}", it.ptr)
	}
	if !bytes.Equal(it.key, EncodeInt4(1)) {
		t.Errorf("key = %x, want %x", it.key, EncodeInt4(1))
	}
	if got := it.marshal(); !bytes.Equal(got, raw) {
		t.Errorf("pivot did not survive a parse/marshal round trip:\n got %x\nwant %x", got, raw)
	}
}

// TestNegativeInfinityPivotHasZeroNAtts: the first downlink of every internal
// page has no key at all, which upstream represents as a pivot with natts = 0
// (_bt_truncate's fully-truncated form). amcheck checks that count, so a
// keyless downlink stamped natts = 1 would read as a corrupt index.
func TestNegativeInfinityPivotHasZeroNAtts(t *testing.T) {
	raw := downlinkItem(nil, 7).marshal()
	if !BTreeTupleIsPivot(raw) {
		t.Fatal("minus-infinity downlink is not a pivot tuple")
	}
	if got := BTreeTupleGetNAtts(raw, 1); got != 0 {
		t.Errorf("natts = %d, want 0", got)
	}
	if got := BTreeTupleGetDownLink(raw); got != 7 {
		t.Errorf("downlink = %d, want 7", got)
	}
	if got := PGIndexTupleSize(raw); got != SizeOfIndexTupleData {
		t.Errorf("size = %d, want %d", got, SizeOfIndexTupleData)
	}
}

// TestHighKeyIsPivotTuple: P_HIKEY is a pivot too (M0130-S11.4 slice 3a). Its
// t_tid block half is P_NONE — goopg does not use the top-parent link — and the
// separator bytes are the tuple's data area.
func TestHighKeyIsPivotTuple(t *testing.T) {
	sep := EncodeInt4(99)
	raw := highKeyItem(sep).marshal()
	if !BTreeTupleIsPivot(raw) {
		t.Fatal("high key is not a pivot tuple")
	}
	if got := BTreeTupleGetDownLink(raw); got != 0 {
		t.Errorf("high-key t_tid block = %d, want P_NONE (0)", got)
	}
	if got := BTreeTupleGetNAtts(raw, 1); got != 1 {
		t.Errorf("natts = %d, want 1", got)
	}
	if !bytes.Equal(raw[SizeOfIndexTupleData:], sep) {
		t.Errorf("separator bytes = %x, want %x", raw[SizeOfIndexTupleData:], sep)
	}
}

// TestParsePivotStripsTiebreakerHeapTID: goopg does not yet WRITE the
// tiebreaker representation (PGBTPivotRaw's ledger row), but a pivot that
// carries one keeps a trailing ItemPointerData that is not part of the key —
// decoding it as key bytes would corrupt every comparison against that
// separator.
func TestParsePivotStripsTiebreakerHeapTID(t *testing.T) {
	key := EncodeInt4(5)
	tid := storage.ItemPointer{Block: 3, Offset: 4}
	raw := make([]byte, SizeOfIndexTupleData+len(key)+SizeOfItemPointerData)
	copy(raw[SizeOfIndexTupleData:], key)
	PutPGItemPointer(raw[len(raw)-SizeOfItemPointerData:], tid)
	binary.LittleEndian.PutUint16(raw[6:8], uint16(len(raw)))
	if err := BTreeTupleSetNAtts(raw, 1, true); err != nil {
		t.Fatalf("BTreeTupleSetNAtts: %v", err)
	}

	it, err := parseItem(raw)
	if err != nil {
		t.Fatalf("parseItem: %v", err)
	}
	if !bytes.Equal(it.key, key) {
		t.Errorf("key = %x, want %x (trailing heap TID must not be part of the key)", it.key, key)
	}
	if got, ok := BTreeTupleGetHeapTID(raw); !ok || got != tid {
		t.Errorf("BTreeTupleGetHeapTID = %+v (ok=%v), want %+v", got, ok, tid)
	}
}

// TestParseItemRejectsSizeMismatch: t_info's size is now the ONLY record of
// where the key ends, so a truncated or over-long line pointer must be a clean
// error rather than a short read.
func TestParseItemRejectsSizeMismatch(t *testing.T) {
	raw := item{ptr: storage.ItemPointer{Block: 1, Offset: 1}, key: EncodeInt4(7)}.marshal()
	if _, err := parseItem(raw[:len(raw)-1]); err == nil {
		t.Error("parseItem accepted a truncated item")
	}
	if _, err := parseItem(append(raw, 0)); err == nil {
		t.Error("parseItem accepted an over-long item")
	}
	if _, err := parseItem(raw[:4]); err == nil {
		t.Error("parseItem accepted a sub-header item")
	}
}

// TestPostingTupleIsUpstreamShape checks the posting layout against nbtree.h's
// own accessors rather than against the writer that produced it: t_tid carries
// (postingOffset, nhtids|BT_IS_POSTING), the array sits at postingOffset, and
// BTreeTupleGetHeapTID returns the lowest heap TID (what _bt_compare uses).
func TestPostingTupleIsUpstreamShape(t *testing.T) {
	key := EncodeInt4(42)
	tids := []storage.ItemPointer{{Block: 0, Offset: 1}, {Block: 2, Offset: 3}, {Block: 900, Offset: 5}}
	raw := blobFormat.marshalPosting(key, tids)

	if !BTreeTupleIsPosting(raw) {
		t.Fatal("BTreeTupleIsPosting = false")
	}
	if BTreeTupleIsPivot(raw) {
		t.Error("a posting tuple must not read as a pivot tuple")
	}
	if n := BTreeTupleGetNPosting(raw); int(n) != len(tids) {
		t.Errorf("nhtids = %d, want %d", n, len(tids))
	}
	wantOff := SizeOfIndexTupleData + len(key)
	if off := BTreeTupleGetPostingOffset(raw); off != wantOff {
		t.Errorf("posting offset = %d, want %d", off, wantOff)
	}
	if n := PGIndexTupleSize(raw); n != len(raw) {
		t.Errorf("t_info size = %d, want %d", n, len(raw))
	}
	first, ok := BTreeTupleGetHeapTID(raw)
	if !ok || first != tids[0] {
		t.Errorf("BTreeTupleGetHeapTID = %+v/%v, want %+v", first, ok, tids[0])
	}

	gotKey, gotTIDs, err := blobFormat.parsePostingRaw(raw)
	if err != nil {
		t.Fatalf("parsePostingRaw: %v", err)
	}
	if !bytes.Equal(gotKey, key) {
		t.Errorf("key = %x, want %x", gotKey, key)
	}
	for i := range tids {
		if gotTIDs[i] != tids[i] {
			t.Errorf("tid[%d] = %+v, want %+v", i, gotTIDs[i], tids[i])
		}
	}
	if !bytes.Equal(postingKeyOf(raw), key) {
		t.Errorf("postingKeyOf = %x, want %x", postingKeyOf(raw), key)
	}
}

// TestPostingBoundsRejectsCorruption: the TID array's extent is derived from
// two independent header fields, so a mismatch between them must not read off
// the end of the tuple.
func TestPostingBoundsRejectsCorruption(t *testing.T) {
	raw := blobFormat.marshalPosting(EncodeInt4(1), []storage.ItemPointer{{Block: 1, Offset: 1}, {Block: 1, Offset: 2}})

	bad := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint16(bad[4:6], 9|BTIsPosting) // nhtids too large
	if _, _, err := blobFormat.parsePostingRaw(bad); err == nil {
		t.Error("parsePostingRaw accepted an over-large TID count")
	}
	if postingKeyOf(bad) != nil {
		t.Error("postingKeyOf returned a key for a corrupt posting tuple")
	}

	bad = append([]byte(nil), raw...)
	BTreeTupleSetDownLink(bad, 2) // posting offset inside the header
	if _, _, err := blobFormat.parsePostingRaw(bad); err == nil {
		t.Error("parsePostingRaw accepted a posting offset inside the header")
	}
}
