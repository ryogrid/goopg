package wal

import (
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
