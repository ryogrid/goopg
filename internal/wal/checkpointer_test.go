package wal

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

type fakeFlusher struct {
	mu              sync.Mutex
	calls           int
	failuresRemain  int
	err             error
	flushSignalChan chan struct{}
}

func (f *fakeFlusher) FlushAll() error {
	f.mu.Lock()
	f.calls++
	if f.flushSignalChan != nil {
		select {
		case f.flushSignalChan <- struct{}{}:
		default:
		}
	}
	if f.failuresRemain > 0 {
		f.failuresRemain--
		err := f.err
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeFlusher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestCheckpointerCheckpointNowSynchronous covers the operator-
// driven path used by the SQL `CHECKPOINT` verb: a single call
// flushes dirty pages, appends a marker, and advances
// LastCheckpointLSN before returning.
func TestCheckpointerCheckpointNowSynchronous(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	flusher := &fakeFlusher{flushSignalChan: make(chan struct{}, 4)}
	cp := NewCheckpointer(flusher, w, CheckpointerConfig{
		Interval: time.Hour, // never ticks during the test
		Logger:   slog.New(slog.NewTextHandler(nilDiscardWriter{}, nil)),
	})

	if err := cp.CheckpointNow(); err != nil {
		t.Fatalf("CheckpointNow: %v", err)
	}
	if got := cp.LastCheckpointLSN(); got == 0 {
		t.Fatal("LastCheckpointLSN unchanged after CheckpointNow")
	}
	if flusher.Calls() != 1 {
		t.Fatalf("flusher.Calls=%d want 1", flusher.Calls())
	}
}

func TestCheckpointerWritesCheckpointMarkers(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	flusher := &fakeFlusher{flushSignalChan: make(chan struct{}, 16)}
	cp := NewCheckpointer(flusher, w, CheckpointerConfig{
		Interval: 10 * time.Millisecond,
		Logger:   slog.New(slog.NewTextHandler(nilDiscardWriter{}, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = cp.Run(ctx)
		close(done)
	}()

	waitSignals(t, flusher.flushSignalChan, 2, 2*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkpointer did not stop after cancel")
	}

	recs, err := ReadAll(walDir, 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("expected at least one checkpoint record")
	}
	for i, r := range recs {
		if len(r.Payload) == 0 || r.Payload[0] != RecordKindCheckpoint {
			t.Fatalf("record %d kind = %v, want checkpoint", i, firstByte(r.Payload))
		}
	}
	if cp.LastCheckpointLSN() == 0 {
		t.Fatal("last checkpoint lsn should be non-zero")
	}
}

func TestCheckpointerRecoversFromFlushFailures(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	flusher := &fakeFlusher{
		failuresRemain:  2,
		err:             errors.New("simulated flush failure"),
		flushSignalChan: make(chan struct{}, 16),
	}
	cp := NewCheckpointer(flusher, w, CheckpointerConfig{
		Interval: 10 * time.Millisecond,
		Logger:   slog.New(slog.NewTextHandler(nilDiscardWriter{}, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = cp.Run(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for cp.LastCheckpointLSN() == 0 {
		select {
		case <-flusher.flushSignalChan:
		case <-deadline:
			cancel()
			<-done
			t.Fatal("checkpoint never succeeded after transient flush failures")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkpointer did not stop after cancel")
	}

	recs, err := ReadAll(walDir, 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("expected checkpoint records after recovery")
	}
	if flusher.Calls() < 3 {
		t.Fatalf("flush calls = %d, want at least 3", flusher.Calls())
	}
}

func TestEncodeDecodePageImageRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 123, Fork: storage.MainFork}
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatal(err)
	}
	p[321] = 0xA1

	encoded, err := EncodePageImage(rel, 7, p)
	if err != nil {
		t.Fatal(err)
	}
	gotRel, gotBlk, gotPage, err := DecodePageImage(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if gotRel != rel {
		t.Fatalf("rel = %+v, want %+v", gotRel, rel)
	}
	if gotBlk != 7 {
		t.Fatalf("blk = %d, want 7", gotBlk)
	}
	if gotPage[321] != 0xA1 {
		t.Fatalf("page byte = %#x, want 0xA1", gotPage[321])
	}
}

func waitSignals(t *testing.T, ch <-chan struct{}, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	count := 0
	for count < n {
		select {
		case <-ch:
			count++
		case <-deadline:
			t.Fatalf("timed out waiting for %d signals (got %d)", n, count)
		}
	}
}

func firstByte(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

type nilDiscardWriter struct{}

func (nilDiscardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}
