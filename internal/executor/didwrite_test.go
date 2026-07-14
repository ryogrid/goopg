package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestContextDidWrite verifies the write-XID predicate that gates post-commit
// forced GC (perf-optimize3-dash/08 doc 10). A transaction that never assigned
// an XID (a read-only SELECT under M0093 lazy allocation) must report
// DidWrite()==false so pgbench -S skips maybeForceGCAfterCommit entirely;
// once a write materialises an XID it must report true.
func TestContextDidWrite(t *testing.T) {
	c := &Context{}
	if c.Tx.XID != storage.InvalidTransactionID {
		t.Fatalf("fresh Context should have InvalidTransactionID, got %d", c.Tx.XID)
	}
	if c.DidWrite() {
		t.Fatal("read-only transaction (XID==Invalid) must report DidWrite()==false")
	}

	// Simulate what MaterializeWriterXID does at the first write site.
	c.Tx.XID = storage.TransactionID(42)
	if !c.DidWrite() {
		t.Fatal("after a real XID is assigned, DidWrite() must report true")
	}
}
