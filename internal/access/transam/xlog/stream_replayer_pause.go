package xlog

import (
	"context"
	"encoding/binary"
	"time"
)

// Recovery pause states (RecoveryPauseState, xlogrecovery.h).
const (
	recoveryNotPaused      = 0
	recoveryPauseRequested = 1
	recoveryPaused         = 2
)

// SetApplyDelay installs the recovery_min_apply_delay source, read before
// every commit record (it is a SIGHUP GUC). M0149-0013.
func (sr *StreamReplayer) SetApplyDelay(fn func() time.Duration) { sr.applyDelay.Store(&fn) }

// RequestPause is SetRecoveryPause(true) (pg_wal_replay_pause): replay stops
// before the next record and the state becomes "paused" once it has.
func (sr *StreamReplayer) RequestPause() {
	sr.pauseState.CompareAndSwap(recoveryNotPaused, recoveryPauseRequested)
}

// Resume is SetRecoveryPause(false) (pg_wal_replay_resume).
func (sr *StreamReplayer) Resume() { sr.pauseState.Store(recoveryNotPaused) }

// PauseState is pg_get_wal_replay_pause_state's text.
func (sr *StreamReplayer) PauseState() string {
	switch sr.pauseState.Load() {
	case recoveryPauseRequested:
		return "pause requested"
	case recoveryPaused:
		return "paused"
	}
	return "not paused"
}

// TriggerPromote is CheckForStandbyTrigger turning true: a paused replay or an
// apply delay ends so promotion can proceed (recoveryPausesHere,
// recoveryApplyDelay).
func (sr *StreamReplayer) TriggerPromote() {
	sr.promoteTriggered.Store(true)
	sr.pauseState.Store(recoveryNotPaused)
}

// recoveryPausesHere blocks while a pause is requested or in effect, until
// pg_wal_replay_resume, a promotion trigger or ctx ends it
// (xlogrecovery.c recoveryPausesHere).
func (sr *StreamReplayer) recoveryPausesHere(ctx context.Context) {
	if sr.pauseState.Load() == recoveryNotPaused {
		return
	}
	sr.pauseState.CompareAndSwap(recoveryPauseRequested, recoveryPaused)
	for sr.pauseState.Load() != recoveryNotPaused && !sr.promoteTriggered.Load() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
		// A pause requested again while paused stays paused.
		sr.pauseState.CompareAndSwap(recoveryPauseRequested, recoveryPaused)
	}
}

// recoveryApplyDelay holds a COMMIT or COMMIT PREPARED record until its
// xact_time plus recovery_min_apply_delay has passed (xlogrecovery.c
// recoveryApplyDelay). The delay is re-read while waiting; a promotion
// trigger ends the wait.
func (sr *StreamReplayer) recoveryApplyDelay(ctx context.Context, rec Record) {
	delayFn := sr.applyDelay.Load()
	if delayFn == nil || rec.XLog == nil || rec.XLog.Header.Rmid != RmgrXact {
		return
	}
	op := rec.XLog.Header.Info & xlogXactOpMask
	if op != xlogXactCommit && op != xlogXactCommitPrepared {
		return
	}
	if len(rec.XLog.MainData) < 8 {
		return
	}
	xtime := pgEpochTime.Add(time.Duration(int64(binary.LittleEndian.Uint64(rec.XLog.MainData[:8]))) * time.Microsecond)
	for {
		delay := (*delayFn)()
		if delay <= 0 || sr.promoteTriggered.Load() {
			return
		}
		wait := time.Until(xtime.Add(delay))
		if wait <= 0 {
			return
		}
		if wait > 100*time.Millisecond {
			wait = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

var pgEpochTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
