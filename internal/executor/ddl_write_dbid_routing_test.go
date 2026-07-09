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

// TestExecCreateViewRoutesToConnectionRealDBOid covers M0122-0007 slice
// 4d-ii-part-2b item (1): catalog.InMemory.CreateView/DropView previously had
// no dbOid parameter at all and always wrote/read c.ns(DefaultDBOid), which
// was a bigger gap than the LookupTable/LookupIndex sweep — a CREATE VIEW
// issued on a connection bound to a genuinely distinct dbOid always landed
// under DefaultDBOid regardless of ctx.CurrentDatabaseOid (a namespace
// collision, not merely a lookup miss). The view body is a literal SELECT
// (no FROM) to isolate this fix from the separate, still-open gap that
// o.planCatalog() itself is not dbOid-aware for FROM-table resolution
// (tracked in the deferral ledger).
func TestExecCreateViewRoutesToConnectionRealDBOid(t *testing.T) {
	const otherDBOid = 4747
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE VIEW v AS SELECT 1 AS id"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}

	name := parser.ObjectName{Schema: "public", Name: "v"}
	if _, ok := ctx.Catalog.LookupTable(name); ok {
		t.Fatal("LookupTable(DefaultDBOid) unexpectedly found a view created under a distinct connection dbOid")
	}
	tbl, ok := ctx.Catalog.LookupTable(name, otherDBOid)
	if !ok {
		t.Fatalf("LookupTable(dbOid=%d) did not find the view created by this connection", otherDBOid)
	}
	if tbl.View == nil {
		t.Fatal("found relation is not a view")
	}

	if err := runDDL(t, ctx, "DROP VIEW v"); err != nil {
		t.Fatalf("DROP VIEW: %v", err)
	}
	if _, ok := ctx.Catalog.LookupTable(name, otherDBOid); ok {
		t.Fatal("LookupTable(otherDBOid) still finds the view after DROP VIEW on the same connection")
	}
}

// TestIndexesOnTableFindsOwnDistinctDBOidTable covers the same 4d-ii-part-2b
// item (1) gap for catalog.InMemory.IndexesOnTable: before this fix it always
// scanned c.ns(DefaultDBOid).byTable, so an index on a table created under a
// genuinely distinct dbOid was unreachable via IndexesOnTable — the exact
// blocker noted in the design doc as breaking the collectViewPKDeps/
// addGroupByPKDeps cluster's white-box test.
func TestIndexesOnTableFindsOwnDistinctDBOidTable(t *testing.T) {
	const otherDBOid = 4848
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX widgets_id_idx ON widgets (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "widgets"}, otherDBOid)
	if !ok {
		t.Fatal("LookupTable(otherDBOid) did not find the table")
	}
	if idxs := ctx.Catalog.IndexesOnTable(tbl); len(idxs) != 0 {
		t.Fatalf("IndexesOnTable(tbl) [defaults to DefaultDBOid] unexpectedly found %d indexes for a table created under a distinct connection dbOid", len(idxs))
	}
	idxs := ctx.Catalog.IndexesOnTable(tbl, otherDBOid)
	if len(idxs) != 1 {
		t.Fatalf("IndexesOnTable(tbl, otherDBOid)=%d indexes, want 1", len(idxs))
	}
	if idxs[0].Name != "widgets_id_idx" {
		t.Fatalf("IndexesOnTable(tbl, otherDBOid)[0].Name=%q, want widgets_id_idx", idxs[0].Name)
	}
}


// TestExecCreateViewFromClauseRoutesToConnectionRealDBOid closes the
// o.planCatalog() FROM-table-resolution gap noted above and in the deferral
// ledger (AI-20260710-011513-001 / 4d-ii-part-2b item 3): before
// executor.ctxPlanCatalog wrapped ctx.Catalog in a dbOid-seeded
// catalog.SearchPathCatalog when ctx.PlanCatalog was unset (as it is via
// this package's own test fixtures, which never run server/dispatch.go's
// ectx wiring), planner.Plan's internal LookupTable call for the view's FROM
// table always fell back to DefaultDBOid, so `CREATE VIEW ... FROM base` on
// a distinct-dbOid connection failed to resolve `base` even though `base`
// exists under that connection's own dbOid.
func TestExecCreateViewFromClauseRoutesToConnectionRealDBOid(t *testing.T) {
	const otherDBOid = 4949
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE base (id int4, val text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW v AS SELECT id, val FROM base"); err != nil {
		t.Fatalf("CREATE VIEW ... FROM base: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "v"}, otherDBOid)
	if !ok {
		t.Fatalf("LookupTable(dbOid=%d) did not find the view created by this connection", otherDBOid)
	}
	if tbl.View == nil {
		t.Fatal("found relation is not a view")
	}
	// The view's column list is planner-derived (execCreateView's planSchema
	// path); it only has 2 real columns (id, val) when the FROM table
	// actually resolved during planning. Before the fix, planner.Plan failed
	// to find `base` under otherDBOid, so execCreateView fell back to the
	// target-list path and still recorded 2 columns from the unresolved
	// target names — so the real signal is that the CREATE VIEW above did
	// not error and the columns carry the expected names/types below (an
	// unresolved `id`/`val` reference would have failed planning with
	// 42P01/42703, which execCreateView surfaces via the non-0A000 error
	// branch).
	if len(tbl.Columns) != 2 {
		t.Fatalf("view has %d columns, want 2", len(tbl.Columns))
	}
	if tbl.Columns[0].Name != "id" || tbl.Columns[0].Type.Name != "int4" {
		t.Fatalf("view column 0 = %+v, want id/int4", tbl.Columns[0])
	}
	if tbl.Columns[1].Name != "val" || tbl.Columns[1].Type.Name != "text" {
		t.Fatalf("view column 1 = %+v, want val/text", tbl.Columns[1])
	}
}


// TestExecCreateViewGroupByPKDepRegistersUnderDistinctDBOid closes a second,
// closely related gap surfaced while writing the FROM-clause test above:
// addGroupByPKDeps (the GROUP-BY-functional-dependency half of
// collectViewPKDeps, called from execCreateView) called
// cat.IndexesOnTable(tbl) with no dbOid argument even though it already
// receives one and threads it into the sibling cat.LookupTable call two
// lines above — so on a distinct-dbOid connection it could never find the
// base table's PK index, and CREATE VIEW never registered the
// view→PK-constraint dependency (catalog.InMemory.RegisterViewConstraintDep)
// that DROP CONSTRAINT RESTRICT relies on to detect a dependent view. Fixed
// by passing dbOid through to IndexesOnTable, mirroring the LookupTable call
// immediately above it. M0122-0007 slice 4d-ii-part-2b item 3.
func TestExecCreateViewGroupByPKDepRegistersUnderDistinctDBOid(t *testing.T) {
	const otherDBOid = 5151
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE base (id int4 PRIMARY KEY, val text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW v AS SELECT id, count(*) FROM base GROUP BY id"); err != nil {
		t.Fatalf("CREATE VIEW ... GROUP BY id: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "base"}, otherDBOid)
	if !ok {
		t.Fatalf("LookupTable(dbOid=%d) did not find base", otherDBOid)
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("ctx.Catalog is not *catalog.InMemory")
	}
	deps := im.ViewsDependingOnConstraint(tbl.OID, "base_pkey")
	if len(deps) != 1 || deps[0] != "v" {
		t.Fatalf("ViewsDependingOnConstraint(base.OID, base_pkey)=%v, want [v]", deps)
	}
}

// TestRelFileNodeUsesTableOwnDBOidNotProcessWideDefault closes M0122-0007
// 4d-ii-part-2b item 2: catalog.InMemory.RelFileNode/IndexRelFileNode used to
// stamp storage.RelFileNode.DBOid from the single process-wide
// InMemory.dbOid (SetDBOID/DBOID) regardless of which database a table's own
// namespace lives under, so two tables of the same name created on distinct
// connection dbOids would alias onto the SAME on-disk relfilenode path —
// physical storage was never actually per-database. Fixed by giving
// Table/Index their own DBOid field, set at CreateTable/CreateIndex time
// from the same catalog.NamespaceDBOid(ctx.CurrentDatabaseOid) value already
// threaded through every executor DDL call site (item 1/item 3), and having
// RelFileNode/IndexRelFileNode prefer it over the process-wide fallback.
func TestRelFileNodeUsesTableOwnDBOidNotProcessWideDefault(t *testing.T) {
	const otherDBOid = 6262
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE (default dbOid): %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE (dbOid=%d): %v", otherDBOid, err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("ctx.Catalog is not *catalog.InMemory")
	}
	name := parser.ObjectName{Schema: "public", Name: "widgets"}

	defaultTbl, ok := im.LookupTable(name, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("LookupTable(DefaultDBOid) did not find the first widgets table")
	}
	otherTbl, ok := im.LookupTable(name, otherDBOid)
	if !ok {
		t.Fatalf("LookupTable(dbOid=%d) did not find the second widgets table", otherDBOid)
	}

	defaultRel := im.RelFileNode(defaultTbl)
	otherRel := im.RelFileNode(otherTbl)
	if defaultRel.DBOid != catalog.DefaultDBOid {
		t.Fatalf("RelFileNode(default-dbOid widgets).DBOid = %d, want %d", defaultRel.DBOid, catalog.DefaultDBOid)
	}
	if otherRel.DBOid != otherDBOid {
		t.Fatalf("RelFileNode(dbOid=%d widgets).DBOid = %d, want %d", otherDBOid, otherRel.DBOid, otherDBOid)
	}
	if defaultRel.RelOid == otherRel.RelOid && defaultRel.DBOid == otherRel.DBOid {
		t.Fatalf("the two distinct-dbOid widgets tables collided onto the same RelFileNode %+v", defaultRel)
	}

	defaultIdx, ok := im.LookupIndex(parser.ObjectName{Schema: "public", Name: "widgets_pkey"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("LookupIndex(DefaultDBOid) did not find widgets_pkey")
	}
	otherIdx, ok := im.LookupIndex(parser.ObjectName{Schema: "public", Name: "widgets_pkey"}, otherDBOid)
	if !ok {
		t.Fatalf("LookupIndex(dbOid=%d) did not find widgets_pkey", otherDBOid)
	}
	defaultIdxRel := im.IndexRelFileNode(defaultIdx)
	otherIdxRel := im.IndexRelFileNode(otherIdx)
	if defaultIdxRel.DBOid != catalog.DefaultDBOid {
		t.Fatalf("IndexRelFileNode(default-dbOid widgets_pkey).DBOid = %d, want %d", defaultIdxRel.DBOid, catalog.DefaultDBOid)
	}
	if otherIdxRel.DBOid != otherDBOid {
		t.Fatalf("IndexRelFileNode(dbOid=%d widgets_pkey).DBOid = %d, want %d", otherDBOid, otherIdxRel.DBOid, otherDBOid)
	}
}
