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
