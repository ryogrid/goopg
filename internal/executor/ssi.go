package executor

import (
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// ssiActive reports whether the current ctx is running a SERIALIZABLE
// transaction with the mvcc registry wired in. All hook helpers in this file
// short-circuit on this guard so RC/RR (and bootstrap contexts that lack
// TxnMgr) pay nothing past the inline comparison.
func ssiActive(ctx *Context) bool {
	return ctx != nil &&
		ctx.TxnMgr != nil &&
		ctx.Tx.Isolation == mvcc.IsolationSerializable &&
		ctx.Tx.Handle != 0
}

// ssiRecordTupleRead is the executor-side hook for SERIALIZABLE reads. When
// a SERIALIZABLE reader observes a tuple at (rel, block, slot) produced by
// `writerXmin`, it:
//
//   1. Acquires a tuple-grain SIREAD predicate lock on (rel, block, slot)
//      via mvcc.Manager.AcquirePredicateLock so future SERIALIZABLE writers
//      that touch this tuple — or a covering page / relation — see the read.
//   2. Calls mvcc.Manager.CheckForSerializableConflictOut so any rw-edge
//      from this reader to the writer (when the writer is a concurrent
//      SERIALIZABLE xact) is installed in both peers' in/outConflict slices.
//
// Both manager calls short-circuit again for invalid/bootstrap/frozen and
// self xids and for RC/RR readers, so callers do not need to filter further.
// Block/slot sanity gating mirrors mvcc.TupleLockTag's invariants — calling
// it with InvalidBlockNumber or slot==0 panics, so we filter here.
func ssiRecordTupleRead(ctx *Context, rel storage.RelFileNode, block storage.BlockNumber, slot uint16, writerXmin storage.TransactionID) {
	if !ssiActive(ctx) {
		return
	}
	if block == storage.InvalidBlockNumber || slot == 0 {
		return
	}
	tag := mvcc.TupleLockTag(rel.DBOid, rel.RelOid, block, slot)
	ctx.TxnMgr.AcquirePredicateLock(ctx.Tx.Handle, tag)
	ctx.TxnMgr.CheckForSerializableConflictOut(ctx.Tx.Handle, writerXmin)
}

// ssiRecordTupleWrite is the executor-side hook for SERIALIZABLE writes.
// When a SERIALIZABLE writer modifies the tuple at (rel, block, slot), the
// hook walks the predicate-lock holder set on the exact tag plus every
// covering ancestor (tuple → page → relation) and installs rw-conflict
// edges R → W for every concurrent SERIALIZABLE reader covering the target.
//
// For INSERTs the tuple was just allocated, so the per-tuple holder set is
// guaranteed empty — but covering page/relation holders are still found.
// For UPDATE/DELETE the old tuple's (block, slot) is the canonical target.
func ssiRecordTupleWrite(ctx *Context, rel storage.RelFileNode, block storage.BlockNumber, slot uint16) {
	if !ssiActive(ctx) {
		return
	}
	if block == storage.InvalidBlockNumber || slot == 0 {
		return
	}
	tag := mvcc.TupleLockTag(rel.DBOid, rel.RelOid, block, slot)
	ctx.TxnMgr.CheckForSerializableConflictIn(ctx.Tx.Handle, tag)
}

// ssiPreCommitCheck is the executor-side wrapper over
// mvcc.Manager.PreCommitCheckForSerializationFailure. transactionOp.execCommit
// invokes it immediately before TxnMgr.Commit so a dangerous rw-cycle on the
// committing xact aborts the COMMIT path with SQLSTATE 40001 — the upstream
// "could not serialize access due to read/write dependencies among
// transactions" phrasing.
//
// Returns nil for RC/RR (no PreCommit walk required), for SERIALIZABLE xacts
// without an allocated handle (write-less and never registered), and when
// the underlying check observes no dangerous structure.
func ssiPreCommitCheck(ctx *Context, tx mvcc.Transaction) error {
	if tx.Isolation != mvcc.IsolationSerializable {
		return nil
	}
	if ctx == nil || ctx.TxnMgr == nil || tx.Handle == 0 {
		return nil
	}
	if err := ctx.TxnMgr.PreCommitCheckForSerializationFailure(tx.Handle); err != nil {
		return &ExecError{
			Code:    "40001",
			Message: "could not serialize access due to read/write dependencies among transactions: " + err.Error(),
		}
	}
	return nil
}
