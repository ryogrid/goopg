package transam

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestManagerHasAbortedXID pins the M0100-0005w companion API: callers
// (FK-on-delete wait path, others) need a definitive post-WaitForXID
// answer to "did this xact commit or abort?", because RR snapshots are
// frozen and cannot reflect aborts that happened after BEGIN. The
// manager already tracks aborts in m.abortedXIDs (finish() → XactAbort
// branch); HasAbortedXID surfaces that under a lock without forcing a
// fresh snapshot capture.
func TestManagerHasAbortedXID(t *testing.T) {
	m := NewManager()
	// Invalid xid is never aborted.
	if m.HasAbortedXID(storage.InvalidTransactionID) {
		t.Fatal("HasAbortedXID(Invalid) = true; must be false")
	}
	// Begin + assign two xids, then commit one and abort the other.
	tx1, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin(tx1): %v", err)
	}
	x1, err := m.AssignXID(tx1)
	if err != nil {
		t.Fatalf("AssignXID(tx1): %v", err)
	}
	tx2, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin(tx2): %v", err)
	}
	x2, err := m.AssignXID(tx2)
	if err != nil {
		t.Fatalf("AssignXID(tx2): %v", err)
	}
	// While both are in-flight, neither is aborted.
	if m.HasAbortedXID(x1) || m.HasAbortedXID(x2) {
		t.Fatalf("in-flight xacts: HasAbortedXID(%d)=%v HasAbortedXID(%d)=%v want both false",
			x1, m.HasAbortedXID(x1), x2, m.HasAbortedXID(x2))
	}
	// Commit tx1, abort tx2.
	if err := m.Commit(tx1); err != nil {
		t.Fatalf("Commit(tx1): %v", err)
	}
	if err := m.Rollback(tx2); err != nil {
		t.Fatalf("Rollback(tx2): %v", err)
	}
	// Committed xact must not be reported as aborted.
	if m.HasAbortedXID(x1) {
		t.Fatalf("HasAbortedXID(committed=%d) = true; must be false", x1)
	}
	// Aborted xact must be reported as aborted.
	if !m.HasAbortedXID(x2) {
		t.Fatalf("HasAbortedXID(aborted=%d) = false; must be true", x2)
	}
}
