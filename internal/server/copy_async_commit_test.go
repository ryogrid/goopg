package server

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// TestCommitCopyTxRespectsAsyncCommit pins the M0117-0007 Part B follow-up:
// COPY's own commit sites (dispatchCopyViaExecutor's CopyTo/CopyFrom-file
// branches, handleCopyInFrame's CopyDone/binary-trailer branches) must honor
// the session-effective synchronous_commit GUC the same way every other live
// commit call site does via executor.Context.CommitTransaction, instead of
// always committing synchronously.
func TestCommitCopyTxRespectsAsyncCommit(t *testing.T) {
	mgr := mvcc.NewManager()
	var got []bool
	mgr.SetXactMarkerLogger(func(_ storage.TransactionID, _ mvcc.XactMarker, waitLocalFlush bool) error {
		got = append(got, waitLocalFlush)
		return nil
	})

	beginAndAssign := func() mvcc.Transaction {
		tx, err := mgr.Begin(mvcc.IsolationReadCommitted)
		if err != nil {
			t.Fatal(err)
		}
		xid, err := mgr.AssignXID(tx)
		if err != nil {
			t.Fatal(err)
		}
		tx.XID = xid
		return tx
	}

	tx1 := beginAndAssign()
	if err := commitCopyTx(mgr, tx1, false); err != nil {
		t.Fatalf("commitCopyTx (sync): %v", err)
	}

	tx2 := beginAndAssign()
	if err := commitCopyTx(mgr, tx2, true); err != nil {
		t.Fatalf("commitCopyTx (async): %v", err)
	}

	want := []bool{true, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("waitLocalFlush sequence=%v want=%v (asyncCommit=false, asyncCommit=true)", got, want)
	}
}
