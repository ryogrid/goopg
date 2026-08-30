package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// TestDecodedRowDoesNotAliasSourceBuffer is the guard that makes
// storage.PageGetHeapTupleInto's scratch-buffer reuse safe.
//
// seqScanOp copies each tuple into ONE buffer that the next tuple overwrites.
// That is only sound while no Datum produced by the heap decoder points into
// the tuple bytes: such a Datum would read the *next* row's bytes, silently,
// with no error anywhere. Every varlena arm copies today (into the per-page
// arena, via string(), or via an explicit append) — this test turns that from
// an audit into something a future arm that starts aliasing trips over.
//
// Method: decode a row, overwrite the source buffer with 0xFF, and assert the
// Datums still read back exactly what they did before.
func TestDecodedRowDoesNotAliasSourceBuffer(t *testing.T) {
	cols := []catalog.Column{
		{Name: "c_int2", Type: catalog.Type{Name: "int2"}, Ordinal: 0},
		{Name: "c_int4", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
		{Name: "c_int8", Type: catalog.Type{Name: "int8"}, Ordinal: 2},
		{Name: "c_bool", Type: catalog.Type{Name: "bool"}, Ordinal: 3},
		{Name: "c_float8", Type: catalog.Type{Name: "float8"}, Ordinal: 4},
		{Name: "c_text", Type: catalog.Type{Name: "text"}, Ordinal: 5},
		{Name: "c_varchar", Type: catalog.Type{Name: "varchar"}, Ordinal: 6},
		{Name: "c_bpchar", Type: catalog.Type{Name: "bpchar"}, Ordinal: 7},
		{Name: "c_bytea", Type: catalog.Type{Name: "bytea"}, Ordinal: 8},
		{Name: "c_numeric", Type: catalog.Type{Name: "numeric"}, Ordinal: 9},
		{Name: "c_uuid", Type: catalog.Type{Name: "uuid"}, Ordinal: 10},
		{Name: "c_date", Type: catalog.Type{Name: "date"}, Ordinal: 11},
		{Name: "c_ts", Type: catalog.Type{Name: "timestamp"}, Ordinal: 12},
	}
	src := Row{
		NewIntDatum(-12345),
		NewIntDatum(987654),
		NewIntDatum(-9000000000),
		NewBoolDatum(true),
		floatTextDatum(PGFloatOut(2.5, 64)),
		NewStringDatum("the quick brown fox jumps over the lazy dog"),
		NewStringDatum("varchar payload"),
		NewStringDatum("bpchar padded"),
		NewBytesDatum([]byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x11}),
		Datum{Kind: KindNumeric, Int: 1234567, Scale: 3},
		NewStringDatum("6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
		NewDateDatum(time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)),
		NewTimeDatum(time.Date(2026, 8, 30, 11, 22, 33, 0, time.UTC)),
	}

	data, err := EncodeRowPG(cols, src)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	// Decode out of a buffer we are free to scribble on, exactly as the scan's
	// scratch buffer is reused.
	buf := append([]byte(nil), data...)

	got := make(Row, len(cols))
	if _, err := DecodeRowRangeIntoMctxPGTupleStyled(got, cols, buf, nil, len(cols), nil,
		array.DefaultOutputStyle(), 0, len(cols), 0); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Snapshot the observable form of every Datum BEFORE the scribble.
	before := make([]string, len(got))
	for i := range got {
		before[i] = datumObservable(got[i])
	}

	for i := range buf {
		buf[i] = 0xFF
	}

	for i := range got {
		if after := datumObservable(got[i]); after != before[i] {
			t.Errorf("col %d (%s) ALIASES the source buffer: %q before the scribble, %q after."+
				"\n  A Datum that points into the tuple bytes breaks"+
				"\n  storage.PageGetHeapTupleInto's scratch-buffer reuse in seqScanOp:"+
				"\n  it would silently read the NEXT row's bytes."+
				"\n  Fix the decode arm to copy (arena AllocBytes, string(), or append).",
				i, cols[i].Name, before[i], after)
		}
	}
}

// TestDecodedRowAliasDetectorActuallyDetects is the positive control for the
// test above. A guard that silently stops guarding is worse than no guard, and
// TestDecodedRowDoesNotAliasSourceBuffer would pass just as happily if
// datumObservable returned a constant. This builds a Datum that DOES alias a
// buffer and asserts the detector notices.
func TestDecodedRowAliasDetectorActuallyDetects(t *testing.T) {
	buf := []byte("aliased-payload")
	aliasing := Datum{Kind: KindBytes, Buf: buf} // deliberately points AT buf
	before := datumObservable(aliasing)
	for i := range buf {
		buf[i] = 0xFF
	}
	if after := datumObservable(aliasing); after == before {
		t.Fatalf("alias detector is broken: an aliasing Datum read %q both before and "+
			"after the buffer was overwritten, so TestDecodedRowDoesNotAliasSourceBuffer "+
			"cannot catch a real aliasing arm", before)
	}
}

// datumObservable renders everything about a Datum that a consumer can read,
// so an aliased payload shows up as a changed string.
func datumObservable(d Datum) string {
	if d.IsNull() {
		return "NULL"
	}
	switch d.Kind {
	case KindString:
		return "s:" + d.StringValue()
	case KindBytes:
		return "b:" + string(d.BytesValue())
	default:
		return "o:" + d.Format()
	}
}

// TestPageGetHeapTupleOwnsItsMemory pins the pre-existing contract that
// PageGetHeapTuple's result is independent of the page it was read from. The
// take-6 change makes that function do ONE copy instead of three, so the
// contract has to be asserted rather than assumed.
func TestPageGetHeapTupleOwnsItsMemory(t *testing.T) {
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}
	data, err := EncodeRowPG(cols, Row{NewIntDatum(7), NewStringDatum("payload-on-page")})
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	tup := storage.HeapTuple{Data: data}
	tup.Header.SetNatts(len(cols))
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	slot, err := storage.PageAddItemRaw(page, raw)
	if err != nil {
		t.Fatalf("PageAddItem: %v", err)
	}

	read, err := storage.PageGetHeapTuple(page, slot)
	if err != nil {
		t.Fatalf("PageGetHeapTuple: %v", err)
	}
	snapshot := append([]byte(nil), read.Data...)

	// Scribble the whole page. An owning tuple is unaffected.
	for i := range page {
		page[i] = 0xFF
	}
	if string(read.Data) != string(snapshot) {
		t.Errorf("PageGetHeapTuple result aliases the page: Data changed after the page was overwritten")
	}
}

// TestPageGetHeapTupleIntoMatchesAndGrows checks that the scratch-buffer entry
// point returns the same bytes as the allocating one across ascending tuple
// sizes fed through a SINGLE buffer — i.e. that the grow path is right and a
// larger tuple after a smaller one is not truncated.
func TestPageGetHeapTupleIntoMatchesAndGrows(t *testing.T) {
	var buf []byte
	for n := 1; n <= 64; n += 7 {
		page := make(storage.Page, storage.BlockSize)
		if err := storage.InitPage(page); err != nil {
			t.Fatalf("InitPage: %v", err)
		}
		cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "text"}, Ordinal: 0}}
		payload := make([]byte, n)
		for i := range payload {
			payload[i] = byte('a' + i%26)
		}
		data, err := EncodeRowPG(cols, Row{NewStringDatum(string(payload))})
		if err != nil {
			t.Fatalf("n=%d EncodeRowPG: %v", n, err)
		}
		tup := storage.HeapTuple{Data: data}
		tup.Header.SetNatts(1)
		raw, err := tup.MarshalBinary()
		if err != nil {
			t.Fatalf("n=%d MarshalBinary: %v", n, err)
		}
		slot, err := storage.PageAddItemRaw(page, raw)
		if err != nil {
			t.Fatalf("n=%d PageAddItem: %v", n, err)
		}
		want, err := storage.PageGetHeapTuple(page, slot)
		if err != nil {
			t.Fatalf("n=%d PageGetHeapTuple: %v", n, err)
		}
		var got storage.HeapTuple
		got, buf, err = storage.PageGetHeapTupleInto(page, slot, buf)
		if err != nil {
			t.Fatalf("n=%d PageGetHeapTupleInto: %v", n, err)
		}
		if string(got.Data) != string(want.Data) {
			t.Errorf("n=%d: Into Data = %q, want %q", n, got.Data, want.Data)
		}
		if string(got.Bitmap) != string(want.Bitmap) {
			t.Errorf("n=%d: Into Bitmap = %x, want %x", n, got.Bitmap, want.Bitmap)
		}
		if got.Header != want.Header {
			t.Errorf("n=%d: Into Header = %+v, want %+v", n, got.Header, want.Header)
		}
	}
}
