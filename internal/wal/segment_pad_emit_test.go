package wal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

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
	priorHead := walBuf.head
	priorBase := walBuf.base

	memRing := NewMemRing(1024)

	if err := emitSegmentPad(walBuf, memRing, gapStart, boundary, gapPrev); err != nil {
		t.Fatalf("emitSegmentPad: %v", err)
	}

	if got := walBuf.tail.Load(); got != priorTail {
		t.Fatalf("walBuf.tail moved from %d → %d", priorTail, got)
	}
	if walBuf.head != priorHead {
		t.Fatalf("walBuf.head moved from %d → %d", priorHead, walBuf.head)
	}
	if walBuf.base != priorBase {
		t.Fatalf("walBuf.base moved from %d → %d", priorBase, walBuf.base)
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
	if err := emitSegmentPad(nil, memRing, gapStart, boundary, 0); err != nil {
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
	if err := emitSegmentPad(walBuf, nil, gapStart, boundary, 7); err != nil {
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
	if err := emitSegmentPad(nil, nil, 0, 24, 0); err != nil {
		t.Fatalf("emitSegmentPad: %v", err)
	}
	// Malformed padLen still surfaces even when no ring is wired —
	// builder error is independent of ring presence.
	if err := emitSegmentPad(nil, nil, 0, 23, 0); err == nil {
		t.Fatalf("emitSegmentPad with padLen<24 should error, got nil")
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
			err := emitSegmentPad(newWALBuffer(1024), NewMemRing(1024), c.gapStart, c.boundary, 0)
			if err == nil {
				t.Fatalf("expected error for boundary=%d gapStart=%d", c.boundary, c.gapStart)
			}
			if !strings.Contains(err.Error(), "must exceed") {
				t.Fatalf("error message %q, want substring %q", err.Error(), "must exceed")
			}
		})
	}
}

// TestEmitSegmentPadPropagatesBuilderErrors pins that builder-level
// padLen failures surface verbatim to the composer caller.
func TestEmitSegmentPadPropagatesBuilderErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		padLen            uint64
		wantSubstr        string
	}{
		{"below_minimum_padLen_8", 8, "below minimum"},
		{"below_minimum_padLen_23", 23, "below minimum"},
		{"one_byte_body_padLen_25", 25, "1-byte body"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			walBuf := newWALBuffer(1024)
			memRing := NewMemRing(1024)
			err := emitSegmentPad(walBuf, memRing, 0, c.padLen, 0)
			if err == nil {
				t.Fatalf("expected builder error for padLen=%d", c.padLen)
			}
			if !strings.Contains(err.Error(), c.wantSubstr) {
				t.Fatalf("error %q, want substring %q", err.Error(), c.wantSubstr)
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
	err := emitSegmentPad(walBuf, nil, 100, 132, 0)
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
	err := emitSegmentPad(nil, memRing, 100, 132, 0)
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
	if err := emitSegmentPad(walBuf, nil, 500, 532, 0); err != nil {
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
	if err := emitSegmentPad(nil, memRing, 0, 32, 0); err != nil {
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
			if err := emitSegmentPad(walBuf, memRing, gapStart, boundary, 0xCAFEBABE); err != nil {
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
