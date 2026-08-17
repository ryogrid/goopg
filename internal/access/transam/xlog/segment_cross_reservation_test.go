package xlog

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrentAppendAcrossSegmentBoundariesNoOverflow is the regression
// guard for the M0118-0001 WAL-buffer ring overflow that tripped the
// multiple-row-versions isolation spec's 1,000,000-row bulk-insert setup
// ~50% of runs with errWALBufferReservedOutOfRange.
//
// Root cause: the buffered stripe append paths (state.tryAppend and
// state.appendPGCompat Path B) reserved only `conservativeSize`
// (paddedLen+64) ring bytes, but a record whose LSN reservation straddles a
// WAL segment boundary is re-landed at the boundary by
// reserveEmittedAndPublish AFTER first emitting an XLOG_NOOP pad over the gap
// via emitSegmentPad → writeReserved. The pad and the re-landed record are
// two separate writeReserved calls whose combined footprint approaches
// 2*conservativeSize. When the ring was near-full at the crossing,
// writeReserved's [head, head+cap) bounds check rejected the second write —
// surfacing as a query error (slow Path B) or, worse, a silently-swallowed
// error in the fast path that leaves an unwritten hole in the WAL stream.
//
// The fix reserves the worst-case 2*conservativeSize footprint so both the
// pad and the record always land inside the ring window. This test drives
// many concurrent appends across many small segment boundaries with a small
// near-full buffer and asserts no append ever overflows.
//
// To verify this test actually catches the regression, temporarily revert
// `reserveSize := 2 * conservativeSize` back to `conservativeSize` in
// writer.go: this test then fails with errWALBufferReservedOutOfRange.
func TestConcurrentAppendAcrossSegmentBoundariesNoOverflow(t *testing.T) {
	const (
		writers        = 8
		appendsPerWr   = 6000
		segSize        = int64(4 * XLOGBlockSize) // 32 KiB: frequent crossings
		walBufCap      = int64(12000)             // small + not a divisor of segSize
		basePayloadLen = 200
	)

	dir := t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      filepath.Clean(dir),
		SegmentSize: segSize,
		WALBuffers:  walBufCap,
		PageHeaders: true,
		SystemID:    1,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	var (
		overflowSeen atomic.Bool
		otherErr     atomic.Value // error
		wg           sync.WaitGroup
	)
	ready := make(chan struct{})

	for s := 0; s < writers; s++ {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			for i := 0; i < appendsPerWr; i++ {
				// Vary the payload length so emitted records de-align from
				// segment boundaries — guarantees crossings land at a range
				// of gap sizes (including gap > 64, the overflow trigger).
				n := basePayloadLen + (i % 96)
				payload := make([]byte, n)
				payload[0] = byte(s)
				if _, _, aerr := w.Append(payload); aerr != nil {
					if errors.Is(aerr, errWALBufferReservedOutOfRange) {
						overflowSeen.Store(true)
					} else {
						otherErr.Store(aerr)
					}
					return
				}
			}
		}()
	}

	close(ready)
	wg.Wait()

	if overflowSeen.Load() {
		t.Fatalf("Append returned errWALBufferReservedOutOfRange: the segment-crossing reservation footprint regressed")
	}
	if v := otherErr.Load(); v != nil {
		t.Fatalf("Append returned unexpected error: %v", v.(error))
	}
}
