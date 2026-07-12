package wal

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDrainSafetyStress is the slice-7 drain-safety certification for the
// backend-driven WAL flush (docs/design/wal-backend-flush/). It drives every
// concurrent WAL path that touches the ring / dirty set / segment files at once
// and asserts the run is race-free (go test -race) and the LSN result invariant
// holds throughout:
//
//	writeLSN >= drainedLSN >= flushedLSN   (PG logInsert >= logWrite >= logFlush)
//
// Concurrency exercised (production pageHeaders mode):
//   - fast appends (tryAppend RLock + atomic ring reservation)
//   - slow appends (state.append under writeMu — forced by a tiny WAL buffer so
//     most appends overflow and drain)
//   - many concurrent committers (FlushUpTo — emergent group commit under
//     writeMu, the leader draining + fdatasyncing for the followers)
//   - the background walwriter (BackgroundWrite — plain-lock pre-write+flush)
//   - segment recycling (RemoveOldSegments — files/dirty mutation under writeMu)
//   - Close racing an in-flight walwriter tick
//
// If the writeMu discipline installed in slices 3–6 had a gap — e.g. a drain or
// a dirty-map mutation running outside the lock — this test trips either the
// race detector or a "concurrent map access" panic. The single-drainer
// invariant (0107-0007ai, now "the drainer is the writeMu holder") is what keeps
// the tiny-buffer overflow drains from corrupting the ring under the committers.
func TestDrainSafetyStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test; skipped in -short")
	}
	dir := t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      filepath.Clean(dir),
		SegmentSize: 256 * 1024, // small so recycling actually rolls segments
		WALBuffers:  8 * 1024,   // tiny: most appends overflow → slow-path drain
		PageHeaders: true,
		SystemID:    0x5157_0007,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	const (
		appenders = 8
		flushers  = 6
		runFor    = 1200 * time.Millisecond
	)
	deadline := time.Now().Add(runFor)
	var wg sync.WaitGroup
	var appendErrs, flushErrs atomic.Int64
	var invariantViolations atomic.Int64

	checkInvariant := func() {
		// Read the three atomics; a transient stale read is fine, but the
		// published ordering must never invert.
		wr := w.writeLSNAtomic.Load()
		dr := w.drainedLSNAtomic.Load()
		fl := w.flushedLSNAtomic.Load()
		if dr > wr || fl > dr {
			invariantViolations.Add(1)
			t.Errorf("LSN invariant violated: write=%d drained=%d flushed=%d", wr, dr, fl)
		}
	}

	// Appenders: mixed sizes so some fit the tiny buffer (fast path) and some
	// overflow (slow path under writeMu).
	for i := 0; i < appenders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			small := []byte("drain-safety")
			big := make([]byte, 3*1024) // > buffer/2, forces overflow drains
			for j := range big {
				big[j] = byte(id + j)
			}
			n := 0
			for time.Now().Before(deadline) {
				p := small
				if n%3 == 0 {
					p = big
				}
				if _, _, err := w.Append(p); err != nil {
					appendErrs.Add(1)
				}
				n++
				if n%64 == 0 {
					checkInvariant()
				}
			}
		}(i)
	}

	// Committers: emergent group commit — each flushes the current frontier.
	for i := 0; i < flushers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if err := w.FlushUpTo(w.WrittenLSN()); err != nil {
					flushErrs.Add(1)
				}
				checkInvariant()
			}
		}()
	}

	// Background walwriter.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			_ = w.BackgroundWrite()
			time.Sleep(200 * time.Microsecond)
		}
	}()

	// Recycler: retire segments strictly below the durable frontier, lagged by
	// two segments so it never races the active write head.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			durable := w.flushedLSNAtomic.Load()
			if durable > 2*256*1024 {
				keep := durable - 2*256*1024
				_, _, _ = w.RemoveOldSegments(keep)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	wg.Wait()

	// Final durability barrier + invariant.
	if err := w.FlushUpTo(w.WrittenLSN()); err != nil {
		t.Fatalf("final FlushUpTo: %v", err)
	}
	checkInvariant()
	if got := w.flushedLSNAtomic.Load(); got < w.WrittenLSN() {
		t.Errorf("final flushedLSN=%d did not reach writtenLSN=%d", got, w.WrittenLSN())
	}

	// Close must be clean even though a walwriter tick was live until Wait
	// returned (Close serialises the teardown under writeMu).
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent Close.
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if invariantViolations.Load() != 0 {
		t.Fatalf("%d LSN-invariant violations", invariantViolations.Load())
	}
	t.Logf("appendErrs=%d flushErrs=%d (both expected 0 under correct locking)",
		appendErrs.Load(), flushErrs.Load())
	if appendErrs.Load() != 0 {
		t.Errorf("appends failed %d times", appendErrs.Load())
	}
}
