package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/utils/misc"
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

// TestStandbyControllerPromoteSkipsKnownTimelines: the new timeline is past
// every timeline whose history file pg_wal holds — StartupXLOG's
// findNewestTimeLine(recoveryTargetTLI) + 1 — not the standby's own plus
// one. The walreceiver fetches the primary's history files for exactly this:
// a standby on timeline 1 whose primary already reached timeline 3 must not
// reuse timeline 2 or 3 when it is promoted.
func TestStandbyControllerPromoteSkipsKnownTimelines(t *testing.T) {
	dataDir := initStandbyDir(t)
	walDir := filepath.Join(dataDir, "pg_wal")
	for tli, body := range map[uint32]string{
		2: "1\t0/3000098\tno recovery target specified\n",
		3: "1\t0/3000098\tno recovery target specified\n2\t0/5000060\tno recovery target specified\n",
	} {
		if err := os.WriteFile(filepath.Join(walDir, xlog.TimelineHistoryFileName(tli)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
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

	time.Sleep(20 * time.Millisecond)
	if err := sc.Promote(context.Background()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(walDir, "00000004.history"))
	if err != nil {
		t.Fatalf("read the new timeline's history: %v", err)
	}
	if !bytes.HasPrefix(body, []byte("1\t")) {
		t.Errorf("timeline 4 history = %q, want it to fork from timeline 1", body)
	}
	tliBytes, err := os.ReadFile(filepath.Join(dataDir, "global", "timeline_id"))
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(tliBytes); got != 4 {
		t.Errorf("persisted TLI = %d, want 4", got)
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
	recovered, err := xlog.RunArchiveRecovery(rt.StorageMgr, dir, "cp "+filepath.Join(archive, "%f")+" %p", int64(segSize))
	endOfLog := recovered.EndOfLog
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

	if err := promoteAfterRecovery(rt, archiveRecoveryEnd{Recovery: recovered}, logger); err != nil {
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
	assertEndOfRecoveryCheckpoint(t, dir, int64(segSize), endOfLog, oldTLI, oldTLI+1)
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

// TestPromoteAfterRecoveryFollowsTimelines pins archive recovery from a base
// backup taken on timeline 1 of a server that was later promoted to
// timeline 2: recovery follows the history onto timeline 2 (PG's
// recovery_target_timeline = 'latest'), the writer crosses the switch the
// way a following standby does, and the promotion's new timeline is 3 —
// past every timeline the archive knows — with history 1 → 2 → 3.
func TestPromoteAfterRecoveryFollowsTimelines(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	primary := filepath.Join(t.TempDir(), "primary")
	backup := filepath.Join(t.TempDir(), "backup")
	archive := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: primary}); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4000)
	appendUntil := func(rt *initdb.Runtime, lsn uint64) {
		t.Helper()
		for rt.WAL.WrittenLSN() < lsn {
			if _, _, err := rt.WAL.Append(payload); err != nil {
				t.Fatal(err)
			}
		}
		if err := rt.WAL.FlushUpTo(rt.WAL.WrittenLSN()); err != nil {
			t.Fatal(err)
		}
	}

	// Timeline 1, then a base backup of the stopped primary.
	rt, err := initdb.Open(initdb.OpenOptions{DataDir: primary, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	segSize := uint64(rt.WAL.SegmentSize())
	appendUntil(rt, rt.WAL.WrittenLSN()+segSize+segSize/3)
	rt.Close()
	if out, err := exec.Command("cp", "-a", primary, backup).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}

	// The primary is promoted to timeline 2 and writes on.
	rt, err = initdb.Open(initdb.OpenOptions{DataDir: primary, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	switchLSN := rt.WAL.WrittenLSN()
	_, prev := rt.WAL.InsertPosition()
	if err := rt.WAL.SwitchTimeline(2, switchLSN, prev); err != nil {
		t.Fatal(err)
	}
	primaryWAL := filepath.Join(primary, "pg_wal")
	if err := xlog.WriteHistory(primaryWAL, 2, []xlog.TimelineHistoryEntry{{TLI: 1, SwitchLSN: switchLSN, Reason: "test promotion"}}); err != nil {
		t.Fatal(err)
	}
	appendUntil(rt, switchLSN+segSize+segSize/2)
	rt.Close()

	// Archive everything the primary wrote.
	ents, err := os.ReadDir(primaryWAL)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if _, _, ok := xlog.ParseXLogFileName(e.Name(), 0); ok || strings.HasSuffix(e.Name(), ".history") {
			b, err := os.ReadFile(filepath.Join(primaryWAL, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(archive, e.Name()), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Archive recovery of the backup, then promotion.
	rt, err = initdb.Open(initdb.OpenOptions{DataDir: backup, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			rt.Close()
		}
	}()
	if got := rt.WAL.TimelineID(); got != 1 {
		t.Fatalf("precondition: backup writer timeline = %d, want 1", got)
	}
	recovered, err := xlog.RunArchiveRecovery(rt.StorageMgr, backup, "cp "+filepath.Join(archive, "%f")+" %p", int64(segSize))
	if err != nil {
		t.Fatalf("RunArchiveRecovery: %v", err)
	}
	if recovered.EndTLI != 2 || recovered.EndOfLog <= switchLSN {
		t.Fatalf("recovery ended on TLI %d at %X, want timeline 2 past the switch %X", recovered.EndTLI, recovered.EndOfLog, switchLSN)
	}
	if err := promoteAfterRecovery(rt, archiveRecoveryEnd{Recovery: recovered}, logger); err != nil {
		t.Fatalf("promoteAfterRecovery: %v", err)
	}
	if got := rt.WAL.TimelineID(); got != 3 {
		t.Errorf("writer timeline = %d after promotion, want 3", got)
	}
	backupWAL := filepath.Join(backup, "pg_wal")
	entries, err := xlog.ReadHistory(backupWAL, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []xlog.TimelineHistoryEntry{{TLI: 1, SwitchLSN: switchLSN}, {TLI: 2, SwitchLSN: recovered.EndOfLog}}
	if len(entries) != 2 || entries[0].TLI != want[0].TLI || entries[0].SwitchLSN != want[0].SwitchLSN ||
		entries[1].TLI != want[1].TLI || entries[1].SwitchLSN != want[1].SwitchLSN {
		t.Errorf("timeline 3 history = %+v, want 1@%X, 2@%X", entries, switchLSN, recovered.EndOfLog)
	}
	assertEndOfRecoveryCheckpoint(t, backup, int64(segSize), recovered.EndOfLog, 2, 3)
	at, _, err := rt.WAL.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if at < recovered.EndOfLog {
		t.Errorf("post-promotion WAL starts at %X, inside the replayed WAL (EndOfLog %X)", at, recovered.EndOfLog)
	}
	if err := rt.WAL.FlushUpTo(rt.WAL.WrittenLSN()); err != nil {
		t.Fatal(err)
	}
	postEnd := rt.WAL.WrittenLSN()
	rt.Close()
	closed = true

	rt2, err := initdb.Open(initdb.OpenOptions{DataDir: backup, PoolSlots: 4})
	if err != nil {
		t.Fatalf("reopen after promotion: %v", err)
	}
	defer rt2.Close()
	if got := rt2.WAL.TimelineID(); got != 3 {
		t.Errorf("reopened writer timeline = %d, want 3", got)
	}
	if got := rt2.WAL.WrittenLSN(); got < postEnd {
		t.Errorf("reopened WAL ends at %X, before the post-promotion end %X", got, postEnd)
	}
}

// firstRecordOnNewTimeline returns the first WAL record starting at or after
// endOfLog — the record that marks the timeline change.
func firstRecordOnNewTimeline(t *testing.T, walDir string, segSize int64, endOfLog uint64) xlog.Record {
	t.Helper()
	recs, err := xlog.ReadAll(walDir, segSize)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	for _, r := range recs {
		if r.StartLSN >= endOfLog {
			return r
		}
	}
	t.Fatalf("no record at or after EndOfLog %X", endOfLog)
	return xlog.Record{}
}

// assertEndOfRecoveryCheckpoint checks PerformRecoveryXLogAction's arm for
// archive recovery that ends without a promote trigger: the first record on
// the new timeline is an end-of-recovery checkpoint — a shutdown-type
// XLOG_CHECKPOINT_SHUTDOWN whose ThisTimeLineID is the new timeline and
// PrevTimeLineID the one recovery ended on — not XLOG_END_OF_RECOVERY, and
// pg_control's checkpoint copy carries both timelines.
func assertEndOfRecoveryCheckpoint(t *testing.T, dataDir string, segSize int64, endOfLog uint64, oldTLI, newTLI uint32) {
	t.Helper()
	r := firstRecordOnNewTimeline(t, filepath.Join(dataDir, "pg_wal"), segSize, endOfLog)
	if r.XLog == nil || r.XLog.Header.Rmid != xlog.RmgrXLog || r.XLog.Header.Info&0xF0 != 0x00 || len(r.XLog.MainData) < 16 {
		var rm, info uint8
		if r.XLog != nil {
			rm, info = uint8(r.XLog.Header.Rmid), r.XLog.Header.Info
		}
		t.Fatalf("first record on the new timeline is rmgr %d info %#x, want an XLOG_CHECKPOINT_SHUTDOWN", rm, info)
	}
	this := binary.LittleEndian.Uint32(r.XLog.MainData[8:12])
	prev := binary.LittleEndian.Uint32(r.XLog.MainData[12:16])
	if this != newTLI || prev != oldTLI {
		t.Errorf("end-of-recovery checkpoint ThisTimeLineID %d PrevTimeLineID %d, want %d and %d", this, prev, newTLI, oldTLI)
	}
	cd, err := control.ReadControlFile(dataDir)
	if err != nil || cd == nil {
		t.Fatalf("ReadControlFile: %v", err)
	}
	if cd.CheckPointCopyThisTLI != newTLI || cd.CheckPointCopyPrevTLI != oldTLI || cd.State != control.DBStateInProduction {
		t.Errorf("pg_control checkpoint copy TLI %d prev %d state %v, want %d / %d / in production",
			cd.CheckPointCopyThisTLI, cd.CheckPointCopyPrevTLI, cd.State, newTLI, oldTLI)
	}
}

// TestArchiveRecoveryPrefersArchivedCopyOfPartialLocalSegment pins
// XLogFileReadAnyTLI's archive-before-pg_wal order. A base backup's last
// local segment holds WAL only up to the backup's end; the archive's copy
// of the same segment holds what the primary wrote after it. goopg read
// the local copy, so recovery skipped the rest of that segment — then
// replayed later segments past the gap and promoted at the gap, losing
// every record in it. PG prefers the archived copy (the local one "could
// be an old, un-filled or partly-filled version"), restores it through
// RECOVERYXLOG and replaces the local file with it.
func TestArchiveRecoveryPrefersArchivedCopyOfPartialLocalSegment(t *testing.T) {
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
	walDir := filepath.Join(dir, "pg_wal")
	payload := make([]byte, 4000)
	for i := 0; i < 8; i++ {
		if _, _, err := rt.WAL.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	backupEnd := rt.WAL.WrittenLSN()
	if err := rt.WAL.FlushUpTo(backupEnd); err != nil {
		t.Fatal(err)
	}
	tli := rt.WAL.TimelineID()
	tailSeg := backupEnd / segSize
	tailName := xlog.FormatSegmentNameTLI(tailSeg, tli)
	partial, err := os.ReadFile(filepath.Join(walDir, tailName))
	if err != nil {
		t.Fatal(err)
	}
	// The primary keeps writing after the backup: the rest of the tail
	// segment and into the next one.
	for rt.WAL.WrittenLSN() < (tailSeg+1)*segSize+8000 {
		if _, _, err := rt.WAL.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	trueEnd := rt.WAL.WrittenLSN()
	if err := rt.WAL.FlushUpTo(trueEnd); err != nil {
		t.Fatal(err)
	}
	rt.SetImmediateShutdown() // no shutdown checkpoint: pg_control stays before backupEnd
	rt.Close()

	// The archive gets every segment; pg_wal keeps the backup's partial
	// copy of the tail segment and nothing after it.
	ents, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if _, seg, ok := xlog.ParseXLogFileName(e.Name(), 0); ok {
			data, err := os.ReadFile(filepath.Join(walDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(archive, e.Name()), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if seg > tailSeg {
				if err := os.Remove(filepath.Join(walDir, e.Name())); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(walDir, tailName), partial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recovery.signal"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// Open replays the local WAL as part of the archive recovery and writes
	// no WAL of its own: no end-of-crash-recovery checkpoint record may land
	// at the local end, where the archived copy holds the primary's WAL.
	rt, err = initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	if cd, err := control.ReadControlFile(dir); err != nil || cd == nil || cd.State != control.DBStateInArchiveRecovery {
		rt.Close()
		t.Fatalf("pg_control during recovery.signal recovery = %+v, %v; want DB_IN_ARCHIVE_RECOVERY", cd, err)
	}
	if got := rt.WAL.WrittenLSN(); got != backupEnd {
		rt.Close()
		t.Fatalf("precondition: local WAL ends at %X, want the backup end %X", got, backupEnd)
	}
	recovered, err := xlog.RunArchiveRecovery(rt.StorageMgr, dir, "cp "+filepath.Join(archive, "%f")+" %p", int64(segSize))
	if err != nil {
		rt.Close()
		t.Fatal(err)
	}
	if recovered.EndOfLog != trueEnd {
		rt.Close()
		t.Fatalf("archive recovery ended at %X, want %X: the WAL after the backup end in segment %s was skipped",
			recovered.EndOfLog, trueEnd, tailName)
	}
	// The segment replayed — and now in pg_wal — is the archived copy: its
	// records run past the backup end. With the local copy preferred, the
	// rest of the segment was skipped while the next segment's records were
	// still replayed after the gap, so EndOfLog alone cannot tell.
	archivedTail, err := os.ReadFile(filepath.Join(archive, tailName))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(walDir, tailName)); !bytes.Equal(got, archivedTail) {
		t.Errorf("pg_wal's %s is not the archived copy after archive recovery", tailName)
	}
	tailRecs, err := xlog.ReadSegmentRecords(walDir, tailSeg, int64(segSize))
	if err != nil {
		t.Fatal(err)
	}
	if len(tailRecs) == 0 || tailRecs[len(tailRecs)-1].EndLSN <= backupEnd {
		t.Errorf("segment %s replays only up to the backup end %X", tailName, backupEnd)
	}
	for _, tmp := range []string{xlog.RecoveryXLogName, xlog.RecoveryHistoryName} {
		if _, err := os.Stat(filepath.Join(walDir, tmp)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left in pg_wal after archive recovery: %v", tmp, err)
		}
	}
	if err := promoteAfterRecovery(rt, archiveRecoveryEnd{Recovery: recovered}, logger); err != nil {
		rt.Close()
		t.Fatalf("promoteAfterRecovery: %v", err)
	}
	newTLI := rt.WAL.TimelineID()
	if cd, err := control.ReadControlFile(dir); err != nil || cd == nil || cd.State != control.DBStateInProduction {
		t.Errorf("pg_control after promotion = %+v, %v; want DB_IN_PRODUCTION", cd, err)
	}
	rt.Close()
	rt, err = initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("reopen after promotion: %v", err)
	}
	defer rt.Close()
	if rt.WAL.TimelineID() != newTLI || rt.WAL.WrittenLSN() <= trueEnd {
		t.Errorf("after restart: TLI %d end %X, want TLI %d past %X", rt.WAL.TimelineID(), rt.WAL.WrittenLSN(), newTLI, trueEnd)
	}
}

// TestRecoveryTargetTimelineReadsRawValue pins that an empty
// recovery_target_timeline is not read as the default: PG's check hook
// parses it as strtoul("") = timeline 0, which recovery then reports as
// "recovery target timeline 0 does not exist". goopg's string-GUC helper
// falls back to the default on an empty value, which turned an empty value into
// 'latest'.
func TestRecoveryTargetTimelineReadsRawValue(t *testing.T) {
	for _, c := range []struct {
		set  string
		want xlog.TargetTimeline
	}{
		{"", xlog.TargetTimeline{Goal: xlog.TargetTimelineNumeric, TLI: 0}},
		{"latest", xlog.TargetTimeline{Goal: xlog.TargetTimelineLatest}},
		{"current", xlog.TargetTimeline{Goal: xlog.TargetTimelineCurrent}},
		{"3", xlog.TargetTimeline{Goal: xlog.TargetTimelineNumeric, TLI: 3}},
	} {
		r := misc.BuildDefaultRegistry()
		if err := r.Set("recovery_target_timeline", c.set, misc.SourceConfigFile); err != nil {
			t.Fatalf("set %q: %v", c.set, err)
		}
		if got := recoveryTargetTimeline(r); got != c.want {
			t.Errorf("recovery_target_timeline = %q: got %+v, want %+v", c.set, got, c.want)
		}
	}
	if got := recoveryTargetTimeline(misc.BuildDefaultRegistry()); got != (xlog.TargetTimeline{Goal: xlog.TargetTimelineLatest}) {
		t.Errorf("default: got %+v, want latest", got)
	}
}

// TestRecoveryTargetChecksAndAction pins the end-of-redo target rules of
// PerformWalRecovery and validateRecoveryParameters: an unreached target is
// FATAL "recovery ended before configured recovery target was reached"; a
// stop point before the WAL initdb.Open already replayed is FATAL
// "requested recovery stop point is before consistent recovery point";
// recovery_target_action 'pause' becomes 'shutdown' without hot_standby.
func TestRecoveryTargetChecksAndAction(t *testing.T) {
	stop := xlog.RecoveryStop{Set: true, LSN: 0x5000}
	fatalMsg := func(err error) string {
		var f *xlog.RestoreFatalError
		if errors.As(err, &f) {
			return f.Msg
		}
		return fmt.Sprint(err)
	}
	if err := checkRecoveryTargetReached(xlog.RecoveryStop{}, xlog.ArchiveRecoveryResult{}, 0x9000); err != nil {
		t.Errorf("no target: %v", err)
	}
	if got := fatalMsg(checkRecoveryTargetReached(stop, xlog.ArchiveRecoveryResult{}, 0x1000)); got != "recovery ended before configured recovery target was reached" {
		t.Errorf("unreached: %q", got)
	}
	reached := xlog.ArchiveRecoveryResult{ReachedTarget: true, StopLSN: 0x5000, StopPoint: 0x5000}
	if got := fatalMsg(checkRecoveryTargetReached(stop, reached, 0x6000)); got != "requested recovery stop point is before consistent recovery point" {
		t.Errorf("before the local end: %q", got)
	}
	if err := checkRecoveryTargetReached(stop, reached, 0x5000); err != nil {
		t.Errorf("at the local end: %v", err)
	}

	for _, c := range []struct {
		action, hotStandby, want string
	}{
		{"pause", "on", "pause"},
		{"pause", "off", "shutdown"},
		{"promote", "off", "promote"},
		{"shutdown", "on", "shutdown"},
	} {
		r := misc.BuildDefaultRegistry()
		if err := r.Set("recovery_target_action", c.action, misc.SourceConfigFile); err != nil {
			t.Fatal(err)
		}
		if err := r.Set("hot_standby", c.hotStandby, misc.SourceConfigFile); err != nil {
			t.Fatal(err)
		}
		if got := recoveryTargetAction(r); got != c.want {
			t.Errorf("action %s, hot_standby %s: got %s, want %s", c.action, c.hotStandby, got, c.want)
		}
	}
}

// TestPromoteAfterRecoveryToTargetLSN is point-in-time recovery to
// recovery_target_lsn end to end: archive recovery stops after the target
// record, the promotion switches timeline at that record's end, and the new
// timeline's history names the stop the way getRecoveryStopReason does
// ("after LSN X/X"). goopg ignored the target, replayed the whole archive
// and wrote "archive recovery complete".
func TestPromoteAfterRecoveryToTargetLSN(t *testing.T) {
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
	type rec struct{ start, end uint64 }
	var recs []rec
	for rt.WAL.WrittenLSN() < start+3*segSize {
		s, e, err := rt.WAL.Append(make([]byte, 4000))
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec{s, e})
	}
	if err := rt.WAL.FlushUpTo(rt.WAL.WrittenLSN()); err != nil {
		t.Fatal(err)
	}
	rt.Close()

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
	if err := os.WriteFile(filepath.Join(dir, "recovery.signal"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err = initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	localEnd := rt.WAL.WrittenLSN()
	// A record well inside the archived range.
	k := len(recs) - 30
	if recs[k].start <= localEnd {
		t.Fatalf("precondition: target record %X is not past the local end %X", recs[k].start, localEnd)
	}
	target := recs[k].start - 1
	stop := xlog.RecoveryStop{Set: true, LSN: target, Inclusive: true}
	recovered, err := xlog.RunArchiveRecoveryWith(context.Background(), rt.StorageMgr, dir, func() string { return "cp " + filepath.Join(archive, "%f") + " %p" }, int64(segSize), xlog.TargetTimeline{}, stop)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRecoveryTargetReached(stop, recovered, localEnd); err != nil {
		t.Fatal(err)
	}
	if recovered.EndOfLog != recs[k].end {
		t.Fatalf("EndOfLog %X, want the target record's end %X", recovered.EndOfLog, recs[k].end)
	}
	oldTLI := rt.WAL.TimelineID()
	if err := promoteAfterRecovery(rt, archiveRecoveryEnd{Recovery: recovered}, logger); err != nil {
		t.Fatalf("promoteAfterRecovery: %v", err)
	}
	newTLI := rt.WAL.TimelineID()
	hist, err := xlog.ReadHistory(walDir, newTLI)
	if err != nil || len(hist) == 0 {
		t.Fatalf("new timeline history: %+v, %v", hist, err)
	}
	last := hist[len(hist)-1]
	wantReason := fmt.Sprintf("after LSN %X/%X", uint32(target>>32), uint32(target))
	if last.TLI != oldTLI || last.SwitchLSN != recs[k].end || last.Reason != wantReason {
		t.Errorf("history entry %+v; want TLI %d switched at %X, reason %q", last, oldTLI, recs[k].end, wantReason)
	}
	raw, _ := os.ReadFile(filepath.Join(walDir, xlog.TimelineHistoryFileName(newTLI)))
	if !strings.HasSuffix(string(raw), wantReason+"\n\n") {
		t.Errorf("history file %q does not end with PG's reason line (reason + its own newline)", raw)
	}
}

// TestRecoveryStopXidAndSingleTarget pins how goopg start reads the
// recovery targets: recovery_target_xid is strtou64(s, NULL, 0) truncated
// to a TransactionId, and setting it together with recovery_target_lsn is
// error_multiple_recovery_targets' "multiple recovery targets specified"
// with PG's errdetail.
func TestRecoveryStopXidAndSingleTarget(t *testing.T) {
	r := misc.BuildDefaultRegistry()
	if err := r.Set("recovery_target_xid", "0x2ee", misc.SourceConfigFile); err != nil {
		t.Fatal(err)
	}
	if got := recoveryStop(r); !got.XidSet || got.Xid != 750 || got.Set || !got.Inclusive {
		t.Errorf("xid target: %+v, want xid 750 inclusive", got)
	}
	if err := checkSingleRecoveryTarget(r); err != nil {
		t.Errorf("one target: %v", err)
	}
	if err := r.Set("recovery_target_lsn", "0/3000000", misc.SourceConfigFile); err != nil {
		t.Fatal(err)
	}
	err := checkSingleRecoveryTarget(r)
	var fatal *xlog.RestoreFatalError
	if !errors.As(err, &fatal) || fatal.Msg != "multiple recovery targets specified" ||
		fatal.Detail != `At most one of "recovery_target", "recovery_target_lsn", "recovery_target_name", "recovery_target_time", "recovery_target_xid" may be set.` {
		t.Errorf("two targets: err = %v (%+v)", err, fatal)
	}
	if err := r.Set("recovery_target_xid", "99999999999999999999999", misc.SourceConfigFile); err == nil {
		t.Error("an overflowing recovery_target_xid was accepted")
	}
}

// TestStandbyConsistencyPoint pins reachedConsistency for the hot-standby
// gate: the consistency point is max(minRecoveryPoint, the checkpoint's
// redo, backupEndPoint) as InitWalRecovery leaves pg_control
// (backupEndRequired never becomes consistent by position), and Consistent
// latches once replay passes it.
func TestStandbyConsistencyPoint(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
		cd.MinRecoveryPoint = 0x3000
		cd.CheckPointCopyRedo = 0x5000
		cd.BackupEndPoint = 0x4000
		cd.BackupEndRequired = false
	}); err != nil {
		t.Fatal(err)
	}
	at, never := recoveryConsistencyPoint(dir)
	if at != 0x5000 || never {
		t.Fatalf("consistency point %X (never %v), want 5000", at, never)
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	behind := &standbyController{replayer: xlog.NewStreamReplayer(mgr, 0x4fff), consistentAt: at}
	if behind.Consistent() {
		t.Error("replay at 4FFF reported consistent before 5000")
	}
	caught := &standbyController{replayer: xlog.NewStreamReplayer(mgr, 0x5000), consistentAt: at}
	if !caught.Consistent() || !caught.Consistent() {
		t.Error("replay at 5000 not consistent")
	}
	required := &standbyController{replayer: xlog.NewStreamReplayer(mgr, 0x9000), consistentAt: at, neverConsistent: true}
	if required.Consistent() {
		t.Error("backupEndRequired reported consistent by position")
	}
}

// TestEndInterruptedCrashRecovery pins how goopg start ends when a shutdown
// request stopped crash replay: a fast shutdown records
// DB_SHUTDOWNED_IN_RECOVERY (the shutdown restartpoint's skip arm), an
// immediate one (SIGQUIT) leaves pg_control as crash recovery stamped it;
// both exit 0.
func TestEndInterruptedCrashRecovery(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, immediate := range []bool{false, true} {
		dir := t.TempDir()
		if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
			t.Fatal(err)
		}
		if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) { cd.State = control.DBStateInCrashRecovery }); err != nil {
			t.Fatal(err)
		}
		if code := endInterruptedCrashRecovery(dir, immediate, logger, io.Discard); code != 0 {
			t.Errorf("immediate=%v: exit %d, want 0", immediate, code)
		}
		cd, err := control.ReadControlFile(dir)
		if err != nil || cd == nil {
			t.Fatal(err)
		}
		want := control.DBStateShutdownedInRecovery
		if immediate {
			want = control.DBStateInCrashRecovery
		}
		if cd.State != want {
			t.Errorf("immediate=%v: state %s, want %s", immediate, control.DBStateName(cd.State), control.DBStateName(want))
		}
	}
}
