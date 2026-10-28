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
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/postmaster"
	"github.com/goopg/goopg/internal/replication"
	"github.com/goopg/goopg/internal/utils/misc"
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

// promoteSignalPollInterval is how often the standby checks for the
// presence of `<datadir>/promote.signal`. 250 ms matches PG's
// `min_recovery_apply_delay` granularity; faster is wasted poll
// traffic on idle standbys, slower makes M0102 E2E tests sluggish.
const promoteSignalPollInterval = 250 * time.Millisecond

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
	replayer       *xlog.StreamReplayer

	// signalCancel cancels the promote.signal poller goroutine.
	// Close cancels it explicitly; a successful Promote also
	// returns from the watcher loop on its own.
	signalCancel context.CancelFunc
	signalDone   chan struct{}

	// promoting guards against concurrent or repeated PROMOTE
	// commands. A second invocation after a successful promote is
	// a no-op that returns nil; one already in flight returns an
	// "already promoting" error so the operator notices. A promote
	// that FAILED clears the flag again — promotion is retryable,
	// which is what promoteSignalWatcher's "re-create the file to
	// retry" contract assumes (review/260831-2 CM-1).
	promoting  atomic.Bool
	promoteErr atomic.Pointer[error]
	promoted   atomic.Bool

	// lastReceived is the highest WAL position a walreceiver reported. PG's
	// pg_last_wal_receive_lsn keeps answering WalRcv->flushedUpto after the
	// walreceiver exits (promotion, a reconnect); goopg's receiver registry
	// forgets a stopped receiver, so recoveryLSNs remembers it here.
	lastReceived atomic.Uint64

	// walLevel is the wal_level enum (minimal 0, replica 1, logical 2)
	// XLOG_END_OF_RECOVERY records.
	walLevel int32
}

// walLevelEnum maps a wal_level setting to its enum value (WalLevel,
// access/xlog.h).
func walLevelEnum(s string) int32 {
	switch s {
	case "minimal":
		return 0
	case "logical":
		return 2
	}
	return 1
}

// endOfLog is where the promoted timeline begins — PG's EndOfLog — and
// the 0-based start of the record before it, the first new record's
// xl_prev. It is the end of the last record replay applied. With none
// applied it is the replayer's baseline, the end of the WAL the writer
// opened with, whose prev the writer's tracker still holds (walreceiver
// appends never move it).
func (sc *standbyController) endOfLog() (end, prev uint64) {
	if sc.replayer != nil {
		if start, e, ok := sc.replayer.LastApplied(); ok {
			return e, start - 1
		}
	}
	if sc.rt.WAL == nil {
		return 0, 0
	}
	_, prev = sc.rt.WAL.InsertPosition()
	if sc.replayer != nil {
		return sc.replayer.ApplyLSN(), prev
	}
	return sc.rt.WAL.WrittenLSN(), prev
}

// recoveryLSNs reports pg_last_wal_receive_lsn's and pg_last_wal_replay_lsn's
// positions: the WAL received from the primary, and the end of the last
// record replayed (StreamReplayer.ApplyLSN, PG's lastReplayedEndRecPtr).
func (sc *standbyController) recoveryLSNs() (receive, replay uint64) {
	if st, ok := sc.rt.WalReceivers.Snapshot(); ok {
		for {
			prev := sc.lastReceived.Load()
			if st.ReceivedLSN <= prev || sc.lastReceived.CompareAndSwap(prev, st.ReceivedLSN) {
				break
			}
		}
	}
	if sc.replayer != nil {
		replay = sc.replayer.ApplyLSN()
	}
	return sc.lastReceived.Load(), replay
}

// startStandby launches the receiver + replayer goroutines and
// returns a controller. parent is the top-level server context;
// cancelling it (e.g. SIGTERM) tears down both goroutines via
// Close. registry supplies the GUC values the receiver needs
// (`primary_conninfo` and friends).
func startStandby(parent context.Context, rt *initdb.Runtime, registry *misc.Registry, logger *slog.Logger) *standbyController {
	rcvCtx, rcvCancel := context.WithCancel(parent)
	rplCtx, rplCancel := context.WithCancel(parent)
	sigCtx, sigCancel := context.WithCancel(parent)

	rcvDone := make(chan struct{})
	rplDone := make(chan struct{})
	sigDone := make(chan struct{})

	replayer := startStandbyReplayer(rplCtx, rplDone, rt, logger)
	// Recovery flushes become restartpoints: the checkpointer adopts the
	// last checkpoint record replay applied into pg_control
	// (CreateRestartPoint), so a restarted standby replays from there.
	if rt.Checkpointer != nil && replayer != nil {
		rt.Checkpointer.SetRestartPointSource(replayer.LastCheckpoint)
		// The max_wal_size trigger requests restartpoints by the replayed
		// WAL, as PG's XLogPageRead does, not by the received WAL.
		rt.Checkpointer.SetReplayPositionSource(replayer.ApplyLSN)
	}
	replication.StartWalReceiver(rcvCtx, rcvDone, replication.WalReceiverLauncherConfig{
		WAL:          rt.WAL,
		Receivers:    rt.WalReceivers,
		Registry:     registry,
		Logger:       logger,
		ApplyLSNFunc: standbyApplyLSNFunc(replayer),
	})

	sc := &standbyController{
		rt:             rt,
		logger:         logger,
		receiverCancel: rcvCancel,
		receiverDone:   rcvDone,
		replayerCancel: rplCancel,
		replayerDone:   rplDone,
		replayer:       replayer,
		signalCancel:   sigCancel,
		signalDone:     sigDone,
		walLevel:       walLevelEnum(stringGUC(registry, "wal_level", "replica")),
	}
	// Clear any stale promote.signal left by a previous run before
	// starting the watcher. Upstream's xlogrecovery.c initialises
	// the trigger state in StartupXLOG before the replay loop ever
	// calls CheckForStandbyTrigger; a residual file would otherwise
	// cause an immediate promote on the next start, surprising the
	// operator.
	if rt.DataDir != "" {
		path := filepath.Join(rt.DataDir, initdb.PromoteSignalFile)
		if err := os.Remove(path); err == nil {
			logger.Warn("removed stale promote.signal at standby startup",
				"path", path)
		}
	}
	go sc.promoteSignalWatcher(sigCtx)
	return sc
}

func standbyApplyLSNFunc(replayer *xlog.StreamReplayer) func() uint64 {
	if replayer == nil {
		return nil
	}
	return replayer.ApplyLSN
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
// Concurrent callers see an "already promoting" error. A promote that
// failed is NOT sticky: the node is still a standby, so a later call
// runs the sequence again (steps 1-2 are idempotent — the receiver
// context stays cancelled and its done channel stays closed).
func (sc *standbyController) Promote(ctx context.Context) error {
	if sc.promoted.Load() {
		return nil
	}
	if !sc.promoting.CompareAndSwap(false, true) {
		return errors.New("promotion already in progress")
	}
	// Release the in-flight guard on EVERY exit, success or failure:
	// a promote that returned an error (drain timeout, cancelled
	// context) leaves the node a standby, and the operator must be
	// able to try again. Leaving the flag set wedged every later
	// PROMOTE — control socket and promote.signal alike — behind a
	// misleading "already in progress" (review/260831-2 CM-1).
	defer sc.promoting.Store(false)
	// A promote may have completed between the two loads above.
	if sc.promoted.Load() {
		return nil
	}
	err := sc.runPromote(ctx)
	sc.promoteErr.Store(&err)
	if err == nil {
		sc.promoted.Store(true)
	}
	return err
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
		// `target` is a byte position, `applied` a record boundary, and
		// the two need not ever meet: the primary's walsender cuts the
		// stream at an arbitrary byte offset, so the tail the receiver
		// appended is routinely a partially-transmitted record (or page
		// padding no record covers). Upstream recovery ends at the last
		// *complete* record in exactly this situation
		// (xlogrecovery.c ReadRecord: a short tail is end-of-WAL, not an
		// error), so once replay is parked with nothing left to read we
		// are drained even though applied < target. Sound only because
		// the receiver above has already exited — nothing can append
		// after this point. Nightly AI-20260812-005501-001 failed here
		// with a 208-byte shortfall that no amount of waiting could
		// close.
		if sc.replayer != nil && sc.replayer.AtEndOfWAL() {
			sc.logger.Info("promote: replay reached end of WAL short of the written tail",
				"apply_lsn", applied, "written_lsn", target,
				"partial_tail_bytes", target-applied)
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

// finalizePromotion runs the M0102-0003 timeline-bump sequence then
// flips the runtime flag. Steps, in order:
//
//  1. Compute the new TLI (oldTLI + 1) from the persisted value.
//  2. Append a history entry for oldTLI + EndOfLog to any pre-existing
//     history chain and atomically write `pg_wal/<newTLI>.history`.
//  3. Persist newTLI to `global/timeline_id` and pg_control.
//  4. Switch the running WAL writer to newTLI at the end of the last
//     replayed record and write XLOG_END_OF_RECOVERY there
//     (Writer.SwitchTimeline; design m0119-promotion-timeline-switch.md).
//  5. Remove `standby.signal` so a future restart still comes up as
//     primary even if the operator forgets to clear it.
//
// Errors at any step are returned to the caller — leaving the
// standby half-promoted is preferable to silently masking an
// inconsistency that a heterogeneous reattach would later trip on.
func (sc *standbyController) finalizePromotion() error {
	if sc.rt.DataDir != "" {
		oldTLI, err := initdb.LoadOrCreateTimelineID(sc.rt.DataDir)
		if err != nil {
			return fmt.Errorf("promote: load timeline_id: %w", err)
		}
		newTLI := oldTLI + 1
		walDir := walDirFor(sc.rt)

		// The switch point is the end of the last record replay
		// applied: received bytes past it are a partial record, which
		// the new timeline overwrites (endOfLog).
		endLSN, prevRecPtr := sc.endOfLog()

		prev, err := xlog.ReadHistory(walDir, oldTLI)
		if err != nil {
			return fmt.Errorf("promote: read prior history: %w", err)
		}
		entries := append(prev, xlog.TimelineHistoryEntry{
			TLI:       oldTLI,
			SwitchLSN: endLSN,
			Reason:    "no recovery target specified",
		})
		if err := xlog.WriteHistory(walDir, newTLI, entries); err != nil {
			return fmt.Errorf("promote: write timeline history: %w", err)
		}
		if err := initdb.WriteTimelineID(sc.rt.DataDir, newTLI); err != nil {
			return fmt.Errorf("promote: persist new timeline_id: %w", err)
		}
		// M0130-S8: update pg_control's CheckPoint copy so the
		// authoritative TLI source reflects the promotion. Mirrors
		// upstream CreateEndOfRecoveryRecord's control-file update
		// (xlog.c:5638-5642). The flat file above is updated first so a
		// crash between WriteTimelineID and UpdateControlFile still has
		// the correct TLI in at least one place — LoadOrCreateTimelineID
		// gives pg_control precedence on restart.
		if err := control.UpdateControlFile(sc.rt.DataDir, func(cd *control.ControlFileData) {
			cd.CheckPointCopyThisTLI = newTLI
			cd.CheckPointCopyPrevTLI = oldTLI
		}); err != nil {
			return fmt.Errorf("promote: update pg_control TLI: %w", err)
		}
		// Move the running writer onto the new timeline at once, and
		// make XLOG_END_OF_RECOVERY its first record, as StartupXLOG does
		// at the end of archive recovery (XLogInitNewTimeline,
		// CreateEndOfRecoveryRecord). The writer used to stay on the old
		// timeline until restart, so post-promotion WAL carried the old
		// TLI and the first checkpoint stamped it back into pg_control.
		if sc.rt.WAL != nil && endLSN > 0 {
			if err := sc.rt.WAL.SwitchTimeline(newTLI, endLSN, prevRecPtr); err != nil {
				return fmt.Errorf("promote: switch WAL timeline: %w", err)
			}
			rec, err := xlog.EncodeEndOfRecoveryPG(xlog.PGTimestampTz(time.Now()), newTLI, oldTLI, sc.walLevel)
			if err != nil {
				return fmt.Errorf("promote: encode end-of-recovery record: %w", err)
			}
			_, recEnd, err := sc.rt.WAL.Append(rec)
			if err != nil {
				return fmt.Errorf("promote: write end-of-recovery record: %w", err)
			}
			if err := sc.rt.WAL.FlushUpTo(recEnd); err != nil {
				return fmt.Errorf("promote: flush end-of-recovery record: %w", err)
			}
			// So crash recovery follows the timeline change to here.
			if err := control.UpdateControlFile(sc.rt.DataDir, func(cd *control.ControlFileData) {
				cd.MinRecoveryPoint = recEnd
				cd.MinRecoveryPointTLI = newTLI
			}); err != nil {
				return fmt.Errorf("promote: update pg_control min recovery point: %w", err)
			}
		}
		sc.logger.Info("promote: timeline bumped",
			"old_tli", oldTLI, "new_tli", newTLI, "switch_lsn", endLSN,
			"history_file", filepath.Join(walDir, xlog.TimelineHistoryFileName(newTLI)))
	}

	if err := initdb.RemoveStandbySignal(sc.rt.DataDir); err != nil {
		return fmt.Errorf("promote: remove standby.signal: %w", err)
	}
	sc.rt.Standby = false
	// Recovery-mode checkpoints intentionally avoid creating local WAL. Once
	// promotion completes this runtime owns its timeline again, so restore the
	// normal checkpoint path before accepting primary writes.
	if sc.rt.Checkpointer != nil {
		sc.rt.Checkpointer.SetRecoveryMode(false)
		sc.rt.Checkpointer.SetRestartPointSource(nil)
		sc.rt.Checkpointer.SetReplayPositionSource(nil)
	}
	// Recovery is over: pg_control leaves DB_IN_ARCHIVE_RECOVERY for
	// DB_IN_PRODUCTION, as the end of StartupXLOG stamps it after promotion
	// (xlog.c:6204-6211) (M0122-0013).
	if err := control.UpdateControlFile(sc.rt.DataDir, func(cd *control.ControlFileData) {
		cd.State = control.DBStateInProduction
		cd.Time = time.Now().Unix()
	}); err != nil {
		return fmt.Errorf("promote: stamp pg_control DB_IN_PRODUCTION: %w", err)
	}
	sc.logger.Info("promote: standby.signal removed; runtime is now primary")
	return nil
}

// Close cancels both goroutines and waits for them to exit. Safe to
// call after Promote — the cancellations are idempotent.
func (sc *standbyController) Close() {
	sc.receiverCancel()
	sc.replayerCancel()
	if sc.signalCancel != nil {
		sc.signalCancel()
	}
	<-sc.receiverDone
	<-sc.replayerDone
	if sc.signalDone != nil {
		<-sc.signalDone
	}
}

// promoteSignalWatcher polls for `<DataDir>/promote.signal` at
// promoteSignalPollInterval and triggers Promote(ctx) when the file
// appears. Mirrors upstream's `CheckForStandbyTrigger` in
// `postgres/src/backend/access/transam/xlogrecovery.c`, which checks
// the same filename on each WAL replay cycle.
//
// On detect: remove the file first (so a partial Promote can be
// retried by re-creating the file), then call Promote. The
// promoted/promoting flags inside Promote provide idempotency
// against the control-socket PROMOTE path. The watcher exits after a successful trigger; on
// context cancellation (Close, shutdown) it also exits cleanly.
func (sc *standbyController) promoteSignalWatcher(ctx context.Context) {
	defer close(sc.signalDone)
	if sc.rt.DataDir == "" {
		// No data directory means no file to poll for; in-process
		// tests that hit this path want the goroutine to exit
		// immediately rather than spin.
		<-ctx.Done()
		return
	}
	path := filepath.Join(sc.rt.DataDir, initdb.PromoteSignalFile)
	t := time.NewTicker(promoteSignalPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := os.Stat(path); err != nil {
				continue
			}
			// Remove first to make re-trigger explicit on Promote
			// failure. Errors here are surfaced for visibility but
			// do not block the Promote call — if removal fails the
			// next loop iteration will re-fire.
			if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				sc.logger.Warn("promote.signal removal failed",
					"path", path, "err", rmErr)
			}
			sc.logger.Info("promote.signal detected; triggering Promote",
				"path", path)
			if err := sc.Promote(ctx); err != nil {
				sc.logger.Error("promote.signal triggered Promote failed",
					"err", err)
			}
			return
		}
	}
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

// promoteAfterRecovery runs the post-recovery promotion sequence for
// archive recovery (M0130-S9). Mirrors finalizePromotion but does not
// require a standby controller — the archive recovery loop already
// replayed all available WAL. Steps:
//
//  1. Compute the new TLI (oldTLI + 1).
//  2. Append a history entry for oldTLI + endLSN.
//  3. Write the timeline history file for newTLI.
//  4. Persist newTLI to the flat file and pg_control.
//  5. Remove recovery.signal.
func promoteAfterRecovery(rt *initdb.Runtime, logger *slog.Logger) error {
	if rt.DataDir == "" {
		return nil
	}
	oldTLI, err := initdb.LoadOrCreateTimelineID(rt.DataDir)
	if err != nil {
		return fmt.Errorf("promote after recovery: load timeline_id: %w", err)
	}
	newTLI := oldTLI + 1
	walDir := walDirFor(rt)

	// Use WrittenLSN as the switch point (the archive recovery loop
	// replayed everything through the WAL writer).
	var endLSN uint64
	if rt.WAL != nil {
		endLSN = rt.WAL.WrittenLSN()
	}

	prev, err := xlog.ReadHistory(walDir, oldTLI)
	if err != nil {
		return fmt.Errorf("promote after recovery: read prior history: %w", err)
	}
	entries := append(prev, xlog.TimelineHistoryEntry{
		TLI:       oldTLI,
		SwitchLSN: endLSN,
		Reason:    "archive recovery complete",
	})
	if err := xlog.WriteHistory(walDir, newTLI, entries); err != nil {
		return fmt.Errorf("promote after recovery: write timeline history: %w", err)
	}
	if err := initdb.WriteTimelineID(rt.DataDir, newTLI); err != nil {
		return fmt.Errorf("promote after recovery: persist new timeline_id: %w", err)
	}
	if err := control.UpdateControlFile(rt.DataDir, func(cd *control.ControlFileData) {
		cd.CheckPointCopyThisTLI = newTLI
		cd.CheckPointCopyPrevTLI = oldTLI
	}); err != nil {
		return fmt.Errorf("promote after recovery: update pg_control TLI: %w", err)
	}

	if err := initdb.RemoveRecoverySignal(rt.DataDir); err != nil {
		return fmt.Errorf("promote after recovery: remove recovery.signal: %w", err)
	}
	logger.Info("archive recovery promotion complete",
		"old_tli", oldTLI, "new_tli", newTLI, "switch_lsn", endLSN,
		"history_file", filepath.Join(walDir, xlog.TimelineHistoryFileName(newTLI)))
	return nil
}

// reqStandbyConfig is a no-op compile-time check that the wal +
// initdb + server packages we just used are all reachable; without
// this Go would still compile (since the rest of the file uses
// them) but moving things around in a future refactor could quietly
// drop an import. Keeping the alias barrier explicit avoids that
// surprise.
var _ postmaster.Config
var _ = xlog.DefaultSegmentSize
var _ = walDirFor
