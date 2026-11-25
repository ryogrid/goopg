package xlog

import (
	"context"
	"time"
)

// ReplayTarget is a standby's recovery target (recovery_target,
// recovery_target_lsn / _xid / _time / _name): continuous replay stops there
// as PerformWalRecovery does (recoveryStopsBefore / recoveryStopsAfter).
// M0149-0023b.
type ReplayTarget struct {
	Stop RecoveryStop
	// Consistent reports reachedConsistency, for recovery_target =
	// 'immediate'.
	Consistent func() bool
	// Reached is called once, when replay reaches the target — before
	// applying the record, or after when the stop includes it. It returns
	// whether replay pauses there (recovery_target_action = pause).
	Reached func(StopEval) bool
	// Resumed is called when pg_wal_replay_resume ends that pause: recovery
	// ends and the standby promotes.
	Resumed func()
}

// SetRecoveryTarget installs the recovery target. Must be called before Run.
func (sr *StreamReplayer) SetRecoveryTarget(t ReplayTarget) { sr.target = &t }

// targetBefore reports whether replay stops before applying rec.
func (sr *StreamReplayer) targetBefore(rec Record) (StopEval, bool) {
	if sr.target == nil {
		return StopEval{}, false
	}
	ev, hit := EvaluateRecoveryStop(sr.target.Stop, rec, false)
	return ev, hit && !ev.After
}

// targetAfter reports whether replay stops after having applied rec.
func (sr *StreamReplayer) targetAfter(rec Record) (StopEval, bool) {
	t := sr.target
	if t == nil {
		return StopEval{}, false
	}
	consistent := t.Consistent != nil && t.Consistent()
	ev, hit := EvaluateRecoveryStop(t.Stop, rec, consistent)
	return ev, hit && ev.After
}

// reachTarget ends replay at the target: Reached reports it, and with
// recovery_target_action = pause replay waits paused at the end of recovery
// (recoveryPausesHere with endOfRecovery) until pg_wal_replay_resume —
// which ends recovery, so Resumed is called — or a promotion or shutdown.
func (sr *StreamReplayer) reachTarget(ctx context.Context, ev StopEval) {
	if sr.target.Reached == nil || !sr.target.Reached(ev) {
		return
	}
	sr.pauseState.Store(recoveryPaused)
	for sr.pauseState.Load() != recoveryNotPaused && !sr.promoteTriggered.Load() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
		// pg_wal_replay_pause while paused at the target stays paused.
		sr.pauseState.CompareAndSwap(recoveryPauseRequested, recoveryPaused)
	}
	if !sr.promoteTriggered.Load() && sr.target.Resumed != nil {
		sr.target.Resumed()
	}
}
