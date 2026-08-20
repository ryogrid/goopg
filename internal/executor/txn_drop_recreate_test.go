package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// newTxnDropRecreateFixture spins up a storage-backed context with an empty
// catalog and no active transaction (BEGIN is issued as an ordinary
// statement, like a real client session), for exercising the
// create->drop->recreate-same-name idiom end to end. M0134-0023.
func newTxnDropRecreateFixture(t *testing.T) (*Context, *catalog.InMemory, *BasicSession, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	mgrMVCC := transam.NewManager()
	sess := NewBasicSession()

	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mgrMVCC
	ctx.Session = sess

	cleanup := func() {
		if sess.InExplicitTransaction() {
			tx2, _, _ := sess.CurrentTransaction()
			_ = mgrMVCC.Rollback(tx2)
		}
		_ = pool.Close()
		_ = mgr.Close()
	}
	return ctx, cat, sess, cleanup
}

// runTxnDropRecreateStmt parses, plans, and executes a single statement
// against ctx. Unlike runSavepointStmt/runTransactionStmt (which plan against
// a throwaway empty catalog — fine for statements that don't need catalog
// resolution, like BEGIN/COMMIT/ROLLBACK/CREATE/DROP TABLE), this planner
// call uses ctx's own catalog so INSERT/SELECT/CTAS statements can resolve
// their target tables.
func runTxnDropRecreateStmt(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): got %d statements", sql, len(stmts))
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	_, err = Run(op, ctx)
	return err
}

// runTxnDropRecreateStmts runs each statement in order via
// runTxnDropRecreateStmt, failing the test at the first error. Every setup
// sequence below opens an explicit BEGIN before its COMMIT — a bare
// autocommit CREATE TABLE (no BEGIN) leaves its RecordDDLCreate undo entry
// undrained by this minimal harness (draining happens either at a genuine
// explicit-transaction boundary via BasicSession.EndExplicitTransaction, or
// — in the real server — dispatch.go's message-scoped autocommit batch
// handling, neither of which a bare COMMIT-with-no-BEGIN triggers here).
func runTxnDropRecreateStmts(t *testing.T, ctx *Context, stmts ...string) {
	t.Helper()
	for _, sql := range stmts {
		if err := runTxnDropRecreateStmt(t, ctx, sql); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
}

func colNames(tbl *catalog.Table) []string {
	names := make([]string, len(tbl.Columns))
	for i, c := range tbl.Columns {
		names[i] = c.Name
	}
	return names
}

func hasCol(tbl *catalog.Table, name string) bool {
	for _, c := range tbl.Columns {
		if c.Name == name {
			return true
		}
	}
	return false
}

// seqScanAllNamed returns all visible rows from the named table (like
// seqScanAll in savepoint_test.go, but parameterized on table name since this
// file's fixtures create tables named "t" and "src", not "items").
func seqScanAllNamed(t *testing.T, ctx *Context, cat catalog.Catalog, name string) []Row {
	t.Helper()
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
	if !ok {
		t.Fatalf("seqScanAllNamed: table %q not found", name)
	}
	scan := &optimizer.SeqScan{Table: tbl}
	op, err := Build(scan)
	if err != nil {
		t.Fatalf("Build seqscan: %v", err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("SeqScan Run: %v", err)
	}
	return rows
}

// TestTxnDropRecreate_RollbackAfterNoCommit is acceptance criterion 1: the
// write_parallel.sql shape — CREATE, DROP, CREATE the same name inside one
// transaction that never commits. Before the fix this raised a false 42P07
// on the second CREATE. PG oracle: heap_create_with_catalog's collision
// check runs on the MVCC snapshot, so a same-txn-deleted tuple is invisible
// to it (postgres/src/backend/catalog/heap.c).
func TestTxnDropRecreate_RollbackAfterNoCommit(t *testing.T) {
	ctx, _, _, cleanup := newTxnDropRecreateFixture(t)
	defer cleanup()

	runTxnDropRecreateStmts(t, ctx,
		"BEGIN",
		"CREATE TABLE t (a int)",
		"DROP TABLE t",
		"CREATE TABLE t (b text)",
		"ROLLBACK",
	)
}

// TestTxnDropRecreate_CommitLandmine is acceptance criterion 2: a
// same-transaction DROP + recreate that COMMITs. The replacement table must
// survive with its NEW shape. Without piece 3a (cancelling the matching
// pending drop), ApplyPendingTableDrops deletes the freshly created
// replacement by name at COMMIT — a silent data-loss bug write_parallel.sql
// itself can never catch, since it always ends in ROLLBACK.
func TestTxnDropRecreate_CommitLandmine(t *testing.T) {
	ctx, cat, _, cleanup := newTxnDropRecreateFixture(t)
	defer cleanup()

	runTxnDropRecreateStmts(t, ctx,
		"BEGIN",
		"CREATE TABLE t (a int)",
		"COMMIT",
	)
	runTxnDropRecreateStmts(t, ctx,
		"BEGIN",
		"DROP TABLE t",
		"CREATE TABLE t (b text)",
		"COMMIT",
	)

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t must still exist after COMMIT of drop+recreate")
	}
	if !hasCol(tbl, "b") || hasCol(tbl, "a") {
		t.Fatalf("table t has columns %v, want the NEW shape [b]", colNames(tbl))
	}
}

// TestTxnDropRecreate_RollbackLandmine is acceptance criterion 3: a
// same-transaction DROP + recreate that ROLLBACKs. The ORIGINAL
// already-committed table must be restored — including its rows. Without
// piece 3b (DDLUndoEntry.ShadowedTable + rollbackDDLCreate restoring it via
// InMemory.RegisterTable), rollbackDDLCreate's bare
// Catalog.DropTable(entry.Name) leaves the slot empty and the table is lost.
func TestTxnDropRecreate_RollbackLandmine(t *testing.T) {
	ctx, cat, _, cleanup := newTxnDropRecreateFixture(t)
	defer cleanup()

	runTxnDropRecreateStmts(t, ctx,
		"BEGIN",
		"CREATE TABLE t (a int)",
		"INSERT INTO t VALUES (1)",
		"INSERT INTO t VALUES (2)",
		"COMMIT",
	)
	runTxnDropRecreateStmts(t, ctx,
		"BEGIN",
		"DROP TABLE t",
		"CREATE TABLE t (b text)",
		"ROLLBACK",
	)

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t must still exist after ROLLBACK of drop+recreate — the ORIGINAL committed table must be restored")
	}
	if !hasCol(tbl, "a") || hasCol(tbl, "b") {
		t.Fatalf("table t has columns %v, want the ORIGINAL shape [a]", colNames(tbl))
	}

	// Reading committed data needs a fresh snapshot: ctx.Tx/ctx.Snap are
	// cleared after the preceding ROLLBACK, so wrap the read in its own
	// throwaway transaction (mirrors savepoint_test.go's post-commit check).
	runTxnDropRecreateStmts(t, ctx, "BEGIN")
	rows := seqScanAllNamed(t, ctx, cat, "t")
	runTxnDropRecreateStmts(t, ctx, "ROLLBACK")
	if len(rows) != 2 {
		t.Fatalf("table t has %d rows after rollback, want 2 (the original committed rows)", len(rows))
	}
}

// TestTxnDropRecreate_NoRegression is acceptance criterion 4: the deferral's
// original reason for existing must be unaffected — a plain DROP-then-ROLLBACK
// (no recreate) still restores the table, and a genuine duplicate CREATE (no
// intervening DROP) still raises 42P07.
func TestTxnDropRecreate_NoRegression(t *testing.T) {
	t.Run("DropThenRollbackNoRecreate", func(t *testing.T) {
		ctx, cat, _, cleanup := newTxnDropRecreateFixture(t)
		defer cleanup()

		runTxnDropRecreateStmts(t, ctx,
			"BEGIN",
			"CREATE TABLE t (a int)",
			"COMMIT",
		)
		runTxnDropRecreateStmts(t, ctx,
			"BEGIN",
			"DROP TABLE t",
			"ROLLBACK",
		)
		if _, ok := cat.LookupTable(parser.ObjectName{Name: "t"}); !ok {
			t.Fatal("table t must still exist after DROP+ROLLBACK (no recreate)")
		}
	})

	t.Run("PlainDuplicateStill42P07", func(t *testing.T) {
		ctx, _, _, cleanup := newTxnDropRecreateFixture(t)
		defer cleanup()

		runTxnDropRecreateStmts(t, ctx,
			"BEGIN",
			"CREATE TABLE t (a int)",
		)
		err := runTxnDropRecreateStmt(t, ctx, "CREATE TABLE t (a int)")
		if err == nil {
			t.Fatal("second CREATE TABLE t (no DROP) should fail with 42P07")
		}
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42P07" {
			t.Fatalf("second CREATE TABLE t error = %v, want 42P07", err)
		}
	})
}

// TestTxnDropRecreate_CTAS is acceptance criterion 5: CTAS shares the
// execCreateTable existence-check site, so the drop+recreate idiom must also
// work for `CREATE TABLE ... AS SELECT`.
func TestTxnDropRecreate_CTAS(t *testing.T) {
	ctx, cat, _, cleanup := newTxnDropRecreateFixture(t)
	defer cleanup()

	runTxnDropRecreateStmts(t, ctx,
		"BEGIN",
		"CREATE TABLE src (a int)",
		"INSERT INTO src VALUES (1)",
		"INSERT INTO src VALUES (2)",
		"COMMIT",
	)
	runTxnDropRecreateStmts(t, ctx,
		"BEGIN",
		"CREATE TABLE t (x int)",
		"DROP TABLE t",
		"CREATE TABLE t AS SELECT a FROM src",
		"COMMIT",
	)

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("table t must exist after CTAS recreate + COMMIT")
	}
	if !hasCol(tbl, "a") || hasCol(tbl, "x") {
		t.Fatalf("table t has columns %v, want the CTAS shape [a]", colNames(tbl))
	}
	runTxnDropRecreateStmts(t, ctx, "BEGIN")
	rows := seqScanAllNamed(t, ctx, cat, "t")
	runTxnDropRecreateStmts(t, ctx, "ROLLBACK")
	if len(rows) != 2 {
		t.Fatalf("CTAS table t has %d rows, want 2 (copied from src)", len(rows))
	}
}
