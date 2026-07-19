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

	// ExplainSettings returns the EXPLAIN (SETTINGS) list: GUCs flagged
	// FlagExplain whose effective value differs from their built-in
	// default. Wired by the server from SessionRegistry.ExplainVariables;
	// nil means EXPLAIN (SETTINGS) always reports no modified GUCs (the
	// TEXT format then omits the "Settings:" line entirely, matching
	// upstream's behaviour with zero modified GUCs).
	ExplainSettings func() []SettingValue

	// GetSettingDisplay and AllSettingsDisplay are the client-visible-display
	// counterparts of GetSetting/AllSettings — unit-flagged GUCs come back
	// formatted like real PG's SHOW/current_setting() (e.g. "77MB" rather
	// than the bare "78848" GetSetting returns for internal consumers).
	// Wired by the server alongside GetSetting/AllSettings; nil falls back
	// to the raw GetSetting/AllSettings so callers that predate this field
	// (tests, embedded contexts) keep working unformatted. Used by SHOW /
	// SHOW ALL execution and the current_setting() SQL builtin — never by
	// internal GUC consumers that parse the raw numeric value themselves.
	GetSettingDisplay  func(name string) (string, bool)
	AllSettingsDisplay func() []SettingValue

	// ResetSetting and ResetAllSettings expose RESET name / RESET ALL to
	// executor-run utility statements.
	ResetSetting     func(name string) error
	ResetAllSettings func()

	// BeginLocalTransaction and EndLocalTransaction bracket an explicit
	// transaction so that SET LOCAL changes are discarded on COMMIT/ROLLBACK.
	// Wired to SessionRegistry.BeginTransaction / EndTransaction. M0097-0023.
	BeginLocalTransaction func()
	EndLocalTransaction   func()

	// PLpgSQLCommitChain commits (rollback=false) or rolls back (rollback=true)
	// the current transaction and immediately begins a fresh one, updating
	// ctx.Tx / ctx.Snap in place. It backs PL/pgSQL `COMMIT;` / `ROLLBACK;`
	// transaction-control statements in a non-atomic execution context (a
	// top-level DO block / procedure run outside an explicit transaction). It is
	// installed by the simple-query dispatch ONLY when the statement runs in
	// auto-commit mode; inside an explicit BEGIN block it is nil, so the runtime
	// raises SQLSTATE 2D000 (invalid transaction termination). M0118-0008.
	PLpgSQLCommitChain func(rollback bool) error

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
	// PendingCreatedRangeTypes tracks range types created via CREATE TYPE …
	// AS RANGE within the current explicit transaction.  On ROLLBACK,
	// created types are dropped from the catalog so they do not outlive the
	// aborted transaction (mirrors PendingCreatedComposites; range types had
	// no undo tracking at all until this field was added).
	// map[rangeTypeName(lowercase)]=true.  M0122-0007 4e follow-up.
	PendingCreatedRangeTypes map[string]bool

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

	// AsyncCommit is true only when the session-effective
	// `synchronous_commit` GUC is literally "off" — the one level that also
	// skips waiting for the LOCAL WAL flush (SyncCommitMode/SyncRepOff above
	// collapses "off" and "local" together, since both skip the *remote*
	// wait; "local" still requires the local flush, so it is NOT distinct
	// here). CommitTransaction below is the single call site that reads
	// this. M0117-0007 Part B.
	AsyncCommit bool

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

	// OnRoleDropped, when non-nil, notifies the server layer that DROP
	// ROLE/USER/GROUP removed a role, so the connection-time role set and
	// the auth UserStore stay in sync with the catalog registry. DROP ROLE
	// parses as a generic DropStmt and lands in execDropCompat's role arm —
	// unlike CREATE/ALTER ROLE, which only the server-side intercept sees —
	// so without this hook the two role-DDL paths silently diverge
	// (root-0021; the recurring sibling-path trap). Set by dispatch.
	OnRoleDropped func(name string)

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

	// DeadlockVictim is set by the pg_class in-place lock waits
	// (waitPgClassInplaceXID) when this statement is chosen as a deadlock
	// victim and raises SQLSTATE 40P01. The wire dispatch layer reads it on the
	// failure path to abort this transaction's MVCC state in place — clearing
	// its proc-array slot and waking any WaitForXID waiter — while keeping the
	// transaction block open, mirroring PostgreSQL's AbortTransaction releasing
	// the XID the moment the deadlock is reported (the winner blocked on the
	// victim's catalog-tuple xmax must unblock at the abort, not at the victim's
	// later explicit ROLLBACK). Reset per statement. Design 0118-0115
	// (intra-grant-inplace perm 8).
	DeadlockVictim bool

	// PrepStmtsRows, when non-nil, is called by the valuesOp that backs
	// pg_prepared_statements to return the session's current prepared
	// statement rows (name, statement, parameter_types, result_types).
	// Wired by the server from the per-connection preparedStatements store.
	PrepStmtsRows func() [][]string

	// CurrentDatabase is the name of the database this connection is bound to
	// (from the startup packet). Used to scope per-database catalogs such as
	// pg_extension. Empty in embedded/test contexts. M0110-0003 (AC-002 gap #7c).
	CurrentDatabase string

	// CurrentDatabaseOid is the REAL, physical pg_database.oid resolved for
	// CurrentDatabase via catalog.(*InMemory).ResolveDatabaseOid — the same
	// oid that keys on-disk storage/ACLs, NOT pg_database's displayed oid
	// column (which shows "postgres" as a legacy 16384 placeholder for
	// compat, see ResolveDatabaseOid's doc comment). Zero if CurrentDatabase
	// is empty or unresolvable (embedded/test contexts, or a database name
	// the catalog doesn't recognize). This is plumbing only today — no
	// lookup site yet keys off it, since catalog.InMemory's table/index
	// namespace is still one shared, process-wide map regardless of which
	// database a connection is bound to (M0122-0007 physical-storage-
	// isolation slice 4; see docs/design/0122-0018-per-database-catalog-namespace.md).
	CurrentDatabaseOid uint32

	// ExtensionRows, when non-nil, is called by the valuesOp that backs
	// pg_extension to return the rows visible in CurrentDatabase. pg_extension is
	// per-database in PostgreSQL but goopg shares one in-memory catalog, so the
	// server wires this to catalog.ExtensionRowsForDB(CurrentDatabase) to filter
	// the global registry per connecting database. M0110-0003 (AC-002 gap #7c).
	ExtensionRows func() [][]string

	// PgClassRows, when non-nil, is called by the valuesOp that backs pg_class
	// to return the rows visible to this connection: CurrentDatabaseOid's own
	// tables/indexes rather than always DefaultDBOid's (catalog.InMemory's
	// PGClassRowsForDBOid, mirroring ExtensionRows above). Wired by the server
	// to close over CurrentDatabaseOid. M0122-0007 4e.
	PgClassRows func() [][]string

	// PgIndexesRows / PgTablesRows mirror PgClassRows above for the pg_indexes
	// and pg_tables views: each lists CurrentDatabaseOid's own tables/indexes
	// rather than always DefaultDBOid's (catalog.InMemory's
	// PGIndexesRowsForDBOid / PGTablesRowsForDBOid). Wired by the server to
	// close over CurrentDatabaseOid. M0122-0007 4e follow-up 24.
	PgIndexesRows func() [][]string
	PgTablesRows  func() [][]string

	// PgConstraintRows mirrors PgClassRows above for the pg_constraint table:
	// it lists CurrentDatabaseOid's own tables'/indexes' constraints rather
	// than always DefaultDBOid's (catalog.InMemory's
	// PGConstraintRowsForDBOid). Wired by the server to close over
	// CurrentDatabaseOid. M0122-0007 4e follow-up 25.
	PgConstraintRows func() [][]string

	// PgIndexRows mirrors PgConstraintRows above for the pg_index catalog
	// table: it lists CurrentDatabaseOid's own indexes rather than always
	// DefaultDBOid's (catalog.InMemory's PGIndexRowsForDBOid). Wired by the
	// server to close over CurrentDatabaseOid. M0122-0007 4e follow-up 26.
	PgIndexRows func() [][]string

	// PgAttrdefRows / PgDependRows mirror PgIndexRows above for the pg_attrdef
	// and pg_depend catalog tables: each lists CurrentDatabaseOid's own column
	// defaults / dependency rows rather than always DefaultDBOid's
	// (catalog.InMemory's PGAttrdefRowsForDBOid / PGDependRowsForDBOid). Both
	// must be wired with the SAME dbOid so their oid numbering stays in
	// lockstep (see PGAttrdefRowsForDBOid's doc comment). Wired by the server
	// to close over CurrentDatabaseOid. M0122-0007 4e follow-up 27.
	PgAttrdefRows func() [][]string
	PgDependRows  func() [][]string

	// PgInheritsRows mirrors PgIndexRows above for the pg_inherits catalog
	// table: it lists CurrentDatabaseOid's own inheritance/partition
	// parent-child rows rather than always DefaultDBOid's (catalog.InMemory's
	// PGInheritsRowsForDBOid). Wired by the server to close over
	// CurrentDatabaseOid. M0122-0007 4e follow-up 28.
	PgInheritsRows func() [][]string

	// PgPolicyRows mirrors PgInheritsRows above for the pg_policy catalog
	// table: it lists CurrentDatabaseOid's own tables' row-level-security
	// policies rather than always DefaultDBOid's (catalog.InMemory's
	// PGPolicyRowsForDBOid). Wired by the server to close over
	// CurrentDatabaseOid. M0122-0007 4e follow-up 29.
	PgPolicyRows func() [][]string

	// PgTriggerRows mirrors PgPolicyRows above for the pg_trigger catalog
	// table: it lists CurrentDatabaseOid's own tables' triggers rather than
	// always DefaultDBOid's (catalog.InMemory's PGTriggerRowsForDBOid). Wired
	// by the server to close over CurrentDatabaseOid. M0122-0007 4e
	// follow-up 30.
	PgTriggerRows func() [][]string

	// PgRewriteRows mirrors PgTriggerRows above for the pg_rewrite catalog
	// table: it lists CurrentDatabaseOid's own tables' CREATE RULE DO-NOTHING
	// rules rather than always DefaultDBOid's (catalog.InMemory's
	// PGRewriteRowsForDBOid). Wired by the server to close over
	// CurrentDatabaseOid. M0122-0007 4e follow-up 31.
	PgRewriteRows func() [][]string

	// PgForeignTableRows mirrors PgRewriteRows above for the pg_foreign_table
	// catalog table: it lists CurrentDatabaseOid's own foreign tables rather
	// than always DefaultDBOid's (catalog.InMemory's
	// PGForeignTableRowsForDBOid). Wired by the server to close over
	// CurrentDatabaseOid. M0122-0007 4e follow-up 32.
	PgForeignTableRows func() [][]string

	// PgSequenceRows mirrors PgForeignTableRows above for the pg_sequence
	// catalog table: it lists CurrentDatabaseOid's own sequences rather than
	// always DefaultDBOid's (catalog.InMemory's PGSequenceRowsForDBOid).
	// Wired by the server to close over CurrentDatabaseOid. M0122-0007 4e
	// follow-up 34.
	PgSequenceRows func() [][]string

	// PgForeignServerRows mirrors PgForeignTableRows above for the
	// pg_foreign_server catalog table: it lists CurrentDatabaseOid's own
	// CREATE SERVER'd servers rather than always DefaultDBOid's
	// (catalog.InMemory's PGForeignServerRowsForDBOid). Wired by the server
	// to close over CurrentDatabaseOid. M0122-0007 4e follow-up 36.
	PgForeignServerRows func() [][]string

	// PgUserMappingsRows mirrors PgForeignServerRows above for the
	// pg_user_mappings catalog table: it lists CurrentDatabaseOid's own
	// CREATE USER MAPPING'd mappings rather than always DefaultDBOid's
	// (catalog.InMemory's PGUserMappingsRowsForDBOid). Wired by the server
	// to close over CurrentDatabaseOid. M0122-0007 4e follow-up 37.
	PgUserMappingsRows func() [][]string

	// PgCollationRows mirrors PgForeignServerRows above for the pg_collation
	// catalog table: it lists CurrentDatabaseOid's own CREATE COLLATION'd
	// collations (plus the shared BKI-pinned builtins) rather than always
	// DefaultDBOid's (catalog.InMemory's PGCollationRowsForDBOid). Wired by
	// the server to close over CurrentDatabaseOid. M0122-0007 4e follow-up
	// (DU-002 round-trip probe unblock).
	PgCollationRows func() [][]string

	// PgConversionRows mirrors PgCollationRows above for the pg_conversion
	// catalog table: it lists CurrentDatabaseOid's own CREATE [DEFAULT]
	// CONVERSION'd conversions rather than always DefaultDBOid's
	// (catalog.InMemory's PGConversionRowsForDBOid). Wired by the server to
	// close over CurrentDatabaseOid. M0122-0007 4e follow-up (DU-002
	// round-trip probe unblock).
	PgConversionRows func() [][]string

	// PgTSDictRows mirrors PgConversionRows above for the pg_ts_dict catalog
	// table: it lists CurrentDatabaseOid's own CREATE TEXT SEARCH
	// DICTIONARY'd dictionaries (plus the shared BKI-pinned "simple" builtin)
	// rather than always DefaultDBOid's (catalog.InMemory's
	// PGTSDictRowsForDBOid). Wired by the server to close over
	// CurrentDatabaseOid. M0122-0007 4e follow-up (DU-002 round-trip probe
	// unblock).
	PgTSDictRows func() [][]string

	// NonSuperuserRole, when non-empty, means the session is currently running
	// under a non-superuser role (set via SET SESSION AUTHORIZATION). Privilege
	// checks that require superuser (e.g. LEAKPROOF function attribute) must
	// reject when this is set.
	NonSuperuserRole string

	// SetSessionAuthorization, when non-nil, updates the per-connection
	// NonSuperuserRole when SET SESSION AUTHORIZATION is executed via the
	// parser path (multi-statement batches). The caller (operators_utility_settings)
	// passes the role name (or "" to restore superuser status) and whether the
	// statement was SET LOCAL — the server snapshots the pre-change role so a
	// LOCAL change reverts at COMMIT/ROLLBACK (mirrors PostgreSQL's
	// GUC_ACTION_LOCAL stack, guc.c). M0119-0004.
	SetSessionAuthorization func(role string, local bool)

	// SetRole, when non-nil, updates the per-connection NonSuperuserRole when
	// SET ROLE / RESET ROLE is executed via the parser path (multi-statement
	// simple-query batches and the extended-query protocol). Same contract
	// as SetSessionAuthorization: the role name (or "" to restore superuser
	// status) plus the LOCAL flag. Wired to the same closure as
	// SetSessionAuthorization by the server (SET ROLE and SET SESSION
	// AUTHORIZATION both flip connTx.NonSuperuserRole identically). M0119-0004.
	SetRole func(role string, local bool)

	// CancelBackend, when non-nil, signals the backend identified by pid to
	// cancel its currently-executing query (the engine behind the
	// pg_cancel_backend(pid) SQL function). Returns true if a backend with that
	// pid is connected (signal sent — true even when the target is idle, as in
	// PG), false if the pid is unknown. The server wires this to its
	// process-wide cancel registry; nil in unit/embedded contexts where no peer
	// backends exist. M0118-0008 (detach-partition-concurrently-3/4 s1cancel).
	CancelBackend func(pid int32) bool

	// TerminateBackend, when non-nil, signals the backend identified by pid to
	// terminate (close its connection) — the engine behind the
	// pg_terminate_backend(pid) SQL function (the SQL analog of SIGTERM).
	// Returns true if a backend with that pid is connected, false if the pid is
	// unknown. The server wires this to its process-wide cancel registry, whose
	// entries carry the connection's root-context cancel func; nil in
	// unit/embedded contexts. Self-termination does NOT go through this callback
	// (the expr layer returns ErrSelfTerminate instead, so the server can emit
	// the FATAL and tear down its own connection cleanly). M0118-0009
	// (temp-schema-cleanup process-exit permutation).
	TerminateBackend func(pid int32) bool

	// QueueNotify, when non-nil, buffers a notification (the engine behind the
	// pg_notify(channel, payload) SQL function) into the connection's
	// transaction so it is published to LISTENers at commit — exactly as the
	// NOTIFY statement does. The server wires this to connTxState.bufferNotify;
	// nil in unit/embedded contexts. M0118-0009 (async-notify).
	QueueNotify func(channel, payload string)

	// NotifyQueueUsage, when non-nil, returns the fraction (in [0, 1]) of the
	// async-notification queue currently occupied by undelivered notifications —
	// the engine behind the pg_notification_queue_usage() SQL function. The
	// server wires this to notifyHub.QueueUsage; nil in unit/embedded contexts
	// (treated as 0.0). M0118-0009 (async-notify).
	NotifyQueueUsage func() float64

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

// CommitTransaction commits tx through TxnMgr, choosing the synchronous or
// async-commit path per AsyncCommit (the session-effective
// `synchronous_commit` GUC). Every live commit call site should route
// through this method rather than calling TxnMgr.Commit directly, so a
// session's synchronous_commit setting is honoured consistently.
// M0117-0007 Part B.
func (c *Context) CommitTransaction(tx mvcc.Transaction) error {
	if c.AsyncCommit {
		return c.TxnMgr.CommitAsync(tx)
	}
	return c.TxnMgr.Commit(tx)
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
	lm, backend, timeout := c.tupleLockManager()
	if lm == nil {
		return nil
	}
	lockCtx := context.Background()
	if c.Ctx != nil {
		lockCtx = c.Ctx
	}
	tag := tupleLockTag(rel, ptr)
	err := lm.AcquireWithTimeout(lockCtx, backend, tag, mode, timeout)
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

// tupleLockManager selects the lock manager and backend identity that
// tuple-level (row) locks acquire on, plus whether tuple locking is active
// at all (returns a nil manager when it should be a no-op).
//
//   - In tests, Context.LockMgr is a real per-context manager; use it under
//     the per-statement BackendID exactly as before.
//   - In the production server Context.LockMgr is nil by design (relation
//     locking stays off the hot path — see the tableLockMgr comment). Route
//     *tuple* locks to the always-on tableLockMgr under the per-statement
//     BackendID so concurrent FOR UPDATE / FOR SHARE waiters on the same row
//     acquire in FIFO arrival order (design 0021-0012). dispatch.go's
//     per-statement ReleaseTupleLocks(backendID) drops them at Query-message
//     end; the "wait until the holder's (sub)txn ends" half is still enforced
//     by the persisted xmax via mvcc.WaitForXID, not this lock. A zero
//     BackendID (e.g. the extended-protocol path, which never sets it) keeps
//     tuple locking a no-op there, preserving the pre-existing
//     xmax/WaitForXID-only behaviour with no lock leak.
func (c *Context) tupleLockManager() (*lockmgr.LockManager, lockmgr.BackendID, time.Duration) {
	if c.LockMgr != nil {
		// Preserve the historical test-path timeout: the manager's own
		// configured deadlock_timeout (lockmgr.useConfiguredTimeout sentinel).
		return c.LockMgr, c.BackendID, lockmgr.UseConfiguredTimeout
	}
	if c.BackendID == 0 {
		return nil, 0, 0
	}
	return tableLockMgr, c.BackendID, c.deadlockTimeout()
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

// ReleaseTupleLocks drops every tuple-level (row) lock the given
// PER-STATEMENT backend identity took on the always-on tableLockMgr
// (design 0021-0012). dispatch.go calls it from the Query-message defer,
// alongside the per-statement Context.LockMgr.ReleaseAll, so a FOR UPDATE /
// FOR SHARE tuple lock lives exactly as long as the statement that took it —
// long enough to sequence concurrent waiters in FIFO arrival order, after
// which the persisted xmax takes over the "wait until the holder's txn ends"
// duty. It shares tableLockMgr with LOCK TABLE, but LOCK TABLE holds under the
// disjoint TxnLockBackendID, so releasing the statement backend id here never
// touches a LOCK TABLE holding.
func ReleaseTupleLocks(b lockmgr.BackendID) {
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
//   - inside an explicit transaction block (TxnLockBackendID != 0) the ACCESS
//     SHARE lock is held to end-of-transaction (acquireRelLockTxn);
//   - in autocommit it is acquired and immediately released (transient), so a
//     single-statement read still blocks while a conflicting ACCESS EXCLUSIVE
//     (TRUNCATE / DDL / LOCK TABLE) is held by another backend, then proceeds
//     once the holder commits — but holds nothing itself (acquireRelLockMaybeTransient);
//   - system catalogs (OID < firstNormalObjectOID) are skipped.
//
// ACCESS SHARE is self-compatible and conflicts only with ACCESS EXCLUSIVE, so
// in the common case (no concurrent LOCK TABLE / DDL) the acquire is granted
// instantly, and lockmgr is idempotent so re-scanning the same relation within a
// transaction is a cheap mask check. When it does wait, the lock_timeout /
// statement_timeout budget carried on the statement context governs the wait
// exactly like LOCK TABLE does. This is the table-level half of the timeouts
// isolation spec (M0118-0009) and the scan-side of inherit-temp's
// TRUNCATE-blocks-parent-scan permutations (design 0118-0037).
func (c *Context) acquireScanReadLockTxn(rel storage.RelFileNode) error {
	if rel.RelOid < firstNormalObjectOID {
		return nil
	}
	return c.acquireRelLockMaybeTransient(rel, lockmgr.AccessShareLock)
}

// acquireScanIndexReadLocksTxn takes a transaction-scoped ACCESS SHARE lock on
// every index of tbl, mirroring PostgreSQL where the planner opens — and, for a
// read, locks in AccessShare — ALL of a scanned relation's indexes regardless of
// which scan method is finally chosen (get_relation_info → index_open with
// AccessShareLock). A bare SELECT against a leaf partition therefore holds
// AccessShare on the partition's own indexes too, so a concurrent DROP INDEX
// (which AccessExclusive-locks the index relation tree, design 0118-0071) blocks
// behind the still-open reader. This is the index-side companion to the
// table-level acquireScanReadLockTxn and shares its confinement exactly: held to
// end-of-transaction inside an explicit transaction block, acquired+released
// transiently in autocommit, and system catalogs skipped (both via the
// acquireScanReadLockTxn hook). AccessShare is self-compatible and conflicts
// only with AccessExclusive, so concurrent reads never block on each other.
// M0118-0008 (partition-drop-index-locking, blocker 2).
func (c *Context) acquireScanIndexReadLocksTxn(tbl *catalog.Table) error {
	if tbl == nil || c.Catalog == nil {
		return nil
	}
	for _, idx := range c.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(c.CurrentDatabaseOid)) {
		if idx == nil {
			continue
		}
		if err := c.acquireScanReadLockTxn(c.Catalog.IndexRelFileNode(idx)); err != nil {
			return err
		}
	}
	return nil
}

// acquireWriteLockTxn takes a transaction-scoped ROW EXCLUSIVE heavyweight lock
// on a relation a DML write (INSERT/UPDATE/DELETE) is about to modify, so a
// concurrent CREATE TRIGGER / CREATE INDEX / ALTER TABLE (any acquirer of
// ShareLock / ShareRowExclusiveLock / ExclusiveLock / AccessExclusiveLock) in
// another session blocks until this transaction ends — matching PostgreSQL,
// where the target of a write is locked in ROW EXCLUSIVE mode for the lifetime
// of the transaction.
//
// Same confinement as acquireScanReadLockTxn (the read-side sibling): a no-op
// outside an explicit transaction block and for system catalogs. ROW EXCLUSIVE
// is self-compatible and compatible with ACCESS SHARE / ROW SHARE / ROW
// EXCLUSIVE, so concurrent DML on the same relation never blocks at the table
// level (it only conflicts with DDL-grade lock modes), keeping the common path
// — including pgbench's UPDATE-heavy TPC-B — free of new cross-session blocking.
// This is the write half of the table-level DDL/DML conflict machinery used by
// the create-trigger isolation spec. M0118-0008.
func (c *Context) acquireWriteLockTxn(rel storage.RelFileNode) error {
	// Symmetric with the read-side acquireScanReadLockTxn: held to
	// end-of-transaction inside an explicit block, and acquired+released
	// transiently in autocommit so the acquisition still WAITS behind a
	// conflicting holder (e.g. an AccessExclusiveLock from DETACH … FINALIZE on
	// the partition). RowExclusive is self-compatible and compatible with
	// AccessShare/RowShare/RowExclusive, so concurrent DML/reads never block at
	// the table level — only a DDL-grade lock (Share/ShareRowExclusive/
	// Exclusive/AccessExclusive) does, matching PostgreSQL. M0118-0008
	// (detach-partition-concurrently-3, autocommit INSERT behind FINALIZE).
	if err := c.acquireRelLockMaybeTransient(rel, lockmgr.RowExclusiveLock); err != nil {
		return err
	}
	// A write to a toast-bearing table also locks that table's TOAST relation
	// (PG opens the toast rel with RowExclusiveLock when it stores a toasted
	// value). goopg stores toast inline, but the lock is observable: REINDEX …
	// CONCURRENTLY pg_toast.<name> waits for toast-relation lockers, so a
	// concurrent DML write must register one — whereas a bare LOCK TABLE on the
	// parent must not (it never touches the toast rel). M0118-0008
	// (reindex-concurrently-toast, design 0118-0088).
	if im, ok := c.Catalog.(*catalog.InMemory); ok {
		if toastRel, has := im.ToastRelFileNode(rel); has {
			if err := c.acquireRelLockMaybeTransient(toastRel, lockmgr.RowExclusiveLock); err != nil {
				return err
			}
		}
	}
	return nil
}

// acquireDDLLockTxn takes a transaction-scoped heavyweight lock in the requested
// mode on a relation a DDL statement is about to alter, so the lock is held
// until COMMIT/ROLLBACK and conflicting concurrent DML/DDL blocks. CREATE
// TRIGGER uses ShareRowExclusiveLock (conflicts with concurrent writes but not
// reads or SELECT ... FOR UPDATE), mirroring PostgreSQL. Same confinement as the
// scan/write siblings: a no-op outside an explicit transaction and for system
// catalogs. M0118-0008.
func (c *Context) acquireDDLLockTxn(rel storage.RelFileNode, mode lockmgr.Mode) error {
	if c.TxnLockBackendID == 0 || rel.RelOid < firstNormalObjectOID {
		return nil
	}
	return c.acquireRelLockTxn(rel, mode, false)
}

// acquireSequenceLockTxn takes the heavyweight ROW EXCLUSIVE lock that nextval()
// holds on a sequence relation, mirroring PostgreSQL's lock_and_open_sequence
// (sequence.c, RowExclusiveLock). It conflicts with the AccessExclusiveLock that
// ALTER SEQUENCE takes (acquireDDLLockTxn), so:
//
//   - inside an explicit transaction the lock is held until COMMIT/ROLLBACK
//     under the transaction backend identity (TxnLockBackendID), so a later
//     ALTER SEQUENCE in another session waits for this transaction to end; and
//   - in autocommit the same lock is taken transiently under the per-statement
//     backend identity and released as soon as it is granted — the WAIT still
//     happens during acquisition (so an autocommit nextval blocks while another
//     session holds a conflicting ALTER SEQUENCE lock from its open
//     transaction), matching PostgreSQL's single-statement implicit transaction.
//
// RowExclusiveLock is self-compatible, so concurrent nextval()s (including
// SERIAL/IDENTITY inserts) never block each other at the table level. System
// catalogs (OID < firstNormalObjectOID) are skipped. M0118-0008 (sequence-ddl).
func (c *Context) acquireSequenceLockTxn(rel storage.RelFileNode) error {
	return c.acquireRelLockMaybeTransient(rel, lockmgr.RowExclusiveLock)
}

// acquireRelLockMaybeTransient acquires the heavyweight lock `mode` on rel.
// Inside an explicit transaction (TxnLockBackendID != 0) the lock is held to
// end-of-transaction (via acquireRelLockTxn); in autocommit it is acquired
// transiently under the per-statement BackendID and released the instant it is
// granted, so the WAIT on a conflicting holder still happens during acquisition
// but no lock lingers past the statement — matching PostgreSQL's single-
// statement implicit transaction. The per-statement BackendID is globally
// unique (minted from the same counter as LockBackendID) and never touches
// tableLockMgr elsewhere, so the targeted Release drops exactly this lock with
// no risk to other backends. System catalogs (OID < firstNormalObjectOID) are
// skipped. Used by nextval() (RowExclusiveLock, sequence-ddl) and by REINDEX
// SCHEMA (ShareLock per relation, reindex-schema). M0118-0008.
func (c *Context) acquireRelLockMaybeTransient(rel storage.RelFileNode, mode lockmgr.Mode) error {
	if rel.RelOid < firstNormalObjectOID {
		return nil
	}
	if c.TxnLockBackendID != 0 {
		// Explicit transaction: held until end-of-transaction.
		return c.acquireRelLockTxn(rel, mode, false)
	}
	if c.BackendID == 0 {
		return nil
	}
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	if c.Activity != nil {
		c.Activity.WaitEventStart(c.ProcNum, activity.WaitTypeLock, activity.WaitRelationLock)
	}
	lockCtx := context.Background()
	if c.Ctx != nil {
		lockCtx = c.Ctx
	}
	err := tableLockMgr.AcquireWithTimeout(lockCtx, c.BackendID, tag, mode, c.deadlockTimeout())
	if c.Activity != nil {
		c.Activity.WaitEventEnd(c.ProcNum)
	}
	if err == nil {
		tableLockMgr.Release(c.BackendID, tag, mode)
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

// tryAcquireMaintenanceLock is the conditional (non-blocking) lock acquire that
// backs VACUUM/ANALYZE (SKIP_LOCKED): it mirrors PostgreSQL's
// ConditionalLockRelationOid, returning true when `mode` is free to take on rel
// and false the instant a conflicting lock is held by any other backend (the
// caller then skips the relation rather than waiting). The lock is released
// immediately on success — goopg's VACUUM/ANALYZE is a self-contained statement
// that needs the lock only to detect contention, not to hold off concurrent
// access for its duration. System catalogs (OID < firstNormalObjectOID) are
// never contended this way, so they always report available.
//
// The acquire runs under whichever backend identity is active — the transaction
// backend inside an explicit block, else the per-statement backend — both of
// which are minted from the same counter and unique, so the targeted Release
// drops exactly this probe. A zero backend (no identity at all) reports
// available. M0118-0008 (vacuum-skip-locked).
func (c *Context) tryAcquireMaintenanceLock(rel storage.RelFileNode, mode lockmgr.Mode) bool {
	if rel.RelOid < firstNormalObjectOID {
		return true
	}
	backend := c.BackendID
	if c.TxnLockBackendID != 0 {
		backend = c.TxnLockBackendID
	}
	if backend == 0 {
		return true
	}
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	if err := tableLockMgr.TryAcquire(backend, tag, mode); err != nil {
		return false
	}
	tableLockMgr.Release(backend, tag, mode)
	return true
}

// waitForRelationLockers blocks until no OTHER backend holds a transaction-
// scoped table lock on rel, mirroring PostgreSQL's WaitForLockers, which
// REINDEX … CONCURRENTLY and CREATE INDEX CONCURRENTLY invoke at each phase
// boundary. Unlike a lock acquisition it takes NO conflicting lock of its own,
// so concurrent DML (ROW EXCLUSIVE) and reads (ACCESS SHARE) proceed unimpeded
// while it waits — that is the CONCURRENTLY contract (a ShareUpdateExclusive
// holder must not block ordinary readers/writers). It returns once every other
// transaction that held the relation locked during the wait has committed or
// rolled back (its locks dropped by ReleaseTableLocks at end-of-transaction).
//
// Because the heavyweight table locks it observes are only registered inside an
// explicit transaction (acquireScanReadLockTxn / acquireWriteLockTxn under
// TxnLockBackendID), a bare BEGIN with no table access registers nothing, so a
// CONCURRENTLY operation started before any concurrent session has touched the
// relation returns immediately — matching PostgreSQL, where REINDEX CONCURRENTLY
// only waits for transactions that actually hold a lock on the table.
//
// System catalogs (OID < firstNormalObjectOID) are skipped, and a context
// cancellation/timeout (statement_timeout / client cancel) aborts the wait with
// the matching SQLSTATE-57014 error exactly like the lock-wait helpers.
// M0118-0008 (reindex-concurrently isolation spec).
func (c *Context) waitForRelationLockers(rel storage.RelFileNode) error {
	if rel.RelOid < firstNormalObjectOID {
		return nil
	}
	tag := lockmgr.LockTag{DB: rel.DBOid, Rel: rel.RelOid}
	lockCtx := context.Background()
	if c.Ctx != nil {
		lockCtx = c.Ctx
	}
	const pollInterval = 10 * time.Millisecond
	waiting := false
	for {
		hasOther := false
		for b := range tableLockMgr.Holders(tag) {
			if b == c.TxnLockBackendID || b == c.BackendID {
				continue
			}
			hasOther = true
			break
		}
		if !hasOther {
			if waiting && c.Activity != nil {
				c.Activity.WaitEventEnd(c.ProcNum)
			}
			return nil
		}
		if !waiting {
			waiting = true
			if c.Activity != nil {
				c.Activity.WaitEventStart(c.ProcNum, activity.WaitTypeLock, activity.WaitRelationLock)
			}
		}
		select {
		case <-lockCtx.Done():
			if c.Activity != nil {
				c.Activity.WaitEventEnd(c.ProcNum)
			}
			if ee := lockWaitCancelError(lockCtx.Err()); ee != nil {
				return ee
			}
			return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		case <-time.After(pollInterval):
		}
	}
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
	// LastCheckpointRecordLSN returns the start LSN (1-based) of the most
	// recent checkpoint RECORD — distinct from the redo point since
	// A9-checkpoint-opcode (an ONLINE checkpoint's record is preceded by
	// XLOG_RUNNING_XACTS). BASE_BACKUP stamps it into backup_label's
	// CHECKPOINT LOCATION and pg_control's CheckPoint. 0 when no
	// checkpoint has completed yet.
	LastCheckpointRecordLSN() uint64
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
	if c.Tx.XID != storage.InvalidTransactionID {
		// Already materialized (or pre-stamped, e.g. the frozen-xid compat
		// paths) — no manager needed. B1.1 moved this check ahead of the
		// TxnMgr guard so pre-stamped contexts work without one.
		return nil
	}
	if c.TxnMgr == nil {
		return &ExecError{Code: "XX000", Message: "executor: no TxnMgr in context"}
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

// DidWrite reports whether this transaction has been assigned a real XID —
// i.e. some write-path operation called MaterializeWriterXID. A read-only
// transaction keeps XID == storage.InvalidTransactionID (M0093 lazy XID
// allocation), so this is the cheap "did this transaction produce
// garbage/durable work" predicate. Used to gate post-commit forced GC to
// writing transactions (a read-only SELECT produces no retained heap worth
// forcing a GC for). Read c.Tx.XID (the Context's in-place-updated copy),
// never a stale outer snapshot of the Transaction value.
func (c *Context) DidWrite() bool {
	return c.Tx.XID != storage.InvalidTransactionID
}

// EnumRenameEntry records one ALTER TYPE … RENAME TO operation for transactional rollback.
// OldName and NewName are lowercase. M0097-0022.
type EnumRenameEntry struct{ OldName, NewName string }

// sessionUniqueIDer is satisfied by *config.SessionRegistry (and test doubles).
// It exposes the stable per-connection identity without the executor importing
// the config package's concrete type into this signature.
type sessionUniqueIDer interface{ UniqueID() uint64 }

// sessionTempOwner returns the owning-session token for temporary relations
// created by, or visible to, this Context. It is derived from the stable
// per-connection SessionRegistry carried in AdvisorySessionIdentity. The empty
// string means "no session identity" (internal/test contexts): such temp
// relations are owned by nobody and stay visible to all sessions, preserving
// legacy single-session behaviour. Design 0118-0036 (M0118-0008 inherit-temp).
func sessionTempOwner(ctx *Context) string {
	if ctx == nil {
		return ""
	}
	if s, ok := ctx.AdvisorySessionIdentity.(sessionUniqueIDer); ok && s != nil {
		if id := s.UniqueID(); id != 0 {
			return "s" + strconv.FormatUint(id, 10)
		}
	}
	return ""
}
