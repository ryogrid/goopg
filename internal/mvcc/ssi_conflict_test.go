package mvcc

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// beginAndAssign starts a SERIALIZABLE transaction and materialises its
// XID so the test can simulate the writer-side perspective without
// having to drive a real write through the storage engine.
func beginAndAssign(t *testing.T, m *Manager) Transaction {
	t.Helper()
	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatalf("AssignXID: %v", err)
	}
	tx.XID = xid
	return tx
}

func TestCheckForSerializableConflictOut_RegistersEdgeBetweenSerializable(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("CheckForSerializableConflictOut returned false; expected new edge")
	}
	if got := m.OutConflictCount(reader.Handle); got != 1 {
		t.Fatalf("reader.outConflicts = %d, want 1", got)
	}
	if got := m.InConflictCount(writer.Handle); got != 1 {
		t.Fatalf("writer.inConflicts = %d, want 1", got)
	}
	if !m.HasRWConflict(reader.Handle, writer.Handle) {
		t.Fatal("HasRWConflict(reader→writer) = false, want true")
	}
	if m.HasRWConflict(writer.Handle, reader.Handle) {
		t.Fatal("HasRWConflict(writer→reader) = true, want false (edge is directed)")
	}
}

func TestCheckForSerializableConflictOut_IdempotentEdgeInstall(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("first call returned false")
	}
	if m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("second call returned true; expected idempotent no-op")
	}
	if got := m.OutConflictCount(reader.Handle); got != 1 {
		t.Fatalf("reader.outConflicts = %d, want 1 (no duplicate edge)", got)
	}
	if got := m.InConflictCount(writer.Handle); got != 1 {
		t.Fatalf("writer.inConflicts = %d, want 1 (no duplicate edge)", got)
	}
}

func TestCheckForSerializableConflictOut_NoOpForRCReader(t *testing.T) {
	m := NewManager()
	rc, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin RC: %v", err)
	}
	writer := beginAndAssign(t, m)
	if m.CheckForSerializableConflictOut(rc.Handle, writer.XID) {
		t.Fatal("RC reader registered a conflict edge; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d after RC reader, want 0", got)
	}
}

func TestCheckForSerializableConflictOut_NoOpForRRReader(t *testing.T) {
	m := NewManager()
	rr, err := m.Begin(IsolationRepeatableRead)
	if err != nil {
		t.Fatalf("Begin RR: %v", err)
	}
	writer := beginAndAssign(t, m)
	if m.CheckForSerializableConflictOut(rr.Handle, writer.XID) {
		t.Fatal("RR reader registered a conflict edge; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d after RR reader, want 0", got)
	}
}

func TestCheckForSerializableConflictOut_NoOpForRCWriter(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	rc, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin RC: %v", err)
	}
	rcXID, err := m.AssignXID(rc)
	if err != nil {
		t.Fatalf("AssignXID RC: %v", err)
	}
	if m.CheckForSerializableConflictOut(reader.Handle, rcXID) {
		t.Fatal("registered conflict edge against RC writer; expected no-op")
	}
	if got := m.OutConflictCount(reader.Handle); got != 0 {
		t.Fatalf("reader.outConflicts = %d, want 0 (RC writer is not SSI-trackable)", got)
	}
}

func TestCheckForSerializableConflictOut_NoOpForSelfXID(t *testing.T) {
	m := NewManager()
	tx := beginAndAssign(t, m)
	if m.CheckForSerializableConflictOut(tx.Handle, tx.XID) {
		t.Fatal("self-XID registered an edge; expected no-op")
	}
	if got := m.OutConflictCount(tx.Handle); got != 0 {
		t.Fatalf("self.outConflicts = %d, want 0", got)
	}
}

func TestCheckForSerializableConflictOut_NoOpForReservedXIDs(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	for _, xid := range []storage.TransactionID{
		storage.InvalidTransactionID,
		BootstrapTransactionID,
		FrozenTransactionID,
	} {
		if m.CheckForSerializableConflictOut(reader.Handle, xid) {
			t.Fatalf("reserved writer xid %d registered an edge; expected no-op", xid)
		}
	}
	if got := m.OutConflictCount(reader.Handle); got != 0 {
		t.Fatalf("reader.outConflicts = %d, want 0", got)
	}
}

// TestCheckForSerializableConflictOut_EdgeToRetainedCommittedWriter asserts the
// M0118-0001 retention behaviour: a SERIALIZABLE writer that has COMMITTED while
// a concurrent reader is still in-flight is retained (ssiState.finished), so a
// reader that subsequently observes the committed writer's data still installs
// the reader -> writer rw-edge. This is the inverse of the pre-M0118 contract,
// where a finished writer's bookkeeping was dropped and the edge was silently
// lost (defeating multi-xact dangerous-structure detection, e.g. two-ids).
func TestCheckForSerializableConflictOut_EdgeToRetainedCommittedWriter(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	writerXID := writer.XID
	// Reader is still in-flight, so the committed writer overlaps it and is
	// retained rather than purged.
	if err := m.Commit(writer); err != nil {
		t.Fatalf("Commit writer: %v", err)
	}
	if !m.CheckForSerializableConflictOut(reader.Handle, writerXID) {
		t.Fatal("retained committed writer did not register an edge; want edge installed")
	}
	if got := m.OutConflictCount(reader.Handle); got != 1 {
		t.Fatalf("reader.outConflicts = %d, want 1", got)
	}
	// No dangerous structure here (reader has no in-conflict, writer no
	// out-conflict), so the reader must not have been doomed.
	if m.IsDoomedForTest(reader.Handle) {
		t.Fatal("reader doomed by a benign edge to a committed writer")
	}
}

// TestCheckForSerializableConflictOutReportingFailure_ReaderKillMidRead asserts
// the M0118-0001 mid-statement abort: when an in-flight reader closes a dangerous
// structure R -> W -> T2 to an already-COMMITTED writer W that carries a
// conflict-out (W committed after T2 committed first), the reader is the pivot's
// victim and the read-path hook must surface SQLSTATE 40001 IN PLACE — upstream's
// "Canceled on conflict out to pivot, during read" (predicate.c). This is the
// total-cash spec's distinguishing behaviour vs the deferred pivot-writer doom.
func TestCheckForSerializableConflictOutReportingFailure_ReaderKillMidRead(t *testing.T) {
	m := NewManager()
	// The eventual victim stays in-flight throughout so retention keeps the
	// committed W and T2 reachable.
	reader := beginAndAssign(t, m)
	t2 := beginAndAssign(t, m)
	w := beginAndAssign(t, m)
	// W reads T2's data: W -> T2 edge.
	if !m.CheckForSerializableConflictOut(w.Handle, t2.XID) {
		t.Fatal("W->T2 edge not installed")
	}
	// T2 commits first, then W commits carrying the conflict-out flag.
	if err := m.Commit(t2); err != nil {
		t.Fatalf("commit t2: %v", err)
	}
	if err := m.Commit(w); err != nil {
		t.Fatalf("commit w: %v", err)
	}
	// The in-flight reader now reads W's committed data, closing R -> W -> T2.
	err := m.CheckForSerializableConflictOutReportingFailure(reader.Handle, w.XID)
	if err == nil {
		t.Fatal("expected a mid-statement serialization failure, got nil")
	}
	if !IsSerializationFailure(err) {
		t.Fatalf("error is not a SerializationFailureError: %v", err)
	}
	if !m.IsDoomedForTest(reader.Handle) {
		t.Fatal("reader was not doomed by the reader-kill")
	}
	// Entry-doom path: a subsequent read by the now-doomed reader must abort
	// immediately, before any conflict logic — even against an unrelated xid.
	if err := m.CheckForSerializableConflictOutReportingFailure(reader.Handle, t2.XID); err == nil {
		t.Fatal("already-doomed reader's next read did not abort (entry-doom check)")
	}
}

// TestCheckForSerializableConflictOutReportingFailure_DeferredPivotReturnsNil is
// the safety counterpart: when the dangerous structure makes the IN-FLIGHT writer
// the pivot (R -> W -> T2 with W not yet committed), upstream dooms the writer and
// defers the abort to the writer's COMMIT. The read-path hook must therefore
// return nil — the reader keeps running. This is the two-ids / simple-write-skew
// shape, and surfacing it mid-read here would regress those passing specs.
func TestCheckForSerializableConflictOutReportingFailure_DeferredPivotReturnsNil(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	t2 := beginAndAssign(t, m)
	w := beginAndAssign(t, m)
	// W -> T2 edge, then T2 commits first; W stays in-flight (the pivot).
	if !m.CheckForSerializableConflictOut(w.Handle, t2.XID) {
		t.Fatal("W->T2 edge not installed")
	}
	if err := m.Commit(t2); err != nil {
		t.Fatalf("commit t2: %v", err)
	}
	// Reader reads the in-flight W's data, closing R -> W -> T2 with W as the
	// in-flight pivot.
	if err := m.CheckForSerializableConflictOutReportingFailure(reader.Handle, w.XID); err != nil {
		t.Fatalf("deferred pivot-writer doom must NOT surface mid-read: %v", err)
	}
	if !m.IsDoomedForTest(w.Handle) {
		t.Fatal("in-flight pivot writer was not doomed")
	}
	if m.IsDoomedForTest(reader.Handle) {
		t.Fatal("reader was wrongly doomed in the deferred case")
	}
}

// TestCheckForSerializableConflictOutReportingFailure_ReaderKillTwoCycle
// reproduces the total-cash spec's distinguishing structure at the Manager
// level: a 2-cycle s1 -> s2 -> s1 where s1 has COMMITTED. When the in-flight s2
// reads s1's data (closing s2 -> s1), s2 becomes the pivot of T0(s1) -> s2 ->
// W(s1) with the writer committed, so Case 3 of onConflictCheckLocked must doom
// the reader and the read-path hook must surface 40001 mid-statement.
func TestCheckForSerializableConflictOutReportingFailure_ReaderKillTwoCycle(t *testing.T) {
	m := NewManager()
	s1 := beginAndAssign(t, m)
	s2 := beginAndAssign(t, m)
	// s1 -> s2 edge (s1 read s2's data, or s2 wrote what s1 read).
	if !m.CheckForSerializableConflictOut(s1.Handle, s2.XID) {
		t.Fatal("s1->s2 edge not installed")
	}
	// s1 commits while s2 is still in-flight: s1 is retained, edge intact.
	if err := m.Commit(s1); err != nil {
		t.Fatalf("commit s1: %v", err)
	}
	// s2 now reads s1's committed data, closing s2 -> s1 (the 2-cycle).
	err := m.CheckForSerializableConflictOutReportingFailure(s2.Handle, s1.XID)
	if err == nil {
		t.Fatal("expected mid-statement serialization failure for the 2-cycle pivot reader, got nil")
	}
	if !m.IsDoomedForTest(s2.Handle) {
		t.Fatal("pivot reader s2 was not doomed")
	}
}

func TestCheckForSerializableConflictOut_NoOpForUnknownReader(t *testing.T) {
	m := NewManager()
	writer := beginAndAssign(t, m)
	// Use a handle that never existed. CheckForSerializableConflictOut
	// must not panic and must not allocate edges into thin air.
	if m.CheckForSerializableConflictOut(TxnHandle(99999), writer.XID) {
		t.Fatal("unknown reader handle registered an edge; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d, want 0", got)
	}
}

// TestSerializableXact_PeerEdgesRetainedThenScrubbed asserts the M0118-0001
// retention lifecycle: committing the writer while the reader is still in-flight
// RETAINS the edge (the committed writer stays reachable for dangerous-structure
// detection); the edge is only scrubbed once the overlapping reader also
// finishes and the retained writer is purged.
func TestSerializableXact_PeerEdgesRetainedThenScrubbed(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("CheckForSerializableConflictOut returned false")
	}

	// Capture the still-live reader pointer so we can inspect its
	// outConflicts slice after the writer is released. Manager removes
	// finished xacts from ssiState.xacts so we can't ask via the public API
	// once the writer is gone.
	readerSX := m.SerializableXact(reader.Handle)
	if readerSX == nil {
		t.Fatal("reader SerializableXact = nil before commit")
	}
	if len(readerSX.outConflicts) != 1 {
		t.Fatalf("readerSX.outConflicts pre-commit = %d, want 1", len(readerSX.outConflicts))
	}

	// Commit the writer: reader is still in-flight, so the writer overlaps it
	// and the edge is retained, NOT scrubbed.
	if err := m.Commit(writer); err != nil {
		t.Fatalf("Commit writer: %v", err)
	}
	m.ssiMu.Lock()
	got := len(readerSX.outConflicts)
	m.ssiMu.Unlock()
	if got != 1 {
		t.Fatalf("readerSX.outConflicts after writer Commit = %d, want 1 (retained while reader overlaps)", got)
	}

	// Finish the reader: no active xact remains, so the retained writer is
	// purged and the edge is finally scrubbed.
	if err := m.Commit(reader); err != nil {
		t.Fatalf("Commit reader: %v", err)
	}
	m.ssiMu.Lock()
	got = len(readerSX.outConflicts)
	m.ssiMu.Unlock()
	if got != 0 {
		t.Fatalf("readerSX.outConflicts after reader Commit = %d, want 0 (purged)", got)
	}
}

func TestSerializableXact_PeerEdgesScrubbedOnAbort(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("CheckForSerializableConflictOut returned false")
	}

	writerSX := m.SerializableXact(writer.Handle)
	if writerSX == nil {
		t.Fatal("writer SerializableXact = nil before abort")
	}
	if len(writerSX.inConflicts) != 1 {
		t.Fatalf("writerSX.inConflicts pre-abort = %d, want 1", len(writerSX.inConflicts))
	}

	if err := m.Rollback(reader); err != nil {
		t.Fatalf("Rollback reader: %v", err)
	}

	m.ssiMu.Lock()
	got := len(writerSX.inConflicts)
	m.ssiMu.Unlock()
	if got != 0 {
		t.Fatalf("writerSX.inConflicts after reader Rollback = %d, want 0 (peer scrub)", got)
	}
}

func TestCheckForSerializableConflictOut_MultiplePeersDistinctEdges(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	w1 := beginAndAssign(t, m)
	w2 := beginAndAssign(t, m)
	w3 := beginAndAssign(t, m)

	for _, w := range []Transaction{w1, w2, w3} {
		if !m.CheckForSerializableConflictOut(reader.Handle, w.XID) {
			t.Fatalf("CheckForSerializableConflictOut for writer XID %d returned false", w.XID)
		}
	}
	if got := m.OutConflictCount(reader.Handle); got != 3 {
		t.Fatalf("reader.outConflicts = %d, want 3", got)
	}
	for _, w := range []Transaction{w1, w2, w3} {
		if got := m.InConflictCount(w.Handle); got != 1 {
			t.Fatalf("writer %d inConflicts = %d, want 1", w.XID, got)
		}
		if !m.HasRWConflict(reader.Handle, w.Handle) {
			t.Fatalf("HasRWConflict(reader, writer %d) = false, want true", w.XID)
		}
	}
}

func TestCheckForSerializableConflictIn_RegistersEdgeForExactSIREADHolder(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	tag := TupleLockTag(1, 16384, 7, 3)
	if !m.AcquirePredicateLock(reader.Handle, tag) {
		t.Fatal("AcquirePredicateLock for reader returned false")
	}
	if !m.CheckForSerializableConflictIn(writer.Handle, tag) {
		t.Fatal("CheckForSerializableConflictIn returned false; expected new edge")
	}
	if got := m.OutConflictCount(reader.Handle); got != 1 {
		t.Fatalf("reader.outConflicts = %d, want 1", got)
	}
	if got := m.InConflictCount(writer.Handle); got != 1 {
		t.Fatalf("writer.inConflicts = %d, want 1", got)
	}
	if !m.HasRWConflict(reader.Handle, writer.Handle) {
		t.Fatal("HasRWConflict(reader→writer) = false, want true")
	}
	if m.HasRWConflict(writer.Handle, reader.Handle) {
		t.Fatal("HasRWConflict(writer→reader) = true, want false (edge is directed)")
	}
}

func TestCheckForSerializableConflictIn_IdempotentEdgeInstall(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	tag := TupleLockTag(1, 16384, 7, 3)
	if !m.AcquirePredicateLock(reader.Handle, tag) {
		t.Fatal("AcquirePredicateLock for reader returned false")
	}
	if !m.CheckForSerializableConflictIn(writer.Handle, tag) {
		t.Fatal("first call returned false")
	}
	if m.CheckForSerializableConflictIn(writer.Handle, tag) {
		t.Fatal("second call returned true; expected idempotent no-op")
	}
	if got := m.OutConflictCount(reader.Handle); got != 1 {
		t.Fatalf("reader.outConflicts = %d, want 1 (no duplicate edge)", got)
	}
	if got := m.InConflictCount(writer.Handle); got != 1 {
		t.Fatalf("writer.inConflicts = %d, want 1 (no duplicate edge)", got)
	}
}

func TestCheckForSerializableConflictIn_FiresOnPageLockHoldingForTupleWrite(t *testing.T) {
	// Reader covers a whole page; writer modifies a single tuple on
	// that page. The substrate must walk upward from the writer's
	// tuple tag and discover the page-level holder.
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	pageTag := PageLockTag(1, 16384, 9)
	if !m.AcquirePredicateLock(reader.Handle, pageTag) {
		t.Fatal("AcquirePredicateLock(page) returned false")
	}
	tupleTag := TupleLockTag(1, 16384, 9, 5)
	if !m.CheckForSerializableConflictIn(writer.Handle, tupleTag) {
		t.Fatal("CheckForSerializableConflictIn returned false; expected page-level holder to fire")
	}
	if !m.HasRWConflict(reader.Handle, writer.Handle) {
		t.Fatal("HasRWConflict(reader→writer) = false, want true")
	}
}

func TestCheckForSerializableConflictIn_FiresOnRelationLockHoldingForTupleWrite(t *testing.T) {
	// Reader covers a whole relation; writer modifies a tuple. The
	// upward walk must discover the relation-level holder.
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	relTag := RelationLockTag(1, 16384)
	if !m.AcquirePredicateLock(reader.Handle, relTag) {
		t.Fatal("AcquirePredicateLock(relation) returned false")
	}
	tupleTag := TupleLockTag(1, 16384, 9, 5)
	if !m.CheckForSerializableConflictIn(writer.Handle, tupleTag) {
		t.Fatal("CheckForSerializableConflictIn returned false; expected relation-level holder to fire")
	}
	if !m.HasRWConflict(reader.Handle, writer.Handle) {
		t.Fatal("HasRWConflict(reader→writer) = false, want true")
	}
}

func TestCheckForSerializableConflictIn_NoOpForFinerDescendantHolder(t *testing.T) {
	// Reader holds a tuple-level lock; writer writes a different
	// tuple on the same page. The upward walk for writer's tuple tag
	// should not surface the unrelated reader tuple lock.
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(reader.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("AcquirePredicateLock for reader returned false")
	}
	if m.CheckForSerializableConflictIn(writer.Handle, TupleLockTag(1, 16384, 9, 7)) {
		t.Fatal("conflict registered against unrelated reader tuple; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d, want 0", got)
	}
}

func TestCheckForSerializableConflictIn_NoOpForDifferentRelation(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(reader.Handle, RelationLockTag(1, 16384)) {
		t.Fatal("AcquirePredicateLock for reader returned false")
	}
	// Writer touches a different relation in the same database. The
	// substrate's coverage hierarchy must keep these disjoint.
	if m.CheckForSerializableConflictIn(writer.Handle, TupleLockTag(1, 16385, 9, 5)) {
		t.Fatal("conflict registered against unrelated relation; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d, want 0", got)
	}
}

func TestCheckForSerializableConflictIn_NoOpForRCWriter(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	tag := TupleLockTag(1, 16384, 7, 3)
	if !m.AcquirePredicateLock(reader.Handle, tag) {
		t.Fatal("AcquirePredicateLock for reader returned false")
	}
	rc, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin RC: %v", err)
	}
	if m.CheckForSerializableConflictIn(rc.Handle, tag) {
		t.Fatal("RC writer registered a conflict edge; expected no-op")
	}
	if got := m.OutConflictCount(reader.Handle); got != 0 {
		t.Fatalf("reader.outConflicts = %d after RC writer, want 0", got)
	}
}

func TestCheckForSerializableConflictIn_NoOpForSelfHolder(t *testing.T) {
	// A SERIALIZABLE xact may legitimately read a tuple (acquiring a
	// SIREAD lock) and then write it — that is not a self-conflict.
	m := NewManager()
	tx := beginAndAssign(t, m)
	tag := TupleLockTag(1, 16384, 7, 3)
	if !m.AcquirePredicateLock(tx.Handle, tag) {
		t.Fatal("AcquirePredicateLock for tx returned false")
	}
	if m.CheckForSerializableConflictIn(tx.Handle, tag) {
		t.Fatal("self-write registered an edge; expected no-op")
	}
	if got := m.OutConflictCount(tx.Handle); got != 0 {
		t.Fatalf("self.outConflicts = %d, want 0", got)
	}
	if got := m.InConflictCount(tx.Handle); got != 0 {
		t.Fatalf("self.inConflicts = %d, want 0", got)
	}
}

func TestCheckForSerializableConflictIn_NoOpForInvalidTag(t *testing.T) {
	m := NewManager()
	writer := beginAndAssign(t, m)
	if m.CheckForSerializableConflictIn(writer.Handle, PredicateLockTag{}) {
		t.Fatal("invalid tag registered an edge; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d, want 0", got)
	}
}

func TestCheckForSerializableConflictIn_NoOpForUnknownWriter(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	tag := TupleLockTag(1, 16384, 7, 3)
	if !m.AcquirePredicateLock(reader.Handle, tag) {
		t.Fatal("AcquirePredicateLock for reader returned false")
	}
	if m.CheckForSerializableConflictIn(TxnHandle(99999), tag) {
		t.Fatal("unknown writer handle registered an edge; expected no-op")
	}
	if got := m.OutConflictCount(reader.Handle); got != 0 {
		t.Fatalf("reader.outConflicts = %d, want 0", got)
	}
}

func TestCheckForSerializableConflictIn_NoHoldersIsSilentNoOp(t *testing.T) {
	// Hot path: most writes touch tags no concurrent reader covers.
	// The hook must return false without allocating edges.
	m := NewManager()
	writer := beginAndAssign(t, m)
	if m.CheckForSerializableConflictIn(writer.Handle, TupleLockTag(1, 16384, 7, 3)) {
		t.Fatal("no-holder write registered an edge; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d, want 0", got)
	}
}

func TestCheckForSerializableConflictIn_MultipleReadersDistinctEdges(t *testing.T) {
	// One write, three concurrent SERIALIZABLE readers covering it
	// at different granularities. All three must receive the edge.
	m := NewManager()
	r1 := beginAndAssign(t, m)
	r2 := beginAndAssign(t, m)
	r3 := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(r1.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("AcquirePredicateLock(r1) returned false")
	}
	if !m.AcquirePredicateLock(r2.Handle, PageLockTag(1, 16384, 9)) {
		t.Fatal("AcquirePredicateLock(r2) returned false")
	}
	if !m.AcquirePredicateLock(r3.Handle, RelationLockTag(1, 16384)) {
		t.Fatal("AcquirePredicateLock(r3) returned false")
	}
	if !m.CheckForSerializableConflictIn(writer.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("CheckForSerializableConflictIn returned false; expected three new edges")
	}
	if got := m.InConflictCount(writer.Handle); got != 3 {
		t.Fatalf("writer.inConflicts = %d, want 3", got)
	}
	for _, r := range []Transaction{r1, r2, r3} {
		if !m.HasRWConflict(r.Handle, writer.Handle) {
			t.Fatalf("HasRWConflict(reader handle %d → writer) = false, want true", r.Handle)
		}
		if got := m.OutConflictCount(r.Handle); got != 1 {
			t.Fatalf("reader handle %d outConflicts = %d, want 1", r.Handle, got)
		}
	}
}

func TestCheckForSerializableConflictIn_PeerEdgesRetainedThenScrubbed(t *testing.T) {
	// Symmetric counterpart of the read-path retention test: after a reader
	// installed via the write-path commits while the writer is still
	// in-flight, the edge is RETAINED (M0118-0001); it is scrubbed from the
	// writer's inConflicts only once the writer also finishes and the retained
	// reader is purged.
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	tag := TupleLockTag(1, 16384, 7, 3)
	if !m.AcquirePredicateLock(reader.Handle, tag) {
		t.Fatal("AcquirePredicateLock for reader returned false")
	}
	if !m.CheckForSerializableConflictIn(writer.Handle, tag) {
		t.Fatal("CheckForSerializableConflictIn returned false")
	}

	writerSX := m.SerializableXact(writer.Handle)
	if writerSX == nil {
		t.Fatal("writer SerializableXact = nil before commit")
	}
	if len(writerSX.inConflicts) != 1 {
		t.Fatalf("writerSX.inConflicts pre-commit = %d, want 1", len(writerSX.inConflicts))
	}

	// Commit the reader: the writer is still in-flight, so the committed
	// reader overlaps it and the edge is retained.
	if err := m.Commit(reader); err != nil {
		t.Fatalf("Commit reader: %v", err)
	}
	m.ssiMu.Lock()
	got := len(writerSX.inConflicts)
	m.ssiMu.Unlock()
	if got != 1 {
		t.Fatalf("writerSX.inConflicts after reader Commit = %d, want 1 (retained while writer overlaps)", got)
	}

	// Finish the writer: nothing active remains, the retained reader is
	// purged, and the edge is scrubbed.
	if err := m.Commit(writer); err != nil {
		t.Fatalf("Commit writer: %v", err)
	}
	m.ssiMu.Lock()
	got = len(writerSX.inConflicts)
	m.ssiMu.Unlock()
	if got != 0 {
		t.Fatalf("writerSX.inConflicts after writer Commit = %d, want 0 (purged)", got)
	}
}
