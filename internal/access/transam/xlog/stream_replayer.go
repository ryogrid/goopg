// Continuous (streaming) WAL replay for standby servers.
//
// The crash-recovery path (ReplayRecords / ReplayFromDirWithMgr) is
// batch-oriented: it reads all records under pg_wal, finds the last
// checkpoint, and applies the tail in a single pass. That model
// works for the "open the data dir, replay, then start serving" boot
// flow but is wrong for streaming replication, where new records
// arrive indefinitely and must apply as soon as the writer has
// flushed them.
//
// `StreamReplayer` wraps the per-record `ApplyRecord` kernel and
// drives it from a `RecordIterator`. The standby's main loop wires
// it up alongside the walreceiver: walreceiver Append's into the
// local WAL writer, the iterator wakes on the writer's flush
// subscription, and `StreamReplayer.Run` applies each record
// one-at-a-time. Apply is idempotent via `pd_lsn` so restart-resume
// never needs a separate apply cursor — we always start the
// iterator at the writer's WrittenLSN at boot time and the
// previously-applied tail is silently skipped.
//
// See docs/design/0005-0002-standby-recovery-and-replay.md.
package xlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// StreamReplayer is the standby-side continuous-replay driver.
// Construct via `NewStreamReplayer`, then call `Run(ctx, iter)` from
// a goroutine. `ApplyLSN()` is safe to call concurrently and returns
// the LSN of the last record successfully applied (or the
// constructor-supplied baseline when no records have been applied
// yet).
type StreamReplayer struct {
	mgr *storage.Manager

	// onXactReplay is an optional hook called for every commit/abort
	// record the replayer processes. The first argument is the
	// transaction's XID; the second is true for commit, false for
	// abort. Used by the standby to advance the local MVCC manager's
	// nextXID so replayed tuples become visible to queries.
	onXactReplay func(xid storage.TransactionID, committed bool)
	// onPrepareReplay / onPreparedFinish: two-phase replay (PrepareRedoAdd /
	// PrepareRedoRemove). M0149-0015b.
	onPrepareReplay  func(endLSN uint64, state []byte)
	onPreparedFinish func(xid storage.TransactionID)
	// onCatalogReplay / catalogDBs: the live catalog refresh (M0149-0010a).
	onCatalogReplay func(dbOid uint32)
	onSeqReplay     func(rel storage.RelFileNode) // M0149-0077a
	catalogDBs      map[uint32]struct{}
	// Recovery pause / apply delay (stream_replayer_pause.go). M0149-0013.
	applyDelay       atomic.Pointer[func() time.Duration]
	pauseState       atomic.Int32
	promoteTriggered atomic.Bool

	mu       sync.Mutex
	applyLSN uint64
	// lastStart is the StartLSN of the record that set applyLSN (0 before
	// any): promotion resumes WAL after it with xl_prev naming it.
	lastStart uint64
	records   uint64
	applied   uint64
	// iter is the iterator the current Run call is draining. Held so
	// observers outside the replay goroutine (the promotion drain)
	// can ask whether replay has reached end-of-WAL. nil before the
	// first Run.
	iter *RecordIterator
	// lastCheckpoint is the last checkpoint record replay applied, the
	// restartpoint candidate (see ReplayedCheckpoint); hasCheckpoint is
	// false before the first.
	lastCheckpoint ReplayedCheckpoint
	hasCheckpoint  bool
}

// ReplayedCheckpoint is the last checkpoint record a standby replayed: PG's
// XLogCtl->lastCheckPointRecPtr / lastCheckPointEndPtr / lastCheckPoint,
// which RecoveryRestartPoint (xlog.c) records for every replayed
// XLOG_CHECKPOINT_ONLINE/SHUTDOWN and CreateRestartPoint later adopts into
// pg_control (M0122-0013).
type ReplayedCheckpoint struct {
	// StartLSN is the record's internal 1-based start (PG's ReadRecPtr is
	// StartLSN-1); EndLSN is PG's EndRecPtr.
	StartLSN uint64
	EndLSN   uint64
	// Fields and Time are the record's decoded CheckPoint struct.
	Fields CheckPointFields
	Time   int64
}

// LastCheckpoint returns the last checkpoint record replay applied; ok is
// false before the first.
func (sr *StreamReplayer) LastCheckpoint() (ReplayedCheckpoint, bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.lastCheckpoint, sr.hasCheckpoint
}

// NewStreamReplayer constructs a replayer rooted at `mgr`.
// `baselineLSN` seeds the initial `ApplyLSN()` reading; pass the
// local WAL writer's `WrittenLSN()` at boot so observers see a
// monotone progression even before the first record arrives.
func NewStreamReplayer(mgr *storage.Manager, baselineLSN uint64) *StreamReplayer {
	if mgr == nil {
		panic("wal: NewStreamReplayer: nil storage manager")
	}
	return &StreamReplayer{mgr: mgr, applyLSN: baselineLSN}
}

// SetXactReplayHook installs a callback that is invoked for every
// commit or abort record the replayer encounters. fn(xid, true)
// signals a commit; fn(xid, false) signals an abort. The typical
// standby use-case wires this to mvcc.Manager.ReplayXactCommit /
// ReplayXactAbort so queries on the standby see replayed tuples as
// committed (xmin < snap.Xmax) rather than as "future" XIDs.
// Must be called before Run.
func (sr *StreamReplayer) SetXactReplayHook(fn func(xid storage.TransactionID, committed bool)) {
	sr.onXactReplay = fn
}

// SetTwoPhaseReplayHooks installs the two-phase replay callbacks: prepare
// is called with each XLOG_XACT_PREPARE record's end LSN and state buffer
// (xact_redo's PrepareRedoAdd), finish with the xid each COMMIT/ABORT
// PREPARED record finishes (PrepareRedoRemove). Must be called before Run.
func (sr *StreamReplayer) SetTwoPhaseReplayHooks(prepare func(endLSN uint64, state []byte), finish func(xid storage.TransactionID)) {
	sr.onPrepareReplay = prepare
	sr.onPreparedFinish = finish
}

// SetCatalogReplayHook installs a callback invoked, after a replayed commit,
// for every database whose system catalogs the replayed WAL changed since the
// previous commit. It is the standby's counterpart of the relcache
// invalidation PG's commit records carry (XACT_XINFO_HAS_INVALS): the hook
// refreshes the running standby's catalog so relations the primary created
// become visible without a restart. Must be called before Run. M0149-0010a.
// SetSequenceReplayHook installs fn, called after an XLOG_SEQ_LOG record
// rebuilt the page of sequence relation rel (seq_redo), so the server's
// sequence state follows the replayed page. M0149-0077a.
func (sr *StreamReplayer) SetSequenceReplayHook(fn func(rel storage.RelFileNode)) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.onSeqReplay = fn
}

func (sr *StreamReplayer) SetCatalogReplayHook(fn func(dbOid uint32)) {
	sr.onCatalogReplay = fn
}

// noteCatalogBlocks records the databases of the system-catalog relations
// (relfilenode below FirstNormalObjectId) rec changed.
func (sr *StreamReplayer) noteCatalogBlocks(rec Record) {
	if sr.onCatalogReplay == nil || rec.XLog == nil {
		return
	}
	for _, b := range rec.XLog.Blocks {
		if b.Rel.RelOid != 0 && b.Rel.RelOid < 16384 && b.Rel.DBOid != 0 {
			if sr.catalogDBs == nil {
				sr.catalogDBs = make(map[uint32]struct{})
			}
			sr.catalogDBs[b.Rel.DBOid] = struct{}{}
		}
	}
}

// ApplyLSN returns the LSN of the most recently applied record (or
// the baseline supplied to NewStreamReplayer when no records have
// been applied yet).
func (sr *StreamReplayer) ApplyLSN() uint64 {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.applyLSN
}

// LastApplied returns the StartLSN and EndLSN of the last record applied;
// ok is false before the first. The end is PG's lastReplayedEndRecPtr, where
// a promotion's new timeline begins, and the start (0-based: start-1) is
// what the first record on it names as xl_prev.
func (sr *StreamReplayer) LastApplied() (start, end uint64, ok bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.lastStart, sr.applyLSN, sr.lastStart != 0
}

// AtEndOfWAL reports that replay has applied every complete record
// the local WAL writer holds and is parked waiting for bytes that
// have not arrived. Returns false before Run has started.
//
// The promotion drain uses this as its stop condition because
// ApplyLSN can never reach WrittenLSN when the received stream was
// cut mid-record — see RecordIterator.AtEndOfWAL for the full
// argument and its correctness precondition (the appender must be
// stopped first).
func (sr *StreamReplayer) AtEndOfWAL() bool {
	sr.mu.Lock()
	iter := sr.iter
	sr.mu.Unlock()
	if iter == nil {
		return false
	}
	return iter.AtEndOfWAL()
}

// Stats returns a snapshot of the replayer's progress counters: the
// total number of records observed (including markers) and the
// number that triggered a real page mutation.
func (sr *StreamReplayer) Stats() (records, applied uint64) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.records, sr.applied
}

// Run pulls records from `iter` and applies each one until the
// context is cancelled or the iterator returns io.EOF (the writer
// closed). Returns:
//   - nil when ctx is cancelled cleanly
//   - nil when the iterator hits io.EOF (writer closed or iterator
//     closed) — graceful end-of-stream is not an error
//   - the wrapped apply error on any per-record failure (the caller
//     decides whether to crash or retry; in v0 cmd/goopg start logs
//     and exits the standby because a divergence between primary
//     WAL and standby data files is unrecoverable without a fresh
//     base backup)
func (sr *StreamReplayer) Run(ctx context.Context, iter *RecordIterator) error {
	if iter == nil {
		return errors.New("wal: StreamReplayer.Run: nil iterator")
	}
	sr.mu.Lock()
	sr.iter = iter
	sr.mu.Unlock()
	for {
		rec, err := iter.Next(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			if errors.Is(err, io.EOF) || errors.Is(err, ErrClosed) {
				return nil
			}
			return fmt.Errorf("wal: stream replay read: %w", err)
		}
		// The recovery loop checks for a requested pause before each record,
		// and holds commit records for recovery_min_apply_delay
		// (xlogrecovery.c PerformWalRecovery). M0149-0013.
		sr.recoveryPausesHere(ctx)
		sr.recoveryApplyDelay(ctx, rec)
		sr.recoveryPausesHere(ctx)
		if err := verifyOverwriteContrecord(rec); err != nil {
			return err
		}
		applied, err := ApplyRecord(sr.mgr, rec)
		if err != nil {
			return fmt.Errorf("wal: stream replay record lsn[%d,%d]: %w", rec.StartLSN, rec.EndLSN, err)
		}
		// Notify the standby MVCC manager about commit/abort so that
		// replayed tuples become visible to queries (standby hot-read
		// visibility). The hook is called outside the replayer's mutex
		// because the mvcc.Manager has its own lock; holding both would
		// risk a deadlock with concurrent snapshot takers.
		sr.noteCatalogBlocks(rec)
		if sr.onSeqReplay != nil && rec.XLog != nil && rec.XLog.Header.Rmid == RmgrSeq &&
			rec.XLog.Header.Info&XLRRmgrInfoMask == xlogSeqLog {
			if b, ok := xlogBlockRefByID(rec.XLog, 0); ok {
				sr.onSeqReplay(b.Rel)
			}
		}
		finishesPrepared := false
		if rec.XLog != nil && rec.XLog.Header.Rmid == RmgrXact {
			switch rec.XLog.Header.Info & xlogXactOpMask {
			case xlogXactPrepare:
				if sr.onPrepareReplay != nil {
					sr.onPrepareReplay(rec.EndLSN, append([]byte(nil), rec.XLog.MainData...))
				}
			case xlogXactCommitPrepared, xlogXactAbortPrepared:
				finishesPrepared = true
			}
		}
		if xid, committed, ok := replayedXactInfo(rec); ok {
			if sr.onXactReplay != nil {
				// xact_redo_commit / xact_redo_abort mark the whole
				// transaction tree: the record's subtransactions first, then
				// the top-level xid (TransactionIdCommitTree). M0149-0010a.
				for _, sub := range replayedSubxacts(rec) {
					sr.onXactReplay(sub, committed)
				}
				sr.onXactReplay(xid, committed)
			}
			if finishesPrepared && sr.onPreparedFinish != nil {
				sr.onPreparedFinish(xid)
			}
			if committed && sr.onCatalogReplay != nil && len(sr.catalogDBs) > 0 {
				for db := range sr.catalogDBs {
					sr.onCatalogReplay(db)
				}
				sr.catalogDBs = nil
			}
		}
		// RecoveryRestartPoint (xlog.c): remember the checkpoint for the
		// checkpointer's next restartpoint. A legacy 1-byte marker carries
		// no CheckPoint struct and is not a restartpoint candidate.
		var cpRec ReplayedCheckpoint
		cpOK := false
		if isCheckpointRecord(rec) {
			if f, t, ok := decodeCheckPointStruct(checkpointStructOf(rec)); ok {
				cpRec, cpOK = ReplayedCheckpoint{StartLSN: rec.StartLSN, EndLSN: rec.EndLSN, Fields: f, Time: t}, true
			}
		}
		sr.mu.Lock()
		if cpOK {
			sr.lastCheckpoint, sr.hasCheckpoint = cpRec, true
		}
		sr.records++
		if applied {
			sr.applied++
		}
		if rec.EndLSN > sr.applyLSN {
			sr.applyLSN = rec.EndLSN
			sr.lastStart = rec.StartLSN
		}
		sr.mu.Unlock()
	}
}

func replayedXactInfo(rec Record) (storage.TransactionID, bool, bool) {
	if len(rec.Payload) > 0 {
		switch rec.Payload[0] {
		case RecordKindXactCommit:
			xid, err := DecodeXactMarker(rec.Payload)
			return xid, true, err == nil
		case RecordKindXactAbort:
			xid, err := DecodeXactMarker(rec.Payload)
			return xid, false, err == nil
		}
	}
	if rec.XLog == nil || rec.XLog.Header.Rmid != RmgrXact {
		return 0, false, false
	}
	switch rec.XLog.Header.Info & xlogXactOpMask {
	case xlogXactCommit:
		return storage.TransactionID(rec.XLog.Header.XID), true, true
	case xlogXactAbort:
		return storage.TransactionID(rec.XLog.Header.XID), false, true
	case xlogXactCommitPrepared, xlogXactAbortPrepared:
		// The finished transaction is the record's twophase xid, not the
		// header xid of the backend that ran COMMIT/ROLLBACK PREPARED.
		// M0149-0012.
		parsed, err := ParseXactRecord(rec.XLog.Header.Info, rec.XLog.MainData)
		if err != nil || parsed.TwophaseXid == 0 {
			return 0, false, false
		}
		return storage.TransactionID(parsed.TwophaseXid), rec.XLog.Header.Info&xlogXactOpMask == xlogXactCommitPrepared, true
	default:
		return 0, false, false
	}
}

// replayedSubxacts returns the subtransaction xids a PG commit or abort record
// lists (XACT_XINFO_HAS_SUBXACTS), or nil.
func replayedSubxacts(rec Record) []storage.TransactionID {
	if rec.XLog == nil || rec.XLog.Header.Rmid != RmgrXact {
		return nil
	}
	parsed, err := ParseXactRecord(rec.XLog.Header.Info, rec.XLog.MainData)
	if err != nil || len(parsed.Subxacts) == 0 {
		return nil
	}
	out := make([]storage.TransactionID, len(parsed.Subxacts))
	for i, x := range parsed.Subxacts {
		out[i] = storage.TransactionID(x)
	}
	return out
}
