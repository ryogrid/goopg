package executor

import (
	"errors"

	"github.com/goopg/goopg/internal/catalog"
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
//  1. Acquires a tuple-grain SIREAD predicate lock on (rel, block, slot)
//     via mvcc.Manager.AcquirePredicateLock so future SERIALIZABLE writers
//     that touch this tuple — or a covering page / relation — see the read.
//  2. Calls mvcc.Manager.CheckForSerializableConflictOut so any rw-edge
//     from this reader to the writer (when the writer is a concurrent
//     SERIALIZABLE xact) is installed in both peers' in/outConflict slices.
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

// ssiRecordRelationRead is the executor-side hook for a SERIALIZABLE
// *sequential* scan. Mirroring upstream heap_beginscan (heapam.c:1150-1171),
// a seq scan in a serializable transaction acquires a relation-grain SIREAD
// predicate lock on the whole table:
//
//	"For seqscan and sample scans in a serializable transaction, acquire a
//	 predicate lock on the entire relation. This is required not only to lock
//	 all the matching tuples, but also to conflict with new insertions into
//	 the table. ... in a heap scan there is nothing more fine-grained to lock."
//
// This is the PHANTOM mechanism: per-tuple SIREAD locks (ssiRecordTupleRead)
// only cover rows that already exist, so a concurrent SERIALIZABLE writer that
// INSERTs a row matching the scan's qual would form no rw-edge — the
// false-negative behind the project-manager and classroom-scheduling specs.
// The coarse relation lock makes the later INSERT's conflict-in walk
// (tuple → page → relation) find this reader, closing the dangerous structure.
// Per-tuple conflict-out detection still runs from ssiRecordTupleRead during
// the scan; the per-tuple AcquirePredicateLock there becomes a no-op once this
// coarser lock is held (AcquirePredicateLock prunes finer covered tags).
//
// Upstream gates this via SerializationNeededForRead /
// PredicateLockingNeededForRelation, which excludes ONLY system catalogs
// (OID < FirstUnpinnedObjectId) and temp (local-buffer) relations — NOT
// materialized views, which are ordinary heaps for SSI purposes. The
// system-catalog gate lives here; the caller elides this coarse relation-grain
// lock for temp / matview relations (it holds the catalog.Table). A matview
// scan still acquires per-tuple SIREAD locks via ssiRecordTupleRead, and a
// concurrent REFRESH MATERIALIZED VIEW conflicts against those through the
// relation-wide ssiRecordTableWrite hook (matview-write-skew spec).
func ssiRecordRelationRead(ctx *Context, rel storage.RelFileNode) {
	if !ssiActive(ctx) {
		return
	}
	if catalog.IsSystemRelation(rel.RelOid) {
		return
	}
	tag := mvcc.RelationLockTag(rel.DBOid, rel.RelOid)
	ctx.TxnMgr.AcquirePredicateLock(ctx.Tx.Handle, tag)
}

// ssiRecordInvisibleTupleRead is the executor-side SSI hook for a tuple that a
// SERIALIZABLE scan physically encounters but that is NOT visible to the
// reader's snapshot because a CONCURRENT transaction inserted it (its xmin is
// still in flight, or committed after the reader's snapshot). Upstream's
// HeapCheckForSerializableConflictOut (heapam.c:9254) runs for every tuple the
// scan examines, visible or not: in the not-visible cases it forms the
// rw-antidependency reader → inserter using the tuple's xmin. This is the
// second half of the PHANTOM mechanism — it catches the INSERT-before-READ
// ordering, where the inserting writer ran *before* the reader acquired its
// relation predicate lock, so ssiRecordRelationRead's lock alone cannot see it
// (the reader was not yet a predicate-lock holder when the INSERT's
// conflict-in walk ran).
//
// No predicate lock is acquired: the relation-level SIREAD from
// ssiRecordRelationRead already covers the whole table, and an invisible tuple
// is not "read" by this transaction. The Manager filters aborted / bootstrap /
// frozen xmins and the writer-committed-before-our-snapshot (wr-dependency)
// case, so passing xmin unconditionally is safe. A non-nil error means the read
// closed a dangerous structure to an already-committed writer and the reader
// must abort mid-statement (40001), exactly like ssiRecordTupleRead.
func ssiRecordInvisibleTupleRead(ctx *Context, rel storage.RelFileNode, xmin storage.TransactionID) error {
	if !ssiActive(ctx) {
		return nil
	}
	if catalog.IsSystemRelation(rel.RelOid) {
		return nil
	}
	if err := ctx.TxnMgr.CheckForSerializableConflictOutReportingFailure(ctx.Tx.Handle, xmin); err != nil {
		return ssiReadAbortError(err)
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
// covering ancestor (tuple → page → relation) — and the retained-committed
// reader set — and installs rw-conflict edges R → W for every SERIALIZABLE
// reader covering the target.
//
// For INSERTs the tuple was just allocated, so the per-tuple holder set is
// guaranteed empty — but covering page/relation holders (and committed readers
// holding a relation-level SIREAD from a seq scan) are still found. For
// UPDATE/DELETE the old tuple's (block, slot) is the canonical target.
//
// M0118-0001: returns a non-nil error (SQLSTATE 40001) when this write closes a
// dangerous structure in which the writer is the pivot with an out-conflict to
// an ALREADY-COMMITTED transaction — the writer must abort mid-statement
// (upstream's "Canceled on identification as a pivot, during write"). The caller
// (the storage write operator) must propagate the error so the
// INSERT/UPDATE/DELETE/MERGE step fails in place, marking the transaction failed
// (25P02). Deferred pivot/write-skew dooms (partner still in flight) return nil
// here and surface at the writer's own COMMIT, exactly as before.
func ssiRecordTupleWrite(ctx *Context, rel storage.RelFileNode, block storage.BlockNumber, slot uint16) error {
	if !ssiActive(ctx) {
		return nil
	}
	if block == storage.InvalidBlockNumber || slot == 0 {
		return nil
	}
	tag := mvcc.TupleLockTag(rel.DBOid, rel.RelOid, block, slot)
	if err := ctx.TxnMgr.CheckForSerializableConflictInReportingFailure(ctx.Tx.Handle, tag); err != nil {
		return ssiReadAbortError(err)
	}
	return nil
}

// ssiRecordTableWrite is the executor-side SSI hook for a relation-wide
// logical mass delete/rewrite — TRUNCATE, DROP, or REFRESH MATERIALIZED VIEW.
// Mirroring upstream CheckTableForSerializableConflictIn (predicate.c), it
// installs an rw-conflict R -> W from EVERY SERIALIZABLE reader holding a
// predicate lock of ANY granularity (relation / page / tuple) on the relation,
// because the rewrite logically destroys every row those readers saw.
//
// Why REFRESH MATERIALIZED VIEW needs this and a per-tuple write hook would
// not suffice: execRefreshMatView truncates the matview heap and re-populates
// it through the low-level writeHeapRow path, which — unlike the INSERT
// operator — never calls ssiRecordTupleWrite. So the refresh produces no
// per-tuple write tag, and ssiRecordTupleWrite's UPWARD (tuple -> page ->
// relation) conflict-in walk could never find a concurrent reader's
// fine-grained SIREAD on the matview. The relation-wide scan here finds it
// regardless of granularity, closing the s2_read -> s1_refresh dangerous
// structure (matview-write-skew spec, M0118-0001).
//
// Returns 40001 only when this write newly dooms the WRITER itself as a pivot
// with an out-conflict to an already-committed transaction (mirrors
// ssiRecordTupleWrite); the common deferred-pivot case returns nil and surfaces
// at the partner's COMMIT.
func ssiRecordTableWrite(ctx *Context, rel storage.RelFileNode) error {
	if !ssiActive(ctx) {
		return nil
	}
	if catalog.IsSystemRelation(rel.RelOid) {
		return nil
	}
	if err := ctx.TxnMgr.CheckTableForSerializableConflictInReportingFailure(ctx.Tx.Handle, rel.DBOid, rel.RelOid); err != nil {
		return ssiReadAbortError(err)
	}
	return nil
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
