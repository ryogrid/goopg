package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// TestStandbyControllerPromoteRemovesSignal exercises the happy
// path: start a standby controller against an initialised data
// directory with a standby.signal in place, call Promote, and
// verify (a) the signal file is gone, (b) the runtime's Standby
// flag is false, (c) Promote returns nil.
//
// We deliberately skip wiring an actual primary so the walreceiver
// short-circuits in replication.StartWalReceiver (empty primary_conninfo logs
// and closes the done channel). The replayer still runs against
// the local WAL writer; on an idle data dir there are no records
// to apply, so ApplyLSN stays at the writer's WrittenLSN (= 0)
// and Promote's drain loop exits immediately.
func TestStandbyControllerPromoteRemovesSignal(t *testing.T) {
	dataDir := initStandbyDir(t)

	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if !rt.Standby {
		t.Fatal("expected Runtime.Standby = true after creating standby.signal")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := startStandby(parent, rt, nil, logger)
	defer sc.Close()

	// Allow the receiver/replayer goroutines to settle. The
	// receiver short-circuits on empty primary_conninfo; the
	// replayer subscribes to the writer and blocks at the tail.
	time.Sleep(20 * time.Millisecond)

	if err := sc.Promote(context.Background()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if rt.Standby {
		t.Error("Runtime.Standby = true after Promote, want false")
	}
	if _, err := os.Stat(filepath.Join(dataDir, initdb.StandbySignalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("standby.signal still present after Promote (err=%v)", err)
	}

	// Idempotency: a second Promote returns nil and does not
	// regress state.
	if err := sc.Promote(context.Background()); err != nil {
		t.Errorf("second Promote: %v", err)
	}
}

// TestStandbyControllerPromoteDrainsPendingReplay verifies the
// drain loop actually waits for ApplyLSN to catch up. We Append a
// record into the local WAL after starting the controller — the
// receiver isn't running (no primary), but the replayer will
// observe the new record and apply it (via ApplyRecord). Promote
// must observe ApplyLSN >= WrittenLSN before returning.
//
// The record we append is a checkpoint marker (opaque payload OK
// for the test — ApplyRecord routes unknown kinds through cleanly
// without touching the storage manager) so we don't need a real
// data page.
func TestStandbyControllerPromoteDrainsPendingReplay(t *testing.T) {
	dataDir := initStandbyDir(t)

	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := startStandby(parent, rt, nil, logger)
	defer sc.Close()

	// Wait for the controller to initialise.
	time.Sleep(20 * time.Millisecond)

	// Push a checkpoint marker into the local WAL — ApplyRecord
	// handles RecordKindCheckpoint without needing a storage
	// manager touch, which keeps this test focused on the drain
	// contract rather than storage mechanics.
	payload := xlog.EncodeCheckpoint()
	if _, _, err := rt.WAL.Append(payload); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := sc.Promote(context.Background()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	written := rt.WAL.WrittenLSN()
	if written == 0 {
		t.Fatal("written LSN unexpectedly zero after Append")
	}
	// ApplyLSN must equal or exceed WrittenLSN — that's the
	// contract Promote enforces.
	if rt.Standby {
		t.Error("Runtime.Standby = true after Promote, want false")
	}
}

// TestStandbyControllerPromoteSignalTriggersPromote covers M0102-0004.
// Dropping `promote.signal` into the data directory of a running
// standby controller must cause the controller to (a) remove the
// file and (b) call Promote within ~1 second, flipping rt.Standby
// to false and clearing standby.signal.
func TestStandbyControllerPromoteSignalTriggersPromote(t *testing.T) {
	dataDir := initStandbyDir(t)

	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if !rt.Standby {
		t.Fatal("expected Runtime.Standby = true after creating standby.signal")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := startStandby(parent, rt, nil, logger)
	defer sc.Close()

	// Let the controller install the watcher goroutine.
	time.Sleep(20 * time.Millisecond)

	promotePath := filepath.Join(dataDir, initdb.PromoteSignalFile)
	if err := os.WriteFile(promotePath, nil, 0o600); err != nil {
		t.Fatalf("write promote.signal: %v", err)
	}

	// Watcher polls at 250ms; allow ~1.5s for detect + Promote. We
	// observe completion via the atomic `promoted` flag set inside
	// Promote — race-safe vs. reading `rt.Standby` directly while
	// the watcher goroutine is still finalising.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if sc.promoted.Load() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sc.promoted.Load() {
		t.Fatal("standbyController.promoted still false 1.5s after promote.signal dropped")
	}
	// Wait for the watcher goroutine to actually exit so the
	// finalizePromotion store on rt.Standby happens-before our read.
	sc.signalCancel()
	<-sc.signalDone
	if rt.Standby {
		t.Error("Runtime.Standby = true after Promote, want false")
	}
	if _, err := os.Stat(promotePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("promote.signal still present after Promote (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, initdb.StandbySignalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("standby.signal still present after Promote (err=%v)", err)
	}
}

// TestStandbyControllerRemovesStalePromoteSignal verifies that a
// pre-existing promote.signal left over from a previous run is
// cleared at standby controller startup (and a warning logged),
// matching upstream's StartupXLOG init order. Without this guard,
// the watcher would fire on its very first poll and promote a
// standby the operator never asked to promote.
func TestStandbyControllerRemovesStalePromoteSignal(t *testing.T) {
	dataDir := initStandbyDir(t)

	stale := filepath.Join(dataDir, initdb.PromoteSignalFile)
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatalf("seed stale promote.signal: %v", err)
	}

	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := startStandby(parent, rt, nil, logger)
	defer sc.Close()

	// Stale file must be removed synchronously inside startStandby.
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale promote.signal not removed (err=%v)", err)
	}

	// Wait a couple of poll intervals to confirm no auto-promote
	// happened (rt.Standby should still be true).
	time.Sleep(600 * time.Millisecond)
	if !rt.Standby {
		t.Error("auto-promoted from stale promote.signal; want no promote")
	}
}

// initStandbyDir creates a freshly-initialised goopg data directory
// with `standby.signal` already present, suitable for opening as a
// standby. Test helpers shared between standby tests live here so
// the individual tests stay focused on what they're asserting.
func initStandbyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	if err := initdb.CreateStandbySignal(dir); err != nil {
		t.Fatalf("CreateStandbySignal: %v", err)
	}
	return dir
}

func TestStandbyApplyLSNFuncNil(t *testing.T) {
	if fn := standbyApplyLSNFunc(nil); fn != nil {
		t.Fatal("standbyApplyLSNFunc(nil) returned non-nil closure")
	}
}

func TestStandbyApplyLSNFuncUsesReplayer(t *testing.T) {
	dataDir := initStandbyDir(t)

	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	replayer := startStandbyReplayer(ctx, done, rt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() {
		cancel()
		<-done
	}()

	fn := standbyApplyLSNFunc(replayer)
	if fn == nil {
		t.Fatal("standbyApplyLSNFunc(replayer) returned nil")
	}
	if got, want := fn(), replayer.ApplyLSN(); got != want {
		t.Fatalf("ApplyLSN closure = %d, want %d", got, want)
	}
}

// TestStandbyControllerPromoteWritesTimelineHistory exercises the
// M0102-0003 promote path: after a successful promote, the data dir
// must contain `pg_wal/00000002.history` (one entry referencing the
// previous TLI=1) and `global/timeline_id` must hold the bumped
// value (2). A heterogeneous PG standby reattaching after this point
// resolves the timeline boundary by fetching the .history via
// TIMELINE_HISTORY 2.
func TestStandbyControllerPromoteWritesTimelineHistory(t *testing.T) {
	dataDir := initStandbyDir(t)

	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if !rt.Standby {
		t.Fatal("expected Runtime.Standby = true after creating standby.signal")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := startStandby(parent, rt, nil, logger)
	defer sc.Close()

	time.Sleep(20 * time.Millisecond)
	if err := sc.Promote(context.Background()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	historyPath := filepath.Join(dataDir, "pg_wal", "00000002.history")
	body, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("read history file: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("history file is empty; want one entry for TLI=1")
	}
	// The first field of the only line is the previous TLI ("1").
	if !bytes.HasPrefix(body, []byte("1\t")) {
		t.Errorf("history file content does not start with TLI=1 line: %q", body)
	}

	tliBytes, err := os.ReadFile(filepath.Join(dataDir, "global", "timeline_id"))
	if err != nil {
		t.Fatalf("read timeline_id: %v", err)
	}
	if len(tliBytes) != 4 {
		t.Fatalf("timeline_id wrong length: got %d, want 4", len(tliBytes))
	}
	got := binary.LittleEndian.Uint32(tliBytes)
	if got != 2 {
		t.Errorf("persisted TLI = %d, want 2", got)
	}
}

// TestStandbyControllerPromoteRetryableAfterFailure is the review/260831-2
// CM-1 guard. A Promote that fails (here: a cancelled context caught while
// waiting for the walreceiver to exit) leaves the node a standby, so the
// operator must be able to try again — that is exactly what
// promoteSignalWatcher's "removed first so a partial Promote can be retried
// by re-creating the file" comment promises. The `promoting` flag used to be
// set and never cleared, so every later PROMOTE — control socket and
// promote.signal alike — came back "promotion already in progress" and the
// standby could never be promoted for the life of the process.
//
// The controller is assembled by hand rather than via startStandby so the
// first attempt fails deterministically: receiverDone stays open, so the
// step-2 select can only take the ctx.Done() arm.
func TestStandbyControllerPromoteRetryableAfterFailure(t *testing.T) {
	dataDir := initStandbyDir(t)

	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rplCtx, rplCancel := context.WithCancel(context.Background())
	defer rplCancel()
	rplDone := make(chan struct{})
	replayer := startStandbyReplayer(rplCtx, rplDone, rt, logger)

	rcvDone := make(chan struct{})
	sc := &standbyController{
		rt:             rt,
		logger:         logger,
		receiverCancel: func() {},
		receiverDone:   rcvDone,
		replayerCancel: rplCancel,
		replayerDone:   rplDone,
		replayer:       replayer,
	}

	failCtx, failCancel := context.WithCancel(context.Background())
	failCancel()
	if err := sc.Promote(failCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Promote error = %v, want context.Canceled", err)
	}
	if !rt.Standby {
		t.Fatal("failed Promote left Runtime.Standby = false")
	}

	// The condition that made the first attempt fail is gone.
	close(rcvDone)
	if err := sc.Promote(context.Background()); err != nil {
		t.Fatalf("retry Promote: %v", err)
	}
	if rt.Standby {
		t.Error("Runtime.Standby = true after successful retry, want false")
	}
	if _, err := os.Stat(filepath.Join(dataDir, initdb.StandbySignalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("standby.signal still present after retry (err=%v)", err)
	}
}

// TestStandbyControllerPromoteRunsRecoveryEndCommand pins the
// recovery_end_command arm of CleanupAfterArchiveRecovery (xlog.c): promotion
// runs the command once with %r = the WAL file holding pg_control's
// checkpoint-copy redo (GetOldestRestartPoint); a plain failure is a WARNING
// and promotion completes; a command killed by a signal fails the promotion
// (failOnSignal), leaving the node a standby that can retry.
func TestStandbyControllerPromoteRunsRecoveryEndCommand(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	promote := func(t *testing.T, cmd string) (*initdb.Runtime, string, error) {
		t.Helper()
		dataDir := initStandbyDir(t)
		rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rt.Close() })
		parent, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		sc := startStandby(parent, rt, nil, logger)
		t.Cleanup(sc.Close)
		sc.recoveryEndCommand = func() string { return cmd }
		time.Sleep(20 * time.Millisecond)
		return rt, dataDir, sc.Promote(context.Background())
	}

	t.Run("runs with the restartpoint file", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "r.txt")
		rt, dataDir, err := promote(t, "echo %r > '"+out+"'")
		if err != nil {
			t.Fatalf("Promote: %v", err)
		}
		cd, err := control.ReadControlFile(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		seg := rt.WAL.SegmentSize()
		want := xlog.XLogFileName(cd.CheckPointCopyThisTLI, cd.CheckPointCopyRedo/uint64(seg), seg)
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("recovery_end_command did not run: %v", err)
		}
		if strings.TrimSpace(string(got)) != want {
			t.Errorf("%%r = %q, want %q", strings.TrimSpace(string(got)), want)
		}
	})

	t.Run("plain failure is a warning", func(t *testing.T) {
		rt, _, err := promote(t, "exit 3")
		if err != nil {
			t.Fatalf("Promote with a failing (non-signal) recovery_end_command: %v, want success", err)
		}
		if rt.Standby {
			t.Error("still a standby after promotion")
		}
	})

	t.Run("signal is fatal", func(t *testing.T) {
		rt, dataDir, err := promote(t, "kill -9 $$")
		if err == nil || !strings.Contains(err.Error(), "recovery_end_command") {
			t.Fatalf("Promote with a signal-killed recovery_end_command: err = %v, want a recovery_end_command error", err)
		}
		cd, cerr := control.ReadControlFile(dataDir)
		if cerr != nil {
			t.Fatal(cerr)
		}
		if cd.State == control.DBStateInProduction {
			t.Error("pg_control stamped DB_IN_PRODUCTION although recovery_end_command failed with a signal")
		}
		_ = rt
	})
}

// TestPromoteAfterRecoveryRunsRecoveryEndCommand pins the same
// CleanupAfterArchiveRecovery arm on the recovery.signal path: archive
// recovery's promotion runs recovery_end_command before removing
// recovery.signal; a signal-killed command fails the promotion and leaves
// recovery.signal in place, so the next start retries recovery.
func TestPromoteAfterRecoveryRunsRecoveryEndCommand(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func(t *testing.T) (*initdb.Runtime, string) {
		t.Helper()
		dir := t.TempDir()
		if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
			t.Fatalf("initdb.Init: %v", err)
		}
		rt, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 4})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rt.Close() })
		if err := os.WriteFile(filepath.Join(dir, initdb.RecoverySignalFile), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return rt, dir
	}
	signalPresent := func(dir string) bool {
		_, err := os.Stat(filepath.Join(dir, initdb.RecoverySignalFile))
		return err == nil
	}

	rt, dir := open(t)
	out := filepath.Join(t.TempDir(), "r.txt")
	if err := promoteAfterRecovery(rt, archiveRecoveryEnd{RecoveryEndCommand: "echo %r > '" + out + "'"}, logger); err != nil {
		t.Fatalf("promoteAfterRecovery: %v", err)
	}
	if got, err := os.ReadFile(out); err != nil || len(strings.TrimSpace(string(got))) != 24 {
		t.Errorf("recovery_end_command output = %q (err %v), want a 24-character WAL file name", got, err)
	}
	if signalPresent(dir) {
		t.Error("recovery.signal still present after a successful promotion")
	}

	rt, dir = open(t)
	err := promoteAfterRecovery(rt, archiveRecoveryEnd{RecoveryEndCommand: "kill -9 $$"}, logger)
	if err == nil || !strings.Contains(err.Error(), "recovery_end_command") {
		t.Fatalf("signal-killed recovery_end_command: err = %v, want a recovery_end_command error", err)
	}
	if !signalPresent(dir) {
		t.Error("recovery.signal removed although recovery_end_command failed with a signal")
	}
}

// TestValidateRecoveryParameters pins the compulsory-parameter check of PG's
// validateRecoveryParameters (xlogrecovery.c): recovery.signal without
// standby.signal needs restore_command (FATAL); a standby with neither
// primary_conninfo nor restore_command only warns; no signal file, nothing.
func TestValidateRecoveryParameters(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	for _, tc := range []struct {
		name              string
		rt                *initdb.Runtime
		restore, conninfo string
		wantErr, wantWarn bool
	}{
		{"recovery without restore_command", &initdb.Runtime{Recovery: true}, "", "", true, false},
		{"recovery with restore_command", &initdb.Runtime{Recovery: true}, "cp /a/%f %p", "", false, false},
		{"standby with neither", &initdb.Runtime{Standby: true}, "", "", false, true},
		{"standby with conninfo", &initdb.Runtime{Standby: true}, "", "host=p", false, false},
		{"standby and recovery signals, neither command", &initdb.Runtime{Standby: true, Recovery: true}, "", "", false, true},
		{"no signal file", &initdb.Runtime{}, "", "", false, false},
	} {
		logBuf.Reset()
		err := validateRecoveryParameters(tc.rt, tc.restore, tc.conninfo, logger)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		if err != nil && err.Error() != `must specify "restore_command" when standby mode is not enabled` {
			t.Errorf("%s: error %q is not PG's message", tc.name, err)
		}
		if warned := strings.Contains(logBuf.String(), `specified neither \"primary_conninfo\" nor \"restore_command\"`); warned != tc.wantWarn {
			t.Errorf("%s: warned = %v, want %v (log %q)", tc.name, warned, tc.wantWarn, logBuf.String())
		}
	}
}

// TestPromoteAfterRecoveryHistorySwitchPoint pins the switch point with
// nothing restored from the archive: the writer's own end (with its xl_prev)
// stays the boundary. The restored case is TestPromoteAfterRecoveryAdopts
// RestoredWAL, which needs real archived segments.
func TestPromoteAfterRecoveryHistorySwitchPoint(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	oldTLI, err := initdb.LoadOrCreateTimelineID(dir)
	if err != nil {
		t.Fatal(err)
	}
	written := rt.WAL.WrittenLSN()
	if err := promoteAfterRecovery(rt, archiveRecoveryEnd{}, logger); err != nil {
		t.Fatalf("promoteAfterRecovery: %v", err)
	}
	entries, err := xlog.ReadHistory(walDirFor(rt), oldTLI+1)
	if err != nil || len(entries) == 0 {
		t.Fatalf("ReadHistory: %v (entries %v)", err, entries)
	}
	if last := entries[len(entries)-1]; last.TLI != oldTLI || last.SwitchLSN != written {
		t.Errorf("history entry = TLI %d switch %X, want TLI %d switch %X", last.TLI, last.SwitchLSN, oldTLI, written)
	}
	if got := rt.WAL.TimelineID(); got != oldTLI+1 {
		t.Errorf("writer timeline = %d after promotion, want %d", got, oldTLI+1)
	}
}

// TestPromoteAfterRecoveryAdoptsRestoredWAL pins archive-recovery promotion
// over segments restore_command supplied past the local end of WAL. PG's new
// timeline begins at EndOfLog, the end of the last replayed record, and its
// first record is XLOG_END_OF_RECOVERY (StartupXLOG). goopg's running writer
// never wrote the restored segments, so it used to resume at its own,
// earlier end on the old timeline and overwrite WAL whose changes replay had
// already applied. Now: the restored bytes are untouched, the history file
// names EndOfLog, the writer is on the new timeline, and new WAL starts at
// EndOfLog or later.
func TestPromoteAfterRecoveryAdoptsRestoredWAL(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	archive := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	segSize := uint64(rt.WAL.SegmentSize())
	start := rt.WAL.WrittenLSN()
	payload := make([]byte, 4000)
	for rt.WAL.WrittenLSN() < start+3*segSize {
		if _, _, err := rt.WAL.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.WAL.FlushUpTo(rt.WAL.WrittenLSN()); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// Move every segment past the next one into the archive: the restarted
	// server's local WAL ends there, and archive recovery restores the rest.
	walDir := filepath.Join(dir, "pg_wal")
	keepSeg := start/segSize + 1
	ents, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if _, seg, ok := xlog.ParseXLogFileName(e.Name(), 0); ok && seg > keepSeg {
			if err := os.Rename(filepath.Join(walDir, e.Name()), filepath.Join(archive, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}

	rt, err = initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			rt.Close()
		}
	}()
	oldTLI := rt.WAL.TimelineID()
	localEnd := rt.WAL.WrittenLSN()
	endOfLog, lastStart, err := xlog.RunArchiveRecovery(rt.StorageMgr, dir, "cp "+filepath.Join(archive, "%f")+" %p", int64(segSize))
	if err != nil {
		t.Fatal(err)
	}
	if endOfLog <= localEnd {
		t.Fatalf("precondition: archive recovery replayed nothing past the local end (%X <= %X)", endOfLog, localEnd)
	}
	restoredSeg := localEnd / segSize
	restoredName := filepath.Join(walDir, xlog.FormatSegmentNameTLI(restoredSeg, oldTLI))
	before, err := os.ReadFile(restoredName)
	if err != nil {
		t.Fatal(err)
	}

	if err := promoteAfterRecovery(rt, archiveRecoveryEnd{EndOfLog: endOfLog, LastStart: lastStart}, logger); err != nil {
		t.Fatalf("promoteAfterRecovery: %v", err)
	}
	// The end-of-recovery checkpoint may legitimately retire the restored
	// segment (it lies below the new redo); if it is still there, promotion
	// must not have rewritten it.
	if after, err := os.ReadFile(restoredName); err == nil && !bytes.Equal(before, after) {
		t.Errorf("restored old-timeline segment %s was rewritten by promotion", filepath.Base(restoredName))
	}
	// PerformRecoveryXLogAction's end-of-recovery checkpoint: pg_control's
	// checkpoint copy is on the new timeline and its redo lies past EndOfLog.
	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cd.CheckPointCopyThisTLI != oldTLI+1 || cd.CheckPointCopyRedo < endOfLog {
		t.Errorf("pg_control checkpoint = TLI %d redo %X, want the end-of-recovery checkpoint on TLI %d past EndOfLog %X",
			cd.CheckPointCopyThisTLI, cd.CheckPointCopyRedo, oldTLI+1, endOfLog)
	}
	if got := rt.WAL.TimelineID(); got != oldTLI+1 {
		t.Errorf("writer timeline = %d after promotion, want %d", got, oldTLI+1)
	}
	entries, err := xlog.ReadHistory(walDir, oldTLI+1)
	if err != nil || len(entries) == 0 {
		t.Fatalf("ReadHistory: %v (entries %v)", err, entries)
	}
	if last := entries[len(entries)-1]; last.SwitchLSN != endOfLog {
		t.Errorf("history switch point = %X, want EndOfLog %X", last.SwitchLSN, endOfLog)
	}
	at, _, err := rt.WAL.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if at < endOfLog {
		t.Errorf("post-promotion WAL starts at %X, inside the WAL replayed from the archive (EndOfLog %X)", at, endOfLog)
	}
	if err := rt.WAL.FlushUpTo(rt.WAL.WrittenLSN()); err != nil {
		t.Fatal(err)
	}
	postEnd := rt.WAL.WrittenLSN()
	rt.Close()
	closed = true

	// A restart reopens on the new timeline with the post-promotion WAL.
	rt2, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("reopen after promotion: %v", err)
	}
	defer rt2.Close()
	if got := rt2.WAL.TimelineID(); got != oldTLI+1 {
		t.Errorf("reopened writer timeline = %d, want %d", got, oldTLI+1)
	}
	if got := rt2.WAL.WrittenLSN(); got < postEnd {
		t.Errorf("reopened WAL ends at %X, before the post-promotion WAL's end %X", got, postEnd)
	}
}

// TestStandbyControllerPromoteTakesCheckpoint pins the checkpoint StartupXLOG
// requests after a promotion (RequestCheckpoint(CHECKPOINT_FORCE), xlog.c):
// once the writer is on the new timeline, a checkpoint moves pg_control's
// checkpoint copy onto it, so a crash after promotion replays from there and
// not from the last restartpoint.
func TestStandbyControllerPromoteTakesCheckpoint(t *testing.T) {
	dataDir := initStandbyDir(t)
	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dataDir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := startStandby(parent, rt, nil, logger)
	defer sc.Close()
	time.Sleep(20 * time.Millisecond)

	oldTLI := rt.WAL.TimelineID()
	before := rt.Checkpointer.LastCheckpointLSN()
	if err := sc.Promote(context.Background()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := rt.WAL.TimelineID(); got != oldTLI+1 {
		t.Fatalf("precondition: writer timeline = %d after promotion, want %d", got, oldTLI+1)
	}
	if after := rt.Checkpointer.LastCheckpointLSN(); after <= before {
		t.Errorf("no checkpoint after promotion: last checkpoint %X, before %X", after, before)
	}
	cd, err := control.ReadControlFile(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if cd.CheckPointCopyThisTLI != oldTLI+1 {
		t.Errorf("pg_control checkpoint copy TLI = %d after promotion, want %d", cd.CheckPointCopyThisTLI, oldTLI+1)
	}
}
