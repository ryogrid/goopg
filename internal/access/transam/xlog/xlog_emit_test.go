package xlog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func mustReadSegment(t *testing.T, walDir string, segNo uint64, tli uint32) []byte {
	t.Helper()
	path := filepath.Join(walDir, FormatSegmentNameTLI(segNo, tli))
	data, err := os.ReadFile(path)
	if err != nil {
		// Fall back to TLI=1 for tests that write with default naming.
		path1 := filepath.Join(walDir, FormatSegmentName(segNo))
		if path1 != path {
			if data1, err1 := os.ReadFile(path1); err1 == nil {
				return data1
			}
		}
		t.Fatalf("read segment %d (tli=%d): %v", segNo, tli, err)
	}
	return data
}

// pickPageSegSize is a non-default segment size used by the page-aware
// tests so that one test can exercise multiple pages-per-segment and
// segment-spanning records without allocating 16 MiB on disk. We choose
// 16 KiB = 2 pages per segment; that is exactly two XLOGBlockSize-sized
// pages, so a page boundary lands at offset 8 KiB inside every segment
// and a segment boundary lands at offset 16 KiB.
const pickPageSegSize = int64(2 * XLOGBlockSize)

// makePagePayload returns a deterministic byte slice of length n.
func makePagePayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*31 + 7) & 0xFF)
	}
	return b
}

// TestPageEmissionLongHeaderAtSegmentStart pins the rule "every
// segment starts with a 40-byte long page header". We append one
// short record into a fresh writer with PageHeaders=true and
// inspect segment 0 directly.
func TestPageEmissionLongHeaderAtSegmentStart(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
		SystemID:    0xDEADBEEFCAFEBABE,
		TimelineID:  7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := mustReadSegment(t, walDir, 0, w.TimelineID())
	if len(data) < SizeOfXLogLongPHD {
		t.Fatalf("segment too short: %d", len(data))
	}
	long, err := DecodeXLogLongPageHeader(data[:SizeOfXLogLongPHD])
	if err != nil {
		t.Fatalf("decode long header: %v", err)
	}
	if long.Std.Magic != XLOGPageMagic {
		t.Fatalf("magic = 0x%04x, want 0x%04x", long.Std.Magic, XLOGPageMagic)
	}
	if long.Std.TLI != 7 {
		t.Fatalf("TLI = %d, want 7", long.Std.TLI)
	}
	if long.SysID != 0xDEADBEEFCAFEBABE {
		t.Fatalf("SysID = 0x%016x, want 0xDEADBEEFCAFEBABE", long.SysID)
	}
	if long.SegSize != uint32(pickPageSegSize) {
		t.Fatalf("SegSize = %d, want %d", long.SegSize, pickPageSegSize)
	}
	if long.XLogBlcksz != XLOGBlockSize {
		t.Fatalf("XLogBlcksz = %d, want %d", long.XLogBlcksz, XLOGBlockSize)
	}
	if long.Std.PageAddr != 0 {
		t.Fatalf("PageAddr = %d, want 0", long.Std.PageAddr)
	}
	if long.Std.Info&XLPLongHeader == 0 {
		t.Fatalf("XLPLongHeader bit not set: Info=0x%04x", long.Std.Info)
	}
	if long.Std.Info&XLPFirstIsContRecord != 0 {
		t.Fatalf("unexpected XLPFirstIsContRecord on segment 0 page 0")
	}
	if long.Std.RemLen != 0 {
		t.Fatalf("RemLen = %d, want 0 (not a contrecord page)", long.Std.RemLen)
	}
}

// TestPageEmissionShortHeaderAtPageBoundary pins the rule "all other
// page boundaries get a 24-byte short header". We fill page 0 plus
// some bytes into page 1 (segment 0 has two pages with our test
// segSize), then inspect the second page's header.
func TestPageEmissionShortHeaderAtPageBoundary(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
		TimelineID:  3,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Page 0 fits 8 KiB - 40 bytes of records before crossing
	// into page 1. Append a single record large enough to land
	// well past the boundary.
	payload := makePagePayload(XLOGBlockSize) // 8 KiB; record incl. header is ~8 KiB+8
	if _, _, err := w.Append(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := mustReadSegment(t, walDir, 0, w.TimelineID())
	pageOff := XLOGBlockSize
	if len(data) < pageOff+SizeOfXLogShortPHD {
		t.Fatalf("segment too short: %d", len(data))
	}
	short, err := DecodeXLogPageHeader(data[pageOff : pageOff+SizeOfXLogShortPHD])
	if err != nil {
		t.Fatalf("decode short header: %v", err)
	}
	if short.Magic != XLOGPageMagic {
		t.Fatalf("magic = 0x%04x", short.Magic)
	}
	if short.TLI != 3 {
		t.Fatalf("TLI = %d", short.TLI)
	}
	if short.PageAddr != uint64(pageOff) {
		t.Fatalf("PageAddr = %d, want %d", short.PageAddr, pageOff)
	}
	// The record continues onto page 1, so XLPFirstIsContRecord
	// must be set, and RemLen must equal the bytes still to go.
	if short.Info&XLPFirstIsContRecord == 0 {
		t.Fatalf("XLPFirstIsContRecord bit not set: Info=0x%04x", short.Info)
	}
	if short.Info&XLPLongHeader != 0 {
		t.Fatalf("unexpected XLPLongHeader on mid-segment page")
	}
	// Bytes consumed on page 0 = XLOGBlockSize - SizeOfXLogLongPHD;
	// the record itself is XLogRecord header + main-data chunk
	// header + payload bytes.
	bytesOnPage0 := XLOGBlockSize - SizeOfXLogLongPHD
	want := uint32(xlogRecordWireSize(len(payload)) - bytesOnPage0)
	if short.RemLen != want {
		t.Fatalf("RemLen = %d, want %d", short.RemLen, want)
	}
}

// TestPageEmissionRecordCrossesPage round-trips a record that spans
// a 24-byte short page header inside a single segment.
func TestPageEmissionRecordCrossesPage(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Pad with a small record on page 0 so the next record's
	// payload bytes straddle the page-0/page-1 boundary in a
	// non-trivial way (header on page 0, body partly on page 0,
	// rest on page 1).
	pad := []byte("pad")
	if _, _, err := w.Append(pad); err != nil {
		t.Fatal(err)
	}
	payload := makePagePayload(2000)
	startLSN, endLSN, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, pickPageSegSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("record count = %d, want 2 (records=%+v)", len(recs), recs)
	}
	if !bytes.Equal(recs[0].Payload, pad) {
		t.Fatalf("rec[0] payload = %q, want %q", recs[0].Payload, pad)
	}
	if !bytes.Equal(recs[1].Payload, payload) {
		t.Fatalf("rec[1] payload mismatch (len=%d)", len(recs[1].Payload))
	}
	// Append's reported LSN values must match what ReadAll
	// reconstructs — the LSN→byte position mapping is preserved
	// across the writer/reader switchover.
	if recs[1].StartLSN != startLSN || recs[1].EndLSN != endLSN {
		t.Fatalf("rec[1] LSN range = [%d,%d], Append returned [%d,%d]",
			recs[1].StartLSN, recs[1].EndLSN, startLSN, endLSN)
	}
}

// TestPageEmissionRecordCrossesSegment validates the long-header
// branch when a record spans a segment boundary: the next page's
// header is the 40-byte long form, and replay reconstructs the
// payload byte-for-byte.
func TestPageEmissionRecordCrossesSegment(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
		SystemID:    0x1122334455667788,
		TimelineID:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Fill segment 0 (2 pages = 16 KiB total, minus headers ~16 KiB
	// of record bytes) with a record sized to land its tail in
	// segment 1.
	payload := makePagePayload(int(pickPageSegSize)) // 16 KiB
	if _, _, err := w.Append(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Segment 1's first page must be a long-form header
	// (PageAddr = pickPageSegSize, XLPLongHeader+XLPFirstIsContRecord
	// both set).
	data := mustReadSegment(t, walDir, 1, w.TimelineID())
	if len(data) < SizeOfXLogLongPHD {
		t.Fatalf("segment 1 too short: %d", len(data))
	}
	long, err := DecodeXLogLongPageHeader(data[:SizeOfXLogLongPHD])
	if err != nil {
		t.Fatalf("decode long header: %v", err)
	}
	if long.Std.PageAddr != uint64(pickPageSegSize) {
		t.Fatalf("PageAddr = %d, want %d", long.Std.PageAddr, pickPageSegSize)
	}
	if long.Std.Info&XLPLongHeader == 0 {
		t.Fatalf("XLPLongHeader bit not set on segment 1 page 0: Info=0x%04x", long.Std.Info)
	}
	if long.Std.Info&XLPFirstIsContRecord == 0 {
		t.Fatalf("XLPFirstIsContRecord bit not set on segment 1 page 0: Info=0x%04x", long.Std.Info)
	}
	if long.SysID != 0x1122334455667788 {
		t.Fatalf("SysID round-trip failed: got 0x%016x", long.SysID)
	}

	recs, err := ReadAll(walDir, pickPageSegSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("record count = %d, want 1", len(recs))
	}
	if !bytes.Equal(recs[0].Payload, payload) {
		t.Fatalf("payload mismatch after segment-spanning round trip")
	}
}

// TestPageEmissionIteratorRoundTrip exercises the streaming
// RecordIterator path against a page-emitting writer.
func TestPageEmissionIteratorRoundTrip(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	want := [][]byte{
		[]byte("alpha"),
		makePagePayload(2500), // crosses page boundary
		[]byte("omega"),
	}
	var ends []uint64
	for _, p := range want {
		_, end, err := w.Append(p)
		if err != nil {
			t.Fatal(err)
		}
		ends = append(ends, end)
	}

	it, err := NewRecordIterator(w, walDir, pickPageSegSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i, p := range want {
		rec, err := it.Next(ctx)
		if err != nil {
			t.Fatalf("Next[%d]: %v", i, err)
		}
		if !bytes.Equal(rec.Payload, p) {
			t.Fatalf("rec[%d] payload mismatch", i)
		}
		if rec.EndLSN != ends[i] {
			t.Fatalf("rec[%d] EndLSN = %d, want %d", i, rec.EndLSN, ends[i])
		}
	}
}

// TestPageEmissionRecoversCleanly closes a page-emitting writer and
// reopens it, checking that the new writer's WrittenLSN matches the
// previous end and ReadAll still returns every record.
func TestPageEmissionRecoversCleanly(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	cfg := Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
		Preallocate: true,
	}
	w1, err := NewWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	payloads := [][]byte{
		makePagePayload(100),
		makePagePayload(2500),
		makePagePayload(50),
	}
	var lastEnd uint64
	for _, p := range payloads {
		_, end, err := w1.Append(p)
		if err != nil {
			t.Fatal(err)
		}
		lastEnd = end
	}
	if err := w1.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	if err := w1.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := NewWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := w2.WrittenLSN(); got != lastEnd {
		t.Fatalf("reopened WrittenLSN = %d, want %d", got, lastEnd)
	}
	// Append one more and verify ReadAll sees everything in order.
	tail := []byte("tail")
	if _, _, err := w2.Append(tail); err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}

	recs, err := ReadAll(walDir, pickPageSegSize)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([][]byte{}, payloads...), tail)
	if len(recs) != len(want) {
		t.Fatalf("record count = %d, want %d", len(recs), len(want))
	}
	for i, p := range want {
		if !bytes.Equal(recs[i].Payload, p) {
			t.Fatalf("rec[%d] payload mismatch", i)
		}
	}
}

// TestPageEmissionXLogPrevChain pins xl_prev linkage for the
// PG-compatible record framing: every record stores the previous
// record's start LSN (or 0 for the first record).
func TestPageEmissionXLogPrevChain(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: pickPageSegSize,
		PageHeaders: true,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatal(err)
	}

	payloads := [][]byte{
		[]byte("p0"),
		makePagePayload(3000),
		[]byte("p2"),
	}
	starts := make([]uint64, 0, len(payloads))
	for _, p := range payloads {
		start, _, err := w.Append(p)
		if err != nil {
			t.Fatal(err)
		}
		starts = append(starts, start)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	stream, err := readStream(walDir, pickPageSegSize)
	if err != nil {
		t.Fatal(err)
	}
	for i, start := range starts {
		off := int64(start - 1)
		header, _ := extractRecordBytes(stream[off:], off, pickPageSegSize, xlogRecordHeaderSize)
		if len(header) < xlogRecordHeaderSize {
			t.Fatalf("record %d header truncated at off=%d", i, off)
		}
		h, err := DecodeXLogRecordHeader(header)
		if err != nil {
			t.Fatalf("record %d header decode: %v", i, err)
		}
		wantPrev := uint64(0)
		if i > 0 {
			// xl_prev is the upstream 0-based RecPtr of the
			// previous record's start. goopg's `start` LSN is
			// 1-based, so subtract 1.
			wantPrev = starts[i-1] - 1
		}
		if h.Prev != wantPrev {
			t.Fatalf("record %d xl_prev=%d want %d", i, h.Prev, wantPrev)
		}
	}
}

// (TestPageEmissionLegacyDefaultUnchanged was removed in A9 — it guarded the
// legacy IEEE-CRC on-disk byte shape for PageHeaders=false, which is now retired;
// the writer always emits the PG page-headered stream.)

// TestExtractRecordBytesCapBoundedByWantBytes guards the M-NIGHTLY
// AC-003 restart-timeout regression (deferral ledger 2026-07-07):
// extractRecordBytes used to allocate cap(stream) — the entire
// REMAINING input, often most of a 16MB WAL segment — even when
// wantBytes (a single record's header or padded body) was a few
// dozen bytes. readAllPageAware calls this twice per record, and
// initdb.Open runs several independent full-WAL replay passes, so
// a handful of small records turned into tens of seconds of
// allocation+memclr. The returned slice's capacity must scale with
// wantBytes, not with the size of a large caller-supplied buffer.
func TestExtractRecordBytesCapBoundedByWantBytes(t *testing.T) {
	const wantBytes = 24
	hugeStream := make([]byte, 16<<20) // 16 MiB, mirrors a full WAL segment
	// Byte 0 is a page-boundary header; make it non-zero so the
	// EOS/all-zero-header short-circuit doesn't take over and the
	// loop actually reaches the make() under test.
	for i := range hugeStream[:SizeOfXLogShortPHD] {
		hugeStream[i] = 0xAB
	}
	recordBytes, _ := extractRecordBytes(hugeStream, 0, 16<<20, wantBytes)
	if got := cap(recordBytes); got > wantBytes {
		t.Fatalf("extractRecordBytes over-allocated: cap=%d, want <= wantBytes=%d (stream was %d bytes)",
			got, wantBytes, len(hugeStream))
	}
}
