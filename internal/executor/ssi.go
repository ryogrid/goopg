package executor

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"

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
	return ssiConflictOutOnWriters(ctx, writerXmin, writerXmax)
}

// ssiConflictOutTupleRead records ONLY the read→writer conflict-out edges for a
// tuple a SERIALIZABLE reader observed, WITHOUT acquiring a tuple-grain SIREAD
// predicate lock. It is the variant used by hash index-only scans, whose
// phantom coverage comes from a bucket-grain page lock on the INDEX
// (ssiRecordHashBucketRead), not the heap. Acquiring per-tuple heap SIREAD
// locks there would coarsen to a heap-page lock (max_pred_locks_per_page = 2)
// and then spuriously conflict with a DIFFERENT-bucket INSERT that merely lands
// on the same heap page — exactly the false positive predicate-hash checks for.
// PG's index-only scan likewise takes no heap predicate lock; it relies on the
// hash bucket page lock (predicate.c / hash.c PredicateLockPage). The conflict-
// out walk is still required so the write-before-read ordering (reader observes
// an in-flight same-bucket inserter's invisible row) closes the structure.
func ssiConflictOutTupleRead(ctx *Context, writerXmin, writerXmax storage.TransactionID) error {
	if !ssiActive(ctx) {
		return nil
	}
	return ssiConflictOutOnWriters(ctx, writerXmin, writerXmax)
}

// ssiConflictOutOnWriters runs the reader→writer conflict-out checks shared by
// ssiRecordTupleRead and ssiConflictOutTupleRead: against the inserter (xmin —
// the reader-after-write shape) and, when distinct, the deleter/updater (xmax —
// the M0104-0008 write-skew edge where the reader sees a row an in-flight
// SERIALIZABLE peer has DELETEd/UPDATEd). The Manager filters InvalidXID /
// Bootstrap / Frozen and elides the self-modify case.
func ssiConflictOutOnWriters(ctx *Context, writerXmin, writerXmax storage.TransactionID) error {
	if err := ctx.TxnMgr.CheckForSerializableConflictOutReportingFailure(ctx.Tx.Handle, writerXmin); err != nil {
		return ssiReadAbortError(err)
	}
	if writerXmax != writerXmin {
		if err := ctx.TxnMgr.CheckForSerializableConflictOutReportingFailure(ctx.Tx.Handle, writerXmax); err != nil {
			return ssiReadAbortError(err)
		}
	}
	return nil
}

// ssiHashBucket maps an encoded index key to a stable pseudo-bucket page number,
// emulating PG's hash-index bucket assignment for page-level predicate locking
// (PredicateLockPage on the bucket's primary page in _hash_first /
// _hash_doinsert). goopg stores every index physically as a B-tree (no hash
// buckets), so the bucket is derived deterministically from the encoded key
// bytes: equal keys always map to the same bucket; distinct keys collide only
// with ~2^-31 probability. A collision merely re-introduces a false-positive
// conflict (the safe over-abort direction), never a missed one. Masked to 31
// bits so the result is never InvalidBlockNumber (0xFFFFFFFF), which
// mvcc.PageLockTag rejects.
func ssiHashBucket(key []byte) storage.BlockNumber {
	h := fnv.New32a()
	_, _ = h.Write(key)
	return storage.BlockNumber(h.Sum32() & 0x7FFFFFFF)
}

// ssiRecordHashBucketRead acquires a bucket-grain (page) SIREAD predicate lock
// on a hash index for a SERIALIZABLE equality scan, keyed by the index OID and
// the pseudo-bucket of the probe key. This is the phantom mechanism for hash
// indexes: a later SERIALIZABLE INSERT of a row whose indexed value hashes to
// the same bucket (ssiRecordHashIndexInsert) finds this reader via the
// conflict-in walk and forms the rw-edge, while an INSERT into a DIFFERENT
// bucket does not — reproducing PG's reduced false positives (predicate-hash
// spec, "page level predicate locking in hash index"). The index OID is used as
// the predicate-lock relation so these page tags never collide with the heap
// relation's tuple/page locks.
func ssiRecordHashBucketRead(ctx *Context, dbOid, indexOID uint32, key []byte) {
	if !ssiActive(ctx) || indexOID == 0 || len(key) == 0 {
		return
	}
	tag := mvcc.PageLockTag(dbOid, indexOID, ssiHashBucket(key))
	ctx.TxnMgr.AcquirePredicateLock(ctx.Tx.Handle, tag)
}

// ssiRecordHashIndexInsert is the SERIALIZABLE write-path hook for INSERTs into
// a table carrying one or more hash indexes. For each hash index it computes the
// inserted row's bucket page tag (matching ssiRecordHashBucketRead's keying via
// the shared encodeBTreeKeyForColumn encoder used by encodeIndexKeyFromCols) and
// runs the conflict-in walk, installing an rw-edge from every SERIALIZABLE
// reader holding that bucket's SIREAD — and aborting this INSERT in place
// (40001) when it closes a dangerous structure to an already-committed pivot,
// exactly like ssiRecordTupleWrite. No-op for non-hash tables / RC / RR.
func ssiRecordHashIndexInsert(ctx *Context, tbl *catalog.Table, cols []catalog.Column, row Row, dbOid uint32) error {
	if !ssiActive(ctx) || tbl == nil {
		return nil
	}
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		if !idx.DeclaredHash {
			continue
		}
		key, err := encodeIndexKeyFromCols(idx, cols, row, ctx.Catalog)
		if err != nil || len(key) == 0 {
			continue
		}
		tag := mvcc.PageLockTag(dbOid, idx.OID, ssiHashBucket(key))
		if cerr := ctx.TxnMgr.CheckForSerializableConflictInReportingFailure(ctx.Tx.Handle, tag); cerr != nil {
			return ssiReadAbortError(cerr)
		}
	}
	return nil
}

// ssiGistCellSize is the side length (in point-coordinate units) of one cell in
// the synthetic grid that stands in for GiST leaf-page granularity. goopg has no
// native GiST access method — a `USING gist` index is catalog-only (no physical
// tree), so a SERIALIZABLE spatial scan falls back to a seq scan. To reproduce
// PG's PAGE-level GiST predicate locking (predicate-gist spec, "reduced false
// positives") the scan instead locks the grid cell of each matching point: two
// scans of disjoint spatial regions lock disjoint cells (no false conflict),
// while a scan and an INSERT that touch the same spatial neighbourhood share a
// cell (true rw-conflict). The base data (point(g*10,g*10), g=1..1000) is dense
// at this resolution, so every queried sub-region's boundary cells are populated
// — an INSERT landing in a read region always collides with a locked cell.
const ssiGistCellSize = 256.0

// ssiGistGridCell maps a point's (x, y) to a stable pseudo-page number for a
// grid-cell predicate lock. The cell index is the FNV hash of the integer grid
// coordinates (floor(x/cell), floor(y/cell)): equal cells always map to the same
// page; distinct cells collide only with ~2^-31 probability (a collision merely
// re-introduces a false-positive conflict — the safe over-abort direction).
// Masked to 31 bits so the result is never InvalidBlockNumber, which
// mvcc.PageLockTag rejects. FNV (rather than a packed encoding) keeps the result
// bounded for arbitrarily large/negative coordinates; spatial adjacency is not
// needed because only EXACT cell identity must match between a read tuple and a
// later insert.
func ssiGistGridCell(x, y float64) storage.BlockNumber {
	cx := int64(math.Floor(x / ssiGistCellSize))
	cy := int64(math.Floor(y / ssiGistCellSize))
	h := fnv.New32a()
	var b [16]byte
	binary.LittleEndian.PutUint64(b[0:8], uint64(cx))
	binary.LittleEndian.PutUint64(b[8:16], uint64(cy))
	_, _ = h.Write(b[:])
	return storage.BlockNumber(h.Sum32() & 0x7FFFFFFF)
}

// ssiGistIndexForTable returns the OID of a GiST index on tbl and the position
// of its (first) key column within cols, or ok=false when tbl carries no usable
// GiST index. Used by a SERIALIZABLE seq scan to decide whether to switch from
// relation-grain to grid-cell SIREAD locking (design 0118-0137). Expression
// (non-column) GiST keys are unsupported and skipped.
func ssiGistIndexForTable(ctx *Context, tbl *catalog.Table, cols []catalog.Column) (idxOID uint32, colIdx int, ok bool) {
	if ctx == nil || ctx.Catalog == nil || tbl == nil {
		return 0, 0, false
	}
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		if idx == nil || idx.Method != "gist" || len(idx.Columns) == 0 || idx.Columns[0] == "" {
			continue
		}
		for i := range cols {
			if cols[i].Name == idx.Columns[0] {
				return idx.OID, i, true
			}
		}
	}
	return 0, 0, false
}

// ssiRecordGistGridRead acquires a grid-cell (page) SIREAD predicate lock on a
// GiST index for a SERIALIZABLE spatial scan, keyed by the index OID and the
// grid cell of a matching point. The index OID is used as the predicate-lock
// relation so these page tags never collide with the heap relation's tuple/page
// locks. A later SERIALIZABLE INSERT of a point in the same cell
// (ssiRecordGistIndexInsert) finds this reader via the conflict-in walk and
// forms the rw-edge; an INSERT in a different cell does not (design 0118-0137).
func ssiRecordGistGridRead(ctx *Context, dbOid, indexOID uint32, x, y float64) {
	if !ssiActive(ctx) || indexOID == 0 {
		return
	}
	tag := mvcc.PageLockTag(dbOid, indexOID, ssiGistGridCell(x, y))
	ctx.TxnMgr.AcquirePredicateLock(ctx.Tx.Handle, tag)
}

// ssiRecordGistIndexInsert is the SERIALIZABLE write-path hook for INSERTs into
// a table carrying one or more GiST indexes. For each GiST index it computes the
// inserted point's grid-cell page tag (matching ssiRecordGistGridRead's keying)
// and runs the conflict-in walk, installing an rw-edge from every SERIALIZABLE
// reader holding that cell's SIREAD — and aborting this INSERT in place (40001)
// when it closes a dangerous structure to an already-committed pivot, exactly
// like ssiRecordHashIndexInsert. No-op for non-GiST tables / RC / RR.
func ssiRecordGistIndexInsert(ctx *Context, tbl *catalog.Table, cols []catalog.Column, row Row, dbOid uint32) error {
	if !ssiActive(ctx) || tbl == nil {
		return nil
	}
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl) {
		if idx == nil || idx.Method != "gist" || len(idx.Columns) == 0 || idx.Columns[0] == "" {
			continue
		}
		colIdx := -1
		for i := range cols {
			if cols[i].Name == idx.Columns[0] {
				colIdx = i
				break
			}
		}
		if colIdx < 0 || colIdx >= len(row) || row[colIdx].Kind != KindString {
			continue
		}
		pt, pok := parsePointText(row[colIdx].StringValue())
		if !pok {
			continue
		}
		tag := mvcc.PageLockTag(dbOid, idx.OID, ssiGistGridCell(pt[0], pt[1]))
		if cerr := ctx.TxnMgr.CheckForSerializableConflictInReportingFailure(ctx.Tx.Handle, tag); cerr != nil {
			return ssiReadAbortError(cerr)
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
