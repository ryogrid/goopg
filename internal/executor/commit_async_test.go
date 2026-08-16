package executor

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
)

// TestContextCommitTransactionRespectsAsyncCommit pins the M0117-0007 Part B
// contract at the executor layer: Context.CommitTransaction routes through
// TxnMgr.CommitAsync (waitLocalFlush=false) exactly when AsyncCommit is set,
// and through the ordinary synchronous TxnMgr.Commit (waitLocalFlush=true)
// otherwise. Every live commit call site (explicit COMMIT, simple/extended
// autocommit) goes through this one method, so this is the single seam that
// proves the session's synchronous_commit setting reaches the commit path.
func TestContextCommitTransactionRespectsAsyncCommit(t *testing.T) {
	mgr := transam.NewManager()
	var got []bool
	mgr.SetXactMarkerLogger(func(_ storage.TransactionID, _ transam.XactMarker, waitLocalFlush bool) error {
		got = append(got, waitLocalFlush)
		return nil
	})

	beginAndAssign := func() transam.Transaction {
		tx, err := mgr.Begin(transam.IsolationReadCommitted)
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

	ctx := NewContext()
	ctx.TxnMgr = mgr

	ctx.AsyncCommit = false
	tx1 := beginAndAssign()
	if err := ctx.CommitTransaction(tx1); err != nil {
		t.Fatalf("CommitTransaction (sync): %v", err)
	}

	ctx.AsyncCommit = true
	tx2 := beginAndAssign()
	if err := ctx.CommitTransaction(tx2); err != nil {
		t.Fatalf("CommitTransaction (async): %v", err)
	}

	want := []bool{true, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("waitLocalFlush sequence=%v want=%v (AsyncCommit=false, AsyncCommit=true)", got, want)
	}
}
