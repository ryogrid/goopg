package wal

import (
	"encoding/binary"
	"errors"
	"testing"
)

// makeAppendXLogPayloadFixture wires a stripeWriterCore against a 1 MiB
// ring with a 1 MiB segment so the default-path tests have no
// cross-segment pressure. segSize override is via
// makeAppendXLogPayloadFixtureWithCross.
func makeAppendXLogPayloadFixture(t *testing.T, segSize uint64) *stripeWriterCore {
	t.Helper()
	walBuf := newWALBuffer(1 << 20)
	walBuf.reset(0)
	memRing := NewMemRing(1 << 20)
	return newStripeWriterCore(segSize, 0, 0, walBuf, memRing)
}

func TestAppendXLogPayloadHappyPathReturnsPredictedSizes(t *testing.T) {
	t.Parallel()
	c := makeAppendXLogPayloadFixture(t, 1<<20)

	payload := []byte("hello-pg-record")
	const sysID = uint64(0xDEAD_BEEF_CAFE_BABE)
	const tli = uint32(1)

	start, prev, total, leading, err := c.AppendXLogPayload(0, payload, 1<<20, sysID, tli)
	if err != nil {
		t.Fatalf("AppendXLogPayload: %v", err)
	}
	// First reservation lands at start=0 which is segment-aligned →
	// leading = long PHD; prev = 0 (no prior record).
	if start != 0 {
		t.Fatalf("start=%d, want 0", start)
	}
	if prev != 0 {
		t.Fatalf("prev=%d, want 0", prev)
	}
	if leading != SizeOfXLogLongPHD {
		t.Fatalf("leading=%d, want SizeOfXLogLongPHD=%d (start=0 segment-aligned)",
			leading, SizeOfXLogLongPHD)
	}
	// Predicted: realRecLen = 24+2+len(payload); paddedLen =
	// maxAlignXLog(realRecLen).
	_, paddedLen := predictXLogRecordLen(payload)
	if total != SizeOfXLogLongPHD+paddedLen {
		t.Fatalf("total=%d, want SizeOfXLogLongPHD+paddedLen=%d",
			total, SizeOfXLogLongPHD+paddedLen)
	}
}

func TestAppendXLogPayloadTwoRecordsFormChain(t *testing.T) {
	t.Parallel()
	c := makeAppendXLogPayloadFixture(t, 1<<20)

	const sysID = uint64(1)
	const tli = uint32(1)

	payload1 := []byte("first-record")
	start1, _, total1, _, err := c.AppendXLogPayload(0, payload1, 1<<20, sysID, tli)
	if err != nil {
		t.Fatalf("AppendXLogPayload #1: %v", err)
	}

	payload2 := []byte("second-record")
	start2, prev2, _, leading2, err := c.AppendXLogPayload(1, payload2, 1<<20, sysID, tli)
	if err != nil {
		t.Fatalf("AppendXLogPayload #2: %v", err)
	}
	// Reservation #2 lands at start2 = total1 (bytes-after-header path
	// in insertPosTracker.reserveEmittedAndPublish: curr advances by
	// total of #1's reservation including leading PHD).
	if start2 != uint64(total1) {
		t.Fatalf("start2=%d, want total1=%d (contiguous reservation)",
			start2, total1)
	}
	// xl_prev linkage: #2's prev field must equal #1's start (the
	// post-reservation prev returned by reserveEmittedAndPublish is
	// the prior record's start).
	if prev2 != start1 {
		t.Fatalf("prev2=%d, want start1=%d (xl_prev chain)", prev2, start1)
	}
	// Mid-page second reservation gets no leading PHD.
	if leading2 != 0 {
		t.Fatalf("leading2=%d, want 0 (mid-page reservation)", leading2)
	}

	// Publish + decode header from walBuf to pin the on-the-wire
	// xl_prev field.
	c.PublishUpTo(int64(start2) + int64(len(payload2)) + 64)
	hdr := make([]byte, SizeOfXLogRecord)
	if got := c.walBuf.readAt(int64(start2), hdr); got != len(hdr) {
		t.Fatalf("walBuf.readAt(start2) read=%d, want %d", got, len(hdr))
	}
	gotPrev := binary.LittleEndian.Uint64(hdr[8:16])
	if gotPrev != prev2 {
		t.Fatalf("on-wire xl_prev=%d, want %d (matches build closure's prev)",
			gotPrev, prev2)
	}
}

func TestAppendXLogPayloadBytesLandInWalBuf(t *testing.T) {
	t.Parallel()
	c := makeAppendXLogPayloadFixture(t, 1<<20)

	payload := []byte{0xCA, 0xFE, 0xBA, 0xBE, 0xDE, 0xAD, 0xBE, 0xEF}
	const sysID = uint64(0x12345)
	const tli = uint32(7)

	start, _, total, leading, err := c.AppendXLogPayload(2, payload, 1<<20, sysID, tli)
	if err != nil {
		t.Fatalf("AppendXLogPayload: %v", err)
	}

	c.PublishUpTo(int64(start) + int64(total) + 64)

	// Reconstruct what emitWithPageHeaders would have produced
	// directly and confirm walBuf holds the same bytes.
	record, realRecLen, encErr := encodeRecordXLog(payload, /*prev*/ 0)
	if encErr != nil {
		t.Fatalf("encodeRecordXLog: %v", encErr)
	}
	want, wantLeading := emitWithPageHeaders(record, realRecLen, int64(start), 1<<20, sysID, tli)
	if len(want) != total {
		t.Fatalf("direct-emit size %d disagrees with composer total %d",
			len(want), total)
	}
	if wantLeading != leading {
		t.Fatalf("direct-emit leading %d disagrees with composer leading %d",
			wantLeading, leading)
	}
	got := make([]byte, total)
	if n := c.walBuf.readAt(int64(start), got); n != len(got) {
		t.Fatalf("walBuf.readAt n=%d, want %d", n, len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d: got=0x%02x want=0x%02x", i, got[i], want[i])
		}
	}
}

func TestAppendXLogPayloadNilReceiverReturnsError(t *testing.T) {
	t.Parallel()
	var c *stripeWriterCore
	_, _, _, _, err := c.AppendXLogPayload(0, []byte("anything"), 1<<20, 0, 0)
	if !errors.Is(err, errStripeWriterCoreNil) {
		t.Fatalf("err=%v, want errStripeWriterCoreNil", err)
	}
}

func TestAppendXLogPayloadNilPayloadReturnsEmptyRecordError(t *testing.T) {
	t.Parallel()
	c := makeAppendXLogPayloadFixture(t, 1<<20)
	_, _, _, _, err := c.AppendXLogPayload(0, nil, 1<<20, 0, 0)
	if !errors.Is(err, errStripeAppendEmptyRecord) {
		t.Fatalf("err=%v, want errStripeAppendEmptyRecord (nil payload predicts paddedLen=0)", err)
	}
}

func TestAppendXLogPayloadEmptyByteSliceProceeds(t *testing.T) {
	t.Parallel()
	c := makeAppendXLogPayloadFixture(t, 1<<20)
	// []byte{} (non-nil) is a legitimate WAL record:
	//   wrappedLen = 2, realRecLen = 24+2 = 26, paddedLen = 32.
	_, paddedLen := predictXLogRecordLen([]byte{})
	if paddedLen == 0 {
		t.Fatalf("predictXLogRecordLen([]byte{}) returned paddedLen=0; foundation pre-condition broken")
	}
	start, prev, total, leading, err := c.AppendXLogPayload(3, []byte{}, 1<<20, 0, 0)
	if err != nil {
		t.Fatalf("AppendXLogPayload([]byte{}): %v", err)
	}
	if start != 0 || prev != 0 {
		t.Fatalf("start=%d prev=%d, want 0/0", start, prev)
	}
	if leading != SizeOfXLogLongPHD {
		t.Fatalf("leading=%d, want SizeOfXLogLongPHD", leading)
	}
	if total != SizeOfXLogLongPHD+paddedLen {
		t.Fatalf("total=%d, want %d", total, SizeOfXLogLongPHD+paddedLen)
	}
}

func TestAppendXLogPayloadCrossSegmentBoundary(t *testing.T) {
	t.Parallel()
	// segSize == 2 pages so the segment boundary coincides with a page
	// boundary — PG's invariant — and the post-crossing reservation
	// lands on a segment-aligned page (long PHD).
	const segSize = uint64(2 * XLOGBlockSize)
	walBuf := newWALBuffer(1 << 20)
	walBuf.reset(0)
	memRing := NewMemRing(1 << 20)
	c := newStripeWriterCore(segSize, 0, 0, walBuf, memRing)

	// Burn curr so the next paddedLen-sized reservation crosses the
	// segment boundary. The first reservation lands at start=0 with
	// long PHD, consumes (40 + recordLen) bytes; we size recordLen
	// such that curr ends up at segSize-50 after the burn returns.
	burnReal := int(segSize) - 50 - SizeOfXLogLongPHD
	c.posTracker.reserveEmittedAndPublish(burnReal, 0, c.inserting)
	c.inserting.setInsertingAt(0, lsnIdle)

	payload := []byte("crosses-segment-boundary")
	start, _, total, leading, err := c.AppendXLogPayload(0, payload, int64(segSize), 1, 1)
	if err != nil {
		t.Fatalf("AppendXLogPayload (cross-segment): %v", err)
	}
	if start != segSize {
		t.Fatalf("start=%d, want %d (post-boundary)", start, segSize)
	}
	// First record on a new segment-aligned page → long PHD.
	if leading != SizeOfXLogLongPHD {
		t.Fatalf("leading=%d, want SizeOfXLogLongPHD=%d (segment-aligned)",
			leading, SizeOfXLogLongPHD)
	}
	_, paddedLen := predictXLogRecordLen(payload)
	if total != SizeOfXLogLongPHD+paddedLen {
		t.Fatalf("total=%d, want %d", total, SizeOfXLogLongPHD+paddedLen)
	}
}

func TestAppendXLogPayloadEncodeAndEmitSizesAgree(t *testing.T) {
	t.Parallel()
	// Pins the foundation's contract: the composer's `total` MUST
	// equal predictEmittedSize(paddedLen, start, segSize).total for
	// any (payload, start) combination. We sweep a small payload
	// matrix at start=0 (segment-aligned, long PHD) and at start
	// mid-page (no leading PHD).
	const segSize = int64(1 << 20)
	cases := [][]byte{
		[]byte{},
		[]byte{0x01},
		[]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		make([]byte, 100),
		make([]byte, 0xFF),
		make([]byte, 0x100),
		make([]byte, XLOGBlockSize),
	}
	for i, payload := range cases {
		c := makeAppendXLogPayloadFixture(t, uint64(segSize))
		_, _, total, _, err := c.AppendXLogPayload(0, payload, segSize, 0, 0)
		if err != nil {
			t.Fatalf("case %d: AppendXLogPayload: %v", i, err)
		}
		_, paddedLen := predictXLogRecordLen(payload)
		predicted, _ := predictEmittedSize(paddedLen, 0, segSize)
		if total != predicted {
			t.Fatalf("case %d (payload len %d): total=%d, predictEmittedSize=%d",
				i, len(payload), total, predicted)
		}
	}
}
