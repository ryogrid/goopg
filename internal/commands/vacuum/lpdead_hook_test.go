package vacuum

import (
	"os"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestMain: the vacuum unit suite constructs synthetic tuples with literal
// xids and no CLOG; since C3-S3, storage.TupleDeadToAll requires the
// XidCommitted hook (production wires CLog.DidCommit in initdb.Open).
// Install the permissive test hook the suite's fixtures assume.
func TestMain(m *testing.M) {
	storage.XidCommitted = func(storage.TransactionID) bool { return true }
	os.Exit(m.Run())
}
