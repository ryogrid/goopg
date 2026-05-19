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

func TestCheckForSerializableConflictOut_NoOpForFinishedWriter(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	writerXID := writer.XID
	if err := m.Commit(writer); err != nil {
		t.Fatalf("Commit writer: %v", err)
	}
	if m.CheckForSerializableConflictOut(reader.Handle, writerXID) {
		t.Fatal("finished writer registered an edge; expected no-op (retention is M0104-0006)")
	}
	if got := m.OutConflictCount(reader.Handle); got != 0 {
		t.Fatalf("reader.outConflicts = %d, want 0", got)
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

func TestSerializableXact_PeerEdgesScrubbedOnCommit(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("CheckForSerializableConflictOut returned false")
	}

	// Capture the still-live reader pointer so we can inspect its
	// outConflicts slice after the writer is released. Manager
	// removes finished xacts from ssiState.xacts so we can't ask via
	// the public API once the writer is gone.
	readerSX := m.SerializableXact(reader.Handle)
	if readerSX == nil {
		t.Fatal("reader SerializableXact = nil before commit")
	}
	if len(readerSX.outConflicts) != 1 {
		t.Fatalf("readerSX.outConflicts pre-commit = %d, want 1", len(readerSX.outConflicts))
	}

	if err := m.Commit(writer); err != nil {
		t.Fatalf("Commit writer: %v", err)
	}

	m.mu.Lock()
	got := len(readerSX.outConflicts)
	m.mu.Unlock()
	if got != 0 {
		t.Fatalf("readerSX.outConflicts after writer Commit = %d, want 0 (peer scrub)", got)
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

	m.mu.Lock()
	got := len(writerSX.inConflicts)
	m.mu.Unlock()
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

func TestCheckForSerializableConflictIn_PeerEdgesScrubbedOnReaderCommit(t *testing.T) {
	// Symmetric counterpart of the read-path scrub test: after a
	// reader installed via the write-path commits, the writer's
	// inConflicts slice must no longer reference the dying reader.
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

	if err := m.Commit(reader); err != nil {
		t.Fatalf("Commit reader: %v", err)
	}

	m.mu.Lock()
	got := len(writerSX.inConflicts)
	m.mu.Unlock()
	if got != 0 {
		t.Fatalf("writerSX.inConflicts after reader Commit = %d, want 0 (peer scrub)", got)
	}
}
