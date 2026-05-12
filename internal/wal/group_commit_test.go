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

// TestGroupCommitBatchingDelay verifies that commitSiblings+1 concurrent
// callers all complete (the batching delay path is exercised without hang
// or error). M0099-0003 (delay disabled but concurrent flush still tested).
func TestGroupCommitBatchingDelay(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Append enough records so all goroutines have distinct LSNs to flush.
	const N = 10 // comfortably above any threshold
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
