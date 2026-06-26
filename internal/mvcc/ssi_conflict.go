package mvcc

import (
	"github.com/goopg/goopg/internal/storage"
)

// CheckForSerializableConflictOut is the read-path SSI hook: when a
// SERIALIZABLE reader observes that a tuple version it just read was
// inserted (or last modified) by another transaction, this records an
// rw-conflict edge R → W between the reader R and the writer W.
//
// The edge is the goopg analogue of PostgreSQL's
// `CheckForSerializableConflictOut` in
// `postgres/src/backend/storage/lmgr/predicate.c`. An rw-conflict (also
// called an anti-dependency) of R → W means R read a row before W
// wrote it, so any serial execution must order R before W. The edge is
// installed in both directions: R's `outConflicts` gains W, and W's
// `inConflicts` gains R. Storing both directions matches upstream's
// `RWConflict` linkage and lets M0104-0006's dangerous-structure check
// walk in either direction in O(deg(node)).
//
// The call is a no-op (returns false) when:
//
//   - readerHandle is not a SERIALIZABLE in-flight xact (RC/RR or
//     already finished — RC/RR cannot participate in SSI cycles, and
//     finished xacts are scrubbed of their edges at release);
//   - writerXID is `InvalidTransactionID`, `BootstrapTransactionID`,
//     or `FrozenTransactionID` (system writers cannot participate in
//     SSI cycles);
//   - writerXID matches the reader's own assigned XID (self-modify is
//     not a conflict);
//   - the writer is not in the SSI registry — either it is a non-
//     SERIALIZABLE writer (RC/RR) which by definition cannot
//     participate in SSI cycles, or it has already finished. The
//     first-slice substrate releases predicate locks and SSI
//     bookkeeping at finish, so finished-but-still-conflict-relevant
//     retention is M0104-0006's concern; for now the call is silently
//     a no-op against finished writers.
//
// Returns true iff a new edge was installed; existing edges
// (idempotent calls) return false. The duplicate check is O(out-degree
// of R), which is bounded by the small number of concurrent
// SERIALIZABLE writers in practice.
func (m *Manager) CheckForSerializableConflictOut(readerHandle TxnHandle, writerXID storage.TransactionID) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	return m.checkForSerializableConflictOutLocked(readerHandle, writerXID)
}

// Reason strings for the two mid-statement read-path serialization-failure
// sites, matching the errdetail_internal phrases in upstream
// CheckForSerializableConflictOut (predicate.c). isolationtester suppresses
// DETAIL, but psql/wire clients surface it, so keeping them verbatim preserves
// parity.
const (
	// reasonConflictDuringCheck is the entry "already doomed" abort
	// (predicate.c:4038) — a reader doomed by an earlier conflict-out check
	// (an earlier tuple in the same scan, or a peer's commit dooming this
	// reader as a pivot) must die before reading further.
	reasonConflictDuringCheck = "Canceled on identification as a pivot, during conflict out checking"
	// reasonConflictOutDuringRead is the OnConflict reader-kill abort
	// (predicate.c:4679) — the reader closed a dangerous structure to an
	// already-committed writer it cannot abort, so the reader must die now.
	reasonConflictOutDuringRead = "Canceled on conflict out to pivot, during read"
	// reasonPivotDuringWrite is the write-path pivot-doom abort
	// (predicate.c:4667) — a SERIALIZABLE writer's modification closed a
	// dangerous structure in which the writer is the pivot with an out-conflict
	// to an already-committed transaction, so the writer must die mid-statement.
	reasonPivotDuringWrite = "Canceled on identification as a pivot, during write"
)

// CheckForSerializableConflictOutReportingFailure performs the same read-path
// conflict-out check as CheckForSerializableConflictOut, but additionally
// reports — via a non-nil *SerializationFailureError — when the READER must
// abort its current statement immediately. It is goopg's analogue of the two
// mid-statement ereport(ERROR) sites in upstream CheckForSerializableConflictOut
// (predicate.c): the entry "already doomed" check (line 4032) and the
// OnConflict_CheckForSerializationFailure "Canceled on conflict out to pivot,
// during read" case (line 4676).
//
// The detection key is the reader's Doomed flag: it is set ONLY by the read-path
// reader-kill arm of onConflictCheckLocked (the write path dooms the WRITER, and
// PreCommit dooms the pivot at commit). So a reader that is doomed at entry, or
// becomes doomed across this call, is exactly the upstream mid-statement-abort
// reader; a doomed WRITER (the common pivot/write-skew shape) leaves this nil and
// surfaces at the writer's own PreCommit, exactly as before.
//
// Used by the executor read-path hook (ssiRecordTupleRead) to surface SQLSTATE
// 40001 on the offending SELECT/UPDATE/DELETE scan step rather than deferring it
// to the reader's COMMIT — the difference real PG 18.3 exhibits on, e.g., the
// total-cash isolation spec.
func (m *Manager) CheckForSerializableConflictOutReportingFailure(readerHandle TxnHandle, writerXID storage.TransactionID) error {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	// Entry doom check (predicate.c:4032-4040): a reader already doomed by an
	// earlier conflict-out check must die before reading anything further.
	if reader := m.ssiState.xacts[readerHandle]; reader != nil && reader.Doomed {
		return &SerializationFailureError{Reason: reasonConflictDuringCheck}
	}
	m.checkForSerializableConflictOutLocked(readerHandle, writerXID)
	// onConflictCheckLocked's reader-kill arm (writer already committed, so the
	// reader is the victim) just doomed the reader: surface it mid-read.
	if reader := m.ssiState.xacts[readerHandle]; reader != nil && reader.Doomed {
		return &SerializationFailureError{Reason: reasonConflictOutDuringRead}
	}
	return nil
}

func (m *Manager) checkForSerializableConflictOutLocked(readerHandle TxnHandle, writerXID storage.TransactionID) bool {
	if writerXID == storage.InvalidTransactionID ||
		writerXID == BootstrapTransactionID ||
		writerXID == FrozenTransactionID {
		return false
	}
	if m.ssiState.xacts == nil {
		return false
	}
	reader, ok := m.ssiState.xacts[readerHandle]
	if !ok || reader == nil {
		return false
	}
	if reader.XID == writerXID {
		// Self-modify: the reader is observing a tuple it wrote
		// itself. No conflict — same xact.
		return false
	}
	writer := m.serializableXactByXIDLocked(writerXID)
	if writer == nil {
		// Either a non-SERIALIZABLE writer (RC/RR), or a committed
		// SERIALIZABLE writer whose retention window has already closed
		// (purged from ssiState.finished). M0118-0001 retains committed
		// writers, so an in-window committed writer IS found above and the
		// edge installs; only genuinely unreachable writers fall here.
		return false
	}
	if writer == reader {
		// Belt-and-suspenders: serializableXactByXIDLocked already
		// finds writer by XID, but a read-only SERIALIZABLE xact has
		// XID == Invalid and would never match here. Keeping the
		// pointer-equality check defensive against future API
		// changes.
		return false
	}
	if reader.ReadOnly && writer.FinishedAt != InvalidCommitSeqNo {
		// De-facto READ ONLY conflict-out skip (predicate.c
		// CheckForSerializableConflictOut, lines 4123-4137): a declared READ
		// ONLY reader that reads data written by an already-committed writer
		// forms NO rw-conflict — it simply "appears to run first" — UNLESS the
		// writer itself has an rw-conflict OUT to a transaction that committed
		// before this reader's snapshot. Only then can the read-only reader
		// close a dangerous structure (R -> W -> T2 with T2 committed first and
		// in the reader's past). This is what reduces receipt-report from 48
		// aborts to the 6 genuine ones, and it MUST run before the edge is
		// recorded because Case 1 of onConflictCheckLocked would otherwise fire
		// the moment a READ ONLY reader touches any committed writer that holds
		// an out-conflict, regardless of snapshot ordering. The scan over live
		// outConflicts is goopg's analogue of upstream's
		// SXACT_FLAG_CONFLICT_OUT + earliestOutConflictCommit pair. M0118-0001.
		sustains := false
		for _, t2 := range writer.outConflicts {
			if committedBeforeSnapshot(t2, reader) {
				sustains = true
				break
			}
		}
		if !sustains {
			return false
		}
	}
	if !m.readerSnapshotSeesWriterAsConcurrentLocked(readerHandle, writerXID) {
		// M0118-0001: the writer committed before the reader's snapshot, so
		// the reader already sees its effects — this is an ordinary read
		// (wr-dependency), not an rw anti-dependency. Mirrors upstream's
		// `if (!XidIsConcurrent(xid)) return;` in CheckForSerializableConflictOut.
		// Before retention this case was masked because committed writers were
		// dropped from the registry; retaining them surfaces it, so the gate is
		// now load-bearing (e.g. two-ids reading committed D1 without conflict).
		return false
	}
	installed := registerRWConflictLocked(reader, writer)
	if installed {
		// M0118-0001: run the dangerous-structure check as the edge is
		// recorded (current xact = reader on the read path).
		m.onConflictCheckLocked(reader, writer, reader)
	}
	return installed
}

// serializableXactByXIDLocked returns the in-flight SerializableXact
// whose top-level XID equals xid, or nil if no such xact exists. The
// scan is O(active SERIALIZABLE xacts); upstream uses an OID-keyed
// hash table, but goopg's in-flight set is small enough (bounded by
// max_connections) that a linear scan is well under the cost of an
// extra map.
//
// Callers must hold m.ssiMu.
func (m *Manager) serializableXactByXIDLocked(xid storage.TransactionID) *SerializableXact {
	if xid == storage.InvalidTransactionID || m.ssiState.xacts == nil {
		return nil
	}
	for _, sx := range m.ssiState.xacts {
		if sx != nil && sx.XID == xid {
			return sx
		}
	}
	// M0118-0001: a reader may observe a tuple written by a SERIALIZABLE
	// writer that has already COMMITTED. The committed writer is retained
	// in ssiState.finished (not the active map) so its rw-edges remain
	// reachable for the dangerous-structure check; scan it here so the
	// read-path conflict-out edge to a committed writer still installs.
	for _, sx := range m.ssiState.finished {
		if sx != nil && sx.XID == xid {
			return sx
		}
	}
	return nil
}

// readerSnapshotSeesWriterAsConcurrentLocked reports whether writerXID ran
// concurrently with the reader's pinned SERIALIZABLE snapshot — i.e. the
// snapshot does not already see writerXID's commit. It is the goopg analogue of
// the `XidIsConcurrent(xid)` gate in upstream CheckForSerializableConflictOut:
// a writer the reader already sees as committed yields no rw-conflict.
//
// The reader's snapshot lives in its proc slot (firstSnap), pinned at the
// reader's first statement; this hook runs on the reader's own goroutine, so
// the field is stable. A nil snapshot (no statement has pinned one yet)
// conservatively reports concurrent=true so the gate never introduces a false
// negative.
//
// Caller must hold m.ssiMu.
func (m *Manager) readerSnapshotSeesWriterAsConcurrentLocked(readerHandle TxnHandle, writerXID storage.TransactionID) bool {
	procNum := int32(readerHandle) - 1
	if procNum < 0 || int(procNum) >= len(m.procArray.slots) {
		return true
	}
	snap := m.procArray.slots[procNum].firstSnap
	if snap == nil {
		return true
	}
	return snap.XidIsConcurrent(writerXID)
}

// registerRWConflictLocked installs an rw-conflict edge from → to in
// both directions: from.outConflicts gains to, and to.inConflicts
// gains from. Returns true iff the edge was new; an existing edge is
// a no-op (idempotent).
//
// Caller must hold m.ssiMu (the slices are not goroutine-safe — they're
// embedded in SerializableXact, which is only mutated under m.ssiMu).
func registerRWConflictLocked(from, to *SerializableXact) bool {
	for _, peer := range from.outConflicts {
		if peer == to {
			return false
		}
	}
	from.outConflicts = append(from.outConflicts, to)
	to.inConflicts = append(to.inConflicts, from)
	return true
}

// onConflictCheckLocked is goopg's analogue of PostgreSQL's
// OnConflict_CheckForSerializationFailure (predicate.c). It runs the moment a
// new rw-conflict edge reader -> writer is recorded and decides whether that
// edge completes a dangerous structure that mandates an abort. `current` is the
// xact executing the hook — the reader on the read path
// (CheckForSerializableConflictOut), the writer on the write path
// (CheckForSerializableConflictIn).
//
// Three structures are checked, mirroring the upstream cases:
//
//	Case 1  R -> W -> T2   W and T2 both committed (W carries ConflictOut):
//	        the reader R closes the structure by reading W's data.
//	Case 2  R -> W -> T2   W is the pivot with a committed out-conflict T2
//	        that committed first.
//	Case 3  T0 -> R -> W    R is the pivot and the writer W has committed.
//
// Declared READ ONLY transactions ARE modeled (M0118-0001, receipt-report):
// Cases 2 and 3 apply upstream's de-facto READ ONLY refinement via
// committedBeforeSnapshot so a READ ONLY participant only sustains a dangerous
// structure when the relevant peer committed before its snapshot. Two-phase
// PREPARE is still not modeled, so a finished (FinishedAt != Invalid) xact is
// treated as both prepared and committed, with FinishedAt serving as both
// prepareSeqNo and commitSeqNo.
//
// goopg DEFERS the upstream mid-statement ereport: rather than aborting the
// current read/write in place, it sets the SXACT_FLAG_DOOMED equivalent
// (Doomed) on the transaction that upstream would cancel, which then fails at
// its own PreCommit with SQLSTATE 40001. The aborted transaction is the same
// one upstream chooses; only WHERE the error surfaces differs (at COMMIT rather
// than mid-statement) for the read/write-cancel cases. Pivot-doom cases (the
// common write-skew / read-only-anomaly shape) are deferred to commit upstream
// too, so they match exactly.
func (m *Manager) onConflictCheckLocked(reader, writer, current *SerializableXact) {
	if reader == nil || writer == nil {
		return
	}

	writerCommitted := writer.FinishedAt != InvalidCommitSeqNo
	failure := false

	// Case 1: committed writer already flagged with a conflict out. Since the
	// writer has committed, the current xact must be the reader closing the
	// R -> W -> T2 structure.
	if writerCommitted && writer.ConflictOut {
		failure = true
	}

	// Case 2: the writer has become a pivot with an out-conflict to a committed
	// transaction T2 that committed first (no anomaly if the reader or writer
	// committed before T2). The de-facto READ ONLY refinement (predicate.c):
	// when the reader R is declared READ ONLY, R can only close a dangerous
	// structure if T2 committed before R's snapshot — otherwise R's snapshot
	// already excludes T2's effects and no anomaly is possible. This is the sole
	// reason receipt-report has 6 true failures rather than 48.
	if !failure {
		for _, t2 := range writer.outConflicts {
			if t2 == nil {
				continue
			}
			if t2.FinishedAt != InvalidCommitSeqNo {
				if (reader.FinishedAt == InvalidCommitSeqNo || t2.FinishedAt <= reader.FinishedAt) &&
					(writer.FinishedAt == InvalidCommitSeqNo || t2.FinishedAt <= writer.FinishedAt) &&
					(!reader.ReadOnly || committedBeforeSnapshot(t2, reader)) {
					failure = true
					break
				}
				continue
			}
			// M0118-0009: T2 is PREPARED but not yet committed (same-backend
			// 2PC). Upstream gates this branch on SxactIsPrepared(t2) and
			// compares against t2->prepareSeqNo (predicate.c lines 4591-4601):
			// "T2 has already checked for conflicts, so if it commits first,
			// making the above conflict real, it's too late for it to abort."
			if t2.Prepared {
				if (reader.FinishedAt == InvalidCommitSeqNo || t2.PrepareSeqNo <= reader.FinishedAt) &&
					(writer.FinishedAt == InvalidCommitSeqNo || t2.PrepareSeqNo <= writer.FinishedAt) &&
					(!reader.ReadOnly || (reader.SnapshotSeqNo != InvalidCommitSeqNo && t2.PrepareSeqNo <= reader.SnapshotSeqNo)) {
					failure = true
					break
				}
			}
		}
	}

	// Case 3: the reader has become a pivot with a committed writer (no anomaly
	// if every in-conflict partner T0 committed before the writer). A declared
	// READ ONLY reader can never be the pivot — it writes nothing, so no peer can
	// rw-conflict INTO it — and upstream gates the whole block on
	// !SxactIsReadOnly(reader). The READ ONLY refinement on T0 (predicate.c):
	// a READ ONLY T0 only sustains the structure if the writer committed before
	// T0's snapshot.
	if !failure && writerCommitted && !reader.ReadOnly {
		for _, t0 := range reader.inConflicts {
			if t0 == nil || t0.Doomed {
				continue
			}
			if (t0.FinishedAt == InvalidCommitSeqNo || t0.FinishedAt >= writer.FinishedAt) &&
				(!t0.ReadOnly || committedBeforeSnapshot(writer, t0)) {
				failure = true
				break
			}
		}
	}

	// Case 3, PREPARED writer (M0118-0009): identical to the committed-writer
	// case above but the writer is PREPARED-but-not-committed (same-backend 2PC).
	// Upstream gates Case 3 on SxactIsPrepared(writer) — which is true for both a
	// committed and a merely-prepared writer — and compares t0 against
	// writer->prepareSeqNo (predicate.c lines 4618-4647). This is what dooms the
	// reader-pivot the moment it forms an rw-edge to a writer that has already
	// PREPAREd (the prepared-transactions.spec r2-after-p3 case).
	if !failure && !writerCommitted && writer.Prepared && !reader.ReadOnly {
		for _, t0 := range reader.inConflicts {
			if t0 == nil || t0.Doomed {
				continue
			}
			if (t0.FinishedAt == InvalidCommitSeqNo || t0.FinishedAt >= writer.PrepareSeqNo) &&
				(!t0.ReadOnly || (t0.SnapshotSeqNo != InvalidCommitSeqNo && t0.SnapshotSeqNo >= writer.PrepareSeqNo)) {
				failure = true
				break
			}
		}
	}

	if !failure {
		return
	}

	// Pick the victim exactly as upstream does, then doom it (deferred abort).
	switch {
	case current == writer:
		// We are the pivot writer: "Canceled on identification as a pivot,
		// during write." Upstream aborts us in place; we doom ourselves.
		writer.Doomed = true
	case writerCommitted || writer.Prepared:
		// The writer has already committed OR is PREPARED (same-backend 2PC,
		// M0118-0009) and cannot be aborted, so the reader (the current xact)
		// must die: upstream's `else if (SxactIsPrepared(writer))` arm, "Canceled
		// on conflict out to pivot, during read." SxactIsPrepared is set on a
		// committed xact too, hence both committed and prepared select the reader.
		reader.Doomed = true
	default:
		// Normal case: kill the in-flight pivot writer so a retry of the
		// failing xact can make progress.
		writer.Doomed = true
	}
}

// committedBeforeSnapshot reports whether peer committed strictly before
// observer took its first statement snapshot. It is goopg's analogue of the
// upstream test `peer.commitSeqNo <= observer.SeqNo.lastCommitBeforeSnapshot`
// (predicate.c): FinishedAt (the commit stamp) and SnapshotSeqNo (the snapshot
// watermark) both draw from the single nextCommitSeqNo counter, so a commit
// that happened before the snapshot was taken necessarily got a strictly
// smaller stamp. Returns false if peer never committed or observer never took a
// snapshot (InvalidCommitSeqNo), which is the conservative READ ONLY answer —
// "the peer's effects are not in my snapshot". M0118-0001 (receipt-report).
func committedBeforeSnapshot(peer, observer *SerializableXact) bool {
	if peer == nil || observer == nil {
		return false
	}
	return peer.FinishedAt != InvalidCommitSeqNo &&
		observer.SnapshotSeqNo != InvalidCommitSeqNo &&
		peer.FinishedAt < observer.SnapshotSeqNo
}

// removeSerializableXactFromPeersLocked scrubs every reference to
// dying from the in/out-conflict slices of every peer it points at.
// Called from releaseSerializableLocked so the surviving peers do
// not retain dangling pointers once dying is removed from
// ssiState.xacts.
//
// Caller must hold m.ssiMu.
func removeSerializableXactFromPeersLocked(dying *SerializableXact) {
	for _, peer := range dying.outConflicts {
		peer.inConflicts = removeSerializableXactFromSlice(peer.inConflicts, dying)
	}
	for _, peer := range dying.inConflicts {
		peer.outConflicts = removeSerializableXactFromSlice(peer.outConflicts, dying)
	}
}

// removeSerializableXactFromSlice returns slice with all entries
// equal to target removed in place (preserving order is not required;
// callers use these slices as sets). Returns the same backing array
// to avoid an allocation; the unused tail is zeroed so the GC can
// reclaim the released SerializableXact when the dying xact is the
// last reference.
func removeSerializableXactFromSlice(slice []*SerializableXact, target *SerializableXact) []*SerializableXact {
	out := slice[:0]
	for _, sx := range slice {
		if sx == target {
			continue
		}
		out = append(out, sx)
	}
	for i := len(out); i < len(slice); i++ {
		slice[i] = nil
	}
	return out
}

// OutConflictCount returns the number of rw-conflict out-edges held
// by handle. Diagnostic helper for tests and future tooling; the
// pre-commit dangerous-structure check (M0104-0006) walks the slices
// directly. Returns 0 for non-SERIALIZABLE or unknown handles.
func (m *Manager) OutConflictCount(handle TxnHandle) int {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return 0
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return 0
	}
	return len(sx.outConflicts)
}

// InConflictCount returns the number of rw-conflict in-edges held by
// handle. Mirror of OutConflictCount on the receiving side. Returns 0
// for non-SERIALIZABLE or unknown handles.
func (m *Manager) InConflictCount(handle TxnHandle) int {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return 0
	}
	sx, ok := m.ssiState.xacts[handle]
	if !ok || sx == nil {
		return 0
	}
	return len(sx.inConflicts)
}

// CheckForSerializableConflictIn is the write-path SSI hook: when a
// SERIALIZABLE writer modifies a target identified by tag, this records
// an rw-conflict edge R → W for every SERIALIZABLE reader that holds a
// SIREAD predicate-lock covering the target. The edge orientation
// matches the read-path hook (`reader.outConflicts += writer`,
// `writer.inConflicts += reader`); only the discovery polarity differs
// — the write-path walks the predicate-lock holder set instead of
// looking up the writer by xmin/xmax.
//
// The call is the goopg analogue of PostgreSQL's
// `CheckForSerializableConflictIn` in
// `postgres/src/backend/storage/lmgr/predicate.c`. Coverage discovery
// matches upstream's "walk upward" pattern: holders on the exact tag,
// plus holders on any ancestor tag that covers it (page covers tuple,
// relation covers page and tuple). The substrate's covering map
// (`PredicateLockTag.Covers`) defines the hierarchy; the hook does not
// re-derive coverage rules.
//
// The call is a no-op (returns false) when:
//
//   - tag is invalid (Rel == 0);
//   - writerHandle is not a SERIALIZABLE in-flight xact (RC/RR or
//     already finished — RC/RR cannot participate in SSI cycles, and
//     finished writers are scrubbed of their edges at release);
//   - no holder is found on the exact or any covering tag (a frequent
//     hot-path case — most writes touch tags no concurrent reader
//     covers).
//
// Returns true iff at least one new edge was installed; existing edges
// (idempotent calls and self-references) do not count toward the
// return value. Per-holder duplicate checks are O(out-degree of the
// reader), bounded by the small number of concurrent SERIALIZABLE
// writers in practice.
func (m *Manager) CheckForSerializableConflictIn(writerHandle TxnHandle, tag PredicateLockTag) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	return m.checkForSerializableConflictInLocked(writerHandle, tag)
}

// CheckForSerializableConflictInReportingFailure performs the same write-path
// conflict-in check as CheckForSerializableConflictIn, but additionally reports
// — via a non-nil *SerializationFailureError — when the WRITER must abort its
// current statement immediately. It is goopg's analogue of the mid-statement
// ereport(ERROR) in upstream CheckForSerializableConflictIn (predicate.c:4667,
// "Canceled on identification as a pivot, during write.").
//
// The detection key is the writer's Doomed flag becoming newly set across this
// call: on the write path onConflictCheckLocked dooms the writer (current ==
// writer) only via its Case 2 — the writer is a pivot with an out-conflict to an
// already-committed transaction that committed first. That is exactly the
// upstream mid-statement-abort writer. A writer that was already doomed on entry
// would have failed at the earlier statement that doomed it, and a deferred pivot
// doom (partner still in flight) never sets the flag here and still surfaces at
// the writer's own PreCommit — so this stays nil for the common write-skew shape.
//
// Used by the executor write-path hook (ssiRecordTupleWrite) to surface SQLSTATE
// 40001 on the offending INSERT/UPDATE/DELETE/MERGE step rather than deferring it
// to the writer's COMMIT — the difference real PG 18.3 exhibits on, e.g., the
// project-manager and classroom-scheduling isolation specs where one session has
// already committed before the other's conflicting write.
func (m *Manager) CheckForSerializableConflictInReportingFailure(writerHandle TxnHandle, tag PredicateLockTag) error {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	writer := m.ssiState.xacts[writerHandle]
	wasDoomed := writer != nil && writer.Doomed
	m.checkForSerializableConflictInLocked(writerHandle, tag)
	if writer != nil && writer.Doomed && !wasDoomed {
		return &SerializationFailureError{Reason: reasonPivotDuringWrite}
	}
	return nil
}

// CheckTableForSerializableConflictIn is the relation-wide analogue of
// CheckForSerializableConflictIn: the WRITER is performing a logical mass
// delete/rewrite of the ENTIRE relation (TRUNCATE, DROP, or REFRESH
// MATERIALIZED VIEW), which logically destroys every row any reader saw. It
// mirrors upstream's CheckTableForSerializableConflictIn (predicate.c:4419):
// every SERIALIZABLE reader holding a predicate lock of ANY granularity
// (relation / page / tuple) on (db, rel) gets an rw-conflict R -> W installed.
//
// Unlike checkForSerializableConflictInLocked — which walks UPWARD from a
// single tuple/page tag to its covering ancestors — this scans the global
// target registry for every tag matching (db, rel) regardless of granularity,
// because the writer is not touching one tuple but obliterating the whole
// heap. It is the only conflict-in path that finds a reader holding only a
// fine-grained (tuple/page) SIREAD when the writer never produced a matching
// fine-grained write tag (the REFRESH MATERIALIZED VIEW case, whose
// truncate+rematerialize writes the heap via the low-level writeHeapRow path
// that bypasses ssiRecordTupleWrite).
func (m *Manager) CheckTableForSerializableConflictIn(writerHandle TxnHandle, db, rel uint32) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	return m.checkTableForSerializableConflictInLocked(writerHandle, db, rel)
}

// CheckTableForSerializableConflictInReportingFailure is the
// mid-statement-abort-reporting variant of CheckTableForSerializableConflictIn,
// matching CheckForSerializableConflictInReportingFailure: it returns a non-nil
// *SerializationFailureError only when this write newly dooms the WRITER as a
// pivot with an out-conflict to an already-committed transaction. The common
// deferred-pivot case (the conflicting partner is still in flight) returns nil
// and surfaces at the partner's COMMIT.
func (m *Manager) CheckTableForSerializableConflictInReportingFailure(writerHandle TxnHandle, db, rel uint32) error {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	writer := m.ssiState.xacts[writerHandle]
	wasDoomed := writer != nil && writer.Doomed
	m.checkTableForSerializableConflictInLocked(writerHandle, db, rel)
	if writer != nil && writer.Doomed && !wasDoomed {
		return &SerializationFailureError{Reason: reasonPivotDuringWrite}
	}
	return nil
}

func (m *Manager) checkForSerializableConflictInLocked(writerHandle TxnHandle, tag PredicateLockTag) bool {
	if tag.Granularity() == InvalidPredicateGranularity {
		return false
	}
	if m.ssiState.xacts == nil {
		return false
	}
	writer, ok := m.ssiState.xacts[writerHandle]
	if !ok || writer == nil {
		return false
	}
	if m.predicateLocks.targets == nil {
		return false
	}
	installed := false
	covering := coveringPredicateLockTags(tag)
	for _, ancestor := range covering {
		tgt, ok := m.predicateLocks.targets[ancestor]
		if !ok {
			continue
		}
		for holder := range tgt.holders {
			if holder == writerHandle {
				// A SERIALIZABLE xact may legitimately hold a SIREAD on
				// a target it then writes; that is not a conflict with
				// itself.
				continue
			}
			reader, ok := m.ssiState.xacts[holder]
			if !ok || reader == nil {
				// Defensive: the holder slot should never outlive the
				// SerializableXact in ssiState.xacts because
				// releaseSerializableLocked drops predicate locks
				// before clearing the registry entry.
				continue
			}
			if registerRWConflictLocked(reader, writer) {
				installed = true
				// M0118-0001: run the dangerous-structure check as the
				// edge is recorded (current xact = writer on the write
				// path).
				m.onConflictCheckLocked(reader, writer, writer)
			}
		}
	}

	// M0118-0001: also walk RETAINED COMMITTED readers. A reader that
	// predicate-locked a target and then COMMITTED is kept in ssiState.finished
	// with its owned-tag set intact (releaseSerializableLocked detaches a
	// committed xact from the GLOBAL holder sets but does not null its
	// predicateLocks). This write must still form the R -> W rw-edge against
	// such a reader — the INSERT/UPDATE-after-the-partner-committed shape that
	// closes the project-manager / classroom-scheduling dangerous structures,
	// where the reader is gone from the global holder sets above. The committed
	// reader is found here by scanning its own predicateLocks for a covering
	// tag. Only a reader whose lifetime OVERLAPS this writer can still close a
	// dangerous structure (reader.FinishedAt > writer.BeginAt); a reader that
	// committed at or before this writer began is serialization-order-consistent
	// and purgeFinishedSerializableLocked will drop it.
	for _, reader := range m.ssiState.finished {
		if reader == nil || reader == writer || len(reader.predicateLocks) == 0 {
			continue
		}
		if reader.FinishedAt <= writer.BeginAt {
			continue
		}
		holdsCovering := false
		for _, ancestor := range covering {
			if _, ok := reader.predicateLocks[ancestor]; ok {
				holdsCovering = true
				break
			}
		}
		if !holdsCovering {
			continue
		}
		if registerRWConflictLocked(reader, writer) {
			installed = true
			m.onConflictCheckLocked(reader, writer, writer)
		}
	}
	return installed
}

// checkTableForSerializableConflictInLocked is the ssiMu-held body of
// CheckTableForSerializableConflictIn. See that method's contract.
func (m *Manager) checkTableForSerializableConflictInLocked(writerHandle TxnHandle, db, rel uint32) bool {
	if m.ssiState.xacts == nil {
		return false
	}
	writer, ok := m.ssiState.xacts[writerHandle]
	if !ok || writer == nil {
		return false
	}
	installed := false

	// Live holders: any predicate-lock target on (db, rel) at ANY granularity.
	if m.predicateLocks.targets != nil {
		for tag, tgt := range m.predicateLocks.targets {
			if tag.DB != db || tag.Rel != rel {
				continue
			}
			for holder := range tgt.holders {
				if holder == writerHandle {
					// A writer that also read the relation does not
					// conflict with itself.
					continue
				}
				reader, ok := m.ssiState.xacts[holder]
				if !ok || reader == nil {
					continue
				}
				if registerRWConflictLocked(reader, writer) {
					installed = true
					m.onConflictCheckLocked(reader, writer, writer)
				}
			}
		}
	}

	// Retained COMMITTED readers (see checkForSerializableConflictInLocked's
	// second loop): a reader that predicate-locked the relation and then
	// committed is kept in ssiState.finished with its owned-tag set intact.
	// This relation-wide write must still form R -> W against such a reader
	// when their lifetimes overlap (FinishedAt > writer.BeginAt). The reader
	// is found by scanning its own predicateLocks for ANY tag on (db, rel).
	for _, reader := range m.ssiState.finished {
		if reader == nil || reader == writer || len(reader.predicateLocks) == 0 {
			continue
		}
		if reader.FinishedAt <= writer.BeginAt {
			continue
		}
		holds := false
		for tag := range reader.predicateLocks {
			if tag.DB == db && tag.Rel == rel {
				holds = true
				break
			}
		}
		if !holds {
			continue
		}
		if registerRWConflictLocked(reader, writer) {
			installed = true
			m.onConflictCheckLocked(reader, writer, writer)
		}
	}
	return installed
}

// coveringPredicateLockTags returns tag itself plus every coarser
// ancestor that, by the substrate's coverage hierarchy, would also
// imply SIREAD on tag. The list is the upward walk
// `tuple → page → relation`; finer descendants are deliberately not
// included because a writer touching a coarser target than the reader
// owns is conceptually rare in goopg's heap workload (writes are
// tuple-level), and PostgreSQL's `CheckForSerializableConflictIn`
// follows the same upward-only pattern via `GetParentPredicateLockTag`.
//
// Output ordering is finest-first; callers iterate the slice
// linearly so the iteration order is observable in tests but never
// load-bearing for correctness — `registerRWConflictLocked` is
// idempotent.
func coveringPredicateLockTags(tag PredicateLockTag) []PredicateLockTag {
	switch tag.Granularity() {
	case TupleGranularity:
		return []PredicateLockTag{
			tag,
			PageLockTag(tag.DB, tag.Rel, tag.Page),
			RelationLockTag(tag.DB, tag.Rel),
		}
	case PageGranularity:
		return []PredicateLockTag{
			tag,
			RelationLockTag(tag.DB, tag.Rel),
		}
	case RelationGranularity:
		return []PredicateLockTag{tag}
	default:
		return nil
	}
}

// HasRWConflict reports whether an rw-conflict edge from → to has
// been recorded. Diagnostic helper used by tests; production callers
// should not consult this number directly (use the conflict-graph
// walker that lands with M0104-0006).
func (m *Manager) HasRWConflict(from, to TxnHandle) bool {
	m.ssiMu.Lock()
	defer m.ssiMu.Unlock()
	if m.ssiState.xacts == nil {
		return false
	}
	fromSX, ok := m.ssiState.xacts[from]
	if !ok || fromSX == nil {
		return false
	}
	toSX, ok := m.ssiState.xacts[to]
	if !ok || toSX == nil {
		return false
	}
	for _, peer := range fromSX.outConflicts {
		if peer == toSX {
			return true
		}
	}
	return false
}
