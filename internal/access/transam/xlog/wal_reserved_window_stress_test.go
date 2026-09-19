package xlog

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWriteReservedWindowStress reproduces the TestPort_IsolationSuite
// wal regression (2026-09-19): wal: writeReserved range outside buffer
// window under concurrent Path-B stripe appends with multi-page records
// crossing segment boundaries while the ring is under drain pressure.
// TestDrainSafetyStress cannot hit it: its 8 KiB buffer forces every
// multi-page append down Path A (exclusive, direct-to-disk), so Path B
// stripe reservations never contend. Here the buffer is large enough
// for Path B but small enough that drains run continuously, segments
// are 256 KiB so boundary crossings are frequent, and payloads span
// 3..17 WAL pages so the pad+record two-writeReserved geometry fires.
func TestWriteReservedWindowStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test; skipped in -short")
	}
	dir := t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      filepath.Clean(dir),
		SegmentSize: 256 * 1024,
		WALBuffers:  1 << 20, // 1 MiB: Path B viable, drains frequent
		PageHeaders: true,
		SystemID:    0x5157_0019,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	const (
		appenders = 12
		flushers  = 4
		runFor    = 3 * time.Second
	)
	deadline := time.Now().Add(runFor)
	var wg sync.WaitGroup
	var appendErrs, windowErrs atomic.Int64

	for i := 0; i < appenders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			small := []byte("reserved-window")
			// Multi-page payloads: 17 KiB..94 KiB span 3..12 WAL pages.
			big := make([]byte, 17*1024+id*7*1024)
			for j := range big {
				big[j] = byte(id + j)
			}
			n := 0
			for time.Now().Before(deadline) {
				p := small
				if n%2 == 0 {
					p = big
				}
				if _, _, err := w.Append(p); err != nil {
					appendErrs.Add(1)
					if errors.Is(err, errWALBufferReservedOutOfRange) {
						windowErrs.Add(1)
						t.Logf("append id=%d n=%d len=%d: %v", id, n, len(p), err)
					}
				}
				n++
			}
		}(i)
	}
	for i := 0; i < flushers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				_ = w.FlushUpTo(w.WrittenLSN())
			}
		}()
	}
	wg.Wait()

	if err := w.FlushUpTo(w.WrittenLSN()); err != nil {
		t.Fatalf("final FlushUpTo: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	t.Logf("appendErrs=%d windowErrs=%d", appendErrs.Load(), windowErrs.Load())
	if appendErrs.Load() != 0 {
		t.Errorf("appends failed %d times (windowErrs=%d)", appendErrs.Load(), windowErrs.Load())
	}
}
