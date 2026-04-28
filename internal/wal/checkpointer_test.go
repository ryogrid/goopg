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

// TestCheckpointerSpreadPacing pins the milestone-0002
// spread-checkpoint contract: when CompletionTarget is set and a
// timer-driven checkpoint runs, the pacer is invoked once per
// flushed buffer with monotonically increasing progress in
// (0, 1]. The IMMEDIATE-speed paths (CheckpointNow and
// volume-triggered) skip the pacer.
func TestCheckpointerSpreadPacing(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	pf := &pacedFakeFlusher{dirty: 4}
	cp := NewCheckpointer(pf, w, CheckpointerConfig{
		Interval:         100 * time.Millisecond,
		CompletionTarget: 0.5,
	})

	if err := cp.runCheckpoint(context.Background(), true); err != nil {
		t.Fatalf("runCheckpoint(spread): %v", err)
	}
	if len(pf.progresses) != 4 {
		t.Fatalf("pacer called %d times, want 4", len(pf.progresses))
	}
	prev := 0.0
	for i, p := range pf.progresses {
		if p <= prev {
			t.Errorf("progress[%d]=%v not strictly greater than prev=%v", i, p, prev)
		}
		prev = p
	}
	if pf.progresses[len(pf.progresses)-1] != 1.0 {
		t.Errorf("final progress = %v, want 1.0", pf.progresses[len(pf.progresses)-1])
	}

	// Now run an IMMEDIATE-speed checkpoint and expect no pacing.
	pf2 := &pacedFakeFlusher{dirty: 3}
	cp2 := NewCheckpointer(pf2, w, CheckpointerConfig{
		Interval:         100 * time.Millisecond,
		CompletionTarget: 0.5,
	})
	if err := cp2.CheckpointNow(); err != nil {
		t.Fatal(err)
	}
	if len(pf2.progresses) != 0 {
		t.Errorf("CheckpointNow paced %d times, want 0 (IMMEDIATE)", len(pf2.progresses))
	}
	if !pf2.flushAllCalled {
		t.Error("CheckpointNow did not call FlushAll fallback")
	}
}

// TestCheckpointerSpreadHonoursDeadlines verifies that the pacer
// inserts wall-clock delay so total flush time approaches
// Interval*CompletionTarget.
func TestCheckpointerSpreadHonoursDeadlines(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// 4 buffers, target 50ms — pacer should aim for 12.5ms
	// per buffer; total runtime should be at least 30ms
	// (allowing for the last-buffer no-delay).
	pf := &pacedFakeFlusher{dirty: 4}
	cp := NewCheckpointer(pf, w, CheckpointerConfig{
		Interval:         100 * time.Millisecond,
		CompletionTarget: 0.5,
	})
	start := time.Now()
	if err := cp.runCheckpoint(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 30*time.Millisecond {
		t.Errorf("spread checkpoint completed in %v; expected >= 30ms", elapsed)
	}
	// Don't bound from above tightly — CI machines vary.
	if elapsed > 500*time.Millisecond {
		t.Errorf("spread checkpoint took %v; expected < 500ms", elapsed)
	}
}

type pacedFakeFlusher struct {
	dirty          int
	progresses     []float64
	flushAllCalled bool
}

func (p *pacedFakeFlusher) FlushAll() error {
	p.flushAllCalled = true
	return nil
}

func (p *pacedFakeFlusher) FlushAllPaced(pacer func(progress float64) error) error {
	for i := 0; i < p.dirty; i++ {
		progress := float64(i+1) / float64(p.dirty)
		if err := pacer(progress); err != nil {
			return err
		}
		p.progresses = append(p.progresses, progress)
	}
	return nil
}

// TestCheckpointerVolumeTrigger pins the max_wal_size path.
// We arm a small MaxWALBytes threshold, append enough records
// through the writer to cross it, and expect a checkpoint
// before Interval elapses. Interval is set to an hour so any
// success here came from the volume trigger, not the timer.
func TestCheckpointerVolumeTrigger(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	flusher := &fakeFlusher{flushSignalChan: make(chan struct{}, 16)}
	cp := NewCheckpointer(flusher, w, CheckpointerConfig{
		Interval:            time.Hour,
		MaxWALBytes:         128, // very small threshold
		VolumeCheckInterval: 5 * time.Millisecond,
		Logger:              slog.New(slog.NewTextHandler(nilDiscardWriter{}, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = cp.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Force the writer past the threshold.
	for i := 0; i < 10; i++ {
		if _, _, err := w.Append([]byte("aaaaaaaaaaaaaaaa")); err != nil {
			t.Fatal(err)
		}
	}

	// Poll LastCheckpointLSN; the volume-triggered checkpoint
	// fires once the ticker tick observes WrittenLSN >= 128 and
	// finishes its Flush+Append+FlushUpTo dance.
	deadline := time.Now().Add(2 * time.Second)
	for cp.LastCheckpointLSN() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("volume trigger did not fire within 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if flusher.Calls() == 0 {
		t.Error("flusher.FlushAll was never called")
	}
}

// TestCheckpointerVolumeTriggerThreshold pins the boundary on
// volumeTriggerFires: at threshold-1 it must not fire; at
// threshold it must.
func TestCheckpointerVolumeTriggerThreshold(t *testing.T) {
	cp := &Checkpointer{cfg: CheckpointerConfig{MaxWALBytes: 100}}

	// No checkpoint yet, written < threshold -> no fire.
	if cp.volumeTriggerFires(stubReporter{lsn: 99}) {
		t.Error("fired at written=99, threshold=100, lastCkpt=0")
	}
	// No checkpoint yet, written == threshold -> fire.
	if !cp.volumeTriggerFires(stubReporter{lsn: 100}) {
		t.Error("did not fire at written=100, threshold=100, lastCkpt=0")
	}

	// After a checkpoint at lsn=200, gap < threshold -> no fire.
	cp.lastCheckpointLSN.Store(200)
	if cp.volumeTriggerFires(stubReporter{lsn: 250}) {
		t.Error("fired at gap=50, threshold=100")
	}
	// gap == threshold -> fire.
	if !cp.volumeTriggerFires(stubReporter{lsn: 300}) {
		t.Error("did not fire at gap=100, threshold=100")
	}
}

type stubReporter struct{ lsn uint64 }

func (s stubReporter) WrittenLSN() uint64 { return s.lsn }

// TestCheckpointerSetInterval pins the GUC-driven cadence
// override. Construction defaults Interval to 10s; SetInterval
// must update the field so a subsequent Run uses it. We don't
// drive Run here — the periodic-fire path is exercised by
// TestCheckpointerWritesCheckpointMarkers.
func TestCheckpointerSetInterval(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	flusher := &fakeFlusher{flushSignalChan: make(chan struct{}, 1)}
	cp := NewCheckpointer(flusher, w, CheckpointerConfig{})
	if cp.cfg.Interval != 10*time.Second {
		t.Fatalf("default Interval=%v want 10s", cp.cfg.Interval)
	}
	cp.SetInterval(5 * time.Minute)
	if cp.cfg.Interval != 5*time.Minute {
		t.Fatalf("after SetInterval(5m): Interval=%v", cp.cfg.Interval)
	}
	// Non-positive values are ignored (defensive).
	cp.SetInterval(0)
	if cp.cfg.Interval != 5*time.Minute {
		t.Fatalf("SetInterval(0) clobbered the existing value")
	}
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
