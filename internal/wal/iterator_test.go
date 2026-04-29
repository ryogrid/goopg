package wal

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wal")
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, dir
}

// TestRecordIteratorReadsExistingRecords pins the basic case: append
// three records, iterate from LSN 0, get all three back in order with
// matching payloads and contiguous LSNs.
func TestRecordIteratorReadsExistingRecords(t *testing.T) {
	w, dir := newTestWriter(t)
	want := [][]byte{[]byte("alpha"), []byte("beta"), []byte("gamma")}
	starts := make([]uint64, len(want))
	ends := make([]uint64, len(want))
	for i, p := range want {
		s, e, err := w.Append(p)
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		starts[i], ends[i] = s, e
	}

	it, err := NewRecordIterator(w, dir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for i := range want {
		rec, err := it.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		if string(rec.Payload) != string(want[i]) {
			t.Errorf("record %d payload = %q, want %q", i, rec.Payload, want[i])
		}
		if rec.StartLSN != starts[i] || rec.EndLSN != ends[i] {
			t.Errorf("record %d LSNs = (%d, %d), want (%d, %d)",
				i, rec.StartLSN, rec.EndLSN, starts[i], ends[i])
		}
	}
}

// TestRecordIteratorBlocksThenWakesOnAppend pins the streaming
// behaviour: a goroutine that has consumed all current records sleeps
// inside Next; a subsequent Append wakes it and Next returns the new
// record.
func TestRecordIteratorBlocksThenWakesOnAppend(t *testing.T) {
	w, dir := newTestWriter(t)
	if _, _, err := w.Append([]byte("first")); err != nil {
		t.Fatal(err)
	}
	it, err := NewRecordIterator(w, dir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Consume the existing record so we're at the tail.
	if rec, err := it.Next(ctx); err != nil {
		t.Fatalf("first Next: %v", err)
	} else if string(rec.Payload) != "first" {
		t.Fatalf("first.Payload = %q", rec.Payload)
	}

	got := make(chan Record, 1)
	gotErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec, err := it.Next(ctx)
		if err != nil {
			gotErr <- err
			return
		}
		got <- rec
	}()

	// Give the goroutine a chance to enter the blocked select.
	time.Sleep(50 * time.Millisecond)
	if _, _, err := w.Append([]byte("second")); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	select {
	case err := <-gotErr:
		t.Fatalf("Next: %v", err)
	case rec := <-got:
		if string(rec.Payload) != "second" {
			t.Errorf("payload = %q, want second", rec.Payload)
		}
	}
}

// TestRecordIteratorContextCancel pins clean cancel semantics: a
// goroutine blocked in Next returns ctx.Err() promptly.
func TestRecordIteratorContextCancel(t *testing.T) {
	w, dir := newTestWriter(t)
	it, err := NewRecordIterator(w, dir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := it.Next(ctx)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("Next returned nil after cancel")
		}
		if err != context.Canceled {
			t.Errorf("Next err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Next did not return within 1s of cancel")
	}
}

// TestRecordIteratorStartLSNSkipsExisting pins that an iterator
// anchored at a mid-stream record-boundary LSN yields only records
// from that point forward.
func TestRecordIteratorStartLSNSkipsExisting(t *testing.T) {
	w, dir := newTestWriter(t)
	if _, _, err := w.Append([]byte("a")); err != nil {
		t.Fatal(err)
	}
	_, secondEnd, err := w.Append([]byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append([]byte("c")); err != nil {
		t.Fatal(err)
	}
	// Anchor at the LSN of the third record's first byte = secondEnd + 1
	// (note: NewRecordIterator interprets startLSN as a record-boundary,
	// and the next record begins at byte offset = secondEnd).
	it, err := NewRecordIterator(w, dir, 4096, secondEnd+1)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rec, err := it.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(rec.Payload) != "c" {
		t.Errorf("payload = %q, want c", rec.Payload)
	}
}
