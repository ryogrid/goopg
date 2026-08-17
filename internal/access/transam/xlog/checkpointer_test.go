package xlog

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
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

	if err := cp.runCheckpoint(context.Background(), true, false); err != nil {
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
	if err := cp.runCheckpoint(context.Background(), true, false); err != nil {
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

// TestCheckpointerDoDWritePacing is the DoD test for M0048-0004.
//
// The production DoD requires: 200k dirty buffers, target=0.5,
// interval=30s → finishes 14-17s (= target×interval = 15s).
// In-process we scale to 10 buffers, interval=200ms, target=0.5 →
// spread over 100ms. The pacer must introduce ≥60ms of delay
// (targeting 90ms = target×(N-1)/N). Volume-triggered checkpoints
// must run at IMMEDIATE speed with no pacing.
func TestCheckpointerDoDWritePacing(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	const nBufs = 10
	const interval = 200 * time.Millisecond
	const target = 0.5
	// Expected target duration = interval × target = 100ms.
	// Last buffer skips sleep → elapsed ≈ 100ms × (N-1)/N = 90ms.
	const expectedTarget = time.Duration(float64(interval) * target)
	const minElapsed = time.Duration(float64(expectedTarget) * float64(nBufs-1) / float64(nBufs) * 0.67)

	// DoD part 1: timer-driven checkpoint spreads flush over target window.
	pf := &pacedFakeFlusher{dirty: nBufs}
	cp := NewCheckpointer(pf, w, CheckpointerConfig{
		Interval:         interval,
		CompletionTarget: target,
	})
	start := time.Now()
	if err := cp.runCheckpoint(context.Background(), true, false); err != nil {
		t.Fatalf("runCheckpoint(spread): %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("paced checkpoint elapsed: %v (min %v, expected ~%v)", elapsed, minElapsed, expectedTarget)
	if elapsed < minElapsed {
		t.Errorf("paced checkpoint elapsed %v < %v; pacer not throttling writes", elapsed, minElapsed)
	}
	if len(pf.progresses) != nBufs {
		t.Errorf("pacer called %d times, want %d", len(pf.progresses), nBufs)
	}
	if pf.progresses[len(pf.progresses)-1] != 1.0 {
		t.Errorf("final progress = %v, want 1.0", pf.progresses[len(pf.progresses)-1])
	}

	// DoD part 2: volume-triggered checkpoint bypasses pacing (IMMEDIATE speed).
	pf2 := &pacedFakeFlusher{dirty: nBufs}
	cp2 := NewCheckpointer(pf2, w, CheckpointerConfig{
		Interval:         interval,
		CompletionTarget: target,
	})
	immediateStart := time.Now()
	if err := cp2.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("runCheckpoint(immediate): %v", err)
	}
	immediateElapsed := time.Since(immediateStart)
	t.Logf("IMMEDIATE checkpoint elapsed: %v", immediateElapsed)
	if len(pf2.progresses) != 0 {
		t.Errorf("IMMEDIATE checkpoint invoked pacer %d times, want 0", len(pf2.progresses))
	}
	if !pf2.flushAllCalled {
		t.Error("IMMEDIATE checkpoint did not take FlushAll fallback path")
	}
	if immediateElapsed > 20*time.Millisecond {
		t.Errorf("IMMEDIATE checkpoint took %v; want < 20ms (no pacing)", immediateElapsed)
	}
}

// TestCheckpointerVolumeTrigger pins the max_wal_size path.
// We arm a small max_wal_size (2 segments, completion target 0 ->
// CheckPointSegments = 2, so the trigger fires one segment past the
// anchor), append enough records through the writer to cross a segment
// boundary, and expect a checkpoint before Interval elapses. Interval is
// set to an hour so any success here came from the volume trigger, not
// the timer.
func TestCheckpointerVolumeTrigger(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	flusher := &fakeFlusher{flushSignalChan: make(chan struct{}, 16)}
	// armed closes once Run has seeded volumeAnchor. Without this
	// handshake the test is a race, not a flake: Run reads the writer's
	// position from its own goroutine, so a late-scheduled Run (seen under
	// -race at -p=4 on a loaded nightly host, AI-20260813-005117-008)
	// anchors AFTER the appends below and the trigger can then never fire,
	// no matter how generous the deadline.
	armed := make(chan struct{})
	cp := NewCheckpointer(flusher, w, CheckpointerConfig{
		Interval:            time.Hour,
		SegmentSize:         4096,
		MaxWALBytes:         2 * 4096,
		CompletionTarget:    0,
		VolumeCheckInterval: 5 * time.Millisecond,
		Logger:              slog.New(slog.NewTextHandler(nilDiscardWriter{}, nil)),
		OnLoopStart:         func() { close(armed) },
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
	<-armed

	// Force the writer past the first segment boundary.
	payload := bytes.Repeat([]byte("a"), 512)
	for i := 0; i < 16; i++ {
		if _, _, err := w.Append(payload); err != nil {
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

// TestCheckpointerCheckPointSegments pins goopg's CalculateCheckpointSegments
// port (xlog.c:2170-2198): segments = floor(max_wal_size/segsize /
// (1 + checkpoint_completion_target)), floored at 1. The PG-default row is
// the one M0131-S30.4 was about: 1 GiB / 16 MiB = 64 segments, divided by
// 1.9, is 33 — so PG checkpoints 33-1 = 32 segments (512 MiB) past redo,
// where goopg previously waited the full 1024 MiB.
func TestCheckpointerCheckPointSegments(t *testing.T) {
	const mib = 1024 * 1024
	cases := []struct {
		name       string
		maxWAL     uint64
		segSize    int64
		target     float64
		wantSegs   uint64
		wantMiBGap uint64 // (segs-1) * segSize, the actual trigger distance
	}{
		{"pg-defaults", 1024 * mib, 16 * mib, 0.9, 33, 512},
		{"target-zero", 1024 * mib, 16 * mib, 0, 64, 63 * 16},
		{"target-one", 1024 * mib, 16 * mib, 1.0, 32, 31 * 16},
		// CheckPointSegments = 1: upstream still checkpoints once per
		// segment because it only tests at a segment switch, so the
		// polled distance floors at one segment rather than zero.
		{"below-one-segment", 8 * mib, 16 * mib, 0.9, 1, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := &Checkpointer{cfg: CheckpointerConfig{
				MaxWALBytes:      tc.maxWAL,
				SegmentSize:      tc.segSize,
				CompletionTarget: tc.target,
			}}
			if got := cp.checkpointSegments(); got != tc.wantSegs {
				t.Errorf("checkpointSegments = %d; want %d", got, tc.wantSegs)
			}
			gap := cp.elapsedSegmentsNeeded() * uint64(tc.segSize) / mib
			if gap != tc.wantMiBGap {
				t.Errorf("trigger distance = %d MiB; want %d MiB", gap, tc.wantMiBGap)
			}
		})
	}
}

// TestCheckpointerVolumeTriggerThreshold pins the boundary on
// volumeTriggerFires against upstream's XLogCheckpointNeeded: it compares
// SEGMENT NUMBERS (new_segno >= old_segno + CheckPointSegments - 1) and
// anchors on the last checkpoint's REDO pointer, not on the checkpoint
// record's end LSN (M0131-S30.4).
func TestCheckpointerVolumeTriggerThreshold(t *testing.T) {
	const seg = 4096
	// max_wal_size = 4 segments, completion target 0 -> CheckPointSegments
	// = 4 -> fire once 3 segments have elapsed past the anchor.
	cp := &Checkpointer{cfg: CheckpointerConfig{
		MaxWALBytes:      4 * seg,
		SegmentSize:      seg,
		CompletionTarget: 0,
	}}

	// No checkpoint yet: the anchor is where Run observed the writer.
	// Two segments elapsed -> no fire; three -> fire.
	cp.volumeAnchor.Store(0)
	if cp.volumeTriggerFires(stubReporter{lsn: 3*seg - 1}) {
		t.Error("fired after 2 elapsed segments; CheckPointSegments-1 = 3")
	}
	if !cp.volumeTriggerFires(stubReporter{lsn: 3 * seg}) {
		t.Error("did not fire after 3 elapsed segments")
	}

	// A non-zero anchor shifts the window: a server restarted with the
	// WAL already deep into segment 100 must not checkpoint immediately.
	cp.volumeAnchor.Store(100 * seg)
	if cp.volumeTriggerFires(stubReporter{lsn: 100*seg + 10}) {
		t.Error("fired immediately at the start anchor")
	}
	if !cp.volumeTriggerFires(stubReporter{lsn: 103 * seg}) {
		t.Error("did not fire 3 segments past the start anchor")
	}

	// Once a checkpoint has run, redo (stored 1-based) is the anchor —
	// NOT lastCheckpointLSN, which trails redo by the flush phase.
	cp.lastCheckpointRedoLSN.Store(200*seg + 1)
	cp.lastCheckpointLSN.Store(202 * seg) // deliberately far past redo
	if cp.volumeTriggerFires(stubReporter{lsn: 202*seg + seg - 1}) {
		t.Error("fired 2 segments past redo; must measure from redo, not the record LSN")
	}
	if !cp.volumeTriggerFires(stubReporter{lsn: 203 * seg}) {
		t.Error("did not fire 3 segments past redo")
	}
}

// TestCheckpointerVolumeTriggerDisabled pins that max_wal_size = 0 never
// fires, independent of the writer position.
func TestCheckpointerVolumeTriggerDisabled(t *testing.T) {
	cp := &Checkpointer{cfg: CheckpointerConfig{SegmentSize: 4096}}
	if cp.volumeTriggerFires(stubReporter{lsn: 1 << 40}) {
		t.Error("fired with MaxWALBytes = 0")
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

// TestCheckpointerUpdatesCheckPointDistanceEstimate pins the two upstream
// UpdateCheckPointDistanceEstimate behaviours (xlog.c): the moving average
// jumps immediately to a higher observed inter-checkpoint distance, but
// only decays slowly (90/10 weighted) toward a lower one. Exact byte counts
// aren't asserted (checkpoint marker framing overhead is an implementation
// detail) — only the directional jump-up/decay-down shape.
func TestCheckpointerUpdatesCheckPointDistanceEstimate(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 65536})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	flusher := &fakeFlusher{flushSignalChan: make(chan struct{}, 8)}
	cp := NewCheckpointer(flusher, w, CheckpointerConfig{
		Interval: time.Hour, // never ticks during the test
		Logger:   slog.New(slog.NewTextHandler(nilDiscardWriter{}, nil)),
	})

	if got := cp.CheckPointDistanceEstimate(); got != 0 {
		t.Fatalf("estimate before any checkpoint = %v, want 0", got)
	}

	// First checkpoint: no prior redo LSN to compare against, so the
	// estimate stays at its zero value.
	if err := cp.CheckpointNow(); err != nil {
		t.Fatal(err)
	}
	if got := cp.CheckPointDistanceEstimate(); got != 0 {
		t.Fatalf("estimate after first checkpoint (no prior redo) = %v, want 0", got)
	}

	// Second checkpoint after a small append: nbytes > 0 == prior estimate,
	// so it jumps straight to nbytes (the "< nbytes" branch).
	if _, _, err := w.Append(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if err := cp.CheckpointNow(); err != nil {
		t.Fatal(err)
	}
	afterSmall := cp.CheckPointDistanceEstimate()
	if afterSmall <= 0 {
		t.Fatalf("estimate after second checkpoint = %v, want > 0", afterSmall)
	}

	// Third checkpoint after a much larger append: nbytes exceeds the
	// current estimate, so it jumps immediately to (approximately) the new
	// larger value rather than being smoothed.
	if _, _, err := w.Append(make([]byte, 5000)); err != nil {
		t.Fatal(err)
	}
	if err := cp.CheckpointNow(); err != nil {
		t.Fatal(err)
	}
	afterBig := cp.CheckPointDistanceEstimate()
	if afterBig <= afterSmall {
		t.Fatalf("estimate after big cycle = %v, want > %v (should jump up)", afterBig, afterSmall)
	}
	if afterBig < 5000 {
		t.Fatalf("estimate after big cycle = %v, want >= 5000 (jumps to the raw distance on increase)", afterBig)
	}

	// Fourth checkpoint after a small append again: nbytes is now well
	// below the current estimate, so it should decay via 0.90*est +
	// 0.10*nbytes rather than dropping straight to the new small value.
	if _, _, err := w.Append(make([]byte, 50)); err != nil {
		t.Fatal(err)
	}
	if err := cp.CheckpointNow(); err != nil {
		t.Fatal(err)
	}
	afterDecay := cp.CheckPointDistanceEstimate()
	if afterDecay >= afterBig {
		t.Fatalf("estimate after small cycle = %v, want < %v (should decay downward)", afterDecay, afterBig)
	}
	if afterDecay < 0.85*afterBig {
		t.Fatalf("estimate decayed too fast: %v, want >= %v (0.90 weight on the prior estimate should dominate one cycle)", afterDecay, 0.85*afterBig)
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

// TestCheckpointerUpdatesPgControl verifies that runCheckpoint writes an
// updated pg_control to disk (state=DB_IN_PRODUCTION, non-zero checkPoint)
// when CheckpointerConfig.DataDir is set.
func TestCheckpointerUpdatesPgControl(t *testing.T) {
	dir := t.TempDir()
	globalDir := filepath.Join(dir, "global")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	walDir := filepath.Join(dir, "pg_wal")

	// Build a minimal valid pg_control file (8192 bytes, correct CRC).
	buf := make([]byte, 8192)
	le := binary.LittleEndian
	le.PutUint64(buf[0:], 0x0102030405060708) // system_identifier sentinel
	le.PutUint32(buf[8:], 1800)               // pg_control_version
	le.PutUint32(buf[16:], 1)                 // state = DB_SHUTDOWNED
	crcTable := crc32.MakeTable(crc32.Castagnoli)
	crc := crc32.Checksum(buf[:292], crcTable)
	le.PutUint32(buf[292:], crc)
	if err := os.WriteFile(filepath.Join(globalDir, "pg_control"), buf, 0o600); err != nil {
		t.Fatal(err)
	}

	// PageHeaders=true + TimelineID=1 mimics the production server so the
	// first WAL record has a 40-byte leading page header (SizeOfXLogLongPHD),
	// making startLSN=41 and checkLSN0=40 (non-zero) in pg_control.
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096,
		PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		DataDir: dir,
	})
	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("runCheckpoint: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(globalDir, "pg_control"))
	if err != nil {
		t.Fatal(err)
	}

	// system_identifier must not change.
	if sysID := le.Uint64(got[0:]); sysID != 0x0102030405060708 {
		t.Errorf("system_identifier changed: %#x", sysID)
	}
	// State must be DB_IN_PRODUCTION (6).
	if state := le.Uint32(got[16:]); state != 6 {
		t.Errorf("state: got %d want 6 (DB_IN_PRODUCTION)", state)
	}
	// checkPoint must be non-zero.
	if cp := le.Uint64(got[32:]); cp == 0 {
		t.Error("checkPoint is still 0 after checkpoint")
	}
	// CRC must be valid.
	wantCRC := crc32.Checksum(got[:292], crcTable)
	gotCRC := le.Uint32(got[292:])
	if gotCRC != wantCRC {
		t.Errorf("CRC mismatch after checkpoint: got %#x want %#x", gotCRC, wantCRC)
	}
}

// TestCheckpointerShutdownSetsDBShutdowned pins the M0110-0004 invariant:
// CheckpointShutdown stamps pg_control State = DB_SHUTDOWNED (1) while an
// ordinary checkpoint leaves DB_IN_PRODUCTION (6). Upstream tools
// (pg_resetwal/pg_rewind/pg_controldata) read this byte to decide whether
// the cluster needs recovery; a clean goopg shutdown must look clean.
func TestCheckpointerShutdownSetsDBShutdowned(t *testing.T) {
	newCP := func(t *testing.T) (*Checkpointer, string) {
		t.Helper()
		dir := t.TempDir()
		globalDir := filepath.Join(dir, "global")
		if err := os.MkdirAll(globalDir, 0o755); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 8192)
		le := binary.LittleEndian
		le.PutUint64(buf[0:], 0x0102030405060708)
		le.PutUint32(buf[8:], 1800)
		le.PutUint32(buf[16:], 6) // start from DB_IN_PRODUCTION
		crcTable := crc32.MakeTable(crc32.Castagnoli)
		le.PutUint32(buf[292:], crc32.Checksum(buf[:292], crcTable))
		if err := os.WriteFile(filepath.Join(globalDir, "pg_control"), buf, 0o600); err != nil {
			t.Fatal(err)
		}
		w, err := NewWriter(Config{WALDir: filepath.Join(dir, "pg_wal"),
			SegmentSize: 4096, PageHeaders: true, TimelineID: 1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = w.Close() })
		return NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{DataDir: dir}), dir
	}
	readState := func(t *testing.T, dir string) uint32 {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(dir, "global", "pg_control"))
		if err != nil {
			t.Fatal(err)
		}
		return binary.LittleEndian.Uint32(got[16:])
	}

	// CheckpointShutdown → DB_SHUTDOWNED (1).
	cp, dir := newCP(t)
	if err := cp.CheckpointShutdown(); err != nil {
		t.Fatalf("CheckpointShutdown: %v", err)
	}
	if state := readState(t, dir); state != 1 {
		t.Errorf("shutdown checkpoint state: got %d want %d (DB_SHUTDOWNED)",
			state, 1)
	}

	// CheckpointNow → DB_IN_PRODUCTION (6), confirming the flag is honoured
	// in both directions on the same code path.
	cp2, dir2 := newCP(t)
	if err := cp2.CheckpointNow(); err != nil {
		t.Fatalf("CheckpointNow: %v", err)
	}
	if state := readState(t, dir2); state != 6 {
		t.Errorf("online checkpoint state: got %d want %d (DB_IN_PRODUCTION)",
			state, 6)
	}
}

// TestCheckpointerWritesNextXidIntoPgControl pins the M0106-0010
// batched-45 invariant: at every checkpoint the checkpointer must call
// the NextXIDFn hook and write its value into pg_control offset 64
// (checkPointCopy.nextXid). Without this, a PG standby attaching via
// basebackup boots with snapshot xmax = bootstrap XID (3), hiding every
// tuple created after initdb.
func TestCheckpointerWritesNextXidIntoPgControl(t *testing.T) {
	dir := t.TempDir()
	globalDir := filepath.Join(dir, "global")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	walDir := filepath.Join(dir, "pg_wal")

	buf := make([]byte, 8192)
	le := binary.LittleEndian
	le.PutUint64(buf[0:], 0x1122334455667788)
	le.PutUint32(buf[8:], 1800)
	le.PutUint32(buf[16:], 1) // DB_SHUTDOWNED
	le.PutUint64(buf[64:], 3) // seed nextXid = FirstNormalTransactionId
	crcTable := crc32.MakeTable(crc32.Castagnoli)
	crc := crc32.Checksum(buf[:292], crcTable)
	le.PutUint32(buf[292:], crc)
	if err := os.WriteFile(filepath.Join(globalDir, "pg_control"), buf, 0o600); err != nil {
		t.Fatal(err)
	}

	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096,
		PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var nextCalls int
	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		DataDir: dir,
		NextXIDFn: func() uint64 {
			nextCalls++
			return 4711
		},
	})
	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("runCheckpoint: %v", err)
	}
	if nextCalls == 0 {
		t.Errorf("NextXIDFn was not called during checkpoint")
	}

	got, err := os.ReadFile(filepath.Join(globalDir, "pg_control"))
	if err != nil {
		t.Fatal(err)
	}
	if nx := le.Uint64(got[64:]); nx != 4711 {
		t.Errorf("nextXid in pg_control after checkpoint: got %d want 4711", nx)
	}

	// Subsequent checkpoint with a *lower* hook value must NOT roll back
	// nextXid — pg_control's value is monotonic by design.
	cp2 := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		DataDir:   dir,
		NextXIDFn: func() uint64 { return 100 },
	})
	if err := cp2.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("runCheckpoint (regression attempt): %v", err)
	}
	got, err = os.ReadFile(filepath.Join(globalDir, "pg_control"))
	if err != nil {
		t.Fatal(err)
	}
	if nx := le.Uint64(got[64:]); nx != 4711 {
		t.Errorf("nextXid regressed after lower-value checkpoint: got %d want 4711", nx)
	}

	// CRC must still validate.
	wantCRC := crc32.Checksum(got[:292], crcTable)
	gotCRC := le.Uint32(got[292:])
	if gotCRC != wantCRC {
		t.Errorf("CRC mismatch: got %#x want %#x", gotCRC, wantCRC)
	}
}

// TestCheckpointerCallsPostCheckpointFn verifies that runCheckpoint invokes
// PostCheckpointFn exactly once after a successful checkpoint and that an
// error returned by the hook is swallowed (non-fatal). M0106-0011 follow-up (b).
func TestCheckpointerCallsPostCheckpointFn(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096,
		PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var callCount int
	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		PostCheckpointFn: func() error {
			callCount++
			return nil
		},
	})
	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("runCheckpoint: %v", err)
	}
	if callCount != 1 {
		t.Errorf("PostCheckpointFn called %d times, want 1", callCount)
	}

	// A second checkpoint must also invoke the hook.
	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("second runCheckpoint: %v", err)
	}
	if callCount != 2 {
		t.Errorf("after second checkpoint: PostCheckpointFn called %d times total, want 2", callCount)
	}
}

// TestCheckpointerPostCheckpointFnErrorIsNonFatal verifies that a non-nil
// error from PostCheckpointFn does not propagate out of runCheckpoint.
func TestCheckpointerPostCheckpointFnErrorIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096,
		PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		PostCheckpointFn: func() error {
			return fmt.Errorf("simulated hook failure")
		},
	})
	// runCheckpoint must succeed even when the hook errors.
	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Errorf("runCheckpoint returned error %v, want nil (hook errors must be non-fatal)", err)
	}
}

// TestCheckpointerCallsTruncateSubtransFn verifies that runCheckpoint invokes
// TruncateSubtransFn once per successful checkpoint, alongside TruncateCLOGFn
// (M0122-0009).
func TestCheckpointerCallsTruncateSubtransFn(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096,
		PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var callCount int
	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		TruncateSubtransFn: func() error {
			callCount++
			return nil
		},
	})
	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("runCheckpoint: %v", err)
	}
	if callCount != 1 {
		t.Errorf("TruncateSubtransFn called %d times, want 1", callCount)
	}

	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("second runCheckpoint: %v", err)
	}
	if callCount != 2 {
		t.Errorf("after second checkpoint: TruncateSubtransFn called %d times total, want 2", callCount)
	}
}

// TestCheckpointerTruncateSubtransFnErrorIsNonFatal verifies that a non-nil
// error from TruncateSubtransFn does not propagate out of runCheckpoint —
// same best-effort treatment as PostCheckpointFn/TruncateCLOGFn.
func TestCheckpointerTruncateSubtransFnErrorIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096,
		PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		TruncateSubtransFn: func() error {
			return fmt.Errorf("simulated subtrans truncate failure")
		},
	})
	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Errorf("runCheckpoint returned error %v, want nil (hook errors must be non-fatal)", err)
	}
}

// TestCheckpointerCallsFlushCLOGFn verifies that runCheckpoint invokes
// FlushCLOGFn once per successful checkpoint, in the flush phase (i.e. it
// does not need the primary flusher to be dirty). M0117-0007 Part B
// continuation.
func TestCheckpointerCallsFlushCLOGFn(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096,
		PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var callCount int
	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		FlushCLOGFn: func() error {
			callCount++
			return nil
		},
	})
	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("runCheckpoint: %v", err)
	}
	if callCount != 1 {
		t.Errorf("FlushCLOGFn called %d times, want 1", callCount)
	}

	if err := cp.runCheckpoint(context.Background(), false, false); err != nil {
		t.Fatalf("second runCheckpoint: %v", err)
	}
	if callCount != 2 {
		t.Errorf("after second checkpoint: FlushCLOGFn called %d times total, want 2", callCount)
	}
}

// TestCheckpointerFlushCLOGFnErrorFailsCheckpoint verifies that, unlike the
// best-effort PostCheckpointFn/TruncateCLOGFn hooks, a FlushCLOGFn error
// propagates and fails the checkpoint — a checkpoint whose redo LSN
// advances past commits whose CLOG state failed to flush would leave crash
// recovery unable to reconstruct that state.
func TestCheckpointerFlushCLOGFnErrorFailsCheckpoint(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096,
		PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	cp := NewCheckpointer(&fakeFlusher{}, w, CheckpointerConfig{
		FlushCLOGFn: func() error {
			return fmt.Errorf("simulated clog flush failure")
		},
	})
	if err := cp.runCheckpoint(context.Background(), false, false); err == nil {
		t.Fatal("runCheckpoint returned nil, want a propagated FlushCLOGFn error")
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
