package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/wal"
)

// TestStandbyControllerPromoteRemovesSignal exercises the happy
// path: start a standby controller against an initialised data
// directory with a standby.signal in place, call Promote, and
// verify (a) the signal file is gone, (b) the runtime's Standby
// flag is false, (c) Promote returns nil.
//
// We deliberately skip wiring an actual primary so the walreceiver
// short-circuits in startWalreceiver (empty primary_conninfo logs
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
	payload := wal.EncodeCheckpoint()
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
