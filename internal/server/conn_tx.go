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
	"github.com/goopg/goopg/internal/executor"
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
	tx     mvcc.Transaction
	sess   *executor.BasicSession // session state, non-nil when active
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
}

// Begin marks an explicit transaction as active. tx is the TxnMgr
// transaction that was just started; sess is the session state object
// that tracks isolation level, savepoints, etc.
// Fail marks the current explicit transaction as failed (25P02). Subsequent
// statements are rejected until COMMIT or ROLLBACK clears the state.
func (c *connTxState) Fail() {
	c.mu.Lock()
	c.failed = true
	c.mu.Unlock()
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
		c.sess.EndExplicitTransaction()
	}
	c.active = false
	c.failed = false
	c.tx = mvcc.Transaction{}
	c.PendingEnumValues = nil
	c.PendingEnumRenames = nil
	c.PendingCreatedEnums = nil
	c.PendingCreatedComposites = nil
	c.mu.Unlock()
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
