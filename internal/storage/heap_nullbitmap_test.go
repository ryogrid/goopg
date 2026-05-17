package storage

import (
	"bytes"
	"testing"
)

// TestNewHeapTupleWithNullsLayoutMatchesPG18 pins the on-disk layout
// produced by NewHeapTupleWithNulls + MarshalBinary against PG18's
// heap-tuple layout (header, bitmap, alignment padding, data).
// Mirrors PG's heap_fill_tuple semantics: bit=1 means NOT NULL,
// t_hoff = MAXALIGN(SizeofHeapTupleHeader + bitmap_size), and
// HEAP_HASNULL is stamped in t_infomask.
//
// Why pinned: a wrong null bitmap shape silently corrupts every
// catalog tuple PG decodes, surfaces as "cache lookup failed for
// index <oid>" at standby boot (M0106-0010 step 3g symptom), and is
// extremely hard to debug from PG's side because the failure is
// reported as a SearchSysCache1 miss, not a tuple-decoding error.
func TestNewHeapTupleWithNullsLayoutMatchesPG18(t *testing.T) {
	// 21-column row with cols 20 and 21 NULL (mirrors pg_index's
	// indexprs/indpred encoding produced by initdb).
	const natts = 21
	bitmap := []byte{0xFF, 0xFF, 0x07} // cols 1-19 NOT NULL, cols 20-21 NULL
	data := []byte{0xAA, 0xBB, 0xCC, 0xDD}

	tup := NewHeapTupleWithNulls(TransactionID(7), InvalidTransactionID, bitmap, data)
	tup.Header.SetNatts(natts)
	tup.Header.Infomask |= HeapHasVarWidth

	if tup.Header.Hoff != 32 {
		t.Fatalf("Hoff: got %d, want 32 (MAXALIGN(23+3))", tup.Header.Hoff)
	}
	if tup.Header.Infomask&HeapHasNull == 0 {
		t.Fatalf("Infomask: HEAP_HASNULL not set (infomask=0x%04x)", tup.Header.Infomask)
	}

	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if len(raw) != 32+len(data) {
		t.Fatalf("len(raw)=%d, want %d (hoff+len(data))", len(raw), 32+len(data))
	}
	if raw[22] != 32 {
		t.Fatalf("raw[22] (t_hoff): got %d, want 32", raw[22])
	}
	if got := raw[20] | (raw[21] << 1); got&byte(HeapHasNull) == 0 {
		t.Fatalf("infomask byte: HEAP_HASNULL bit not set; raw[20:22]=%v", raw[20:22])
	}
	// Bitmap occupies bytes 23..26; bytes 26..32 are alignment pad.
	if !bytes.Equal(raw[23:26], bitmap) {
		t.Fatalf("bitmap at bytes 23..26: got %x, want %x", raw[23:26], bitmap)
	}
	for i := 26; i < 32; i++ {
		if raw[i] != 0 {
			t.Fatalf("alignment pad byte %d: got 0x%02x, want 0x00", i, raw[i])
		}
	}
	if !bytes.Equal(raw[32:32+len(data)], data) {
		t.Fatalf("column data at bytes 32+: got %x, want %x", raw[32:32+len(data)], data)
	}

	// Roundtrip: ParseHeapTuple recovers bitmap + data identically.
	got, err := ParseHeapTuple(raw)
	if err != nil {
		t.Fatalf("ParseHeapTuple: %v", err)
	}
	if !bytes.Equal(got.Bitmap, bitmap) {
		t.Fatalf("roundtrip bitmap: got %x, want %x", got.Bitmap, bitmap)
	}
	if !bytes.Equal(got.Data, data) {
		t.Fatalf("roundtrip data: got %x, want %x", got.Data, data)
	}
	if got.Header.Hoff != 32 {
		t.Fatalf("roundtrip Hoff: got %d, want 32", got.Header.Hoff)
	}
}

// TestHeapTupleNullBitmapConventionMatchesPG18 pins the PG18 null bitmap
// convention against the inverted "bit=1 means NULL" convention goopg
// previously used. Inverted bits made PG read every catalog tuple as
// "every column is NULL" and "the NULL columns are non-NULL", which
// surfaced as cache-lookup misses at standby boot (M0106-0010 step 3g).
func TestHeapTupleNullBitmapConventionMatchesPG18(t *testing.T) {
	// Two of three columns NULL.
	bitmap := []byte{0x02} // col 1 NULL, col 2 NOT NULL, col 3 NULL
	tup := NewHeapTupleWithNulls(TransactionID(1), InvalidTransactionID, bitmap, []byte{0x42})
	tup.Header.SetNatts(3)
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if raw[23] != 0x02 {
		t.Fatalf("bitmap byte 0: got 0x%02x, want 0x02 (only col 2 NOT NULL)", raw[23])
	}
}
