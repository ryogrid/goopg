package executor

import (
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// Context carries per-statement runtime state into every operator's
// Open / Next call. It is constructed by the wire-protocol path at
// statement start and torn down at statement end.
type Context struct {
	// Params holds bind values for $1, $2, ... — Params[i-1] is $i.
	Params []Datum
	// Now is the wall-clock value `current_timestamp` and friends
	// resolve to. Captured once at statement start so retries see
	// consistent values, matching upstream.
	Now time.Time
	// MaxRows caps the number of rows the executor produces. Zero
	// means unlimited. The extended-query protocol's Execute message
	// passes through here.
	MaxRows int

	// Storage handles. Heap-touching operators (SeqScan/Insert/
	// Update/Delete) require all four to be set; pure-compute
	// statements (SELECT 1, …) don't.
	Pool    *storage.Pool
	Catalog catalog.Catalog
	TxnMgr  *mvcc.Manager
	Tx      mvcc.Transaction
	Snap    mvcc.Snapshot

	// Session, if set, is consulted by the Transaction operator to
	// drive BEGIN/COMMIT/ROLLBACK. It also tracks whether the current
	// statement is running inside an explicit transaction block. The
	// wire-protocol path provides a per-connection implementation;
	// tests can leave it nil when the operator under test doesn't
	// need it.
	Session Session
}

// NewContext builds a Context with sensible defaults: a fresh
// timestamp and no bind parameters. Tests use this directly.
func NewContext() *Context {
	return &Context{Now: time.Now()}
}
