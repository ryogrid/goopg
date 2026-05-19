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
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
)

// connTxState is the per-connection explicit-transaction holder.
// Zero value means "no explicit transaction active" (auto-commit mode).
type connTxState struct {
	mu          sync.Mutex
	active      bool
	tx          mvcc.Transaction
	sess        *executor.BasicSession // session state, non-nil when active
	// TempTableShadows maps table name → original permanent *catalog.Table.
	// Populated when CREATE TEMP TABLE shadows a permanent table. M0097-0003.
	TempTableShadows map[string]*catalog.Table
	// Cursors holds open SQL cursors declared by DECLARE ... CURSOR FOR select.
	// Key = cursor name (case-insensitive), value = SELECT SQL text.
	Cursors map[string]string
}

// Begin marks an explicit transaction as active. tx is the TxnMgr
// transaction that was just started; sess is the session state object
// that tracks isolation level, savepoints, etc.
func (c *connTxState) Begin(tx mvcc.Transaction) {
	c.mu.Lock()
	c.active = true
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
		c.sess.EndExplicitTransaction()
	}
	c.active = false
	c.tx = mvcc.Transaction{}
	c.mu.Unlock()
}

// preparedStatements stores named prepared SQL statements for this connection.
// Keyed by statement name; value is the original SQL text.  Used to implement
// PREPARE name AS … / EXECUTE name (M0096-0006).
type preparedStatements struct {
	mu    sync.Mutex
	stmts map[string]string
}

func newPreparedStatements() *preparedStatements {
	return &preparedStatements{stmts: make(map[string]string)}
}

func (ps *preparedStatements) Store(name, sql string) {
	ps.mu.Lock()
	ps.stmts[name] = sql
	ps.mu.Unlock()
}

func (ps *preparedStatements) Lookup(name string) (string, bool) {
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
	ps.stmts = make(map[string]string)
	ps.mu.Unlock()
}

// cursorDeclare stores a named cursor's SELECT SQL text.
func (c *connTxState) cursorDeclare(name, sql string) {
	c.mu.Lock()
	if c.Cursors == nil {
		c.Cursors = make(map[string]string)
	}
	c.Cursors[strings.ToLower(name)] = sql
	c.mu.Unlock()
}

// cursorLookup returns the SELECT SQL for a named cursor.
func (c *connTxState) cursorLookup(name string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Cursors == nil {
		return "", false
	}
	sql, ok := c.Cursors[strings.ToLower(name)]
	return sql, ok
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
