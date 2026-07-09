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

// TestExecDropTableFindsOwnDistinctDBOidTable covers M0122-0007 slice 4d-ii:
// before this slice, execDropTable located its target via a bare
// o.ctx.Catalog.LookupTable(name) call that always resolved DefaultDBOid, so
// a same-connection DROP TABLE of an object 4d-i's CreateTable routing had
// just created under a genuinely distinct dbOid would report "does not
// exist" (documented as the open gap in
// docs/design/0122-0018-per-database-catalog-namespace.md's 4d-i "critical
// scope finding"). execDropTable now threads
// catalog.NamespaceDBOid(ctx.CurrentDatabaseOid) through its own lookup, so
// CREATE then DROP on the same distinct-dbOid connection round-trips.
func TestExecDropTableFindsOwnDistinctDBOidTable(t *testing.T) {
	const otherDBOid = 4444
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "DROP TABLE widgets"); err != nil {
		t.Fatalf("DROP TABLE: %v", err)
	}

	name := parser.ObjectName{Schema: "public", Name: "widgets"}
	if _, ok := ctx.Catalog.LookupTable(name, otherDBOid); ok {
		t.Fatal("LookupTable(otherDBOid) still finds the table after DROP TABLE on the same connection")
	}
}

// TestExecCreateIndexFindsOwnDistinctDBOidTable covers the CREATE INDEX half
// of the same 4d-ii gap: execCreateIndex's own o.ctx.Catalog.LookupTable(s.Table)
// call must resolve the connection's real dbOid, or a same-connection CREATE
// INDEX on a table 4d-i's CreateTable routing had just created under a
// distinct dbOid would report "relation does not exist".
func TestExecCreateIndexFindsOwnDistinctDBOidTable(t *testing.T) {
	const otherDBOid = 4545
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX widgets_id_idx ON widgets (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}

	idxName := parser.ObjectName{Schema: "public", Name: "widgets_id_idx"}
	if _, ok := ctx.Catalog.LookupIndex(idxName, otherDBOid); !ok {
		t.Fatal("LookupIndex(otherDBOid) did not find the index created by this connection")
	}
}


// TestExecCommentOnFindsOwnDistinctDBOidTable covers M0122-0007 slice
// 4d-ii-part-2 item (1): execCommentOn's per-ObjKind im.LookupTable(s.ObjName)/
// im.LookupIndex(s.ObjName) calls previously carried no dbOid argument, which
// always resolved DefaultDBOid. On a connection bound to a genuinely distinct
// dbOid, COMMENT ON TABLE/COLUMN/INDEX for an object created by that same
// connection would report "does not exist" (or, worse, silently no-op on a
// same-named DefaultDBOid object belonging to a different database).
func TestExecCommentOnFindsOwnDistinctDBOidTable(t *testing.T) {
	const oidPgClass = 1259
	const otherDBOid = 4646
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE base (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX base_id_idx ON base (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	if err := runDDL(t, ctx, "COMMENT ON TABLE base IS 'a table comment'"); err != nil {
		t.Fatalf("COMMENT ON TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "COMMENT ON COLUMN base.id IS 'a column comment'"); err != nil {
		t.Fatalf("COMMENT ON COLUMN: %v", err)
	}
	if err := runDDL(t, ctx, "COMMENT ON INDEX base_id_idx IS 'an index comment'"); err != nil {
		t.Fatalf("COMMENT ON INDEX: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("ctx.Catalog is not *catalog.InMemory")
	}
	tblName := parser.ObjectName{Schema: "public", Name: "base"}
	tbl, ok := im.LookupTable(tblName, otherDBOid)
	if !ok {
		t.Fatal("LookupTable(otherDBOid) did not find the table")
	}
	if desc, ok := im.GetComment(oidPgClass, tbl.OID, 0); !ok || desc != "a table comment" {
		t.Fatalf("COMMENT ON TABLE: GetComment(pg_class, %d, 0)=(%q,%v), want (%q,true)", tbl.OID, desc, ok, "a table comment")
	}
	if desc, ok := im.GetComment(oidPgClass, tbl.OID, 1); !ok || desc != "a column comment" {
		t.Fatalf("COMMENT ON COLUMN: GetComment(pg_class, %d, 1)=(%q,%v), want (%q,true)", tbl.OID, desc, ok, "a column comment")
	}
	idxName := parser.ObjectName{Schema: "public", Name: "base_id_idx"}
	idx, ok := im.LookupIndex(idxName, otherDBOid)
	if !ok {
		t.Fatal("LookupIndex(otherDBOid) did not find the index")
	}
	if desc, ok := im.GetComment(oidPgClass, idx.OID, 0); !ok || desc != "an index comment" {
		t.Fatalf("COMMENT ON INDEX: GetComment(pg_class, %d, 0)=(%q,%v), want (%q,true)", idx.OID, desc, ok, "an index comment")
	}
}

// TestExecCreateStatisticsFindsOwnDistinctDBOidTable covers M0122-0007 slice
// 4d-ii-part-2 item (1): execCreateStatistics's im.LookupTable(s.FromTable)
// call previously carried no dbOid argument, so on a connection bound to a
// genuinely distinct dbOid the FROM table was never found and the new
// statistics object was silently registered with TableOID=0 (an orphan,
// unattached to any real table) instead of the connection's own table.
func TestExecCreateStatisticsFindsOwnDistinctDBOidTable(t *testing.T) {
	const otherDBOid = 4646
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE base (a int4, b int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE STATISTICS stx1 (dependencies) ON a, b FROM base"); err != nil {
		t.Fatalf("CREATE STATISTICS: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("ctx.Catalog is not *catalog.InMemory")
	}
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "public", Name: "base"}, otherDBOid)
	if !ok {
		t.Fatal("LookupTable(otherDBOid) did not find the table")
	}
	obj, ok := im.LookupStatistics("stx1")
	if !ok {
		t.Fatal("LookupStatistics(stx1) not found")
	}
	if obj.TableOID != tbl.OID {
		t.Fatalf("CREATE STATISTICS: TableOID=%d, want %d (the connection's own base table) — FROM-table lookup did not resolve the connection's real dbOid", obj.TableOID, tbl.OID)
	}
}

// TestExecAttrACLChangeFindsOwnDistinctDBOidTable covers M0122-0007 slice
// 4d-ii-part-2 item (1): execAttrACLChange's im.LookupTable(tn) call
// previously carried no dbOid argument, so a column-level GRANT issued on a
// connection bound to a genuinely distinct dbOid silently found nothing
// (continue, no error) and recorded no privilege at all.
func TestExecAttrACLChangeFindsOwnDistinctDBOidTable(t *testing.T) {
	const otherDBOid = 4646
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE base (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "GRANT SELECT (id) ON base TO alice"); err != nil {
		t.Fatalf("GRANT: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("ctx.Catalog is not *catalog.InMemory")
	}
	tbl, ok := im.LookupTable(parser.ObjectName{Schema: "public", Name: "base"}, otherDBOid)
	if !ok {
		t.Fatal("LookupTable(otherDBOid) did not find the table")
	}
	acl := im.AttrACLText(tbl.OID, 1)
	if acl == "" {
		t.Fatal("AttrACLText is empty after GRANT SELECT (id) ON base TO alice — the column privilege was not recorded")
	}
}
