package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// txnConn simulates a single client connection with a persistent BasicSession.
// The session state (inTx, pendingDDL) survives across run() calls,
// matching how the server's dispatch.go reuses a session per connection.
type txnConn struct {
	rt   *Runtime
	sess *executor.BasicSession
	t    *testing.T
}

func newTxnConn(rt *Runtime, t *testing.T) *txnConn {
	t.Helper()
	return &txnConn{rt: rt, sess: executor.NewBasicSession(), t: t}
}

// run executes one SQL statement through the full planner+executor pipeline
// using the persistent session so BEGIN/COMMIT/ROLLBACK maintain their state.
//
// Transaction ownership rules:
// - If NOT in an explicit tx before: create an implicit tx. If the statement
//   started an explicit tx (BEGIN), abort the implicit one (unused). Otherwise
//   auto-commit the implicit tx.
// - If IN an explicit tx: use the session's current tx. After COMMIT/ROLLBACK
//   the transactionOp handled cleanup; we don't touch it.
func (tc *txnConn) run(sql string) {
	tc.t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		tc.t.Fatalf("parse %q: %v", sql, err)
	}
	if len(stmts) != 1 {
		tc.t.Fatalf("expected 1 stmt, got %d", len(stmts))
	}
	plan, err := planner.Plan(stmts[0], tc.rt.Catalog)
	if err != nil {
		tc.t.Fatalf("plan %q: %v", sql, err)
	}
	op, err := executor.Build(plan)
	if err != nil {
		tc.t.Fatalf("build %q: %v", sql, err)
	}

	inExplicitBefore := tc.sess.InExplicitTransaction()

	var tx mvcc.Transaction
	var snap mvcc.Snapshot
	var implicitTx mvcc.Transaction // only set when !inExplicitBefore

	if inExplicitBefore {
		tx, snap, _ = tc.sess.CurrentTransaction()
	} else {
		// Create an implicit tx for this statement.
		tx, err = tc.rt.TxnMgr.Begin(mvcc.IsolationReadCommitted)
		if err != nil {
			tc.t.Fatalf("Begin: %v", err)
		}
		snap, err = tc.rt.TxnMgr.SnapshotFor(tx)
		if err != nil {
			_ = tc.rt.TxnMgr.Rollback(tx)
			tc.t.Fatalf("SnapshotFor: %v", err)
		}
		implicitTx = tx
	}

	ctx := executor.NewContext()
	ctx.Pool = tc.rt.Pool
	ctx.Catalog = tc.rt.Catalog
	ctx.TxnMgr = tc.rt.TxnMgr
	ctx.Tx = tx
	ctx.Snap = snap
	ctx.Session = tc.sess

	if err := op.Open(ctx); err != nil {
		if !inExplicitBefore {
			_ = tc.rt.TxnMgr.Rollback(implicitTx)
		}
		tc.t.Fatalf("Open %q: %v", sql, err)
	}
	for {
		_, nextErr := op.Next()
		if nextErr != nil {
			op.Close()
			if !inExplicitBefore {
				_ = tc.rt.TxnMgr.Rollback(implicitTx)
			}
			tc.t.Fatalf("Next %q: %v", sql, nextErr)
		}
		break
	}
	op.Close()

	// Post-run transaction lifecycle:
	if !inExplicitBefore {
		if tc.sess.InExplicitTransaction() {
			// BEGIN fired: the implicit tx we created is now unused.
			// Abort it so we don't leak it.
			_ = tc.rt.TxnMgr.Rollback(implicitTx)
		} else {
			// Regular DDL/DML in auto-commit mode: commit.
			if err := tc.rt.TxnMgr.Commit(implicitTx); err != nil {
				tc.t.Fatalf("auto-commit after %q: %v", sql, err)
			}
		}
	}
	// If inExplicitBefore: COMMIT/ROLLBACK transactionOps have already
	// handled the tx via TxnMgr. We just leave ctx in its new state.
}

// TestTransactionalCreateTableRollback is the core M0030-0006 test:
// BEGIN; CREATE TABLE t1; ROLLBACK; must leave t1 non-existent.
func TestTransactionalCreateTableRollback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	conn := newTxnConn(rt, t)
	conn.run("BEGIN")
	conn.run("CREATE TABLE txn_test (id int4, val text)")
	conn.run("ROLLBACK")

	if _, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "txn_test"}); ok {
		t.Error("txn_test found in catalog after ROLLBACK — transactional DDL broken")
	}
}

// TestTransactionalCreateTableCommit verifies the happy path:
// BEGIN; CREATE TABLE t; COMMIT; leaves t in the catalog.
func TestTransactionalCreateTableCommit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	conn := newTxnConn(rt, t)
	conn.run("BEGIN")
	conn.run("CREATE TABLE committed_tbl (id int4)")
	conn.run("COMMIT")

	if _, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "committed_tbl"}); !ok {
		t.Error("committed_tbl not in catalog after COMMIT")
	}
}

// TestTransactionalCreateIndexRollback verifies that CREATE INDEX inside
// a rolled-back transaction removes the index from the catalog.
func TestTransactionalCreateIndexRollback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	conn := newTxnConn(rt, t)
	// Create table outside the transaction first.
	conn.run("CREATE TABLE idx_host (id int4)")
	conn.run("BEGIN")
	conn.run("CREATE INDEX idx_host_roll ON idx_host (id)")
	conn.run("ROLLBACK")

	if _, ok := rt.Catalog.LookupIndex(parser.ObjectName{Name: "idx_host_roll"}); ok {
		t.Error("idx_host_roll found in catalog after ROLLBACK — index rollback broken")
	}
}

// TestTransactionalDDLMultipleCreatesRollback verifies that multiple
// CREATE TABLE statements inside one transaction are ALL undone on ROLLBACK.
func TestTransactionalDDLMultipleCreatesRollback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	conn := newTxnConn(rt, t)
	conn.run("BEGIN")
	conn.run("CREATE TABLE multi_a (id int4)")
	conn.run("CREATE TABLE multi_b (id int4)")
	conn.run("CREATE TABLE multi_c (id int4)")
	conn.run("ROLLBACK")

	for _, name := range []string{"multi_a", "multi_b", "multi_c"} {
		if _, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: name}); ok {
			t.Errorf("%s found in catalog after ROLLBACK", name)
		}
	}
}

// TestAutoCommitDDLSurvivesImplicitTransaction verifies that DDL outside an
// explicit BEGIN auto-commits and the table persists.
func TestAutoCommitDDLSurvivesImplicitTransaction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	conn := newTxnConn(rt, t)
	conn.run("CREATE TABLE auto_tbl (id int4)")

	if _, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "auto_tbl"}); !ok {
		t.Error("auto_tbl missing after auto-commit CREATE TABLE")
	}
}

// TestRollbackedTableNotVisibleAfterRestart is the regression test for the
// crash+restart scenario: BEGIN; CREATE TABLE; ROLLBACK followed by a server
// restart must not re-surface the rolled-back table via the heap catalog loader.
//
// Before the fix, rollbackDDLCreate only cleaned up the in-memory catalog and
// deleted the relfile, but left the pg_class/pg_attribute rows on disk with
// xmax=0. After WAL replay on restart, loadUserTablesFromHeap found those rows
// (xmin≠0, xmax=0) and re-registered the table.
//
// After the fix, rollbackDDLCreate stamps xmax on those rows via
// deleteCatalogRowsForOID so the startup scan skips them.
func TestRollbackedTableNotVisibleAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Phase 1: BEGIN; CREATE TABLE rollback_ghost; ROLLBACK then close.
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	conn := newTxnConn(rt1, t)
	conn.run("BEGIN")
	conn.run("CREATE TABLE rollback_ghost (id int4, val text)")
	conn.run("ROLLBACK")

	// Sanity: table must not be visible in the live catalog.
	if _, ok := rt1.Catalog.LookupTable(parser.ObjectName{Name: "rollback_ghost"}); ok {
		t.Error("rollback_ghost still in catalog after ROLLBACK — transactional DDL broken")
	}
	rt1.Close()

	// Phase 2: re-open (simulating crash recovery + WAL replay).
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("re-open after rollback: %v", err)
	}
	defer rt2.Close()

	// Phase 3: rolled-back table must NOT reappear after restart.
	if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "rollback_ghost"}); ok {
		t.Error("rollback_ghost reappeared after restart — catalog heap rows not stamped on rollback")
	}
}

// TestDroppedTableNotVisibleAfterRestart pins M0106-0011 follow-up (a):
// a committed DROP TABLE must stamp xmax on the on-disk pg_class /
// pg_attribute rows so the relation does not reappear after the runtime
// is closed and re-opened. Before the fix, `dropTableByRef` only updated
// the in-memory catalog; the heap rows survived (xmin Committed, xmax 0)
// and `loadUserTablesFromHeap` re-resolved the dropped relation on the
// next Open, leaving the catalog out of sync with itself across restart.
func TestDroppedTableNotVisibleAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Phase 1: CREATE then COMMIT-ed DROP. No explicit BEGIN — both
	// statements auto-commit through txnConn's implicit-tx path so the
	// drop hits the real catalog-heap stamp branch (gated on a non-zero
	// XID and catalogHeapSyncAvailable).
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	conn := newTxnConn(rt1, t)
	conn.run("CREATE TABLE drop_ghost (id int4, val text)")
	if _, ok := rt1.Catalog.LookupTable(parser.ObjectName{Name: "drop_ghost"}); !ok {
		t.Fatal("drop_ghost missing immediately after CREATE")
	}
	conn.run("DROP TABLE drop_ghost")
	if _, ok := rt1.Catalog.LookupTable(parser.ObjectName{Name: "drop_ghost"}); ok {
		t.Error("drop_ghost still in in-memory catalog after DROP")
	}
	rt1.Close()

	// Phase 2: re-open (clean reboot + WAL replay).
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("re-open after drop: %v", err)
	}
	defer rt2.Close()

	// Phase 3: dropped table must NOT reappear.
	if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "drop_ghost"}); ok {
		t.Error("drop_ghost reappeared after restart — DROP TABLE did not persist catalog heap stamp")
	}
}

// TestDroppedIndexNotVisibleAfterRestart pins M0106-0011 follow-up (a):
// a committed DROP INDEX must stamp xmax on the on-disk pg_class row for
// the index so the index does not reappear after re-Open. Same mechanism
// as TestDroppedTableNotVisibleAfterRestart but for the index branch of
// `execDropIndex`.
func TestDroppedIndexNotVisibleAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	conn := newTxnConn(rt1, t)
	conn.run("CREATE TABLE idx_host (id int4, val text)")
	conn.run("CREATE INDEX idx_host_id_idx ON idx_host (id)")
	if _, ok := rt1.Catalog.LookupIndex(parser.ObjectName{Name: "idx_host_id_idx"}); !ok {
		t.Fatal("idx_host_id_idx missing immediately after CREATE")
	}
	conn.run("DROP INDEX idx_host_id_idx")
	if _, ok := rt1.Catalog.LookupIndex(parser.ObjectName{Name: "idx_host_id_idx"}); ok {
		t.Error("idx_host_id_idx still in in-memory catalog after DROP")
	}
	rt1.Close()

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("re-open after drop: %v", err)
	}
	defer rt2.Close()

	if _, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "idx_host_id_idx"}); ok {
		t.Error("idx_host_id_idx reappeared after restart — DROP INDEX did not persist catalog heap stamp")
	}
}
