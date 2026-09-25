package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// runChainDDL executes a DDL statement (e.g. CREATE TABLE) against ctx via
// the same unscoped Plan(stmts[0], ctx.Catalog) the executor package's own
// runDDL test helper uses (storage_ddl_test.go). That is correct here
// because a CREATE TABLE's own target name isn't resolved at plan time;
// namespace placement happens later, at Open/Next, keyed off
// ctx.CurrentDatabaseOid (which is why fk_dbid_routing_test.go's REFERENCES
// resolution also works unscoped).
func runChainDDL(t *testing.T, ctx *executor.Context, sql string) error {
	t.Helper()
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := executor.Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	if _, err := op.Next(); err != executor.EOF {
		return err
	}
	return op.Close()
}

// runChainQuery executes a SELECT against ctx through a dbOid-scoped
// SearchPathCatalog, mirroring internal/executor/fk_dbid_routing_test.go's
// runQueryUnderDBOid. Unlike CREATE TABLE's target name, a FROM-clause table
// name IS resolved at plan time, and must resolve within
// ctx.CurrentDatabaseOid's own namespace rather than DefaultDBOid's.
func runChainQuery(t *testing.T, ctx *executor.Context, sql string) ([]executor.Row, error) {
	t.Helper()
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	planCat := catalog.WithSearchPath(ctx.Catalog, nil)
	planCat.DBOid = ctx.CurrentDatabaseOid
	plan, err := optimizer.Plan(stmts[0], planCat)
	if err != nil {
		return nil, err
	}
	op, err := executor.Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		return nil, err
	}
	var rows []executor.Row
	for {
		slot, nextErr := op.Next()
		if nextErr == executor.EOF {
			break
		}
		if nextErr != nil {
			_ = op.Close()
			return nil, nextErr
		}
		rows = append(rows, slot.Materialize().Row())
	}
	return rows, op.Close()
}

// newChainContext builds an executor.Context bound to dbName's REAL
// catalog-allocated oid via the postmaster-level resolution chain
// (s.wireExtensionRows), the same call a real client connection goes
// through — unlike internal/executor/fk_dbid_routing_test.go's per-dbOid
// tests, which set ctx.CurrentDatabaseOid to a synthetic constant directly
// on the struct field.
func newChainContext(t *testing.T, s *Server, mgrMVCC *transam.Manager, pool *storage.Pool, dbName string) *executor.Context {
	t.Helper()
	tx, err := mgrMVCC.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := mgrMVCC.SnapshotFor(tx)
	if err != nil {
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := executor.NewContext()
	ctx.Pool = pool
	ctx.Catalog = s.cfg.Catalog
	ctx.TxnMgr = mgrMVCC
	ctx.Tx = tx
	ctx.Snap = snap
	s.wireExtensionRows(ctx, dbName)
	return ctx
}

// TestDatabaseDDLChainedExecutorDML closes M0143-0001's "manual psql
// evidence only" gap: CREATE DATABASE (postmaster's string-prefix dispatch,
// tryHandleDatabaseDDL) chained with executor DDL/DML run against the newly
// created database's own REAL, catalog-allocated oid — resolved the same
// way a real client connection resolves it (wireExtensionRows), not a
// synthetic ctx.CurrentDatabaseOid constant set directly on the struct the
// way fk_dbid_routing_test.go's existing per-dbOid coverage does. It
// exercises the two paths M0143-0001 named as previously untested in
// process: pgConstraintTableRel's per-database FK routing
// (PGConstraintRowsForDBOid's `tbl.ForeignKeys` loop) and the per-database
// table-namespace split a bare CREATE TABLE lands in.
func TestDatabaseDDLChainedExecutorDML(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	t.Cleanup(func() { _ = pool.Close() })

	im := catalog.NewInMemory()
	im.SetDBOID(5) // mirrors detectCatalogDBOID's real-world PG18 "postgres" oid
	s := New(Config{Catalog: im, Pool: pool})

	handled, _, err := s.tryHandleDatabaseDDL("CREATE DATABASE r", "postgres", "", nil)
	if !handled || err != nil {
		t.Fatalf("CREATE DATABASE r: handled=%v err=%v", handled, err)
	}

	mgrMVCC := transam.NewManager()
	ectxR := newChainContext(t, s, mgrMVCC, pool, "r")
	ectxPG := newChainContext(t, s, mgrMVCC, pool, "postgres")
	t.Cleanup(func() { _ = mgrMVCC.Rollback(ectxPG.Tx) })
	t.Cleanup(func() { _ = mgrMVCC.Rollback(ectxR.Tx) })

	if ectxR.CurrentDatabaseOid == 0 {
		t.Fatal("wireExtensionRows did not resolve a real oid for CREATE DATABASE r — silently fell back to DefaultDBOid")
	}
	if ectxR.CurrentDatabaseOid == ectxPG.CurrentDatabaseOid {
		t.Fatalf("db r and db postgres resolved to the same oid %d — CREATE DATABASE r did not allocate a distinct database", ectxR.CurrentDatabaseOid)
	}

	// Build a table + FK constraint under each database's own real oid.
	if err := runChainDDL(t, ectxR, "CREATE TABLE parent (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("db r: CREATE TABLE parent: %v", err)
	}
	if err := runChainDDL(t, ectxR, "CREATE TABLE child (id int4, pid int4 REFERENCES parent(id))"); err != nil {
		t.Fatalf("db r: CREATE TABLE child: %v", err)
	}
	if err := runChainDDL(t, ectxPG, "CREATE TABLE other (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("db postgres: CREATE TABLE other: %v", err)
	}
	if err := runChainDDL(t, ectxPG, "CREATE TABLE otherchild (id int4, pid int4 REFERENCES other(id))"); err != nil {
		t.Fatalf("db postgres: CREATE TABLE otherchild: %v", err)
	}

	// pg_constraint (pgConstraintTableRel's per-database branch, reached here
	// via wireExtensionRows' PgConstraintRows wiring) must see exactly the FK
	// created in ITS OWN database. Checking the conname, not just the row
	// count, matters: a swapped-namespace bug (e.g. NamespaceDBOid mapping
	// db r's real oid back to DefaultDBOid the way it deliberately does for
	// PostgresDBOid today) preserves "exactly 1 row" while returning the
	// WRONG database's constraint — caught this exact way while writing this
	// test (temporarily forcing PGConstraintRowsForDBOid's dbOid argument to
	// DefaultDBOid reproduced a same-count, swapped-identity failure that a
	// count-only assertion missed).
	rowsR, err := runChainQuery(t, ectxR, "SELECT conname FROM pg_constraint WHERE contype = 'f'")
	if err != nil {
		t.Fatalf("db r: SELECT pg_constraint: %v", err)
	}
	if len(rowsR) != 1 || string(rowsR[0][0].Buf) != "child_pid_fkey" {
		t.Errorf("db r: pg_constraint FK rows = %v, want exactly [child_pid_fkey] — a wrong count OR a swapped name both indicate cross-database leakage", rowsR)
	}

	rowsPG, err := runChainQuery(t, ectxPG, "SELECT conname FROM pg_constraint WHERE contype = 'f'")
	if err != nil {
		t.Fatalf("db postgres: SELECT pg_constraint: %v", err)
	}
	if len(rowsPG) != 1 || string(rowsPG[0][0].Buf) != "otherchild_pid_fkey" {
		t.Errorf("db postgres: pg_constraint FK rows = %v, want exactly [otherchild_pid_fkey] — a wrong count OR a swapped name both indicate cross-database leakage", rowsPG)
	}

	// Namespace isolation: a table created in one database must be
	// invisible when queried under the other database's own oid.
	if _, err := runChainQuery(t, ectxPG, "SELECT id FROM parent"); err == nil {
		t.Error("db postgres: SELECT FROM parent unexpectedly succeeded — db r's table leaked into postgres's namespace")
	}
	if _, err := runChainQuery(t, ectxR, "SELECT id FROM other"); err == nil {
		t.Error("db r: SELECT FROM other unexpectedly succeeded — db postgres's table leaked into r's namespace")
	}
}
