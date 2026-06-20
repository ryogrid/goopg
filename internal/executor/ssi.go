package executor

import (
	"errors"

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
//
// Returns a non-nil error (SQLSTATE 40001) when this read closes a dangerous
// rw-conflict structure to an already-committed writer that goopg cannot abort,
// so the READER itself must die mid-statement — upstream's "Canceled on conflict
// out to pivot, during read" (predicate.c). The caller (the scan operator) must
// release any page lock it holds and return the error so the SELECT/UPDATE/DELETE
// statement aborts in place, marking the transaction failed (25P02). Deferred
// pivot/write-skew dooms (the WRITER is the victim) return nil here and surface
// at the writer's own COMMIT, exactly as before.
func ssiRecordTupleRead(ctx *Context, rel storage.RelFileNode, block storage.BlockNumber, slot uint16, writerXmin, writerXmax storage.TransactionID) error {
	if !ssiActive(ctx) {
		return nil
	}
	if block == storage.InvalidBlockNumber || slot == 0 {
		return nil
	}
	tag := mvcc.TupleLockTag(rel.DBOid, rel.RelOid, block, slot)
	ctx.TxnMgr.AcquirePredicateLock(ctx.Tx.Handle, tag)
	// Conflict-out against the inserter — handles the reader-after-write
	// shape (reader observes a concurrent SERIALIZABLE writer's new
	// tuple version directly).
	if err := ctx.TxnMgr.CheckForSerializableConflictOutReportingFailure(ctx.Tx.Handle, writerXmin); err != nil {
		return ssiReadAbortError(err)
	}
	// M0104-0008: conflict-out against the deleter/updater. When the
	// reader's snapshot hides a concurrent SERIALIZABLE writer's NEWER
	// version of this tuple, the visible OLD version still carries the
	// concurrent writer's XID in its xmax slot. Reading it must install
	// the reader→writer rw-edge so write-skew (e.g. simple-write-skew
	// spec) closes the 2-cycle at pre-commit. The Manager filters
	// InvalidXID / Bootstrap / Frozen and elides the self-modify case,
	// so an unconditional second check is safe when xmax != xmin.
	if writerXmax != writerXmin {
		if err := ctx.TxnMgr.CheckForSerializableConflictOutReportingFailure(ctx.Tx.Handle, writerXmax); err != nil {
			return ssiReadAbortError(err)
		}
	}
	return nil
}

// ssiReadAbortError converts the mvcc read-path serialization failure into the
// executor's wire-surfacing ExecError (SQLSTATE 40001). The primary message is
// the bare upstream errmsg that psql/isolationtester print; the reason rides in
// DETAIL (suppressed by isolationtester, surfaced by psql), mirroring
// ssiPreCommitCheck so the mid-statement and at-commit forms are byte-identical
// on the wire.
func ssiReadAbortError(err error) error {
	detail := ""
	var sfe *mvcc.SerializationFailureError
	if errors.As(err, &sfe) {
		detail = sfe.Detail()
	}
	return &ExecError{
		Code:    mvcc.SerializationFailureSQLState,
		Message: "could not serialize access due to read/write dependencies among transactions",
		Detail:  detail,
		Hint:    "The transaction might succeed if retried.",
	}
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
		// Surface the bare upstream errmsg as the primary message and carry the
		// reason code as DETAIL, exactly as predicate.c does. Inlining the
		// reason into the primary message (as the prior code did) diverged from
		// psql/isolationtester, which print only the errmsg line.
		detail, hint := "", ""
		if sfe, ok := err.(*mvcc.SerializationFailureError); ok {
			detail = sfe.Detail()
			hint = "The transaction might succeed if retried."
		}
		return &ExecError{
			Code:    "40001",
			Message: "could not serialize access due to read/write dependencies among transactions",
			Detail:  detail,
			Hint:    hint,
		}
	}
	return nil
}
