package executor

import (
	"os"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestMain: the executor unit fixtures (newHOTFixture, newDDLFixture, …)
// allocate xids from an in-process mvcc.Manager with no CLOG wired; since
// C3-S3, storage.TupleDeadToAll (prune / kill oracle) requires the
// XidCommitted hook (production wires CLog.DidCommit in initdb.Open).
// Install the permissive hook the fixtures' committed-writes assumption
// implies; abort-semantics tests override locally with save/restore.
func TestMain(m *testing.M) {
	storage.XidCommitted = func(storage.TransactionID) bool { return true }
	os.Exit(m.Run())
}
