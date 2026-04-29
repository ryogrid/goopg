// Standby-mode lifecycle: launches the walreceiver + continuous-replay
// pair on top of `initdb.Runtime`, then exposes a `Promote` method
// that drains pending replay, removes `standby.signal`, and switches
// the runtime back to primary mode.
//
// All goroutine wiring lives here so `runStart` only deals with one
// handle (`*standbyController`). The control-plane PROMOTE command
// calls into `Promote(ctx)`; clean shutdown calls into `Close()`.
//
// See docs/design/0005-0005-promotion.md.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/server"
	"github.com/goopg/goopg/internal/wal"
)

// drainPollInterval is how often Promote checks the replayer's
// ApplyLSN against the WAL writer's WrittenLSN while waiting for
// in-flight records to land. 10ms is short enough that promotion
// feels instant on an idle standby and doesn't hot-loop on a busy
// one (the replayer wakes on writer flush events; the poll just
// observes the result).
const drainPollInterval = 10 * time.Millisecond

// drainTimeout caps how long Promote will wait for the replayer to
// catch up. Five seconds is generous: the receiver context has
// already been cancelled, so no new records can land — we're only
// waiting for the replayer to apply the records the receiver had
// already written. Longer than this means a real apply problem
// (corrupt page, OOM, etc.) and surfacing the error is better than
// hanging the operator's `goopg promote` indefinitely.
const drainTimeout = 5 * time.Second

// standbyController owns the receiver + replayer goroutines for the
// lifetime of a standby-mode goopg start. It exposes Promote (drains
// and switches to primary) and Close (cancels both goroutines and
// waits for them to exit).
type standbyController struct {
	rt     *initdb.Runtime
	logger *slog.Logger

	receiverCancel context.CancelFunc
	receiverDone   chan struct{}
	replayerCancel context.CancelFunc
	replayerDone   chan struct{}
	replayer       *wal.StreamReplayer

	// promoteOnce protects against concurrent or repeated PROMOTE
	// commands. A second invocation after a successful promote is
	// a no-op that returns nil; one already in flight returns an
	// "already promoting" error so the operator notices.
	promoteOnce sync.Once
	promoting   atomic.Bool
	promoteErr  atomic.Pointer[error]
	promoted    atomic.Bool
}

// startStandby launches the receiver + replayer goroutines and
// returns a controller. parent is the top-level server context;
// cancelling it (e.g. SIGTERM) tears down both goroutines via
// Close. registry supplies the GUC values the receiver needs
// (`primary_conninfo` and friends).
func startStandby(parent context.Context, rt *initdb.Runtime, registry *config.Registry, logger *slog.Logger) *standbyController {
	rcvCtx, rcvCancel := context.WithCancel(parent)
	rplCtx, rplCancel := context.WithCancel(parent)

	rcvDone := make(chan struct{})
	rplDone := make(chan struct{})

	startWalreceiver(rcvCtx, rcvDone, rt, registry, logger)
	replayer := startStandbyReplayer(rplCtx, rplDone, rt, logger)

	return &standbyController{
		rt:             rt,
		logger:         logger,
		receiverCancel: rcvCancel,
		receiverDone:   rcvDone,
		replayerCancel: rplCancel,
		replayerDone:   rplDone,
		replayer:       replayer,
	}
}

// Promote is the OnPromote handler. The sequence:
//
//  1. Cancel the walreceiver so no new WAL records arrive.
//  2. Wait for the receiver goroutine to exit (no more Append calls
//     into rt.WAL).
//  3. Snapshot the current WrittenLSN — that's the drain target. The
//     replayer must apply at least up to here before promotion is
//     safe.
//  4. Poll the replayer's ApplyLSN until it reaches the target (or
//     drainTimeout elapses).
//  5. Cancel the replayer and wait for it to exit.
//  6. Remove `<DataDir>/standby.signal` so a future restart comes up
//     as a primary even if the operator forgets to clear the file.
//
// Steps 1+2 are mandatory before reading WrittenLSN as the drain
// target — otherwise a record that lands during step 3 would be
// counted but not waited for.
//
// Returns nil if the standby was already promoted by a prior call.
// Concurrent callers see an "already promoting" error; once the
// in-flight call returns, subsequent callers see its result.
func (sc *standbyController) Promote(ctx context.Context) error {
	if sc.promoted.Load() {
		return nil
	}
	if !sc.promoting.CompareAndSwap(false, true) {
		return errors.New("promotion already in progress")
	}
	var firstErr error
	sc.promoteOnce.Do(func() {
		firstErr = sc.runPromote(ctx)
		if firstErr == nil {
			sc.promoted.Store(true)
		}
		sc.promoteErr.Store(&firstErr)
	})
	if cached := sc.promoteErr.Load(); cached != nil {
		return *cached
	}
	return firstErr
}

func (sc *standbyController) runPromote(ctx context.Context) error {
	sc.logger.Info("promote: starting; cancelling walreceiver")
	sc.receiverCancel()
	select {
	case <-sc.receiverDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	if sc.rt.WAL == nil {
		// Defensive: a runtime with no WAL writer means there's
		// nothing to drain. Just remove the signal and exit.
		return sc.finalizePromotion()
	}

	target := sc.rt.WAL.WrittenLSN()
	sc.logger.Info("promote: draining replay", "target_lsn", target)

	deadline := time.NewTimer(drainTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(drainPollInterval)
	defer tick.Stop()

	for {
		applied := uint64(0)
		if sc.replayer != nil {
			applied = sc.replayer.ApplyLSN()
		}
		if applied >= target {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("promote: drain cancelled at apply_lsn=%d target=%d: %w",
				applied, target, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("promote: drain timed out after %s (apply_lsn=%d, target=%d)",
				drainTimeout, applied, target)
		case <-tick.C:
		}
	}

	sc.logger.Info("promote: replay drained; cancelling replayer",
		"apply_lsn", sc.replayer.ApplyLSN(), "target_lsn", target)
	sc.replayerCancel()
	select {
	case <-sc.replayerDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	return sc.finalizePromotion()
}

// finalizePromotion removes standby.signal and flips the runtime
// flag. Errors removing the signal file are surfaced — leaving it in
// place would make the next restart come up as a standby again,
// which is the opposite of what the operator just asked for.
func (sc *standbyController) finalizePromotion() error {
	if err := initdb.RemoveStandbySignal(sc.rt.DataDir); err != nil {
		return fmt.Errorf("promote: remove standby.signal: %w", err)
	}
	sc.rt.Standby = false
	sc.logger.Info("promote: standby.signal removed; runtime is now primary")
	return nil
}

// Close cancels both goroutines and waits for them to exit. Safe to
// call after Promote — the cancellations are idempotent.
func (sc *standbyController) Close() {
	sc.receiverCancel()
	sc.replayerCancel()
	<-sc.receiverDone
	<-sc.replayerDone
}

// boundPromoteToServer returns a closure suitable for
// server.Config.Promote: it forwards to sc.Promote with a fresh
// timeout-bounded context so a stuck promote can't wedge the
// control-plane goroutine.
func boundPromoteToServer(sc *standbyController) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), drainTimeout+5*time.Second)
		defer cancel()
		return sc.Promote(ctx)
	}
}

// walDirFor is a small helper used by the standby launchers and
// promotion path so they agree on where pg_wal lives. Centralising
// this avoids drift if the layout ever changes.
func walDirFor(rt *initdb.Runtime) string {
	return filepath.Join(rt.DataDir, "pg_wal")
}

// reqStandbyConfig is a no-op compile-time check that the wal +
// initdb + server packages we just used are all reachable; without
// this Go would still compile (since the rest of the file uses
// them) but moving things around in a future refactor could quietly
// drop an import. Keeping the alias barrier explicit avoids that
// surprise.
var _ server.Config
var _ = wal.DefaultSegmentSize
var _ = walDirFor
