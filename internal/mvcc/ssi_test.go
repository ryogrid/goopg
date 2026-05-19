package mvcc

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestSerializableXact_BeginRegistersAndCommitReleases pins the
// M0104-0002 lifecycle contract: Begin(IsolationSerializable) must
// register a SerializableXact bookkeeping object, and Commit must
// release it with FinishedAt stamped to the next dense CommitSeqNo.
//
// Why pin this now: the predicate-lock + rw-edge slices
// (M0104-0003..0005) attach state to SerializableXact and rely on
// the registry being populated for the entire active span of every
// SERIALIZABLE transaction. A regression that breaks the registry
// would silently mask conflict detection.
func TestSerializableXact_BeginRegistersAndCommitReleases(t *testing.T) {
	m := NewManager()

	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin(Serializable): %v", err)
	}

	sx := m.SerializableXact(tx.Handle)
	if sx == nil {
		t.Fatalf("SerializableXact(%d) returned nil after Begin", tx.Handle)
	}
	if sx.Handle != tx.Handle {
		t.Fatalf("SerializableXact.Handle = %d, want %d", sx.Handle, tx.Handle)
	}
	if sx.XID != storage.InvalidTransactionID {
		t.Fatalf("SerializableXact.XID = %d, want InvalidTransactionID until AssignXID",
			sx.XID)
	}
	if sx.FinishedAt != InvalidCommitSeqNo {
		t.Fatalf("SerializableXact.FinishedAt = %d, want %d while in-flight",
			sx.FinishedAt, InvalidCommitSeqNo)
	}
	if !sx.IsActive() {
		t.Fatal("SerializableXact.IsActive() = false, want true while registered")
	}
	if got := m.SerializableXactCount(); got != 1 {
		t.Fatalf("SerializableXactCount = %d, want 1", got)
	}

	if err := m.Commit(tx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// The pointer returned earlier must reflect post-finish state
	// so observers (logging, future conflict-graph walkers) see the
	// completed CommitSeqNo. The pointer is detached from the
	// registry, but its fields are populated.
	if sx.FinishedAt == InvalidCommitSeqNo {
		t.Fatal("SerializableXact.FinishedAt still Invalid after Commit; want >= 1")
	}
	if sx.IsActive() {
		t.Fatal("SerializableXact.IsActive() = true after Commit, want false")
	}
	if got := m.SerializableXact(tx.Handle); got != nil {
		t.Fatalf("SerializableXact(%d) after Commit = %+v, want nil (released)",
			tx.Handle, got)
	}
	if got := m.SerializableXactCount(); got != 0 {
		t.Fatalf("SerializableXactCount after Commit = %d, want 0", got)
	}
}

// TestSerializableXact_RollbackAlsoReleases pins that the cleanup
// path runs on abort as well as commit. SSI bookkeeping must not
// leak past a rollback: the dangerous-structure check (M0104-0006)
// will iterate the registry, and a stale entry would corrupt the
// rw-conflict graph.
func TestSerializableXact_RollbackAlsoReleases(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if m.SerializableXactCount() != 1 {
		t.Fatalf("count before rollback = %d, want 1", m.SerializableXactCount())
	}
	if err := m.Rollback(tx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := m.SerializableXact(tx.Handle); got != nil {
		t.Fatalf("SerializableXact after Rollback = %+v, want nil", got)
	}
	if got := m.SerializableXactCount(); got != 0 {
		t.Fatalf("count after Rollback = %d, want 0", got)
	}
}

// TestSerializableXact_AssignXIDStampsTopXid pins that the lazy
// XID allocation path (Manager.AssignXID) writes the new top-level
// XID onto the registered SerializableXact. M0104-0004 / M0104-0005
// will look up SerializableXact objects by writer XID when
// register conflict-out edges; this stamping is what makes that
// lookup well-defined.
func TestSerializableXact_AssignXIDStampsTopXid(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	sx := m.SerializableXact(tx.Handle)
	if sx == nil {
		t.Fatal("SerializableXact missing after Begin")
	}
	if sx.XID != storage.InvalidTransactionID {
		t.Fatalf("XID before AssignXID = %d, want Invalid", sx.XID)
	}
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatalf("AssignXID: %v", err)
	}
	if xid == storage.InvalidTransactionID {
		t.Fatal("AssignXID returned Invalid")
	}
	if sx.XID != xid {
		t.Fatalf("SerializableXact.XID = %d after AssignXID, want %d", sx.XID, xid)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// TestSerializableXact_NotRegisteredForRCorRR pins that the
// registry stays empty for non-SERIALIZABLE workloads. Cost-wise,
// the lazy-init of the xacts map avoids charging the SSI footprint
// to the common RC/RR path; behaviour-wise, registering RR would
// confuse downstream conflict-graph walkers (RR has no conflict
// edges to track).
func TestSerializableXact_NotRegisteredForRCorRR(t *testing.T) {
	for _, iso := range []IsolationLevel{IsolationReadCommitted, IsolationRepeatableRead} {
		m := NewManager()
		tx, err := m.Begin(iso)
		if err != nil {
			t.Fatalf("Begin(%v): %v", iso, err)
		}
		if got := m.SerializableXact(tx.Handle); got != nil {
			t.Fatalf("SerializableXact for %v = %+v, want nil", iso, got)
		}
		if got := m.SerializableXactCount(); got != 0 {
			t.Fatalf("SerializableXactCount for %v = %d, want 0", iso, got)
		}
		if err := m.Commit(tx); err != nil {
			t.Fatalf("Commit(%v): %v", iso, err)
		}
	}
}

// TestSerializableXact_CommitSeqNoMonotonic pins the dense,
// monotonically-increasing CommitSeqNo allocation. M0104-0006 will
// use the relative ordering of FinishedAt across pairs of finished
// transactions to decide which rw-edge orientation completes a
// dangerous structure; a regression that re-used or reversed
// CommitSeqNo would invert that decision.
func TestSerializableXact_CommitSeqNoMonotonic(t *testing.T) {
	m := NewManager()
	tA, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin A: %v", err)
	}
	tB, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin B: %v", err)
	}
	sxA := m.SerializableXact(tA.Handle)
	sxB := m.SerializableXact(tB.Handle)
	if sxA == nil || sxB == nil {
		t.Fatalf("missing SerializableXact: A=%v B=%v", sxA, sxB)
	}
	if err := m.Commit(tA); err != nil {
		t.Fatalf("Commit A: %v", err)
	}
	if err := m.Rollback(tB); err != nil {
		t.Fatalf("Rollback B: %v", err)
	}
	if sxA.FinishedAt == InvalidCommitSeqNo || sxB.FinishedAt == InvalidCommitSeqNo {
		t.Fatalf("FinishedAt not stamped: A=%d B=%d", sxA.FinishedAt, sxB.FinishedAt)
	}
	if !(sxA.FinishedAt < sxB.FinishedAt) {
		t.Fatalf("CommitSeqNo not monotonic: A=%d B=%d (expected A<B)",
			sxA.FinishedAt, sxB.FinishedAt)
	}
}
