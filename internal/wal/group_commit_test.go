package wal

import (
	"sync"
	"testing"
	"time"
)

// TestGroupCommitBatchesFlushes verifies that N concurrent FlushUpTo
// callers are served by fewer fdatasyncs than N (group commit behavior).
// M0098-0002.
func TestGroupCommitBatchesFlushes(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Append 10 records so there are LSNs to flush.
	lsns := make([]uint64, 10)
	for i := range lsns {
		_, end, err := w.Append([]byte("payload"))
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		lsns[i] = end
	}

	// Launch 10 goroutines that all call FlushUpTo concurrently.
	const N = 10
	var wg sync.WaitGroup
	errs := make([]error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			errs[i] = w.FlushUpTo(lsns[i])
		}()
	}

	// All should complete within a reasonable time.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("FlushUpTo goroutines did not complete within 10s")
	}

	for i, err := range errs {
		if err != nil {
			t.Errorf("FlushUpTo[%d]: %v", i, err)
		}
	}

	// Emergent group commit: N concurrent flushes of already-written LSNs must
	// be satisfied by FEWER than N fdatasyncs — the first holder flushes the
	// aggregate frontier and the losers return with zero I/O (observable
	// effect, not queue mechanics). Backend-driven path, slice 3.
	if got := w.walBufferCounters.fsyncCount.Sum(); got >= N {
		t.Errorf("fsyncCount = %d for %d concurrent flushes; want < %d (no batching)", got, N, N)
	}
}

// TestGroupCommitSingleCaller verifies FlushUpTo still works for a
// single caller (no regression on the basic case; commitSiblings threshold
// not met so no delay is applied).
func TestGroupCommitSingleCaller(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	_, end, err := w.Append([]byte("hello"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatalf("FlushUpTo: %v", err)
	}
}

// TestFlushUpToPreEnqueueFastExit verifies the fix-03 pre-enqueue fast
// exit: after a flush publishes flushedLSNAtomic, a subsequent FlushUpTo
// for an already-durable LSN returns without work and does not enqueue a
// group-flush request.
func TestFlushUpToPreEnqueueFastExit(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	_, end, err := w.Append([]byte("durable"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatalf("FlushUpTo: %v", err)
	}
	// The mirror must be published after the sync barrier, otherwise the
	// fast exit can never trigger.
	if got := w.flushedLSNAtomic.Load(); got < end {
		t.Fatalf("flushedLSNAtomic=%d not advanced to flushed end=%d", got, end)
	}

	// A second flush at or below the durable LSN must take the fast exit and
	// do NO additional fdatasync (the observable effect of the fix-03
	// pre-lock early return; the backend-driven path has no queue to inspect).
	before := w.walBufferCounters.fsyncCount.Sum()
	if err := w.FlushUpTo(end); err != nil {
		t.Fatalf("FlushUpTo(already-durable): %v", err)
	}
	if after := w.walBufferCounters.fsyncCount.Sum(); after != before {
		t.Errorf("already-durable FlushUpTo did %d extra fdatasync(s); want 0 (fast exit)", after-before)
	}
}

// TestGroupCommitBatchingDelay verifies that many concurrent FlushUpTo callers
// all complete without hang or error under the backend-driven flush path (each
// backend flushes its own frontier; group commit is emergent). Formerly
// exercised the commit_siblings batching-delay queue (M0099-0003), retired in
// slice 6 of docs/design/wal-backend-flush/.
func TestGroupCommitBatchingDelay(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Append enough records so all goroutines have distinct LSNs to flush.
	// 10 is comfortably above the default commit_siblings (5).
	const N = 10
	lsns := make([]uint64, N)
	for i := range lsns {
		_, end, err := w.Append([]byte("batchtest"))
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		lsns[i] = end
	}

	var wg sync.WaitGroup
	errs := make([]error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			errs[i] = w.FlushUpTo(lsns[i])
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("FlushUpTo goroutines did not complete within 10s (batching delay path)")
	}
	for i, e := range errs {
		if e != nil {
			t.Errorf("FlushUpTo[%d]: %v", i, e)
		}
	}
}
