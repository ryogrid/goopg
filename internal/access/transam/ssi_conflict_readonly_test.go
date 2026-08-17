package transam

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// These tests pin the de-facto READ ONLY SSI modeling added for the
// receipt-report isolation spec (M0118-0001): a declared READ ONLY reader that
// reads an already-committed writer's data records NO rw-conflict ("appears to
// run first") UNLESS the writer holds an rw-conflict OUT to a transaction that
// committed before the reader's snapshot. This mirrors
// CheckForSerializableConflictOut lines 4123-4137 in predicate.c.

// TestCommittedBeforeSnapshot pins the snapshot-ordering predicate that drives
// every READ ONLY refinement. FinishedAt and SnapshotSeqNo draw from one
// monotonic counter, so a strictly smaller commit stamp means "committed before
// the snapshot was taken".
func TestCommittedBeforeSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		peerFin  CommitSeqNo
		obsSnap  CommitSeqNo
		expected bool
	}{
		{"peer committed before snapshot", 5, 10, true},
		{"peer committed at snapshot watermark", 10, 10, false},
		{"peer committed after snapshot", 12, 10, false},
		{"peer never committed", InvalidCommitSeqNo, 10, false},
		{"observer never snapshotted", 5, InvalidCommitSeqNo, false},
		{"both invalid", InvalidCommitSeqNo, InvalidCommitSeqNo, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := &SerializableXact{FinishedAt: tc.peerFin}
			obs := &SerializableXact{SnapshotSeqNo: tc.obsSnap}
			if got := committedBeforeSnapshot(peer, obs); got != tc.expected {
				t.Fatalf("committedBeforeSnapshot(fin=%d, snap=%d) = %v, want %v",
					tc.peerFin, tc.obsSnap, got, tc.expected)
			}
		})
	}
	if committedBeforeSnapshot(nil, &SerializableXact{SnapshotSeqNo: 10}) {
		t.Fatal("committedBeforeSnapshot(nil peer) = true, want false")
	}
	if committedBeforeSnapshot(&SerializableXact{FinishedAt: 5}, nil) {
		t.Fatal("committedBeforeSnapshot(nil observer) = true, want false")
	}
}

// TestCheckForSerializableConflictOut_ReadOnlyReaderAppearsFirstNoEdge is the
// core receipt-report false-positive avoidance: the committed writer holds an
// out-conflict to T2, but T2 committed AFTER the READ ONLY reader's snapshot, so
// the reader appears to run before the whole structure and forms no edge.
func TestCheckForSerializableConflictOut_ReadOnlyReaderAppearsFirstNoEdge(t *testing.T) {
	m := NewManager()
	m.ssiState.ensureInit()

	// READ ONLY reader; snapshot watermark = 10.
	reader := &SerializableXact{Handle: 1, XID: storage.InvalidTransactionID, ReadOnly: true, SnapshotSeqNo: 10}
	// T2: writer's out-conflict target, committed at 15 (after reader's snapshot).
	t2 := &SerializableXact{Handle: 2, XID: 200, FinishedAt: 15}
	// Writer: committed at 12, holds out-conflict to T2.
	writer := &SerializableXact{Handle: 3, XID: 300, FinishedAt: 12, ConflictOut: true, outConflicts: []*SerializableXact{t2}}

	m.ssiState.xacts[reader.Handle] = reader
	m.ssiState.finished = append(m.ssiState.finished, writer, t2)

	if m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("expected no edge: READ ONLY reader appears to run first (T2 committed after its snapshot)")
	}
	if m.OutConflictCount(reader.Handle) != 0 {
		t.Fatalf("reader.outConflicts = %d, want 0 (no edge recorded)", m.OutConflictCount(reader.Handle))
	}
	if reader.Doomed {
		t.Fatal("reader doomed; a read-only reader that runs first must never abort")
	}
}

// TestCheckForSerializableConflictOut_ReadOnlyReaderClosesStructureRecordsEdge
// is the complementary genuine anomaly: the writer's out-conflict T2 committed
// BEFORE the READ ONLY reader's snapshot, so the reader closes the dangerous
// structure R -> W -> T2. The edge records and (because the committed writer
// carries ConflictOut) Case 1 of onConflictCheckLocked dooms the reader.
func TestCheckForSerializableConflictOut_ReadOnlyReaderClosesStructureRecordsEdge(t *testing.T) {
	m := NewManager()
	m.ssiState.ensureInit()

	// READ ONLY reader; snapshot watermark = 20.
	reader := &SerializableXact{Handle: 1, XID: storage.InvalidTransactionID, ReadOnly: true, SnapshotSeqNo: 20}
	// T2: committed at 8 (before reader's snapshot).
	t2 := &SerializableXact{Handle: 2, XID: 200, FinishedAt: 8}
	// Writer: committed at 12, holds out-conflict to T2 and carries ConflictOut.
	writer := &SerializableXact{Handle: 3, XID: 300, FinishedAt: 12, ConflictOut: true, outConflicts: []*SerializableXact{t2}}

	m.ssiState.xacts[reader.Handle] = reader
	m.ssiState.finished = append(m.ssiState.finished, writer, t2)

	if !m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("expected an edge: READ ONLY reader closes R -> W -> T2 (T2 committed before its snapshot)")
	}
	if m.OutConflictCount(reader.Handle) != 1 {
		t.Fatalf("reader.outConflicts = %d, want 1", m.OutConflictCount(reader.Handle))
	}
	if !reader.Doomed {
		t.Fatal("reader not doomed; closing a dangerous structure to a committed pivot must doom the reader (Case 1)")
	}
}

// TestCheckForSerializableConflictOut_ReadOnlyReaderSkipsWriterWithNoOutConflict
// confirms a READ ONLY reader reading a committed writer that has NO out-conflict
// at all (a leaf writer) never forms an edge — there is no structure to close.
func TestCheckForSerializableConflictOut_ReadOnlyReaderSkipsWriterWithNoOutConflict(t *testing.T) {
	m := NewManager()
	m.ssiState.ensureInit()

	reader := &SerializableXact{Handle: 1, XID: storage.InvalidTransactionID, ReadOnly: true, SnapshotSeqNo: 20}
	writer := &SerializableXact{Handle: 3, XID: 300, FinishedAt: 12}

	m.ssiState.xacts[reader.Handle] = reader
	m.ssiState.finished = append(m.ssiState.finished, writer)

	if m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("expected no edge: committed writer has no out-conflict, so no dangerous structure exists")
	}
	if reader.Doomed {
		t.Fatal("reader doomed; must not abort against a leaf committed writer")
	}
}

// TestCheckForSerializableConflictOut_ReadWriteReaderNotSubjectToSkip confirms
// the skip is gated on the READ ONLY flag: a normal read-write reader still
// records the edge to a committed writer (so its own dangerous structures are
// detected), exactly as before M0118-0001.
func TestCheckForSerializableConflictOut_ReadWriteReaderNotSubjectToSkip(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	// Commit the writer so it is retained with a stamped FinishedAt, exercising
	// the same committed-writer path the READ ONLY skip guards — but the reader
	// is read-write, so the edge must still install.
	if err := m.Commit(writer); err != nil {
		t.Fatalf("Commit(writer): %v", err)
	}
	if !m.CheckForSerializableConflictOut(reader.Handle, writer.XID) {
		t.Fatal("read-write reader must still record the edge to a committed writer")
	}
	if m.OutConflictCount(reader.Handle) != 1 {
		t.Fatalf("reader.outConflicts = %d, want 1", m.OutConflictCount(reader.Handle))
	}
}
