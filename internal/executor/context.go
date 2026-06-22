package executor

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/lockwait"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/multixact"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
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

	// Mctx is the statement-level memory context (M0107-0001).
	// Operators acquire child ExprContexts from it for per-row
	// scratch; nil disables mctx-backed arena Datums (tests that
	// don't wire a full server still work via GC-heap fallback).
	Mctx *mctx.Context

	// Storage handles. Heap-touching operators (SeqScan/Insert/
	// Update/Delete) require all four to be set; pure-compute
	// statements (SELECT 1, …) don't.
	Pool    *storage.Pool
	Catalog catalog.Catalog
	// PlanCatalog is a search-path-aware wrapper around Catalog used when
	// calling planner.Plan so that unqualified table names resolve via the
	// session's search_path. Falls back to Catalog when nil. M0097-0022.
	PlanCatalog catalog.Catalog
	TxnMgr      *mvcc.Manager
	Tx          mvcc.Transaction
	Snap        mvcc.Snapshot

	// MultiXact is the process-shared MultiXact member store (the SLRU
	// analog). It is consulted by the row-locking path (stampLockInner) to
	// (a) combine a new lock holder with an existing active lock holder into a
	// MultiXactId rather than overwriting it, and (b) resolve a tuple's
	// MultiXactId xmax back to its member transactions when checking lock
	// conflicts. nil disables the multixact path entirely — the single-holder
	// xmax behaviour (one TransactionID per tuple) is preserved, so tests and
	// deployments that don't wire a store keep working unchanged. M0118-0003.
	MultiXact *multixact.Store

	// Session, if set, is consulted by the Transaction operator to
	// drive BEGIN/COMMIT/ROLLBACK. It also tracks whether the current
	// statement is running inside an explicit transaction block. The
	// wire-protocol path provides a per-connection implementation;
	// tests can leave it nil when the operator under test doesn't
	// need it.
	Session Session

	// AdvisorySessionIdentity is a stable per-connection token used by
	// advisory-lock functions when Session is nil (e.g. auto-commit
	// statements outside an explicit BEGIN/COMMIT block).
	AdvisorySessionIdentity any

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

	// CorrSubqOps caches pre-built, pre-opened operators for correlated
	// scalar subqueries so the same plan can be rescanned for each outer
	// row without repeated Build+openPrep (lock acquire + btree.Open)
	// overhead. Populated by subqueryImpl; indexScanOp.Open detects the
	// reuse and skips openPrep on subsequent calls. Operators are not
	// explicitly closed (locks released at commit; GC handles memory).
	CorrSubqOps map[*planner.SubqueryExpr]Operator

	// CorrSubqHashMaps caches hash maps built for correlated scalar subqueries
	// matching Project(Filter(SeqScan, inner_col = OuterColumnRef)). The hash
	// map is built once by scanning the entire inner table (O(N)) and provides
	// O(1) lookups per outer row, avoiding O(N²) correlated SeqScan patterns
	// even when no btree index exists. Keys are datumKey(inner_col_value);
	// values are the projected result datum.
	CorrSubqHashMaps map[*planner.SubqueryExpr]map[string]Datum

	// MultiAssignSubqCache caches the result row of a MultiAssignSubqRow
	// evaluation (tuple SET subquery). Keyed by *planner.MultiAssignSubqRow
	// pointer (as uintptr). Cleared by the update executor at the start of
	// each row's SET evaluation so correlated subqueries are re-evaluated
	// per row while non-correlated ones are evaluated once per row.
	MultiAssignSubqCache map[uintptr][]Datum

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

	// ActivityPID is kept for backward compat but unused; use ProcNum + Activity instead.
	// Deprecated: will be removed in a future cleanup pass.
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

	// TxnLockBackendID identifies the *transaction* (rather than the
	// single statement) to the lock manager. It is stable for the life
	// of an explicit transaction block, so heavyweight locks acquired
	// under it survive the per-statement ReleaseAll(BackendID) in
	// dispatch.go and are only dropped at COMMIT/ROLLBACK. LOCK TABLE
	// uses it to hold its relation lock until end-of-transaction, the
	// way upstream's lockmgr does. Zero means "not inside an explicit
	// transaction" — execLockTable then falls back to display-only
	// registration (no real blocking lock). M0118-0003 (lock-nowait).
	TxnLockBackendID lockmgr.BackendID

	// ProcNum is the backend's slot index in the Manager's ProcArray.
	// Set once per connection in serveConn and carried on every ectx so
	// operators that call TxnMgr.Begin (e.g. execBegin) can supply it.
	ProcNum int32

	// WorkTableRows is set by RecursiveUnionOp during fixpoint
	// iteration and read by WorkTableScanOp to produce rows from
	// the current working table. M0016-0004 (recursive CTE).
	WorkTableRows []Row

	// WorkMem is the per-operator memory budget in bytes for
	// spill-to-disk. Zero means unlimited (no spill). Defaults to
	// 512 MiB when the GUC is active. See milestone 0037.
	WorkMem int64

	// GetSetting returns the effective session GUC value for the given
	// name. Wired by the server from the per-connection SessionRegistry;
	// nil means SQL built-ins like current_setting() cannot resolve
	// session state in this context.
	GetSetting func(name string) (string, bool)

	// SetSetting applies a session or transaction-local GUC mutation.
	// Wired by the server so SQL built-ins like set_config() can mutate
	// the same SessionRegistry as SET / SET LOCAL.
	SetSetting func(name, value string, isLocal bool) error

	// AllSettings returns the effective SHOW ALL view for this session.
	AllSettings func() []SettingValue

	// ResetSetting and ResetAllSettings expose RESET name / RESET ALL to
	// executor-run utility statements.
	ResetSetting     func(name string) error
	ResetAllSettings func()

	// BeginLocalTransaction and EndLocalTransaction bracket an explicit
	// transaction so that SET LOCAL changes are discarded on COMMIT/ROLLBACK.
	// Wired to SessionRegistry.BeginTransaction / EndTransaction. M0097-0023.
	BeginLocalTransaction func()
	EndLocalTransaction   func()

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

	// NoticesWithDetail accumulates NOTICE messages that include a multi-line
	// DETAIL field (e.g. DROP SCHEMA CASCADE cascade list). The Detail string
	// contains newline-separated continuation lines; the protocol layer sends
	// the first continuation as the DETAIL field and psql formats the rest as
	// plain continuation lines in its output. M0097-0020.
	NoticesWithDetail []NoticeWithDetail

	// NoticeFlush, when non-nil, is called for each NOTICE as it is generated
	// so the server can send it to the client in real-time (before CommandComplete).
	// This matches PostgreSQL's behavior where NOTICE messages arrive before
	// any blocking point. M0100-0005.
	NoticeFlush func(string)

	// Warnings accumulates WARNING-severity messages emitted during statement
	// execution (e.g. "you don't own a lock of type ExclusiveLock" from
	// pg_advisory_unlock* called without a matching held lock). The server
	// emits these as NoticeResponse with WARNING severity before CommandComplete.
	// M0097-0021.
	Warnings []string

	// Sequence session state — maps sequence key → last nextval result
	// for currval(); LastSeqVal/LastSeqSet track the lastval() return. M0097-0009.
	CurrSeqVals map[string]int64
	LastSeqVal  int64
	LastSeqSet  bool
	LastSeqName string // name of sequence that produced LastSeqVal (for drop-detection)

	// TempTableShadows maps table name → original permanent *catalog.Table for
	// CREATE TEMP TABLE shadowing. Populated by execCreateTable; restored on DROP.
	TempTableShadows map[string]*catalog.Table
	// PendingEnumValues tracks enum labels added via ALTER TYPE … ADD VALUE inside
	// the current explicit transaction.  They must not be used until COMMIT.
	// map[enumTypeName][label]=true.  Nil when not in an explicit transaction.
	PendingEnumValues map[string]map[string]bool
	// PendingEnumRenames tracks ALTER TYPE … RENAME TO operations within the
	// current explicit transaction.  On ROLLBACK, reversed in reverse order.
	// M0097-0022.
	PendingEnumRenames []EnumRenameEntry
	// PendingCreatedEnums tracks enum types created via CREATE TYPE … AS ENUM
	// within the current explicit transaction.  On ROLLBACK, created types are
	// dropped from the catalog.  map[enumTypeName(lowercase)]=true.  M0097-0022.
	PendingCreatedEnums map[string]bool
	// PendingCreatedComposites tracks composite types created via
	// CREATE TYPE … AS (...) within the current explicit transaction.  On
	// ROLLBACK, created types are dropped from the catalog so they do not
	// outlive the aborted transaction (the enum analogue is
	// PendingCreatedEnums).  map[compositeTypeName(lowercase)]=true.
	// DU-002 slice 244.
	PendingCreatedComposites map[string]bool

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

	// LogCanonical, when non-nil, emits a PG-canonical WAL record (XLOG_HEAP_INSERT,
	// XLOG_BTREE_INSERT_LEAF, …) so a vanilla PG18 standby can replay catalog DDL
	// mutations. Set only when the WAL writer is in PageHeaders mode (PG-compat WAL);
	// nil in tests and legacy-WAL configurations. M0106-0010 batched-32.
	LogCanonical catalog.LogCanonicalFunc

	// Promote, when non-nil, is invoked by pg_promote() to trigger the
	// standby-to-primary promotion sequence. nil means the server is not
	// a standby (or pg_promote is not yet wired) — pg_promote() returns
	// false without error. M0106-0010 batched-34.
	Promote func() error

	// IsStandby reflects whether the server is currently acting as a hot
	// standby. pg_is_in_recovery() returns this value. M0106-0010 batched-34.
	IsStandby bool

	// MaterializedCTEs holds rows from data-modifying CTEs (INSERT/UPDATE/DELETE/MERGE
	// with RETURNING). Populated by cteDMLPrefixOp before the outer query runs.
	// Key is the lowercase CTE name; value is the materialized RETURNING rows.
	MaterializedCTEs map[string][][]Datum

	// CTERowCache holds rows materialized from regular (SELECT) CTEs when the same
	// CTE is referenced more than once in a query. The first CTEScan for a given
	// name buffers all rows here; subsequent scans replay from the buffer.
	// Key is the lowercase CTE name; value is the materialized row set (nil = not yet filled).
	CTERowCache map[string][]Row

	// CTEWriteFence, when non-nil, is a set of row pointers written by DML
	// CTEs during their execution phase. The outer query's seqScanOp skips
	// these tuples to implement CTE snapshot isolation: DML-CTE writes must
	// not be visible to the outer SELECT. Cleared between statements.
	CTEWriteFence map[storage.ItemPointer]struct{}

	// CTENewToOld maps each new tuple pointer added to CTEWriteFence to the
	// original (pre-CTE) tuple it replaced. Used to detect when a sub-command
	// within the CTE's expression evaluation modifies a CTE-written row, which
	// must raise an error in the outer UPDATE/DELETE.
	CTENewToOld map[storage.ItemPointer]storage.ItemPointer

	// CTESelfModifiedErrors is the set of original (pre-CTE) tuple pointers
	// for which the outer UPDATE/DELETE must raise ERRCODE_TRIGGERED_DATA_CHANGE_VIOLATION.
	// Populated when InDMLCTE=true and a write stamps xmax on a CTEWriteFence tuple.
	CTESelfModifiedErrors map[storage.ItemPointer]struct{}

	// CTESelfModErr, when non-nil, is the error scanMatching returns upon
	// encountering an invisible-due-to-own-xmax tuple in CTESelfModifiedErrors.
	CTESelfModErr error

	// InDMLCTE is true while cteDMLPrefixOp is executing its DML sub-plans.
	// Write operators register their output pointers in CTEWriteFence when this is set.
	InDMLCTE bool

	// PrepStmtsRows, when non-nil, is called by the valuesOp that backs
	// pg_prepared_statements to return the session's current prepared
	// statement rows (name, statement, parameter_types, result_types).
	// Wired by the server from the per-connection preparedStatements store.
	PrepStmtsRows func() [][]string

	// CurrentDatabase is the name of the database this connection is bound to
	// (from the startup packet). Used to scope per-database catalogs such as
	// pg_extension. Empty in embedded/test contexts. M0110-0003 (AC-002 gap #7c).
	CurrentDatabase string

	// ExtensionRows, when non-nil, is called by the valuesOp that backs
	// pg_extension to return the rows visible in CurrentDatabase. pg_extension is
	// per-database in PostgreSQL but goopg shares one in-memory catalog, so the
	// server wires this to catalog.ExtensionRowsForDB(CurrentDatabase) to filter
	// the global registry per connecting database. M0110-0003 (AC-002 gap #7c).
	ExtensionRows func() [][]string

	// NonSuperuserRole, when non-empty, means the session is currently running
	// under a non-superuser role (set via SET SESSION AUTHORIZATION). Privilege
	// checks that require superuser (e.g. LEAKPROOF function attribute) must
	// reject when this is set.
	NonSuperuserRole string

	// SetSessionAuthorization, when non-nil, updates the per-connection
	// NonSuperuserRole when SET SESSION AUTHORIZATION is executed via the
	// parser path (multi-statement batches). The caller (operators_utility_settings)
	// passes the role name or "" to restore superuser status.
	SetSessionAuthorization func(role string)

	// MergeAction holds the current MERGE action for MergeActionExpr evaluation
	// in a MERGE RETURNING clause. Set by mergeOp.collectReturningRow. M0100-0007.
	MergeAction planner.MergeActionKind
	// MergeOldRow/MergeNewRow hold the pre/post action rows for MergeWholeRowRef
	// evaluation in a MERGE RETURNING clause. nil = absent (INSERT has no old;
	// DELETE has no new). M0100-0007.
	MergeOldRow Row
	MergeNewRow Row
}

// backendPID resolves the owning backend's PID string for synthetic pg_locks
// rows (spectoken / transactionid) that must join pg_stat_activity USING (pid).
// ActivityPID is deprecated and always empty; the live PID lives in the activity
// registry keyed by ProcNum. Falls back to ActivityPID when no registry is wired
// (unit tests). Returns "" when unresolvable — such rows simply won't join.
func (c *Context) backendPID() string {
	if c.Activity != nil {
		if pid := c.Activity.PIDForProcNum(c.ProcNum); pid != "" {
			return pid
		}
	}
	return c.ActivityPID
}

// SettingValue is one effective session setting exposed to SHOW ALL.
type SettingValue struct {
	Name  string
	Value string
}

// AddNotice appends a NOTICE-severity message to the context's notice queue.
// If NoticeFlush is set, the message is also flushed to the client immediately
// (matching PostgreSQL's real-time notice delivery). M0100-0005.
func (c *Context) AddNotice(msg string) {
	if c.NoticeFlush != nil {
		// Inline flush: send the notice to the client immediately so it
		// arrives before any row-level lock wait. When NoticeFlush is wired
		// (server path), the notice is NOT additionally buffered in Notices
		// to avoid double-delivery at CommandComplete time.
		c.NoticeFlush(msg)
		return
	}
	c.Notices = append(c.Notices, msg)
}

// TakeNotices returns and clears the accumulated notices.
func (c *Context) TakeNotices() []string {
	n := c.Notices
	c.Notices = nil
	return n
}

// NoticeWithDetail is a NOTICE that carries a multi-line DETAIL field.
// Used for DROP CASCADE output where the detail lists all cascaded objects.
type NoticeWithDetail struct {
	Message string
	Detail  string // newline-separated lines; each becomes one psql output line
}

// AddNoticeWithDetail appends a NOTICE with a DETAIL field to the queue.
// The detail lines are formatted by the wire layer so psql emits the first
// line with "DETAIL:" prefix and the rest as plain continuation lines.
func (c *Context) AddNoticeWithDetail(msg, detail string) {
	c.NoticesWithDetail = append(c.NoticesWithDetail, NoticeWithDetail{Message: msg, Detail: detail})
}

// TakeNoticesWithDetail returns and clears the detail-carrying notice queue.
func (c *Context) TakeNoticesWithDetail() []NoticeWithDetail {
	n := c.NoticesWithDetail
	c.NoticesWithDetail = nil
	return n
}

// AddWarning appends a WARNING-severity message to the context's warning queue.
// M0097-0021.
func (c *Context) AddWarning(msg string) {
	c.Warnings = append(c.Warnings, msg)
}

// TakeWarnings returns and clears the accumulated warnings.
// M0097-0021.
func (c *Context) TakeWarnings() []string {
	w := c.Warnings
	c.Warnings = nil
	return w
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
	// Record wait event for lock waits using the atomic hot path (M0107-0005).
	if c.Activity != nil {
		c.Activity.WaitEventStart(c.ProcNum, activity.WaitTypeLock, activity.WaitRelationLock)
	}
	lockCtx := context.Background()
	if c.Ctx != nil {
		lockCtx = c.Ctx
	}
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	err := c.LockMgr.Acquire(lockCtx, c.BackendID, tag, mode)
	if c.Activity != nil {
		c.Activity.WaitEventEnd(c.ProcNum)
	}
	if err == nil {
		return nil
	}
	if err == lockmgr.ErrDeadlockDetected {
		return &ExecError{Code: "40P01", Message: "deadlock detected"}
	}
	if ee := lockWaitCancelError(err); ee != nil {
		return ee
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
	if ee := lockWaitCancelError(err); ee != nil {
		return ee
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

// tableLockMgr is the dedicated, always-on heavyweight lock manager
// that backs `LOCK TABLE` (M0118-0003 lock-nowait). It is deliberately
// SEPARATE from Context.LockMgr (which is nil in the production server,
// so the relation/tuple acquireRelLock helpers are no-ops there and
// cross-statement row blocking instead rides xmax/WaitForXID). Keeping
// LOCK TABLE on its own manager confines the blast radius of real
// heavyweight locking to explicit LOCK statements: ordinary scans,
// DML and DDL never touch it, so enabling it can't introduce new
// blocking on the pgbench/TPC-H hot paths.
var tableLockMgr = lockmgr.New()

// ReleaseTableLocks drops every LOCK TABLE heavyweight lock held by the
// given transaction backend identity. The wire layer calls it from
// connTxState.End() at COMMIT/ROLLBACK (and on connection teardown), so
// LOCK TABLE locks live exactly as long as the transaction that took
// them. M0118-0003 (lock-nowait).
func ReleaseTableLocks(b lockmgr.BackendID) {
	if b == 0 {
		return
	}
	tableLockMgr.ReleaseAll(b)
}

// acquireRelLockTxn acquires a LOCK TABLE relation lock on the dedicated
// tableLockMgr under the *transaction* backend identity
// (TxnLockBackendID) rather than the per-statement BackendID, so the
// lock is held until COMMIT/ROLLBACK instead of being dropped by
// dispatch.go's per-statement ReleaseAll. A LOCK acquired in one
// statement therefore still blocks a conflicting session two statements
// later, the way upstream's transaction-scoped lockmgr does.
//
// nowait selects the non-blocking path (LOCK … NOWAIT): on contention
// it returns SQLSTATE 55P03 immediately instead of waiting. The
// lockmgr early-grant rule lets a session that already holds a
// self-compatible mode jump ahead of a conflicting parked waiter, so
// NOWAIT can still succeed when the requester already holds a stronger
// lock (the lock-nowait spec's s1b step).
//
// A zero TxnLockBackendID (statement run outside an explicit
// transaction block) makes this a no-op so autocommit LOCK keeps its
// historical display-only behaviour and never leaks a lock that nothing
// would release.
func (c *Context) acquireRelLockTxn(rel storage.RelFileNode, mode lockmgr.Mode, nowait bool) error {
	if c.TxnLockBackendID == 0 {
		return nil
	}
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	if nowait {
		err := tableLockMgr.TryAcquire(c.TxnLockBackendID, tag, mode)
		if err == nil {
			return nil
		}
		if err == lockmgr.ErrLockNotAvailable {
			return &ExecError{Code: "55P03", Message: "could not obtain lock on relation"}
		}
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	if c.Activity != nil {
		c.Activity.WaitEventStart(c.ProcNum, activity.WaitTypeLock, activity.WaitRelationLock)
	}
	lockCtx := context.Background()
	if c.Ctx != nil {
		lockCtx = c.Ctx
	}
	err := tableLockMgr.AcquireWithTimeout(lockCtx, c.TxnLockBackendID, tag, mode, c.deadlockTimeout())
	if c.Activity != nil {
		c.Activity.WaitEventEnd(c.ProcNum)
	}
	if err == nil {
		return nil
	}
	if err == lockmgr.ErrDeadlockDetected {
		return &ExecError{Code: "40P01", Message: "deadlock detected"}
	}
	if ee := lockWaitCancelError(err); ee != nil {
		return ee
	}
	return &ExecError{Code: "XX000", Message: err.Error()}
}

// firstNormalObjectOID is the first OID assigned to user-created objects
// (mirrors upstream FirstNormalObjectId). Relations below it are system
// catalogs, which goopg serves from virtual/in-memory builders and never
// AccessExclusive-locks, so scan-read locking skips them.
const firstNormalObjectOID = 16384

// acquireScanReadLockTxn takes a transaction-scoped ACCESS SHARE heavyweight
// lock on the relation a scan is about to read, so a concurrent LOCK TABLE (or
// any other AccessExclusive acquirer) in another session blocks until this
// transaction ends — matching PostgreSQL, where every relation a query reads is
// locked in ACCESS SHARE mode for the lifetime of the transaction.
//
// It is deliberately confined to keep the heavyweight manager off the hot path:
//   - outside an explicit transaction block (TxnLockBackendID == 0) it is a
//     no-op, so single-statement autocommit reads never touch tableLockMgr —
//     they cannot participate in a cross-statement lock conflict anyway;
//   - system catalogs (OID < firstNormalObjectOID) are skipped.
//
// ACCESS SHARE is self-compatible and conflicts only with ACCESS EXCLUSIVE, so
// in the common case (no concurrent LOCK TABLE / DDL) the acquire is granted
// instantly, and lockmgr is idempotent so re-scanning the same relation within a
// transaction is a cheap mask check. When it does wait, the lock_timeout /
// statement_timeout budget carried on the statement context governs the wait
// exactly like LOCK TABLE does (acquireRelLockTxn). This is the table-level half
// of the timeouts isolation spec. M0118-0009.
func (c *Context) acquireScanReadLockTxn(rel storage.RelFileNode) error {
	if c.TxnLockBackendID == 0 || rel.RelOid < firstNormalObjectOID {
		return nil
	}
	return c.acquireRelLockTxn(rel, lockmgr.AccessShareLock, false)
}

// lockWaitCancelError maps the cancellation/timeout error returned by a
// lock-wait primitive (lockmgr.Acquire* or mvcc.WaitForXID) to the matching
// SQLSTATE-57014 ExecError, or nil when err is not a recognised cancellation.
// The three message variants mirror PostgreSQL exactly:
//   - lock_timeout      → "canceling statement due to lock timeout"
//   - statement_timeout → "canceling statement due to statement timeout"
//   - client cancel     → "canceling statement due to user request"
//
// statement_timeout is the only source of a context *deadline* in goopg, so
// context.DeadlineExceeded is reported as a statement timeout; a plain
// context.Canceled is a CancelRequest / connection teardown. M0118-0009.
func lockWaitCancelError(err error) *ExecError {
	if ee := lockWaitTimeoutError(err); ee != nil {
		return ee
	}
	if errors.Is(err, context.Canceled) {
		return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
	}
	return nil
}

// lockWaitTimeoutError is the subset of lockWaitCancelError that recognises
// only the two *timeout* causes — lock_timeout and statement_timeout — and
// returns nil for a plain client cancellation. Lock-wait sites that retry on
// connection teardown (epqWait, the row-lock chain walk) use it so only an
// explicit timeout aborts the statement. M0118-0009.
func lockWaitTimeoutError(err error) *ExecError {
	switch {
	case errors.Is(err, lockwait.ErrLockTimeout):
		return &ExecError{Code: "57014", Message: "canceling statement due to lock timeout"}
	case errors.Is(err, context.DeadlineExceeded):
		return &ExecError{Code: "57014", Message: "canceling statement due to statement timeout"}
	default:
		return nil
	}
}

// deadlockTimeout returns the session-effective `deadlock_timeout` GUC as
// a duration, governing how long a LOCK TABLE wait parks before the
// deadlock detector runs. Falls back to lockmgr.DefaultDeadlockTimeout
// when the GUC is unset or unparseable. The effective GUC value is a bare
// integer in milliseconds (UnitMs canonicalises away the unit suffix).
// M0118-0004.
func (c *Context) deadlockTimeout() time.Duration {
	if c.GetSetting == nil {
		return lockmgr.DefaultDeadlockTimeout
	}
	v, ok := c.GetSetting("deadlock_timeout")
	if !ok {
		return lockmgr.DefaultDeadlockTimeout
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || ms <= 0 {
		return lockmgr.DefaultDeadlockTimeout
	}
	return time.Duration(ms) * time.Millisecond
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

// EnumRenameEntry records one ALTER TYPE … RENAME TO operation for transactional rollback.
// OldName and NewName are lowercase. M0097-0022.
type EnumRenameEntry struct{ OldName, NewName string }
