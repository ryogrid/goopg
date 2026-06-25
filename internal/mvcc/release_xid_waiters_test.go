package mvcc

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestReleaseXIDWaiters pins the deadlock-victim in-place abort half of the
// intra-grant-inplace pg_class wait (design 0118-0115): ReleaseXIDWaiters makes
// an XID appear finished to WaitForXID / IsXIDActive while its proc-array slot
// stays open, so a peer blocked on the victim's catalog-tuple xmax unblocks at
// the deadlock abort rather than at the victim's later explicit COMMIT/ROLLBACK.
// The slot remains usable: the subsequent Commit/Rollback finalises it normally
// and clears the marker.
func TestReleaseXIDWaiters(t *testing.T) {
	m := NewManager()

	tx, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatalf("AssignXID: %v", err)
	}
	if !m.IsXIDActive(xid) {
		t.Fatalf("freshly-assigned xid %d not active", xid)
	}

	// WaitForXID blocks while the slot is open and the XID is not released.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	blocked := make(chan error, 1)
	go func() { blocked <- m.WaitForXID(ctx, xid) }()
	select {
	case <-blocked:
		t.Fatal("WaitForXID returned before the XID was released or finished")
	case <-time.After(40 * time.Millisecond):
		// still blocked — expected
	}

	// Releasing the waiters wakes the blocked WaitForXID immediately, even
	// though the slot is still open (transaction block not yet committed).
	m.ReleaseXIDWaiters(xid)
	select {
	case werr := <-blocked:
		if werr != nil {
			t.Fatalf("WaitForXID after ReleaseXIDWaiters: %v", werr)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForXID did not wake after ReleaseXIDWaiters")
	}
	if m.IsXIDActive(xid) {
		t.Fatalf("xid %d still active after ReleaseXIDWaiters", xid)
	}

	// The slot is still usable: an explicit Commit finalises it through the
	// canonical path and the release marker is cleared.
	if err := m.Commit(tx); err != nil {
		t.Fatalf("Commit on a waiter-released but still-open slot: %v", err)
	}
	if m.xidWaitersReleased(xid) {
		t.Fatalf("release marker for xid %d not cleared at finish", xid)
	}
	// An unrelated invalid xid is never released.
	if m.xidWaitersReleased(storage.InvalidTransactionID) {
		t.Fatal("InvalidTransactionID reported as waiter-released")
	}
}
