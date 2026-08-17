package xlog

import (
	"testing"
)

// The strongest correctness check for predictEmittedSize is a
// byte-for-byte round-trip against emitWithPageHeaders: for every
// (recordLen, startPos) combination, the predicted total must equal
// len(emitWithPageHeaders(...).out) and the predicted leading must
// equal the function's returned leading. The two share zero
// implementation surface — predictEmittedSize counts bytes without
// touching buildPageHeader, emitWithPageHeaders actually constructs
// the headers — so an agreement across a wide input matrix pins the
// arithmetic in one direction and detects drift in the other.

func TestPredictEmittedSizeMatchesEmitWithPageHeaders(t *testing.T) {
	const (
		segSize = int64(16 * 1024 * 1024) // PG default 16 MiB
		sysID   = uint64(0xDEADBEEF)
		tli     = uint32(1)
	)
	recordLens := []int{1, 24, 100, 1024, 8000, 8192, 8193, 16384, 65536, 16*1024*1024 - 100}
	// Cover: page-boundary start; mid-page start; segment-boundary
	// start (also a page boundary); just-before-page-boundary; just-
	// after-page-boundary; multiple pages in.
	startPositions := []int64{
		0,
		1,
		23,
		24,
		25,
		SizeOfXLogShortPHD,
		XLOGBlockSize - 1,
		XLOGBlockSize,
		XLOGBlockSize + 1,
		2 * XLOGBlockSize,
		segSize - XLOGBlockSize,
		segSize - 1,
		segSize,
		segSize + 1,
		segSize + XLOGBlockSize,
		2 * segSize,
	}
	for _, rl := range recordLens {
		// emitWithPageHeaders expects record bytes; the actual content
		// is irrelevant to layout so a deterministic byte fill is
		// sufficient.
		record := make([]byte, rl)
		for i := range record {
			record[i] = byte(i & 0xFF)
		}
		// emitWithPageHeaders takes realRecLen for the contrecord-
		// header xlp_rem_len field; the OUTPUT byte count does not
		// depend on realRecLen (only on len(record)), so we feed
		// rl verbatim. Picking realRecLen=rl matches the no-padding
		// case which is the cleanest mapping.
		for _, sp := range startPositions {
			out, leading := emitWithPageHeaders(record, rl, sp, segSize, sysID, tli)
			pTotal, pLeading := predictEmittedSize(rl, sp, segSize)
			if pTotal != len(out) {
				t.Errorf("recordLen=%d startPos=%d: predicted total=%d emitWithPageHeaders=%d", rl, sp, pTotal, len(out))
			}
			if pLeading != leading {
				t.Errorf("recordLen=%d startPos=%d: predicted leading=%d emitWithPageHeaders=%d", rl, sp, pLeading, leading)
			}
		}
	}
}

func TestPredictEmittedSizeLeadingHeader(t *testing.T) {
	const segSize = int64(16 * 1024 * 1024)
	// Mid-page start → no leading header.
	if _, leading := predictEmittedSize(100, 1, segSize); leading != 0 {
		t.Errorf("mid-page start: leading=%d, want 0", leading)
	}
	// Page boundary, not segment boundary → short header.
	if _, leading := predictEmittedSize(100, XLOGBlockSize, segSize); leading != SizeOfXLogShortPHD {
		t.Errorf("page-boundary start: leading=%d, want %d", leading, SizeOfXLogShortPHD)
	}
	// Segment boundary (also a page boundary) → long header.
	if _, leading := predictEmittedSize(100, 0, segSize); leading != SizeOfXLogLongPHD {
		t.Errorf("segment-boundary start: leading=%d, want %d", leading, SizeOfXLogLongPHD)
	}
	// Higher segment boundary → also long header.
	if _, leading := predictEmittedSize(100, 2*segSize, segSize); leading != SizeOfXLogLongPHD {
		t.Errorf("higher segment-boundary start: leading=%d, want %d", leading, SizeOfXLogLongPHD)
	}
}

func TestPredictEmittedSizeShortContrecord(t *testing.T) {
	const segSize = int64(16 * 1024 * 1024)
	// A record that spans exactly one page boundary should add one
	// short contrecord header (the page being crossed is not segment-
	// boundary-aligned because we start at XLOGBlockSize, which is far
	// from any segment boundary at segSize=16MiB).
	startPos := int64(XLOGBlockSize / 2) // mid-page so no leading
	// Force a contrecord: emit XLOGBlockSize/2 bytes (fills the page)
	// + 100 bytes that continue on the next page.
	rl := XLOGBlockSize/2 + 100
	total, leading := predictEmittedSize(rl, startPos, segSize)
	if leading != 0 {
		t.Fatalf("mid-page start: leading=%d, want 0", leading)
	}
	want := rl + SizeOfXLogShortPHD
	if total != want {
		t.Errorf("one-page-cross: total=%d, want %d (=recordLen %d + short header %d)", total, want, rl, SizeOfXLogShortPHD)
	}
}

func TestPredictEmittedSizeLongContrecordAtSegmentBoundary(t *testing.T) {
	const segSize = int64(16 * 1024 * 1024)
	// Start near a segment boundary so the contrecord header lands on
	// the segment boundary and is therefore a LONG header.
	// startPos = segSize - 100 → mid-page (segSize % XLOGBlockSize ==
	// 0, so segSize itself is page-aligned; minus 100 is not).
	startPos := segSize - 100
	rl := 200 // fills the last 100 bytes of one segment and 100 of the next
	total, leading := predictEmittedSize(rl, startPos, segSize)
	if leading != 0 {
		t.Fatalf("mid-page start: leading=%d, want 0", leading)
	}
	// 100 bytes of record + long header + 100 bytes of record = 200 + 40
	want := rl + SizeOfXLogLongPHD
	if total != want {
		t.Errorf("segment-boundary contrecord: total=%d, want %d", total, want)
	}
}

func TestPredictEmittedSizeMultipleContrecordCrossings(t *testing.T) {
	const segSize = int64(16 * 1024 * 1024)
	// Start at page boundary 0 (long leading), span 3 pages.
	startPos := int64(0)
	// leading long header consumes SizeOfXLogLongPHD bytes of page 0.
	// page 0 then holds XLOGBlockSize - 40 = 8152 record bytes,
	// short contrecord header at page 1 start (24 B), page 1 holds
	// XLOGBlockSize - 24 = 8168 record bytes, short contrecord at
	// page 2 start (24 B), page 2 holds remainder.
	// We want a 3-page span — pick rl = 8152 + 8168 + 5000.
	rl := 8152 + 8168 + 5000
	total, leading := predictEmittedSize(rl, startPos, segSize)
	if leading != SizeOfXLogLongPHD {
		t.Fatalf("seg-boundary start: leading=%d, want %d", leading, SizeOfXLogLongPHD)
	}
	want := SizeOfXLogLongPHD + rl + 2*SizeOfXLogShortPHD
	if total != want {
		t.Errorf("multi-page-cross: total=%d, want %d (=long header %d + recordLen %d + 2×short header %d)",
			total, want, SizeOfXLogLongPHD, rl, SizeOfXLogShortPHD)
	}
}

func TestPredictEmittedSizeInvalidInputsReturnZero(t *testing.T) {
	const segSize = int64(16 * 1024 * 1024)
	cases := []struct {
		name      string
		recordLen int
		startPos  int64
		segSize   int64
	}{
		{"zero recordLen", 0, 100, segSize},
		{"negative recordLen", -1, 100, segSize},
		{"zero segSize", 100, 100, 0},
		{"negative segSize", 100, 100, -1},
		{"negative startPos", 100, -1, segSize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			total, leading := predictEmittedSize(c.recordLen, c.startPos, c.segSize)
			if total != 0 || leading != 0 {
				t.Errorf("predictEmittedSize(%d, %d, %d) = (%d, %d), want (0, 0)", c.recordLen, c.startPos, c.segSize, total, leading)
			}
		})
	}
}

