package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestExecCreateTableRoutesToConnectionRealDBOid exercises M0122-0007 slice
// 4d-i: CREATE TABLE issued through the executor (not the catalog package
// directly) now lands in the connection's own dbOid namespace instead of the
// hardcoded DefaultDBOid, when ctx.CurrentDatabaseOid names a genuinely
// distinct real database. Before this slice, every executor DDL entry point
// called catalog.CreateTable/etc. with no dbOid argument, which always
// resolved to DefaultDBOid regardless of which database a connection was
// bound to (docs/design/0122-0018-per-database-catalog-namespace.md).
func TestExecCreateTableRoutesToConnectionRealDBOid(t *testing.T) {
	const otherDBOid = 4242
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	name := parser.ObjectName{Schema: "public", Name: "widgets"}
	if _, ok := ctx.Catalog.LookupTable(name); ok {
		t.Fatal("LookupTable(DefaultDBOid) unexpectedly found a table created under a distinct connection dbOid")
	}
	if _, ok := ctx.Catalog.LookupTable(name, otherDBOid); !ok {
		t.Fatalf("LookupTable(dbOid=%d) did not find the table created by this connection", otherDBOid)
	}
}

// TestExecCreateTableAsRoutesToConnectionRealDBOid covers CREATE TABLE AS's
// separate execCreateTableAs code path (M0096-0008), which calls
// catalog.CreateTable independently of execCreateTable.
func TestExecCreateTableAsRoutesToConnectionRealDBOid(t *testing.T) {
	const otherDBOid = 4343
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	// A literal SELECT (no FROM clause) needs no catalog table resolution, so
	// this isolates execCreateTableAs's own CreateTable call from the
	// separate (pre-existing, unrelated to this slice) question of whether
	// planner.Plan's own table-name resolution is dbOid-aware in this raw
	// test harness.
	if err := runDDL(t, ctx, "CREATE TABLE dst AS SELECT 1 AS id"); err != nil {
		t.Fatalf("CREATE TABLE AS: %v", err)
	}

	name := parser.ObjectName{Schema: "public", Name: "dst"}
	if _, ok := ctx.Catalog.LookupTable(name); ok {
		t.Fatal("LookupTable(DefaultDBOid) unexpectedly found a CREATE TABLE AS result created under a distinct connection dbOid")
	}
	if _, ok := ctx.Catalog.LookupTable(name, otherDBOid); !ok {
		t.Fatalf("LookupTable(dbOid=%d) did not find the CREATE TABLE AS result", otherDBOid)
	}
}

// TestExecCreateTablePostgresConnectionStaysOnDefaultDBOid pins the
// postgres/DefaultDBOid dual-mirror shim (catalog.NamespaceDBOid) on the
// write side: a connection whose CurrentDatabaseOid resolves to
// catalog.PostgresDBOid (the "postgres" pseudo-database) must still create
// tables under DefaultDBOid, exactly as slice 4c already guarantees for
// reads. Getting this wrong would make every CREATE TABLE issued over a
// "postgres" connection (the overwhelming majority of real traffic —
// TPC-H/pgbench/regress all connect to "postgres") invisible to every other
// session.
func TestExecCreateTablePostgresConnectionStaysOnDefaultDBOid(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.PostgresDBOid

	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	name := parser.ObjectName{Schema: "public", Name: "widgets"}
	if _, ok := ctx.Catalog.LookupTable(name); !ok {
		t.Fatal("LookupTable(DefaultDBOid) did not find a table created over a \"postgres\" connection — dual-mirror shim broken on the write side")
	}
}
