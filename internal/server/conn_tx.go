package server

// conn_tx.go — per-connection explicit transaction state (M0096-0005).
//
// goopg's dispatch creates a new TxnMgr transaction for every statement
// (auto-commit). This file adds the per-connection state needed to
// maintain an *explicit* transaction across multiple statements when the
// client issues BEGIN … COMMIT.
//
// When the client sends BEGIN the dispatch starts a real TxnMgr
// transaction and stores it here instead of immediately committing.
// Subsequent statements from the same connection reuse that transaction
// until COMMIT or ROLLBACK ends it.
//
// This is the minimum required for the isolation-test suite: each spec
// session issues `BEGIN ISOLATION LEVEL …` in its setup block, and the
// test steps run INSERT / UPDATE / SELECT within that open transaction.
// The blocking semantics (INSERT … ON CONFLICT DO NOTHING waiting for a
// concurrent uncommitted insert) only work when the inserting transaction
// is still open at the time the second session tries the same key.

import (
	"sort"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// cursorEntry holds the state of an open SQL cursor. M0097-0042.
// The cursor is materialised lazily: on the first FETCH the server
// executes the stored SELECT, buffers all rows, and sets Materialized.
// Subsequent FETCHes advance/retreat Pos without re-running the query.
// Pos is the *next* row index for a forward fetch; ranges [0, len(Rows)].
type cursorEntry struct {
	SQL          string         // raw SQL containing the DECLARE … FOR <select>
	Rows         []executor.Row // all result rows, nil until Materialized
	Schema       planner.Schema // output schema from the first execution
	Pos          int            // current position: 0 = before first row, len(Rows) = past last
	Materialized bool
}

// connTxState is the per-connection explicit-transaction holder.
// Zero value means "no explicit transaction active" (auto-commit mode).
type connTxState struct {
	mu     sync.Mutex
	active bool
	failed bool // 25P02: in_failed_sql_transaction
	// preparedGid is the global transaction identifier set by
	// `PREPARE TRANSACTION 'gid'`. While non-empty the connection's active
	// transaction is "prepared": its writes, locks and SSI predicate-lock
	// state persist (the transaction is kept open) until a matching
	// `COMMIT PREPARED 'gid'` / `ROLLBACK PREPARED 'gid'` finalises it via the
	// canonical commit/rollback path. Same-backend only — cross-backend commit
	// of a prepared xact and the pg_prepared_xacts view are deferred.
	// M0118-0009 (prepared-transactions).
	preparedGid string
	tx          mvcc.Transaction
	sess        *executor.BasicSession // session state, non-nil when active
	// SessCtx is the per-connection session-level mctx (M0107-0001).
	// Wired by serveConn after creating the session context; stmt-level
	// contexts are acquired as children in dispatchSimpleQueryViaExecutor.
	SessCtx *mctx.Context
	// ProcNum is the backend's slot index in the Manager's ProcArray.
	// Assigned once at connection start in serveConn; used to pass an
	// explicit procNum to Manager.Begin on every statement.
	ProcNum int32
	// DBName is the database this connection is bound to (from the startup
	// packet). Assigned once at connection start; used to scope per-database
	// catalogs such as pg_extension. M0110-0003 (AC-002 gap #7c).
	DBName string
	// TempTableShadows maps table name → original permanent *catalog.Table.
	// Populated when CREATE TEMP TABLE shadows a permanent table. M0097-0003.
	TempTableShadows map[string]*catalog.Table
	// Cursors holds open SQL cursors declared by DECLARE ... CURSOR FOR select.
	// Key = cursor name (case-insensitive), value = cursor state.
	// M0097-0042: cursor entries track materialized rows and position for
	// FETCH FORWARD/BACKWARD support.
	Cursors map[string]*cursorEntry
	// SeqCurrVals holds per-sequence last nextval values for currval().
	// Session-scoped: persists across statements and transactions. M0097-0042.
	SeqCurrVals map[string]int64
	// SeqLastVal / SeqLastSet track the most recent nextval across all sequences
	// (for lastval()). SeqLastName is the name of the sequence that produced it
	// (to detect if that sequence was dropped). Session-scoped. M0097-0042.
	SeqLastVal  int64
	SeqLastSet  bool
	SeqLastName string
	// PendingEnumValues tracks enum labels added via ALTER TYPE … ADD VALUE
	// inside the current explicit transaction.  They are "unsafe" until COMMIT.
	// map[enumTypeName][label]=true.  Cleared on COMMIT/ROLLBACK (End()).
	PendingEnumValues map[string]map[string]bool
	// PendingEnumRenames tracks ALTER TYPE … RENAME TO within the current tx.
	// On ROLLBACK, reversed in reverse order.  Cleared on COMMIT/ROLLBACK. M0097-0022.
	PendingEnumRenames []executor.EnumRenameEntry
	// PendingCreatedEnums tracks CREATE TYPE … AS ENUM within the current tx.
	// On ROLLBACK, created types are dropped.  map[name(lowercase)]=true.  M0097-0022.
	PendingCreatedEnums map[string]bool
	// PendingCreatedComposites tracks CREATE TYPE … AS (...) within the current
	// tx.  On ROLLBACK, created composite types are dropped.
	// map[name(lowercase)]=true.  DU-002 slice 244.
	PendingCreatedComposites map[string]bool
	// NonSuperuserRole is set when SET SESSION AUTHORIZATION is called with a
	// non-default role name. While non-empty, privilege checks that require
	// superuser (e.g. LEAKPROOF function attribute) are rejected. Cleared by
	// RESET SESSION AUTHORIZATION or SET SESSION AUTHORIZATION DEFAULT/postgres.
	NonSuperuserRole string
	// AdvisoryID is the stable per-connection advisory-lock owner identity (the
	// SessionRegistry). It is assigned once at connection start in
	// runPostStartupLoop and matches advisorySessionIDFromContext's preferred
	// identity, so transaction-end and connection-teardown release the same
	// advisory locks that pg_advisory_*lock() acquired — regardless of whether
	// the lock was taken in autocommit or inside an explicit transaction.
	// M0118-0003.
	AdvisoryID any
	// LockBackendID is a stable lock-manager backend identity for the
	// whole connection (distinct from the per-statement BackendID minted
	// in dispatch.go) that backs transaction-scoped LOCK TABLE heavyweight
	// locks. A LOCK acquired in one statement survives the per-statement
	// ReleaseAll and is dropped only here in End() (via
	// executor.ReleaseTableLocks) on COMMIT/ROLLBACK. Assigned once at
	// connection start in runPostStartupLoop. M0118-0003 (lock-nowait).
	LockBackendID lockmgr.BackendID
	// BackendPID is this connection's backend PID (the value in BackendKeyData /
	// pg_backend_pid). Stamped once at connection start; used as the source PID
	// of NOTIFY deliveries (the int32 of the 'A' NotificationResponse).
	// M0118-0009 (async-notify).
	BackendPID uint32
	// NotifySession is the stable per-connection identity used as the
	// LISTEN/NOTIFY hub key (same SessionRegistry as AdvisoryID, typed for
	// direct hub calls). M0118-0009.
	NotifySession *config.SessionRegistry
	// pendingNotify is the savepoint-aware stack of NOTIFY messages emitted by
	// the current transaction. PostgreSQL makes notifications visible only when
	// the notifying transaction commits, so they are published to the hub at
	// COMMIT and discarded at ROLLBACK. Each entry is one (sub)transaction
	// level, mirroring async.c's per-subtransaction pendingNotifies list:
	// pendingNotify[0] is the transaction top level (name ""), and a SAVEPOINT
	// pushes a new level. ROLLBACK TO SAVEPOINT discards the notifications
	// queued since that savepoint; RELEASE SAVEPOINT merges them into the
	// enclosing level. Notifications are deduplicated by (channel,payload)
	// across ALL active levels, the way async.c collapses duplicate
	// same-transaction notifications. M0118-0009.
	pendingNotify []notifyLevel
}

// notifyLevel holds one (sub)transaction level's buffered notifications.
// name is the savepoint name that opened the level ("" for the transaction top
// level). M0118-0009 (async-notify).
type notifyLevel struct {
	name   string
	notifs []Notification
}

// bufferNotify queues one NOTIFY for publication at the current transaction's
// commit, collapsing an exact (channel,payload) duplicate already buffered at
// any active level of this transaction (PostgreSQL's async.c de-duplicates
// identical notifications emitted within one transaction, across
// subtransaction levels). The notification is appended to the current
// (innermost) level so a later ROLLBACK TO SAVEPOINT can discard it.
// M0118-0009.
func (c *connTxState) bufferNotify(channel, payload string, srcPID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, lvl := range c.pendingNotify {
		for _, n := range lvl.notifs {
			if n.Channel == channel && n.Payload == payload {
				return
			}
		}
	}
	if len(c.pendingNotify) == 0 {
		c.pendingNotify = append(c.pendingNotify, notifyLevel{})
	}
	top := len(c.pendingNotify) - 1
	c.pendingNotify[top].notifs = append(c.pendingNotify[top].notifs,
		Notification{PID: srcPID, Channel: channel, Payload: payload})
}

// notifySavepoint pushes a new (empty) notification level for a SAVEPOINT, so
// notifications queued until the matching ROLLBACK TO / RELEASE are isolated to
// it. M0118-0009.
func (c *connTxState) notifySavepoint(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pendingNotify) == 0 {
		c.pendingNotify = append(c.pendingNotify, notifyLevel{})
	}
	c.pendingNotify = append(c.pendingNotify, notifyLevel{name: name})
}

// notifyReleaseSavepoint merges the named savepoint level (and any levels above
// it) into the enclosing level, mirroring RELEASE SAVEPOINT keeping the
// subtransaction's effects. A no-op if the name is not found. M0118-0009.
func (c *connTxState) notifyReleaseSavepoint(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.findNotifyLevel(name)
	if idx <= 0 {
		return
	}
	for i := idx; i < len(c.pendingNotify); i++ {
		c.pendingNotify[idx-1].notifs = append(c.pendingNotify[idx-1].notifs, c.pendingNotify[i].notifs...)
	}
	c.pendingNotify = c.pendingNotify[:idx]
}

// notifyRollbackToSavepoint discards every notification queued since the named
// savepoint was established (the named level and anything above it), keeping
// the savepoint itself active as the current level — mirroring ROLLBACK TO
// SAVEPOINT aborting the subtransaction's pending notifies. A no-op if the name
// is not found. M0118-0009.
func (c *connTxState) notifyRollbackToSavepoint(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.findNotifyLevel(name)
	if idx <= 0 {
		return
	}
	c.pendingNotify[idx].notifs = nil
	c.pendingNotify = c.pendingNotify[:idx+1]
}

// findNotifyLevel returns the index of the topmost notification level opened by
// the named savepoint, or -1 if absent. Caller holds c.mu. M0118-0009.
func (c *connTxState) findNotifyLevel(name string) int {
	for i := len(c.pendingNotify) - 1; i >= 1; i-- {
		if c.pendingNotify[i].name == name {
			return i
		}
	}
	return -1
}

// takePendingNotify flattens every level into a single FIFO slice and clears
// the buffer (called at COMMIT to publish; discards otherwise). M0118-0009.
func (c *connTxState) takePendingNotify() []Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Notification
	for _, lvl := range c.pendingNotify {
		out = append(out, lvl.notifs...)
	}
	c.pendingNotify = nil
	return out
}

// Begin marks an explicit transaction as active. tx is the TxnMgr
// transaction that was just started; sess is the session state object
// that tracks isolation level, savepoints, etc.
// Fail marks the current explicit transaction as failed (25P02). Subsequent
// statements are rejected until COMMIT or ROLLBACK clears the state.
func (c *connTxState) Fail() {
	c.mu.Lock()
	c.failed = true
	// Mirror PostgreSQL's AbortTransaction: when a statement errors at the top
	// level of a transaction (no open savepoint/subtransaction), the aborted
	// transaction's heavyweight LOCK TABLE / DDL / DML table locks are released
	// immediately — pg_locks shows none held — rather than lingering until the
	// explicit ROLLBACK. A concurrent session's conflicting DDL/DML therefore
	// proceeds without waiting for the aborted transaction to be rolled back
	// (alter-table-3 isolation spec, M0118-0008). When a savepoint is open the
	// error aborts only the current subtransaction, whose locks transfer to the
	// parent (PostgreSQL retains them across ROLLBACK TO SAVEPOINT), so we keep
	// them and let End() release them on the eventual COMMIT/ROLLBACK. The
	// release is idempotent: End() releases again under the same identity.
	if c.sess != nil && c.sess.SavepointDepth() == 0 {
		executor.ReleaseTableLocks(c.LockBackendID)
	}
	c.mu.Unlock()
}

// ReleasePinnedSnapshotOnFail clears the transaction-pinned (RR/SSI) snapshot
// marker after a top-level statement error, mirroring PostgreSQL's
// AbortTransaction dropping the transaction snapshot at abort. Gated on
// SavepointDepth()==0 exactly like Fail()'s lock release: when a savepoint is
// open the error aborts only the subtransaction and ROLLBACK TO SAVEPOINT
// resumes the SAME transaction snapshot, so it must be retained. Lets a
// concurrent DETACH PARTITION CONCURRENTLY waiting on this session's snapshot
// unblock as soon as the session errors, before its explicit ROLLBACK/COMMIT.
// Design 0118-0063 (M0118-0008 detach-partition-concurrently-4).
func (c *connTxState) ReleasePinnedSnapshotOnFail(mgr *mvcc.Manager) {
	if mgr == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil && c.sess.SavepointDepth() == 0 {
		mgr.ReleasePinnedSnapshot(c.tx.Handle)
	}
}

// AbortInPlaceOnFail releases this transaction's XID for the purpose of
// WaitForXID / IsXIDActive the moment a top-level statement is aborted as a
// deadlock victim, WITHOUT clearing its proc-array slot. This mirrors
// PostgreSQL's AbortTransaction releasing the transaction's XID at the abort, so
// a concurrent session blocked on this transaction's catalog-tuple xmax (the
// intra-grant-inplace pg_class in-place wait) unblocks at the abort rather than
// at the eventual explicit ROLLBACK. The slot stays open so the subsequent
// COMMIT/ROLLBACK on this block still finalises it through the canonical path —
// the victim is write-less (its statement errored before writing), so there is
// nothing to make visible or roll back.
//
// Gated on SavepointDepth()==0 exactly like Fail()/ReleasePinnedSnapshotOnFail:
// when a savepoint is open the error aborts only the subtransaction and ROLLBACK
// TO SAVEPOINT resumes the same top-level XID, which must therefore stay active.
// Used only when the executor flagged the statement as a deadlock victim
// (Context.DeadlockVictim), so the blast radius is confined to the pg_class
// in-place deadlock path. Design 0118-0115 (intra-grant-inplace perm 8).
func (c *connTxState) AbortInPlaceOnFail(mgr *mvcc.Manager) {
	if mgr == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil || c.sess.SavepointDepth() != 0 {
		return
	}
	tx := c.tx
	if curTx, _, ok := c.sess.CurrentTransaction(); ok {
		tx = curTx
	}
	mgr.ReleaseXIDWaiters(tx.XID)
}

// ClearFailed clears the failed transaction state.  Used after ROLLBACK TO
// SAVEPOINT restores the transaction to a pre-error state.
func (c *connTxState) ClearFailed() {
	c.mu.Lock()
	c.failed = false
	c.mu.Unlock()
}

// IsFailed reports whether the transaction is in the failed state (25P02).
func (c *connTxState) IsFailed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

func (c *connTxState) Begin(tx mvcc.Transaction) {
	c.mu.Lock()
	c.active = true
	c.failed = false
	c.tx = tx
	if c.sess == nil {
		c.sess = executor.NewBasicSession()
	}
	// Mark the session as being in an explicit transaction block so
	// nested BEGIN is treated as a warning and operators that require
	// a session block can verify it.
	c.sess.BeginExplicitTransaction(tx, mvcc.Snapshot{})
	c.mu.Unlock()
}

// InExplicit reports whether an explicit transaction is currently active.
func (c *connTxState) InExplicit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// Tx returns the active explicit TxnMgr transaction. When an XID was
// materialised during the transaction (e.g. by a write), the session's
// up-to-date Transaction (with the assigned XID) is returned so that
// subsequent statements in the same session correctly self-see their
// own writes (M0100-0002).
func (c *connTxState) Tx() mvcc.Transaction {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil {
		if tx, _, ok := c.sess.CurrentTransaction(); ok {
			return tx
		}
	}
	return c.tx
}

// Session returns the BasicSession associated with the active explicit
// transaction. Returns nil when no explicit transaction is active.
func (c *connTxState) Session() *executor.BasicSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

// End clears the explicit transaction state. Should be called after
// TxnMgr.Commit() or TxnMgr.Rollback().
func (c *connTxState) End() {
	c.mu.Lock()
	if c.sess != nil {
		// Release xact-scoped advisory locks under the stable per-connection
		// identity (AdvisoryID = SessionRegistry), which is what acquired them —
		// NOT c.sess (the BasicSession), which is no longer the advisory owner.
		// Falls back to c.sess for callers that never set AdvisoryID (unit tests).
		// M0118-0003.
		advID := c.AdvisoryID
		if advID == nil {
			advID = c.sess
		}
		executor.ReleaseAdvisoryTransactionLocks(advID)
		executor.ReleaseRelationLocks(c.sess)
		// Drop transaction-scoped LOCK TABLE heavyweight locks held under the
		// stable per-connection backend identity. Per-statement locks use a
		// different (per-Query) BackendID and are released in dispatch.go;
		// these survive until end-of-transaction. M0118-0003 (lock-nowait).
		executor.ReleaseTableLocks(c.LockBackendID)
		c.sess.EndExplicitTransaction()
	}
	c.active = false
	c.failed = false
	c.preparedGid = ""
	c.tx = mvcc.Transaction{}
	c.PendingEnumValues = nil
	c.PendingEnumRenames = nil
	c.PendingCreatedEnums = nil
	c.PendingCreatedComposites = nil
	// Discard any NOTIFYs not yet published. On COMMIT the publish happens
	// before End() is called; reaching here with a non-empty buffer means
	// ROLLBACK (or commit-cleanup after publish), where the notifications must
	// not be delivered. M0118-0009.
	c.pendingNotify = nil
	c.mu.Unlock()
}

// MarkPrepared records the two-phase-commit gid on the active transaction. The
// transaction is kept open so its writes, locks and SSI state persist until a
// matching COMMIT/ROLLBACK PREPARED finalises it. M0118-0009.
func (c *connTxState) MarkPrepared(gid string) {
	c.mu.Lock()
	c.preparedGid = gid
	c.mu.Unlock()
}

// PreparedGid returns the gid of the active prepared transaction, or "" when the
// connection has no prepared transaction. M0118-0009.
func (c *connTxState) PreparedGid() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.preparedGid
}

// ClearPrepared drops the prepared marker without ending the transaction. Called
// just before the canonical COMMIT/ROLLBACK path finalises the prepared xact.
// M0118-0009.
func (c *connTxState) ClearPrepared() {
	c.mu.Lock()
	c.preparedGid = ""
	c.mu.Unlock()
}

// DetachPrepared moves this connection's active transaction into a standalone
// connTxState (for parking in the server's prepared-xact registry) and resets
// the live connection to a free auto-commit state — preserving the
// per-connection identities (ProcNum, lock/advisory backend identities, backend
// PID, NOTIFY session) so the connection can run further transactions while the
// prepared one awaits COMMIT/ROLLBACK PREPARED. newTx is the transaction after
// it has been relocated to its dedicated proc slot. The parked session, its
// deferred DROP FUNCTION drops, buffered NOTIFYs and pending enum DDL all travel
// with the returned holder so the later finalisation reuses the canonical
// commit/rollback path. M0118-0009 (stats — cross-backend two-phase commit).
//
// Note: the parked holder shares the originating connection's lock/advisory
// backend identities, so xact-scoped heavyweight/advisory locks held by the
// prepared transaction are released under the same identity the live connection
// keeps using. The isolation specs that exercise cross-backend 2PC hold no such
// locks across the PREPARE..finalise window, so this is sound for them; full
// dummy-PGPROC lock hand-off is deferred.
func (c *connTxState) DetachPrepared(gid string, newTx mvcc.Transaction) *connTxState {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := &connTxState{
		active:                   true,
		failed:                   c.failed,
		preparedGid:              gid,
		tx:                       newTx,
		sess:                     c.sess,
		ProcNum:                  c.ProcNum,
		DBName:                   c.DBName,
		LockBackendID:            c.LockBackendID,
		AdvisoryID:               c.AdvisoryID,
		BackendPID:               c.BackendPID,
		NotifySession:            c.NotifySession,
		PendingEnumValues:        c.PendingEnumValues,
		PendingEnumRenames:       c.PendingEnumRenames,
		PendingCreatedEnums:      c.PendingCreatedEnums,
		PendingCreatedComposites: c.PendingCreatedComposites,
		pendingNotify:            c.pendingNotify,
	}
	// Reset the live connection to free/auto-commit, keeping the per-connection
	// identities (left untouched above).
	c.active = false
	c.failed = false
	c.preparedGid = ""
	c.tx = mvcc.Transaction{}
	c.sess = nil
	c.PendingEnumValues = nil
	c.PendingEnumRenames = nil
	c.PendingCreatedEnums = nil
	c.PendingCreatedComposites = nil
	c.pendingNotify = nil
	return p
}

type preparedStatementDef struct {
	stmt        parser.Stmt
	sql         string   // original PREPARE … AS … text for pg_prepared_statements
	paramTypes  []string // declared parameter types from PREPARE (…); nil = no declaration
	resultTypes []string // inferred output column types; nil = unknown
}

// preparedStatements stores named prepared SQL statements for this connection.
// Keyed by statement name; value is the parsed query body. Used to implement
// PREPARE name AS … / EXECUTE name (M0096-0006).
type preparedStatements struct {
	mu    sync.Mutex
	stmts map[string]preparedStatementDef
}

func newPreparedStatements() *preparedStatements {
	return &preparedStatements{stmts: make(map[string]preparedStatementDef)}
}

// Store saves the prepared statement. Returns false if a statement with that
// name already exists (caller should return "already exists" error).
func (ps *preparedStatements) Store(name string, stmt parser.Stmt, sql string, paramTypes []string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if _, exists := ps.stmts[name]; exists {
		return false
	}
	ps.stmts[name] = preparedStatementDef{stmt: stmt, sql: sql, paramTypes: paramTypes}
	return true
}

// SetParamTypes updates the inferred parameter types for an existing prepared statement.
func (ps *preparedStatements) SetParamTypes(name string, types []string) {
	ps.mu.Lock()
	if def, ok := ps.stmts[name]; ok {
		def.paramTypes = types
		ps.stmts[name] = def
	}
	ps.mu.Unlock()
}

// SetResultTypes updates the inferred result column types for an existing prepared statement.
func (ps *preparedStatements) SetResultTypes(name string, types []string) {
	ps.mu.Lock()
	if def, ok := ps.stmts[name]; ok {
		def.resultTypes = types
		ps.stmts[name] = def
	}
	ps.mu.Unlock()
}

// normPrepParamType maps SQL type aliases to PostgreSQL canonical type names
// as shown in pg_prepared_statements.parameter_types.
func normPrepParamType(t string) string {
	switch strings.ToLower(t) {
	case "int", "int4", "integer":
		return "integer"
	case "int2":
		return "smallint"
	case "int8":
		return "bigint"
	case "float", "float8", "double precision", "double":
		return `"double precision"`
	case "float4":
		return "real"
	case "bool":
		return "boolean"
	default:
		return t
	}
}

// ListRows returns rows for the pg_prepared_statements virtual table.
// Columns: name, statement, parameter_types, result_types.
func (ps *preparedStatements) ListRows() [][]string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	rows := make([][]string, 0, len(ps.stmts))
	for name, def := range ps.stmts {
		paramTypesArr := "{}"
		if len(def.paramTypes) > 0 {
			normalized := make([]string, len(def.paramTypes))
			for i, pt := range def.paramTypes {
				normalized[i] = normPrepParamType(pt)
			}
			paramTypesArr = "{" + strings.Join(normalized, ",") + "}"
		}
		// pg_prepared_statements column order (ordinals 0–7):
		//   0:name, 1:statement, 2:prepare_time, 3:parameter_types,
		//   4:result_types, 5:from_sql, 6:generic_plans, 7:custom_plans
		resultTypesArr := ""
		if len(def.resultTypes) > 0 {
			resultTypesArr = "{" + strings.Join(def.resultTypes, ",") + "}"
		}
		rows = append(rows, []string{
			name,           // 0: name
			def.sql,        // 1: statement
			"",             // 2: prepare_time (not tracked)
			paramTypesArr,  // 3: parameter_types
			resultTypesArr, // 4: result_types
			"true",         // 5: from_sql
			"0",            // 6: generic_plans
			"0",            // 7: custom_plans
		})
	}
	// Sort by name for deterministic output matching pg_prepared_statements ORDER BY name.
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	return rows
}

func (ps *preparedStatements) Lookup(name string) (preparedStatementDef, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s, ok := ps.stmts[name]
	return s, ok
}

func (ps *preparedStatements) Delete(name string) {
	ps.mu.Lock()
	delete(ps.stmts, name)
	ps.mu.Unlock()
}

func (ps *preparedStatements) DeleteAll() {
	ps.mu.Lock()
	ps.stmts = make(map[string]preparedStatementDef)
	ps.mu.Unlock()
}

// cursorDeclare stores a named cursor's SELECT SQL text.
func (c *connTxState) cursorDeclare(name, sql string) {
	c.mu.Lock()
	if c.Cursors == nil {
		c.Cursors = make(map[string]*cursorEntry)
	}
	c.Cursors[strings.ToLower(name)] = &cursorEntry{SQL: sql}
	c.mu.Unlock()
}

// cursorLookup returns the cursor entry for a named cursor.
func (c *connTxState) cursorLookup(name string) (*cursorEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Cursors == nil {
		return nil, false
	}
	e, ok := c.Cursors[strings.ToLower(name)]
	return e, ok
}

// cursorClose removes a named cursor (or all cursors when name is "").
func (c *connTxState) cursorClose(name string) {
	c.mu.Lock()
	if name == "" {
		c.Cursors = nil
	} else if c.Cursors != nil {
		delete(c.Cursors, strings.ToLower(name))
	}
	c.mu.Unlock()
}
