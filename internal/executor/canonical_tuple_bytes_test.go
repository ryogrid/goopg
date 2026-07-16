package executor

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestCanonicalUserRowTupleBytesM0106_0010_36 pins the exact byte layout
// of a PG physical-format heap tuple produced for the bench_log E2E row
// `INSERT INTO public.bench_log (client int, src text) VALUES (-999, 'bootstrap')`
// the way it is written by writeHeapRowReturningPG — i.e. EncodeRowPG +
// NullBitmapPG, then NewHeapTuple, SetNatts(2), Infomask |=
// HEAP_XMAX_INVALID, then MarshalBinary.
//
// The fixture below is what a vanilla PG18 standby must be able to
// deform without segfaulting. M0106-0010 batched-36 documents that the
// PG standby segfaults on `SELECT count(*) FROM public.bench_log WHERE
// client = -999`; this test localises the bytes the segfault sees so
// the next batch can iterate without re-running the full E2E.
//
// Layout (PG18 HeapTupleHeaderData, see postgres/src/include/access/htup_details.h):
//
//	off  size  field
//	  0    4   t_xmin
//	  4    4   t_xmax
//	  8    4   t_field3 (t_cid / t_xvac union)
//	 12    4   t_ctid.ip_blkid  (BlockIdData {bi_hi uint16 LE; bi_lo uint16 LE})
//	 16    2   t_ctid.ip_posid  (uint16 LE)
//	 18    2   t_infomask2
//	 20    2   t_infomask
//	 22    1   t_hoff
//	 23    1   first byte of t_bits[] (or pad when no nulls)
//
// Hoff = MAXALIGN(SizeOfHeapTupleHeaderData + bitmap_size).
// For bench_log (2 NOT-NULL columns) there is no bitmap, so hoff = 24.
func TestCanonicalUserRowTupleBytesM0106_0010_36(t *testing.T) {
	cols := []catalog.Column{
		{Name: "client", Type: catalog.Type{Name: "int4"}},
		{Name: "src", Type: catalog.Type{Name: "text"}},
	}
	row := Row{NewIntDatum(-999), NewStringDatum("bootstrap")}

	body, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	wantBody := []byte{
		// int4 LE -999 (=0xFFFFFC19)
		0x19, 0xFC, 0xFF, 0xFF,
		// text varlena: 1-byte short header for total length 10 (header+payload):
		// va_header = (10 << 1) | 1 = 0x15
		0x15,
		'b', 'o', 'o', 't', 's', 't', 'r', 'a', 'p',
	}
	if !bytes.Equal(body, wantBody) {
		t.Fatalf("EncodeRowPG body mismatch:\n got %x\nwant %x", body, wantBody)
	}

	bitmap := NullBitmapPG(row)
	if bitmap != nil {
		t.Fatalf("NullBitmapPG(non-null row) = %x; want nil", bitmap)
	}

	const xmin storage.TransactionID = 4
	tuple := storage.NewHeapTuple(xmin, storage.InvalidTransactionID, body)
	tuple.Header.SetNatts(len(cols))
	tuple.Header.Infomask |= storage.HeapXmaxInvalid
	gotBytes, err := tuple.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	const hoff = 24
	wantBytes := make([]byte, hoff+len(wantBody))
	// t_xmin = 4
	binary.LittleEndian.PutUint32(wantBytes[0:4], uint32(xmin))
	// t_xmax = 0, t_field3 = 0 (already zero in make)
	// t_ctid: PG layout is BlockIdData{bi_hi, bi_lo} + ip_posid. For
	// InvalidBlockNumber (0xFFFFFFFF), bi_hi=0xFFFF, bi_lo=0xFFFF. Both
	// uint16 LE — produces the same 4 bytes 0xFFFFFFFF as a raw uint32
	// LE encoding of 0xFFFFFFFF, so MarshalBinary's current behaviour
	// agrees on this corner case.
	binary.LittleEndian.PutUint16(wantBytes[12:14], 0xFFFF) // bi_hi
	binary.LittleEndian.PutUint16(wantBytes[14:16], 0xFFFF) // bi_lo
	binary.LittleEndian.PutUint16(wantBytes[16:18], 0)      // ip_posid
	// t_infomask2 = natts (lower 11 bits)
	binary.LittleEndian.PutUint16(wantBytes[18:20], 2)
	// t_infomask = HEAP_XMAX_INVALID (0x0800)
	binary.LittleEndian.PutUint16(wantBytes[20:22], uint16(storage.HeapXmaxInvalid))
	// t_hoff
	wantBytes[22] = hoff
	// wantBytes[23] is pad/zero. body follows at offset 24.
	copy(wantBytes[hoff:], wantBody)

	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("MarshalBinary tuple bytes mismatch:\n got % x\nwant % x", gotBytes, wantBytes)
	}

	if len(gotBytes) != 38 {
		t.Fatalf("tuple length = %d; want 38 (24-byte header + 14-byte body)", len(gotBytes))
	}
}

// TestCanonicalUserRowOnEmptyPageM0106_0010_36 places the canonical
// bench_log tuple on a freshly initialised page and pins the page-level
// invariants PG enforces:
//   - pd_lower advances by exactly itemIDSize (4) past the page header.
//   - The line pointer offset and length round-trip back to PageGetHeapTupleNoCopy.
//   - PG places tuples at MAXALIGN'd offsets from pd_special downwards
//     (PageAddItemExtended computes alignedSize = MAXALIGN(size); upper
//     = pd_upper - alignedSize). goopg's PageAddHeapTuple currently
//     omits the MAXALIGN step, producing tuples at non-8-byte-aligned
//     offsets, which is the residual hypothesis for the PG client
//     backend segfault on `SELECT count(*) FROM bench_log`.
//
// The MAXALIGN expectation is asserted via an explicit subtest so the
// failing test pinpoints the missing alignment exactly.
func TestCanonicalUserRowOnEmptyPageM0106_0010_36(t *testing.T) {
	cols := []catalog.Column{
		{Name: "client", Type: catalog.Type{Name: "int4"}},
		{Name: "src", Type: catalog.Type{Name: "text"}},
	}
	row := Row{NewIntDatum(-999), NewStringDatum("bootstrap")}
	body, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	tuple := storage.NewHeapTuple(4, storage.InvalidTransactionID, body)
	tuple.Header.SetNatts(len(cols))
	tuple.Header.Infomask |= storage.HeapXmaxInvalid
	tupleBytes, err := tuple.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if len(tupleBytes) != 38 {
		t.Fatalf("len(tupleBytes)=%d, want 38", len(tupleBytes))
	}

	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	lineSlot, err := storage.PageAddHeapTuple(page, tuple)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	if lineSlot != 1 {
		t.Fatalf("PageAddHeapTuple lineSlot=%d, want 1", lineSlot)
	}

	h, err := storage.Header(page)
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if got, want := h.Lower(), uint16(storage.SizeOfPageHeaderData+4); got != want {
		t.Fatalf("pd_lower=%d, want %d (page header + 1 line pointer)", got, want)
	}

	t.Run("upper_is_maxaligned", func(t *testing.T) {
		upper := h.Upper()
		if upper%8 != 0 {
			t.Errorf("pd_upper=%d is not 8-byte (MAXALIGN) aligned; "+
				"PG places tuples at MAXALIGN'd offsets and a PG18 "+
				"standby's heap_deform_tuple dereferences alignment-"+
				"sensitive offsets that miscompute on a misaligned "+
				"tuple base. M0106-0010 batched-36.", upper)
		}
		if int(upper) != storage.BlockSize-maxAlign8Helper(38) {
			t.Errorf("pd_upper=%d, want %d (= BlockSize - MAXALIGN(38))",
				upper, storage.BlockSize-maxAlign8Helper(38))
		}
	})
}

// maxAlign8Helper mirrors the MAXALIGN macro for ALIGNOF_LONG=8 PG
// builds (the default on every 64-bit platform). Kept local to avoid
// exporting a redundant helper from internal/storage.
func maxAlign8Helper(n int) int {
	return (n + 7) &^ 7
}

// dumpHex returns a multi-line hex dump suitable for failure messages.
// Used by humans inspecting the test output, not by assertions.
func dumpHex(b []byte) string {
	var s string
	for i := 0; i < len(b); i += 16 {
		end := i + 16
		if end > len(b) {
			end = len(b)
		}
		s += fmt.Sprintf("%04x: % x\n", i, b[i:end])
	}
	return s
}

// silence unused warning if dumpHex is not referenced after edits.
var _ = dumpHex
