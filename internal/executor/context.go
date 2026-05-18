package executor

import (
	"context"
	"time"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// Context carries per-statement runtime state into every operator's
// Open / Next call. It is constructed by the wire-protocol path at
// statement start and torn down at statement end.
type Context struct {
	// Ctx, when non-nil, is the per-query cancellable context. Operators
	// poll Ctx.Err() periodically to detect query cancellation
	// (CancelRequest / psql Ctrl-C → SQLSTATE 57014).
	Ctx context.Context

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

	// SubqueryCache stores subquery results keyed by outer-row
	// values so correlated subqueries are executed at most once
	// per distinct outer value rather than per outer row.  Keys
	// are datumKey-derived strings; values are []Dat um for
	// InExpr or Datum for scalar subqueries.  The cache is
	// notionally per-subquery — a production implementation
	// would namespace by InExpr/SubqueryExpr identity.
	SubqueryCache      map[string][]Datum
	SubqueryCacheScope int // OuterRows len when cached; cleared on change

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

	// Activity is the M0022 backend-activity registry for
	// pg_catalog.pg_stat_activity. nil disables tracking.
	Activity *activity.Registry

	// ActivityPID identifies this backend in the activity registry.
	ActivityPID string

	// LockMgr, when set, is consulted by SQL-touching operators
	// to acquire relation-level locks per
	// docs/design/0012-0003-lock-wait-integration-and-test-matrix.md.
	// nil makes acquireRelLock a no-op so existing tests and
	// non-storage code paths keep working unchanged.
	LockMgr *lockmgr.LockManager

	// BackendID identifies this session/transaction to the lock
	// manager. The wire layer assigns one per connection from a
	// monotonic atomic counter — the youngest-backend victim
	// policy from M0012-0002 relies on the monotonic shape.
	BackendID lockmgr.BackendID

	// WorkTableRows is set by RecursiveUnionOp during fixpoint
	// iteration and read by WorkTableScanOp to produce rows from
	// the current working table. M0016-0004 (recursive CTE).
	WorkTableRows []Row

	// WorkMem is the per-operator memory budget in bytes for
	// spill-to-disk. Zero means unlimited (no spill). Defaults to
	// 512 MiB when the GUC is active. See milestone 0037.
	WorkMem int64

	// EnableOpportunisticPrune mirrors the enable_opportunistic_prune
	// GUC (M0046-0002). When true, the HOT-update path calls
	// PagePruneOpt before falling back to a relation extension.
	EnableOpportunisticPrune bool

	// FSM is the Free Space Map (M0046-0003). When non-nil, writeHeapRow
	// consults it before extending the relation, and VACUUM updates it
	// after reclaiming dead tuples so freed pages can be reused.
	FSM *storage.FSM

	// VM is the Visibility Map (M0046-0004). When non-nil, index-only scans
	// check it to skip heap fetches for ALL_VISIBLE pages; VACUUM sets bits
	// after verifying all tuples are universally visible.
	VM *storage.VisibilityMap

	// FreezeMinAge is the vacuum_freeze_min_age GUC value (M0046-0005).
	// VACUUM rewrites xmin → FrozenTransactionID for tuples older than
	// currentXID − FreezeMinAge. Zero disables freezing.
	FreezeMinAge int64

	// Notices accumulates NOTICE messages emitted during statement execution
	// (e.g. "table X does not exist, skipping" from DROP IF EXISTS).
	// The server reads this after each statement and emits NoticeResponse
	// messages to the client. M0097-0008.
	Notices []string

	// NoticeFlush, when non-nil, is called for each NOTICE as it is generated
	// so the server can send it to the client in real-time (before CommandComplete).
	// This matches PostgreSQL's behavior where NOTICE messages arrive before
	// any blocking point. M0100-0005.
	NoticeFlush func(string)

	// Sequence session state — maps sequence key → last nextval result
	// for currval(); LastSeqVal/LastSeqSet track the lastval() return. M0097-0009.
	CurrSeqVals map[string]int64
	LastSeqVal  int64
	LastSeqSet  bool

	// TempTableShadows maps table name → original permanent *catalog.Table for
	// TEMP TABLE shadowing. Populated by execCreateTable when a TEMP TABLE
	// shadows a permanent one; used by execDropTable to restore it. M0097-0003.
	TempTableShadows map[string]*catalog.Table

	// WAL exposes the cluster's WAL writer so execCommit can read the
	// WrittenLSN after a local flush to bound the SyncRep wait. nil
	// disables the bound and the wait reverts to async behaviour
	// (commit returns immediately). M0102-0005.
	WAL *wal.Writer

	// SyncRep is the synchronous-replication wait primitive. execCommit
	// calls SyncRep.WaitForLSN(commitLSN, mode) after local flush when
	// SyncCommitMode is anything other than SyncRepOff and the configured
	// `synchronous_standby_names` rule is non-empty. nil disables
	// sync replication (async — upstream default). M0102-0005.
	SyncRep *wal.SyncRep

	// SyncCommitMode is the session-effective `synchronous_commit` GUC
	// level. SyncRepOff means commit returns immediately after local
	// flush; remote_* levels block until configured standbys ack.
	// M0102-0005.
	SyncCommitMode wal.SyncRepMode

	// OnSubscriptionChange is invoked after a successful
	// CREATE / DROP SUBSCRIPTION (and, when it lands, ALTER
	// SUBSCRIPTION). The server wires this to ApplyLauncher.Wake so
	// the launcher rescans within milliseconds rather than waiting
	// for its periodic poll. nil disables the wakeup (the launcher
	// still converges via the timer). M0103-0002.
	OnSubscriptionChange func()

	// DataDir is the cluster's data directory, used by DDL operators that
	// write to nailed catalog relations (pg_class, pg_attribute, pg_proc,
	// pg_type) to call catalog.RelcacheInitFileUnlink at commit time via
	// TxnMgr.SetRelcacheInvalPending. Empty means the DDL runs without
	// relcache-init-file invalidation (tests that don't set up a full
	// cluster). M0106-0010 batched-31.
	DataDir string
}

// AddNotice appends a NOTICE-severity message to the context's notice queue.
// If NoticeFlush is set, the message is also flushed to the client immediately
// (matching PostgreSQL's real-time notice delivery). M0100-0005.
func (c *Context) AddNotice(msg string) {
	if c.NoticeFlush != nil {
		c.NoticeFlush(msg)
	}
	c.Notices = append(c.Notices, msg)
}

// TakeNotices returns and clears the accumulated notices.
func (c *Context) TakeNotices() []string {
	n := c.Notices
	c.Notices = nil
	return n
}

// acquireRelLock funnels every operator's relation-level lock
// acquisition through one place so the SQLSTATE 40P01 mapping for
// `lockmgr.ErrDeadlockDetected` and the 57014 mapping for context
// cancellation are consistent. nil-LockMgr is a no-op: tests and
// the legacy COPY-only server path don't have a lock manager
// configured and must keep working.
//
// Locks taken here are transaction-scoped — released at the
// dispatch.go commit/rollback call to LockMgr.ReleaseAll —
// so they survive across statements within a multi-statement
// txn, which is required for any real SQL deadlock to form.
func (c *Context) acquireRelLock(rel storage.RelFileNode, mode lockmgr.Mode) error {
	if c.LockMgr == nil {
		return nil
	}
	// Record wait event for lock waits.
	if c.Activity != nil && c.ActivityPID != "" {
		c.Activity.WaitEventStart(c.ActivityPID, activity.WaitTypeLock, activity.WaitRelationLock)
	}
	lockCtx := context.Background()
	if c.Ctx != nil {
		lockCtx = c.Ctx
	}
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	err := c.LockMgr.Acquire(lockCtx, c.BackendID, tag, mode)
	if c.Activity != nil && c.ActivityPID != "" {
		c.Activity.WaitEventEnd(c.ActivityPID)
	}
	if err == nil {
		return nil
	}
	if err == lockmgr.ErrDeadlockDetected {
		return &ExecError{Code: "40P01", Message: "deadlock detected"}
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
	}
	return &ExecError{Code: "XX000", Message: err.Error()}
}

// acquireTupleLock acquires a tuple-level lock on the given
// (rel, ItemPointer) tag (M0021 tuple-level locking step 2b).
// Used by SELECT FOR UPDATE to record per-row holders, and by
// UPDATE / DELETE to block on a foreign lock holder. Lock-tag
// granularity reuses lockmgr.LockTag with Block/Offset set —
// the (DB, Rel) relation tag and the (DB, Rel, Block, Offset)
// tuple tag are independent map keys, so relation-level
// locking and tuple-level locking don't accidentally block
// each other. Same SQLSTATE mappings as acquireRelLock.
func (c *Context) acquireTupleLock(rel storage.RelFileNode, ptr storage.ItemPointer, mode lockmgr.Mode) error {
	if c.LockMgr == nil {
		return nil
	}
	lockCtx := context.Background()
	if c.Ctx != nil {
		lockCtx = c.Ctx
	}
	tag := tupleLockTag(rel, ptr)
	err := c.LockMgr.Acquire(lockCtx, c.BackendID, tag, mode)
	if err == nil {
		return nil
	}
	if err == lockmgr.ErrDeadlockDetected {
		return &ExecError{Code: "40P01", Message: "deadlock detected"}
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
	}
	return &ExecError{Code: "XX000", Message: err.Error()}
}

// tryAcquireTupleLock is the NOWAIT variant — surfaces
// SQLSTATE 55P03 immediately on contention. Used by SELECT FOR
// UPDATE NOWAIT and by UPDATE / DELETE on a tuple another xact
// holds when the operator wants fail-fast semantics.
func (c *Context) tryAcquireTupleLock(rel storage.RelFileNode, ptr storage.ItemPointer, mode lockmgr.Mode) error {
	if c.LockMgr == nil {
		return nil
	}
	tag := tupleLockTag(rel, ptr)
	err := c.LockMgr.TryAcquire(c.BackendID, tag, mode)
	if err == nil {
		return nil
	}
	if err == lockmgr.ErrLockNotAvailable {
		return &ExecError{Code: "55P03", Message: "could not obtain lock on row"}
	}
	return &ExecError{Code: "XX000", Message: err.Error()}
}

// tupleLockTag synthesises a tuple-level LockTag from a
// (rel, ItemPointer). The encoding (block in Block, line slot in
// Offset) is private to the executor; lockmgr only uses the tag
// as an opaque comparable key. The tuple tag and the matching
// relation tag (Block=0, Offset=0) are independent so
// AccessShareLock / RowExclusiveLock at the relation never
// blocks tuple-level acquirers.
func tupleLockTag(rel storage.RelFileNode, ptr storage.ItemPointer) lockmgr.LockTag {
	return lockmgr.LockTag{
		DB:     rel.DBOid,
		Rel:    rel.RelOid,
		Block:  uint32(ptr.Block) + 1, // shift so Block=0 isn't an alias for "relation tag"
		Offset: uint32(ptr.Offset) + 1,
	}
}

// tryAcquireRelLock is the NOWAIT variant of acquireRelLock —
// returns SQLSTATE 55P03 immediately when the lock isn't
// instantly grantable. Used by `SELECT … FOR UPDATE NOWAIT`.
// Mirrors upstream's "could not obtain lock on row in relation"
// diagnostic; goopg's row-locking is relation-coarse for now so
// the message says "relation" instead of "row" but the SQLSTATE
// is the canonical one tooling greps for.
func (c *Context) tryAcquireRelLock(rel storage.RelFileNode, mode lockmgr.Mode) error {
	if c.LockMgr == nil {
		return nil
	}
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	err := c.LockMgr.TryAcquire(c.BackendID, tag, mode)
	if err == nil {
		return nil
	}
	if err == lockmgr.ErrLockNotAvailable {
		return &ExecError{Code: "55P03", Message: "could not obtain lock on relation"}
	}
	return &ExecError{Code: "XX000", Message: err.Error()}
}

// Checkpointer is the contract the SQL `CHECKPOINT` verb uses to
// drive a synchronous checkpoint. The wire layer fills it from
// server.Config; production servers use a *wal.Checkpointer.
type Checkpointer interface {
	CheckpointNow() error
	CheckpointRedoLSN() uint64
}

// NewContext builds a Context with sensible defaults: a fresh
// timestamp and no bind parameters. Tests use this directly.
func NewContext() *Context {
	return &Context{Now: time.Now()}
}

// MaterializeWriterXID ensures the context's transaction has a real
// XID assigned. M0093 (PG-parity lazy XID allocation): the manager
// no longer allocates an XID at Begin time; the first write site
// must materialise one. Subsequent calls within the same transaction
// short-circuit because c.Tx.XID is already non-zero.
//
// CRITICAL invariant — call this BEFORE any of:
//
//   - isConcurrentlyUpdated (M0090 concurrent-xmax race check). Calling
//     after the check would silently feed it XID=0, producing false
//     negatives that let orphan visible tuples slip through.
//   - storage.NewHeapTuple(ctx.Tx.XID, ...) — would stamp xmin = 0
//     (InvalidTransactionID), making the row invisible to every
//     reader (treated as "no creator").
//   - storage.PageSetHeapTupleXmax(p, slot, ctx.Tx.XID) — would
//     stamp xmax = 0, leaving the old version visible forever.
//   - storage.PageSetHeapTupleLockOnly(p, slot, ctx.Tx.XID, ...)
//
// Read-only paths (scans, snapshot construction, HOT-chain self-
// visibility) tolerate XID=0 — they consult the snapshot's
// InProgress list and never match `h.Xmin == 0`.
//
// When a *BasicSession is wired and we're at the top level (no
// open savepoint), the session's cached tx.XID is updated too so
// later savepoint AllocateSubXid calls see the real parent XID.
func (c *Context) MaterializeWriterXID() error {
	if c.TxnMgr == nil {
		return &ExecError{Code: "XX000", Message: "executor: no TxnMgr in context"}
	}
	if c.Tx.XID != storage.InvalidTransactionID {
		return nil
	}
	xid, err := c.TxnMgr.AssignXID(c.Tx)
	if err != nil {
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	c.Tx.XID = xid
	if sess, ok := c.Session.(*BasicSession); ok {
		sess.OnTopLevelXIDAssigned(xid)
	}
	return nil
}
