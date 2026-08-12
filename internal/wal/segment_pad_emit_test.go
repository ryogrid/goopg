package wal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// emitSegmentPadErr drops emitSegmentPad's leading-page-header size so the
// pre-M0131-S30.1b tests, which only ever asserted on the error, keep their
// original shape.
func emitSegmentPadErr(walBuf *walBuffer, memRing *MemRing, gapStart, boundary, gapPrev uint64, lay padLayout) error {
	_, _, err := emitSegmentPad(walBuf, memRing, gapStart, boundary, gapPrev, lay)
	return err
}

// TestEmitSegmentPadWritesIntoBothRings pins the happy-path composer
// contract: pad bytes land in walBuf at gapStart, mirror into memRing
// at the same LSN, decode back to a well-formed XLOG_NOOP whose xl_prev
// equals gapPrev, and neither ring's publication watermark advances.
func TestEmitSegmentPadWritesIntoBothRings(t *testing.T) {
	t.Parallel()
	const (
		gapStart uint64 = 200
		boundary uint64 = 232 // padLen = 32 — within short-chunk range
		gapPrev  uint64 = 0xDEADBEEF
	)
	walBuf := newWALBuffer(1024)
	walBuf.reset(int64(gapStart))
	priorTail := walBuf.tail.Load()
	priorHead := walBuf.head.Load()
	priorBase := walBuf.base.Load()

	memRing := NewMemRing(1024)

	if err := emitSegmentPadErr(walBuf, memRing, gapStart, boundary, gapPrev, padLayout{}); err != nil {
		t.Fatalf("emitSegmentPad: %v", err)
	}

	if got := walBuf.tail.Load(); got != priorTail {
		t.Fatalf("walBuf.tail moved from %d → %d", priorTail, got)
	}
	if walBuf.head.Load() != priorHead {
		t.Fatalf("walBuf.head moved from %d → %d", priorHead, walBuf.head.Load())
	}
	if walBuf.base.Load() != priorBase {
		t.Fatalf("walBuf.base moved from %d → %d", priorBase, walBuf.base.Load())
	}

	walBuf.tail.Store(int64(boundary))
	out := make([]byte, boundary-gapStart)
	if n := walBuf.readAt(int64(gapStart), out); n != len(out) {
		t.Fatalf("walBuf.readAt: n=%d, want %d", n, len(out))
	}

	memRing.PublishUpTo(int64(boundary))
	memOut := make([]byte, boundary-gapStart)
	if n, ok := memRing.ReadAt(int64(gapStart), memOut); !ok || n != len(memOut) {
		t.Fatalf("memRing.ReadAt: n=%d ok=%t, want %d/true", n, ok, len(memOut))
	}
	if !bytes.Equal(out, memOut) {
		t.Fatalf("walBuf/memRing pad bytes differ")
	}

	h, err := DecodeXLogRecordHeader(out[:SizeOfXLogRecord])
	if err != nil {
		t.Fatalf("DecodeXLogRecordHeader: %v", err)
	}
	if h.TotLen != uint32(boundary-gapStart) {
		t.Fatalf("TotLen=%d, want %d", h.TotLen, boundary-gapStart)
	}
	if h.Rmid != RmgrXLog {
		t.Fatalf("Rmid=%d, want %d", h.Rmid, RmgrXLog)
	}
	if h.Info != xlogInfoNoop {
		t.Fatalf("Info=%#x, want %#x", h.Info, xlogInfoNoop)
	}
	if h.Prev != gapPrev {
		t.Fatalf("Prev=%#x, want %#x", h.Prev, gapPrev)
	}
}

// TestEmitSegmentPadNilWalBufOnlyMemRing pins the partial-ring case:
// when Config.WALBuffers == 0 leaves walBuf nil, the composer still
// mirrors the pad into memRing without error.
func TestEmitSegmentPadNilWalBufOnlyMemRing(t *testing.T) {
	t.Parallel()
	const (
		gapStart uint64 = 0
		boundary uint64 = 24 // smallest legal padLen — header-only branch
	)
	memRing := NewMemRing(256)
	if err := emitSegmentPadErr(nil, memRing, gapStart, boundary, 0, padLayout{}); err != nil {
		t.Fatalf("emitSegmentPad: %v", err)
	}
	memRing.PublishUpTo(int64(boundary))
	out := make([]byte, boundary-gapStart)
	if n, ok := memRing.ReadAt(int64(gapStart), out); !ok || n != len(out) {
		t.Fatalf("memRing.ReadAt: n=%d ok=%t, want %d/true", n, ok, len(out))
	}
	if _, err := DecodeXLogRecordHeader(out[:SizeOfXLogRecord]); err != nil {
		t.Fatalf("DecodeXLogRecordHeader: %v", err)
	}
}

// TestEmitSegmentPadNilMemRingOnlyWalBuf pins the symmetric case for
// wal_sender_memory_buffer == 0 leaving memRing nil.
func TestEmitSegmentPadNilMemRingOnlyWalBuf(t *testing.T) {
	t.Parallel()
	const (
		gapStart uint64 = 100
		boundary uint64 = 132
	)
	walBuf := newWALBuffer(512)
	walBuf.reset(int64(gapStart))
	if err := emitSegmentPadErr(walBuf, nil, gapStart, boundary, 7, padLayout{}); err != nil {
		t.Fatalf("emitSegmentPad: %v", err)
	}
	walBuf.tail.Store(int64(boundary))
	out := make([]byte, boundary-gapStart)
	if n := walBuf.readAt(int64(gapStart), out); n != len(out) {
		t.Fatalf("walBuf.readAt: n=%d, want %d", n, len(out))
	}
	h, err := DecodeXLogRecordHeader(out[:SizeOfXLogRecord])
	if err != nil {
		t.Fatalf("DecodeXLogRecordHeader: %v", err)
	}
	if h.Prev != 7 {
		t.Fatalf("Prev=%d, want 7", h.Prev)
	}
}

// TestEmitSegmentPadBothNilIsNoop pins that both nil is silent — pad
// is built (so a malformed padLen is still caught) but goes nowhere.
func TestEmitSegmentPadBothNilIsNoop(t *testing.T) {
	t.Parallel()
	if err := emitSegmentPadErr(nil, nil, 0, 24, 0, padLayout{}); err != nil {
		t.Fatalf("emitSegmentPad: %v", err)
	}
	// M0131-S30.6: a gap too small for a well-formed record is no longer an
	// error — it is zero-filled and reported as padded=false, so the caller
	// leaves the xl_prev chain alone. (Leaving such a gap UNWRITTEN was the
	// defect: without Config.Preallocate it left the segment file short and
	// replay stopped at the short read.)
	if _, padded, err := emitSegmentPad(nil, nil, 0, 23, 0, padLayout{}); err != nil || padded {
		t.Fatalf("emitSegmentPad(gap 23) = padded=%v err=%v, want padded=false err=nil", padded, err)
	}
}

// TestEmitSegmentPadRejectsNonPositiveGap pins the defence-in-depth
// composer-level check that boundary > gapStart.
func TestEmitSegmentPadRejectsNonPositiveGap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		gapStart, boundary uint64
	}{
		{"equal", 100, 100},
		{"boundary_below_start", 200, 100},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := emitSegmentPadErr(newWALBuffer(1024), NewMemRing(1024), c.gapStart, c.boundary, 0, padLayout{})
			if err == nil {
				t.Fatalf("expected error for boundary=%d gapStart=%d", c.boundary, c.gapStart)
			}
			if !strings.Contains(err.Error(), "must exceed") {
				t.Fatalf("error message %q, want substring %q", err.Error(), "must exceed")
			}
		})
	}
}

// TestEmitSegmentPadZeroFillsUnencodableGaps pins the M0131-S30.6 contract for
// the gap sizes buildSegmentPadRecord cannot encode (below the 24-byte minimum,
// and the 25-byte case whose 1-byte body cannot carry a chunk header): the
// composer writes ZEROS over the gap and reports padded=false instead of
// failing. The bytes must be written — an unwritten gap leaves a hole (a short
// segment file without Preallocate), which is what stopped replay in the
// measured failure.
func TestEmitSegmentPadZeroFillsUnencodableGaps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		padLen uint64
	}{
		{"below_minimum_padLen_8", 8},
		{"below_minimum_padLen_23", 23},
		{"one_byte_body_padLen_25", 25},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			walBuf := newWALBuffer(1024)
			memRing := NewMemRing(1024)
			_, padded, err := emitSegmentPad(walBuf, memRing, 0, c.padLen, 0, padLayout{})
			if err != nil {
				t.Fatalf("emitSegmentPad(gap %d): %v", c.padLen, err)
			}
			if padded {
				t.Fatalf("gap %d reported as padded; want zero-filled", c.padLen)
			}
			walBuf.tail.Store(int64(c.padLen))
			got := make([]byte, c.padLen)
			if n := walBuf.readAt(0, got); n != len(got) {
				t.Fatalf("walBuf.readAt: n=%d, want %d — the gap bytes were not written", n, len(got))
			}
			for i, b := range got {
				if b != 0 {
					t.Fatalf("gap byte %d = %#x, want 0", i, b)
				}
			}
		})
	}
}

// TestEmitSegmentPadPropagatesWalBufOutOfWindow pins that a gapStart
// outside walBuf's allocated [base, base+cap) window surfaces the
// walBuffer's own range error verbatim.
func TestEmitSegmentPadPropagatesWalBufOutOfWindow(t *testing.T) {
	t.Parallel()
	walBuf := newWALBuffer(64)
	walBuf.reset(1000)
	// gapStart below base.
	err := emitSegmentPadErr(walBuf, nil, 100, 132, 0, padLayout{})
	if !errors.Is(err, errWALBufferReservedOutOfRange) {
		t.Fatalf("err=%v, want errWALBufferReservedOutOfRange", err)
	}
}

// TestEmitSegmentPadPropagatesMemRingOutOfWindow pins the symmetric
// case for the MemRing.
func TestEmitSegmentPadPropagatesMemRingOutOfWindow(t *testing.T) {
	t.Parallel()
	memRing := NewMemRing(64)
	memRing.PublishUpTo(2000) // advances head + tail past the gap.
	err := emitSegmentPadErr(nil, memRing, 100, 132, 0, padLayout{})
	if !errors.Is(err, errMemRingReservedOutOfRange) {
		t.Fatalf("err=%v, want errMemRingReservedOutOfRange", err)
	}
}

// TestEmitSegmentPadDoesNotPublishViaWalBuf pins the contract that
// emitSegmentPad never advances walBuf.tail — publication is the
// drain goroutine's job via [[0107-0007q]] publishTail.
func TestEmitSegmentPadDoesNotPublishViaWalBuf(t *testing.T) {
	t.Parallel()
	walBuf := newWALBuffer(1024)
	walBuf.reset(500)
	walBuf.tail.Store(500)
	if err := emitSegmentPadErr(walBuf, nil, 500, 532, 0, padLayout{}); err != nil {
		t.Fatalf("emitSegmentPad: %v", err)
	}
	if got := walBuf.tail.Load(); got != 500 {
		t.Fatalf("walBuf.tail advanced to %d, want 500 (publication must come from drain)", got)
	}
	// readAt of the unpublished pad sees zero bytes.
	out := make([]byte, 32)
	if n := walBuf.readAt(500, out); n != 0 {
		t.Fatalf("readAt of unpublished pad returned %d bytes, want 0", n)
	}
}

// TestEmitSegmentPadDoesNotPublishViaMemRing pins the symmetric
// contract for MemRing: tail/head untouched, ReadAt misses.
func TestEmitSegmentPadDoesNotPublishViaMemRing(t *testing.T) {
	t.Parallel()
	memRing := NewMemRing(1024)
	if err := emitSegmentPadErr(nil, memRing, 0, 32, 0, padLayout{}); err != nil {
		t.Fatalf("emitSegmentPad: %v", err)
	}
	out := make([]byte, 32)
	if n, ok := memRing.ReadAt(0, out); ok || n != 0 {
		t.Fatalf("memRing.ReadAt of unpublished pad returned n=%d ok=%t, want 0/false", n, ok)
	}
}

// TestEmitSegmentPadAcrossPadLengths exercises the three encoding
// branches (header-only, short chunk, long chunk) end-to-end through
// the composer to confirm none of the size paths break the byte
// mirror invariant between walBuf and memRing.
func TestEmitSegmentPadAcrossPadLengths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		padLen uint64
	}{
		{"header_only_24", 24},
		{"short_chunk_32", 32},
		{"short_chunk_281", 281},
		{"long_chunk_282", 282},
		{"long_chunk_1024", 1024},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			const gapStart uint64 = 4096
			boundary := gapStart + c.padLen
			walBuf := newWALBuffer(8192)
			walBuf.reset(int64(gapStart))
			memRing := NewMemRing(8192)
			if err := emitSegmentPadErr(walBuf, memRing, gapStart, boundary, 0xCAFEBABE, padLayout{}); err != nil {
				t.Fatalf("emitSegmentPad: %v", err)
			}
			walBuf.tail.Store(int64(boundary))
			memRing.PublishUpTo(int64(boundary))
			a := make([]byte, c.padLen)
			b := make([]byte, c.padLen)
			if n := walBuf.readAt(int64(gapStart), a); n != int(c.padLen) {
				t.Fatalf("walBuf.readAt: n=%d, want %d", n, c.padLen)
			}
			if n, ok := memRing.ReadAt(int64(gapStart), b); !ok || n != int(c.padLen) {
				t.Fatalf("memRing.ReadAt: n=%d ok=%t, want %d/true", n, ok, c.padLen)
			}
			if !bytes.Equal(a, b) {
				t.Fatalf("walBuf/memRing pad bytes differ for padLen=%d", c.padLen)
			}
			h, err := DecodeXLogRecordHeader(a[:SizeOfXLogRecord])
			if err != nil {
				t.Fatalf("DecodeXLogRecordHeader: %v", err)
			}
			if h.Prev != 0xCAFEBABE {
				t.Fatalf("Prev=%#x, want 0xCAFEBABE", h.Prev)
			}
		})
	}
}
