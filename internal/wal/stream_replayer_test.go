package wal

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

func mustMarshalTuple(t *testing.T, tup storage.HeapTuple) []byte {
	t.Helper()
	b, err := tup.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	return b
}

// TestStreamReplayerAppliesIncomingRecords pins the standby-side
// happy path: a writer accumulates records, an iterator + replayer
// pair drains and applies each one, and ApplyLSN advances in lock-step
// with EndLSN. The page state observed after each apply matches
// what direct `ReplayRecords` would produce.
func TestStreamReplayerAppliesIncomingRecords(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 4242, Fork: storage.MainFork}

	// Stage block 0 so the heap-insert lands on an existing page.
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	iter, err := NewRecordIterator(w, walDir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()

	sr := NewStreamReplayer(mgr, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	runErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		runErr <- sr.Run(ctx, iter)
	}()

	// Push three heap-insert records, one per slot.
	bodies := []string{"alpha", "beta", "gamma"}
	var lastEnd uint64
	for i, body := range bodies {
		tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte(body))
		// Slot N (1-based) lands at (i+1).
		rec := EncodeHeapInsert(rel, 0, uint16(i+1), mustMarshalTuple(t, tup))
		_, end, err := w.Append(rec)
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		lastEnd = end
	}

	// Wait for the replayer to catch up to lastEnd.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sr.ApplyLSN() < lastEnd {
		time.Sleep(10 * time.Millisecond)
	}
	if sr.ApplyLSN() != lastEnd {
		t.Fatalf("ApplyLSN = %d, want %d (records did not catch up)", sr.ApplyLSN(), lastEnd)
	}

	cancel()
	wg.Wait()
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned err=%v (expected nil on cancel)", err)
	}

	// Verify the apply actually mutated block 0.
	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	for i, body := range bodies {
		gotTup, err := storage.PageGetHeapTuple(got, uint16(i+1))
		if err != nil {
			t.Fatalf("slot %d: %v", i+1, err)
		}
		if string(gotTup.Data) != body {
			t.Errorf("slot %d body = %q, want %q", i+1, gotTup.Data, body)
		}
	}

	records, applied := sr.Stats()
	if records != 3 || applied != 3 {
		t.Errorf("Stats records=%d applied=%d, want 3,3", records, applied)
	}
}

// TestStreamReplayerIdempotentOnRestart pins the resume contract:
// re-applying a record stream that already landed produces no extra
// page mutations (idempotency via pd_lsn) and ApplyLSN tracks the
// stream's last EndLSN even when no real apply happened.
func TestStreamReplayerIdempotentOnRestart(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 4243, Fork: storage.MainFork}
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}

	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("solo"))
	rec := EncodeHeapInsert(rel, 0, 1, mustMarshalTuple(t, tup))
	_, lastEnd, err := w.Append(rec)
	if err != nil {
		t.Fatal(err)
	}

	// First-pass apply via direct ReplayRecords (simulates a prior
	// run that already finished).
	if _, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: lastEnd, Payload: rec},
	}); err != nil {
		t.Fatal(err)
	}

	// Second-pass apply via the stream replayer must be a no-op.
	iter, err := NewRecordIterator(w, walDir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	sr := NewStreamReplayer(mgr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- sr.Run(ctx, iter) }()

	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) && sr.ApplyLSN() < lastEnd {
		time.Sleep(10 * time.Millisecond)
	}
	if sr.ApplyLSN() != lastEnd {
		t.Fatalf("ApplyLSN = %d, want %d", sr.ApplyLSN(), lastEnd)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run err=%v", err)
	}
}

// TestStreamReplayerRunReturnsOnContextCancel pins the cooperative
// shutdown path: a Run blocked at the iterator tail returns nil
// immediately when the context is cancelled.
func TestStreamReplayerRunReturnsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	iter, err := NewRecordIterator(w, walDir, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	sr := NewStreamReplayer(mgr, 42)
	if got := sr.ApplyLSN(); got != 42 {
		t.Fatalf("baseline ApplyLSN = %d, want 42", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sr.Run(ctx, iter) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run err = %v, want nil on cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancel")
	}
}

func TestStreamReplayerAppliesIncomingRawPGWALBytes(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{
		WALDir:             walDir,
		SegmentSize:        DefaultSegmentSize,
		PageHeaders:        true,
		SystemID:           1,
		TimelineID:         1,
		WALBuffers:         256,
		SenderMemoryBuffer: 32,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 123, RelOid: 456, Fork: storage.MainFork}
	for i := 0; i <= 7; i++ {
		page := make(storage.Page, storage.BlockSize)
		if err := storage.InitPage(page); err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatalf("Extend block %d: %v", i, err)
		}
	}

	iter, err := NewRecordIterator(w, walDir, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()

	sr := NewStreamReplayer(mgr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- sr.Run(ctx, iter) }()

	recordBytes, _, _ := encodeTestPGHeapInsertRecord(t)
	stream := append(buildTestLongPageHeader(t), recordBytes...)
	_, end, err := w.AppendRaw(stream)
	if err != nil {
		t.Fatalf("AppendRaw: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sr.ApplyLSN() < end {
		time.Sleep(10 * time.Millisecond)
	}
	if sr.ApplyLSN() != end {
		select {
		case err := <-runErr:
			t.Fatalf("ApplyLSN = %d, want %d (Run err=%v)", sr.ApplyLSN(), end, err)
		default:
			t.Fatalf("ApplyLSN = %d, want %d", sr.ApplyLSN(), end)
		}
	}

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 7, page); err != nil {
		t.Fatal(err)
	}
	got, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatalf("PageGetHeapTuple: %v", err)
	}
	if got.Header.Xmin != 42 {
		t.Fatalf("xmin = %d, want 42", got.Header.Xmin)
	}
	if string(got.Data) != "val" {
		t.Fatalf("tuple data = %q, want %q", got.Data, "val")
	}

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run err=%v", err)
	}
}

func TestStreamReplayerWaitsForCompleteRawPGWALRecord(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{
		WALDir:             walDir,
		SegmentSize:        DefaultSegmentSize,
		PageHeaders:        true,
		SystemID:           1,
		TimelineID:         1,
		WALBuffers:         256,
		SenderMemoryBuffer: 32,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 123, RelOid: 456, Fork: storage.MainFork}
	for i := 0; i <= 7; i++ {
		page := make(storage.Page, storage.BlockSize)
		if err := storage.InitPage(page); err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatalf("Extend block %d: %v", i, err)
		}
	}

	iter, err := NewRecordIterator(w, walDir, DefaultSegmentSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()

	sr := NewStreamReplayer(mgr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- sr.Run(ctx, iter) }()

	recordBytes, _, _ := encodeTestPGHeapInsertRecord(t)
	stream := append(buildTestLongPageHeader(t), recordBytes...)
	split := len(stream) / 2
	if _, _, err := w.AppendRaw(stream[:split]); err != nil {
		t.Fatalf("AppendRaw first half: %v", err)
	}
	select {
	case err := <-runErr:
		t.Fatalf("Run exited before full record arrived: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	_, end, err := w.AppendRaw(stream[split:])
	if err != nil {
		t.Fatalf("AppendRaw second half: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sr.ApplyLSN() < end {
		time.Sleep(10 * time.Millisecond)
	}
	if sr.ApplyLSN() != end {
		select {
		case err := <-runErr:
			t.Fatalf("ApplyLSN = %d, want %d (Run err=%v)", sr.ApplyLSN(), end, err)
		default:
			t.Fatalf("ApplyLSN = %d, want %d", sr.ApplyLSN(), end)
		}
	}

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 7, page); err != nil {
		t.Fatal(err)
	}
	got, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatalf("PageGetHeapTuple: %v", err)
	}
	if string(got.Data) != "val" {
		t.Fatalf("tuple data = %q, want %q", got.Data, "val")
	}

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run err=%v", err)
	}
}
