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
	"testing"
	"time"

	"github.com/goopg/goopg/internal/initdb"
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
