package xlog

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/storage"
)

// M0122-0013: a standby restartpoint (PG's RecoveryRestartPoint +
// CreateRestartPoint) — replay remembers the last checkpoint record, and a
// recovery flush adopts it into pg_control when pg_control's
// checkPointCopy.redo is older, so a restarted standby replays from there.

// TestDecodeCheckPointStructRoundTrips: the replay side reads back every
// field the encoder writes.
func TestDecodeCheckPointStructRoundTrips(t *testing.T) {
	want := CheckPointFields{RedoLSN0: 0x1234567, ThisTLI: 3, PrevTLI: 2, FullPageWrites: true, WalLevel: 1,
		NextXid: 901, NextOid: 20001, NextMulti: 7, NextMultiOffset: 9, OldestXid: 700, OldestXidDB: 5,
		OldestMulti: 4, OldestMultiDB: 6, OldestCommitTsXid: 0, NewestCommitTsXid: 0, OldestActiveXid: 899}
	got, tm, ok := decodeCheckPointStruct(encodeCheckPointStruct(want))
	if !ok || got != want {
		t.Fatalf("decode = %+v ok=%v, want %+v", got, ok, want)
	}
	if tm == 0 {
		t.Fatal("decoded time is zero")
	}
	if _, _, ok := decodeCheckPointStruct(make([]byte, 87)); ok {
		t.Fatal("a short struct decoded")
	}
}

// writeTestControl writes a pg_control with the given state and
// checkPointCopy.redo.
func writeTestControl(t *testing.T, dir string, state uint32, redo uint64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o755); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8192)
	le := binary.LittleEndian
	le.PutUint64(buf[0:], 0x1122334455667788)
	le.PutUint32(buf[8:], 1800)
	le.PutUint32(buf[16:], state)
	le.PutUint64(buf[40:], redo) // checkPointCopy.redo
	le.PutUint32(buf[48:], 1)    // ThisTimeLineID
	le.PutUint64(buf[32:], redo) // checkPoint
	crc := crc32.Checksum(buf[:292], crc32.MakeTable(crc32.Castagnoli))
	le.PutUint32(buf[292:], crc)
	if err := os.WriteFile(filepath.Join(dir, "global", "pg_control"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAdoptRestartPointAdvancesOnlyForward pins CreateRestartPoint's
// pg_control rules: a newer replayed checkpoint replaces checkPoint and
// checkPointCopy; an older or equal one leaves pg_control alone; and
// minRecoveryPoint moves only while the state is DB_IN_ARCHIVE_RECOVERY.
func TestAdoptRestartPointAdvancesOnlyForward(t *testing.T) {
	rp := ReplayedCheckpoint{StartLSN: 5001, EndLSN: 5121, Time: 77,
		Fields: CheckPointFields{RedoLSN0: 4000, ThisTLI: 1, PrevTLI: 1, NextXid: 900, NextOid: 17000}}

	dir := t.TempDir()
	writeTestControl(t, dir, control.DBStateInProduction, 100)
	if ok, err := adoptRestartPoint(dir, rp, true, false); err != nil || !ok {
		t.Fatalf("adopt newer checkpoint: ok=%v err=%v", ok, err)
	}
	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cd.CheckPoint != 5000 || cd.CheckPointCopyRedo != 4000 || cd.CheckPointCopyNextXid != 900 ||
		cd.CheckPointCopyNextOid != 17000 || cd.CheckPointCopyTime != 77 {
		t.Fatalf("pg_control after adopt: checkPoint=%d redo=%d nextXid=%d nextOid=%d time=%d",
			cd.CheckPoint, cd.CheckPointCopyRedo, cd.CheckPointCopyNextXid, cd.CheckPointCopyNextOid, cd.CheckPointCopyTime)
	}
	if cd.MinRecoveryPoint != 0 {
		t.Fatalf("minRecoveryPoint moved outside archive recovery: %d", cd.MinRecoveryPoint)
	}
	// The same checkpoint again: not newer, nothing written.
	if ok, err := adoptRestartPoint(dir, rp, true, false); err != nil || ok {
		t.Fatalf("re-adopt: ok=%v err=%v, want no update", ok, err)
	}
	older := rp
	older.Fields.RedoLSN0 = 3000
	if ok, _ := adoptRestartPoint(dir, older, true, false); ok {
		t.Fatal("an older checkpoint moved pg_control back")
	}

	arch := t.TempDir()
	writeTestControl(t, arch, control.DBStateInArchiveRecovery, 100)
	if ok, err := adoptRestartPoint(arch, rp, true, false); err != nil || !ok {
		t.Fatalf("adopt in archive recovery: ok=%v err=%v", ok, err)
	}
	if cd, _ := control.ReadControlFile(arch); cd.MinRecoveryPoint != 5121 || cd.MinRecoveryPointTLI != 1 {
		t.Fatalf("archive recovery minRecoveryPoint = %d/%d, want 5121/1", cd.MinRecoveryPoint, cd.MinRecoveryPointTLI)
	}
}

// TestRecoveryFlushAdoptsReplayedCheckpoint: replay of a checkpoint record
// is remembered by the StreamReplayer, and a recovery-mode checkpoint with
// that replayer as its source writes no WAL yet advances pg_control to it.
func TestRecoveryFlushAdoptsReplayedCheckpoint(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	writeTestControl(t, dir, control.DBStateInProduction, 16)
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 1 << 16, PageHeaders: true, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	iter, err := NewRecordIterator(w, walDir, 1<<16, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	sr := NewStreamReplayer(mgr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = sr.Run(ctx, iter) }()

	// Two "received" checkpoint records; the second is the restartpoint.
	var start2, end2 uint64
	for i, redo := range []uint64{4096, 8192} {
		rec, err := EncodeCheckpointPGFields(false, CheckPointFields{RedoLSN0: redo, ThisTLI: 1, NextXid: uint64(800 + i)})
		if err != nil {
			t.Fatal(err)
		}
		s, e, err := w.Append(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.FlushUpTo(e); err != nil {
			t.Fatal(err)
		}
		start2, end2 = s, e
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		if rp, ok := sr.LastCheckpoint(); ok && rp.Fields.RedoLSN0 == 8192 {
			if rp.StartLSN != start2 || rp.EndLSN != end2 || rp.Fields.NextXid != 801 {
				t.Fatalf("replayed checkpoint = %+v, want start %d end %d nextXid 801", rp, start2, end2)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replayer never recorded the second checkpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	before := w.WrittenLSN()
	flusher := &fakeFlusher{}
	cp := NewCheckpointer(flusher, w, CheckpointerConfig{DataDir: dir})
	cp.SetRecoveryMode(true)
	cp.SetRestartPointSource(sr.LastCheckpoint)
	if err := cp.CheckpointNow(); err != nil {
		t.Fatalf("recovery checkpoint: %v", err)
	}
	if got := w.WrittenLSN(); got != before {
		t.Fatalf("restartpoint advanced WAL from %d to %d", before, got)
	}
	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cd.CheckPointCopyRedo != 8192 || cd.CheckPoint != start2-1 || cd.CheckPointCopyNextXid != 801 {
		t.Fatalf("pg_control after restartpoint: redo=%d checkPoint=%d nextXid=%d; want 8192/%d/801",
			cd.CheckPointCopyRedo, cd.CheckPoint, cd.CheckPointCopyNextXid, start2-1)
	}
	// CreateRestartPoint updates the checkpoint distance estimate from
	// pg_control's prior redo (16) to the adopted one (8192) before removing
	// old WAL — even on the first restartpoint after a start (M0122-0013).
	if got := cp.CheckPointDistanceEstimate(); got != 8192-16 {
		t.Fatalf("checkpoint distance estimate after the restartpoint = %v, want %d", got, 8192-16)
	}
	// ... and moves RedoRecPtr / the checkpoint record to the restartpoint's,
	// the anchor the volume trigger measures max_wal_size from and the pair a
	// BASE_BACKUP from the standby starts at (M0122-0013).
	if got := cp.CheckpointRedoLSN(); got != 8192+1 {
		t.Fatalf("checkpoint redo after the restartpoint = %d, want %d (1-based)", got, 8192+1)
	}
	if got := cp.LastCheckpointRecordLSN(); got != start2 {
		t.Fatalf("checkpoint record after the restartpoint = %d, want %d", got, start2)
	}
	// CreateRestartPoint's skip arm: asked again with no newer replayed
	// checkpoint, it returns before CheckPointGuts — no flush (M0122-0013).
	flusher.mu.Lock()
	flushes := flusher.calls
	flusher.mu.Unlock()
	if flushes != 1 {
		t.Fatalf("adopting restartpoint flushed %d times, want 1", flushes)
	}
	if err := cp.CheckpointNow(); err != nil {
		t.Fatalf("repeat recovery checkpoint: %v", err)
	}
	flusher.mu.Lock()
	flushes = flusher.calls
	flusher.mu.Unlock()
	if flushes != 1 {
		t.Fatalf("restartpoint with nothing newer to adopt flushed (%d flushes, want 1)", flushes)
	}
	// Without a source a recovery flush leaves pg_control alone (e646499b0).
	cp.SetRestartPointSource(nil)
	writeTestControl(t, dir, control.DBStateInProduction, 16)
	if err := cp.CheckpointNow(); err != nil {
		t.Fatal(err)
	}
	if cd, _ := control.ReadControlFile(dir); cd.CheckPointCopyRedo != 16 {
		t.Fatalf("source-less recovery flush moved redo to %d", cd.CheckPointCopyRedo)
	}
	// ... skips the flush, as there is no replayed checkpoint at all ...
	flusher.mu.Lock()
	flushes = flusher.calls
	flusher.mu.Unlock()
	if flushes != 1 {
		t.Fatalf("source-less recovery flush flushed (%d flushes, want 1)", flushes)
	}
	// ... and, adopting nothing, leaves the estimate where it was.
	if got := cp.CheckPointDistanceEstimate(); got != 8192-16 {
		t.Fatalf("source-less recovery flush moved the distance estimate to %v", got)
	}
}

// TestShutdownRestartPointStampsShutdownedInRecovery pins CreateRestartPoint's
// shutdown arms (xlog.c:7685-7692, 7786-7787): a shutdown restartpoint that
// adopts a newer checkpoint in DB_IN_ARCHIVE_RECOVERY leaves
// DB_SHUTDOWNED_IN_RECOVERY, and one with nothing newer to adopt stamps it
// unconditionally; a non-shutdown flush with nothing to adopt writes nothing.
func TestShutdownRestartPointStampsShutdownedInRecovery(t *testing.T) {
	rp := ReplayedCheckpoint{StartLSN: 5001, EndLSN: 5121,
		Fields: CheckPointFields{RedoLSN0: 4000, ThisTLI: 1, PrevTLI: 1}}

	adopt := t.TempDir()
	writeTestControl(t, adopt, control.DBStateInArchiveRecovery, 100)
	if ok, err := adoptRestartPoint(adopt, rp, true, true); err != nil || !ok {
		t.Fatalf("shutdown adopt: ok=%v err=%v", ok, err)
	}
	if cd, _ := control.ReadControlFile(adopt); cd.State != control.DBStateShutdownedInRecovery || cd.CheckPointCopyRedo != 4000 {
		t.Fatalf("after shutdown adopt: state=%d redo=%d", cd.State, cd.CheckPointCopyRedo)
	}

	skip := t.TempDir()
	writeTestControl(t, skip, control.DBStateInArchiveRecovery, 9000)
	if ok, err := adoptRestartPoint(skip, rp, true, true); err != nil || ok {
		t.Fatalf("shutdown skip: ok=%v err=%v", ok, err)
	}
	if cd, _ := control.ReadControlFile(skip); cd.State != control.DBStateShutdownedInRecovery || cd.CheckPointCopyRedo != 9000 {
		t.Fatalf("after shutdown skip: state=%d redo=%d", cd.State, cd.CheckPointCopyRedo)
	}

	none := t.TempDir()
	writeTestControl(t, none, control.DBStateInArchiveRecovery, 100)
	if _, err := adoptRestartPoint(none, ReplayedCheckpoint{}, false, false); err != nil {
		t.Fatal(err)
	}
	if cd, _ := control.ReadControlFile(none); cd.State != control.DBStateInArchiveRecovery {
		t.Fatalf("non-shutdown flush with nothing to adopt changed state to %d", cd.State)
	}
}
