package btree

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

// TestParseItemRejectsAltTID: a pivot or posting tuple decoded as a plain item
// would hand the caller nbtree status bits as a heap TID. The size check alone
// does not catch it (a posting tuple's t_info size is correct), so parseItem
// tests INDEX_ALT_TID_MASK explicitly.
func TestParseItemRejectsAltTID(t *testing.T) {
	post := marshalPosting(EncodeInt4(1), []storage.ItemPointer{{Block: 1, Offset: 1}, {Block: 1, Offset: 2}})
	if _, err := parseItem(post); err == nil {
		t.Fatal("parseItem accepted a posting tuple")
	}

	pivot := item{ptr: storage.ItemPointer{Block: 12}, key: EncodeInt4(1)}.marshal()
	if err := BTreeTupleSetNAtts(pivot, 1, false); err != nil {
		t.Fatalf("BTreeTupleSetNAtts: %v", err)
	}
	if _, err := parseItem(pivot); err == nil {
		t.Fatal("parseItem accepted a pivot tuple")
	}
	// The downlink survives the alt-TID stamp — that is the whole point of
	// keeping the child pointer in t_tid's block half.
	if got := BTreeTupleGetDownLink(pivot); got != 12 {
		t.Errorf("downlink after SetNAtts = %d, want 12", got)
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
	raw := marshalPosting(key, tids)

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

	gotKey, gotTIDs, err := parsePostingRaw(raw)
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
	raw := marshalPosting(EncodeInt4(1), []storage.ItemPointer{{Block: 1, Offset: 1}, {Block: 1, Offset: 2}})

	bad := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint16(bad[4:6], 9|BTIsPosting) // nhtids too large
	if _, _, err := parsePostingRaw(bad); err == nil {
		t.Error("parsePostingRaw accepted an over-large TID count")
	}
	if postingKeyOf(bad) != nil {
		t.Error("postingKeyOf returned a key for a corrupt posting tuple")
	}

	bad = append([]byte(nil), raw...)
	BTreeTupleSetDownLink(bad, 2) // posting offset inside the header
	if _, _, err := parsePostingRaw(bad); err == nil {
		t.Error("parsePostingRaw accepted a posting offset inside the header")
	}
}
