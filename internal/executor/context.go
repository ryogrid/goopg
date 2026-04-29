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

	// Checkpointer, when set, is invoked by the Checkpoint operator
	// to drive a synchronous checkpoint (see milestone 0002). nil
	// means the SQL CHECKPOINT verb fails with feature_not_supported
	// — that's the v0 behaviour for a server started without a WAL
	// writer.
	Checkpointer Checkpointer

	// OuterRows is the lexical-scope row stack used by correlated
	// subqueries. evalSubquery / evalInExpr / evalExistsExpr push
	// the current outer row before opening the inner plan and
	// pop on close. evalOuterColumnRef reads
	// `OuterRows[len(OuterRows)-Level]` — Level 1 is the
	// innermost outer scope.
	OuterRows []Row

	// StatsTarget is the effective `default_statistics_target`
	// GUC value for the current statement. ANALYZE uses
	// `targrows = StatsTarget * 300` for sample sizing, mirrors
	// upstream's analyze.c. Zero means "use the upstream default
	// of 100" — the wire path populates this from the session
	// registry; tests leave it zero unless they care about
	// sample-size behaviour.
	StatsTarget int

	// AnalyzeRandSeed, when non-zero, makes ANALYZE's reservoir
	// sampler reproducible. Tests set it; production leaves it
	// zero so the sampler reseeds from the wall clock.
	AnalyzeRandSeed int64

	// PubSub is the M0008 publication / subscription registry.
	// CREATE PUBLICATION / DROP PUBLICATION / CREATE SUBSCRIPTION
	// / DROP SUBSCRIPTION mutate it. nil means the runtime
	// hasn't wired logical-replication DDL — those statements
	// fail with feature_not_supported.
	PubSub *catalog.PubSub
}

// Checkpointer is the contract the SQL `CHECKPOINT` verb uses to
// drive a synchronous checkpoint. The wire layer fills it from
// server.Config; production servers use a *wal.Checkpointer.
type Checkpointer interface {
	CheckpointNow() error
}

// NewContext builds a Context with sensible defaults: a fresh
// timestamp and no bind parameters. Tests use this directly.
func NewContext() *Context {
	return &Context{Now: time.Now()}
}
