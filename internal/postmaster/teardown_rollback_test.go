package postmaster

import (
	"io"
	"log/slog"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
)

// TestRollbackOpenTxnOnTeardownReleasesXID pins the pgbench TPC-B hang fix: a
// client that drops its connection with an explicit transaction still open
// must have that transaction rolled back on teardown, so its XID is released
// from the ProcArray. Otherwise every concurrent backend blocked in
// WaitForXID on that XID (via epqWait during UPDATE contention) hangs forever.
//
// The test mirrors a client that ran BEGIN + a write (in-progress XID) and
// then disconnected without COMMIT/ROLLBACK: after rollbackOpenTxnOnTeardown
// the transaction is no longer active and the ProcArray slot is freed.
func TestRollbackOpenTxnOnTeardownReleasesXID(t *testing.T) {
	mgr := transam.NewManager()
	srv := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		TxnMgr: mgr,
	})

	tx, err := mgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := mgr.AssignXID(tx); err != nil {
		t.Fatalf("AssignXID: %v", err)
	}

	connTx := &connTxState{}
	connTx.Begin(tx)
	if !connTx.InExplicit() {
		t.Fatal("precondition: connTx should report an open explicit transaction")
	}
	before := mgr.ActiveCount()
	if before < 1 {
		t.Fatalf("precondition: ActiveCount=%d, want >= 1", before)
	}

	// Simulate connection teardown (client disconnect) with the transaction
	// still open — the path that previously leaked the XID.
	srv.rollbackOpenTxnOnTeardown(connTx, nil)

	if connTx.InExplicit() {
		t.Error("after teardown the connection must no longer hold an explicit transaction")
	}
	if after := mgr.ActiveCount(); after != before-1 {
		t.Errorf("ActiveCount after teardown = %d; want %d (XID must be released)", after, before-1)
	}

	// A no-op when there is no open transaction (auto-commit connection).
	srv.rollbackOpenTxnOnTeardown(&connTxState{}, nil)
}
