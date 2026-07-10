package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runDMLUnderDBOid is like runDDL, but plans the statement through a
// catalog.SearchPathCatalog seeded with ctx.CurrentDatabaseOid, mirroring how
// the real server always plans (server.sessionPlanCatalog/ctxPlanCatalog) —
// unlike runDDL's raw planner.Plan(stmts[0], ctx.Catalog) call, which never
// resolves table names under any dbOid but DefaultDBOid. Needed for INSERT/
// TRUNCATE statements, whose target-table name IS resolved at planning time
// (unlike CREATE TABLE/CREATE VIEW's outer statement plan, which routing
// tests elsewhere in this package exercise via plain runDDL).
func runDMLUnderDBOid(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	planCat := catalog.WithSearchPath(ctx.Catalog, nil)
	planCat.DBOid = ctx.CurrentDatabaseOid
	plan, err := planner.Plan(stmts[0], planCat)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	if _, err := op.Next(); err != EOF {
		return err
	}
	return op.Close()
}

// runQueryUnderDBOid is runQuery's counterpart to runDMLUnderDBOid — plans
// through the same dbOid-seeded SearchPathCatalog so a SELECT against a
// distinct-dbOid table resolves.
func runQueryUnderDBOid(t *testing.T, ctx *Context, sql string) []Row {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	planCat := catalog.WithSearchPath(ctx.Catalog, nil)
	planCat.DBOid = ctx.CurrentDatabaseOid
	plan, err := planner.Plan(stmts[0], planCat)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(%q): %v", sql, err)
	}
	rows, err := drainScan(op)
	if err != nil {
		t.Fatalf("drain(%q): %v", sql, err)
	}
	_ = op.Close()
	return rows
}

// TestAssertParentExistsFindsOwnDistinctDBOidParent covers M0122-0007 slice
// 4e's first item, FK target resolution: assertParentExists (the INSERT/
// UPDATE-time FK-parent-exists check) used to call im.LookupTable(fk.RefTable)
// with no dbOid argument, which always resolved DefaultDBOid regardless of
// which database the inserting connection was bound to. On a connection whose
// CurrentDatabaseOid names a genuinely distinct database, the referenced
// table (created under that same distinct dbOid by 4d-i's CreateTable
// routing) was invisible to this lookup — ok=false — so assertParentExists
// took its "referenced table not found (CREATE TABLE out of order) — skip"
// early return and let ANY INSERT through with no FK enforcement at all,
// instead of correctly rejecting rows whose FK value has no matching parent.
func TestAssertParentExistsFindsOwnDistinctDBOidParent(t *testing.T) {
	const otherDBOid = 7001
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE parent (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE child (id int4, pid int4 REFERENCES parent(id))"); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO parent VALUES (1)"); err != nil {
		t.Fatalf("INSERT INTO parent: %v", err)
	}

	// pid=2 has no matching row in parent — must be rejected with 23503.
	err := runDMLUnderDBOid(t, ctx, "INSERT INTO child VALUES (100, 2)")
	if err == nil {
		t.Fatal("INSERT INTO child with a non-existent FK target unexpectedly succeeded — FK enforcement silently skipped under a distinct connection dbOid")
	}
	execErr, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if execErr.Code != "23503" {
		t.Fatalf("expected 23503, got %q: %v", execErr.Code, execErr)
	}

	// pid=1 matches parent's row and must be accepted.
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO child VALUES (101, 1)"); err != nil {
		t.Fatalf("INSERT INTO child with a valid FK target: %v", err)
	}
}

// TestExecTruncateCascadeFindsOwnDistinctDBOidReferencingTable covers M0122-
// 0007 slice 4e's FK target resolution item 3: execTruncate's CASCADE
// expansion built its whole-catalog referencer set via im.AllTables() with no
// dbOid argument, which always scanned DefaultDBOid's namespace. On a
// connection bound to a genuinely distinct dbOid, the real referencing table
// (created under that same dbOid) was invisible to the scan, so TRUNCATE ...
// CASCADE silently failed to cascade to it — leaving its rows behind,
// dangling references to the just-truncated parent.
func TestExecTruncateCascadeFindsOwnDistinctDBOidReferencingTable(t *testing.T) {
	const otherDBOid = 7002
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE parent (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE child (id int4, pid int4 REFERENCES parent(id))"); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO parent VALUES (1)"); err != nil {
		t.Fatalf("INSERT INTO parent: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO child VALUES (1, 1)"); err != nil {
		t.Fatalf("INSERT INTO child: %v", err)
	}

	if err := runDMLUnderDBOid(t, ctx, "TRUNCATE parent CASCADE"); err != nil {
		t.Fatalf("TRUNCATE parent CASCADE: %v", err)
	}

	rows := runQueryUnderDBOid(t, ctx, "SELECT id FROM child")
	if len(rows) != 0 {
		t.Fatalf("child has %d rows after TRUNCATE parent CASCADE, want 0 (CASCADE did not reach the connection's own distinct-dbOid referencing table)", len(rows))
	}
}

// TestPgClassResolvesUnderFreshDistinctDBOid covers M0122-0007 4e's remaining
// gap: a connection to any genuinely distinct dbOid — even one that has never
// had a single object created under it, exactly like a connection to a
// freshly CREATE DATABASE'd database — used to fail to even resolve the name
// "pg_class" ("relation \"pg_class\" does not exist", 42P01), because
// registerSystemTables registers every pg_catalog/information_schema virtual
// table exactly once, under DefaultDBOid's namespace only, and CREATE
// DATABASE never seeds a fresh namespace with references to them.
// InMemory.LookupTable now falls back to DefaultDBOid's namespace for
// pg_catalog/information_schema names specifically (never for plain user
// tables, which must stay isolated per database).
func TestPgClassResolvesUnderFreshDistinctDBOid(t *testing.T) {
	const freshDBOid = 7003
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = freshDBOid // no CreateTable/CreateIndex call under this dbOid — namespace never seeded

	// Must not error with 42P01 "relation \"pg_class\" does not exist".
	_ = runQueryUnderDBOid(t, ctx, "SELECT relname FROM pg_class WHERE relname = 'pg_class'")

	// A genuinely nonexistent user table under the same fresh dbOid must
	// still correctly error — the fallback must not leak DefaultDBOid's real
	// user tables into an unrelated database's connection.
	stmts, err := parser.Parse("SELECT * FROM this_table_does_not_exist_anywhere")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	planCat := catalog.WithSearchPath(ctx.Catalog, nil)
	planCat.DBOid = ctx.CurrentDatabaseOid
	if _, err := planner.Plan(stmts[0], planCat); err == nil {
		t.Fatal("expected \"relation does not exist\" for a genuinely nonexistent table, got no error")
	}
}

// TestPgClassRowsScopedToConnectionDBOid covers M0122-0007 4e's pg_class
// VirtualRows enumeration: catalog.InMemory.PGClassRowsForDBOid must list the
// GIVEN dbOid's own tables, not always DefaultDBOid's — this is the method
// internal/server/dispatch.go's wireExtensionRows wires into
// executor.Context.PgClassRows for the real server; this test exercises it
// directly since this package has no server-side dispatch wiring of its own.
func TestPgClassRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7004
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// CREATE TABLE routes to whichever dbOid ctx.CurrentDatabaseOid names at
	// the time it runs — toggle it so the two tables land in different
	// namespaces of the SAME catalog instance.
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultRows := im.PGClassRowsForDBOid(catalog.DefaultDBOid)
	if pgClassRowsContainName(defaultRows, "only_in_other") {
		t.Fatal("DefaultDBOid's pg_class rows include only_in_other, a table created under a distinct dbOid")
	}
	if !pgClassRowsContainName(defaultRows, "only_in_default") {
		t.Fatal("DefaultDBOid's pg_class rows are missing only_in_default")
	}

	otherRows := im.PGClassRowsForDBOid(otherDBOid)
	if !pgClassRowsContainName(otherRows, "only_in_other") {
		t.Fatal("otherDBOid's pg_class rows are missing only_in_other")
	}
	if pgClassRowsContainName(otherRows, "only_in_default") {
		t.Fatal("otherDBOid's pg_class rows include only_in_default, a table created under DefaultDBOid")
	}
}

func pgClassRowsContainName(rows [][]string, relname string) bool {
	for _, r := range rows {
		if len(r) > 1 && r[1] == relname {
			return true
		}
	}
	return false
}

// TestPgTablesRowsScopedToConnectionDBOid covers M0122-0007 4e follow-up 24's
// pg_tables VirtualRows enumeration: catalog.InMemory.PGTablesRowsForDBOid
// must list the GIVEN dbOid's own tables, not always DefaultDBOid's — mirrors
// TestPgClassRowsScopedToConnectionDBOid above for the pg_tables view.
func TestPgTablesRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7005
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultRows := im.PGTablesRowsForDBOid(catalog.DefaultDBOid)
	if pgTablesRowsContainName(defaultRows, "only_in_other") {
		t.Fatal("DefaultDBOid's pg_tables rows include only_in_other, a table created under a distinct dbOid")
	}
	if !pgTablesRowsContainName(defaultRows, "only_in_default") {
		t.Fatal("DefaultDBOid's pg_tables rows are missing only_in_default")
	}

	otherRows := im.PGTablesRowsForDBOid(otherDBOid)
	if !pgTablesRowsContainName(otherRows, "only_in_other") {
		t.Fatal("otherDBOid's pg_tables rows are missing only_in_other")
	}
	if pgTablesRowsContainName(otherRows, "only_in_default") {
		t.Fatal("otherDBOid's pg_tables rows include only_in_default, a table created under DefaultDBOid")
	}
}

func pgTablesRowsContainName(rows [][]string, tablename string) bool {
	for _, r := range rows {
		if len(r) > 1 && r[1] == tablename {
			return true
		}
	}
	return false
}

// TestPgIndexesRowsScopedToConnectionDBOid covers M0122-0007 4e follow-up 24's
// pg_indexes VirtualRows enumeration: catalog.InMemory.PGIndexesRowsForDBOid
// must list the GIVEN dbOid's own indexes, not always DefaultDBOid's —
// mirrors TestPgClassRowsScopedToConnectionDBOid above for the pg_indexes view.
func TestPgIndexesRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7006
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultRows := im.PGIndexesRowsForDBOid(catalog.DefaultDBOid)
	if pgIndexesRowsContainTable(defaultRows, "only_in_other") {
		t.Fatal("DefaultDBOid's pg_indexes rows include only_in_other's index, a table created under a distinct dbOid")
	}
	if !pgIndexesRowsContainTable(defaultRows, "only_in_default") {
		t.Fatal("DefaultDBOid's pg_indexes rows are missing only_in_default's index")
	}

	otherRows := im.PGIndexesRowsForDBOid(otherDBOid)
	if !pgIndexesRowsContainTable(otherRows, "only_in_other") {
		t.Fatal("otherDBOid's pg_indexes rows are missing only_in_other's index")
	}
	if pgIndexesRowsContainTable(otherRows, "only_in_default") {
		t.Fatal("otherDBOid's pg_indexes rows include only_in_default's index, a table created under DefaultDBOid")
	}
}

func pgIndexesRowsContainTable(rows [][]string, tablename string) bool {
	for _, r := range rows {
		if len(r) > 1 && r[1] == tablename {
			return true
		}
	}
	return false
}

// TestPgConstraintRowsScopedToConnectionDBOid covers M0122-0007 4e follow-up
// 25's pg_constraint VirtualRows enumeration:
// catalog.InMemory.PGConstraintRowsForDBOid must list the GIVEN dbOid's own
// tables'/indexes' constraints, not always DefaultDBOid's — mirrors
// TestPgIndexesRowsScopedToConnectionDBOid above for the pg_constraint table.
func TestPgConstraintRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7007
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultRows := im.PGConstraintRowsForDBOid(catalog.DefaultDBOid)
	if pgConstraintRowsContainName(defaultRows, "only_in_other_pkey") {
		t.Fatal("DefaultDBOid's pg_constraint rows include only_in_other_pkey, a constraint created under a distinct dbOid")
	}
	if !pgConstraintRowsContainName(defaultRows, "only_in_default_pkey") {
		t.Fatal("DefaultDBOid's pg_constraint rows are missing only_in_default_pkey")
	}

	otherRows := im.PGConstraintRowsForDBOid(otherDBOid)
	if !pgConstraintRowsContainName(otherRows, "only_in_other_pkey") {
		t.Fatal("otherDBOid's pg_constraint rows are missing only_in_other_pkey")
	}
	if pgConstraintRowsContainName(otherRows, "only_in_default_pkey") {
		t.Fatal("otherDBOid's pg_constraint rows include only_in_default_pkey, a constraint created under DefaultDBOid")
	}
}

func pgConstraintRowsContainName(rows [][]string, conname string) bool {
	for _, r := range rows {
		if len(r) > 1 && r[1] == conname {
			return true
		}
	}
	return false
}

// TestPgIndexRowsScopedToConnectionDBOid covers M0122-0007 4e follow-up 26's
// pg_index VirtualRows enumeration: catalog.InMemory.PGIndexRowsForDBOid must
// list the GIVEN dbOid's own indexes, not always DefaultDBOid's — mirrors
// TestPgConstraintRowsScopedToConnectionDBOid above for the pg_index catalog
// table.
func TestPgIndexRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7008
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultTbl, ok := im.LookupTable(parser.ObjectName{Name: "only_in_default"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("LookupTable(only_in_default, DefaultDBOid) not found")
	}
	otherTbl, ok := im.LookupTable(parser.ObjectName{Name: "only_in_other"}, otherDBOid)
	if !ok {
		t.Fatal("LookupTable(only_in_other, otherDBOid) not found")
	}

	defaultRows := im.PGIndexRowsForDBOid(catalog.DefaultDBOid)
	if pgIndexRowsContainIndrelid(defaultRows, otherTbl.OID) {
		t.Fatal("DefaultDBOid's pg_index rows include only_in_other's index, a table created under a distinct dbOid")
	}
	if !pgIndexRowsContainIndrelid(defaultRows, defaultTbl.OID) {
		t.Fatal("DefaultDBOid's pg_index rows are missing only_in_default's index")
	}

	otherRows := im.PGIndexRowsForDBOid(otherDBOid)
	if !pgIndexRowsContainIndrelid(otherRows, otherTbl.OID) {
		t.Fatal("otherDBOid's pg_index rows are missing only_in_other's index")
	}
	if pgIndexRowsContainIndrelid(otherRows, defaultTbl.OID) {
		t.Fatal("otherDBOid's pg_index rows include only_in_default's index, a table created under DefaultDBOid")
	}
}

func pgIndexRowsContainIndrelid(rows [][]string, tableOID uint32) bool {
	want := fmt.Sprintf("%d", tableOID)
	for _, r := range rows {
		if len(r) > 1 && r[1] == want {
			return true
		}
	}
	return false
}

// TestPgAttrdefRowsScopedToConnectionDBOid covers M0122-0007 4e follow-up 27's
// pg_attrdef VirtualRows enumeration: catalog.InMemory.PGAttrdefRowsForDBOid
// must list the GIVEN dbOid's own tables' column defaults, not always
// DefaultDBOid's — mirrors TestPgIndexRowsScopedToConnectionDBOid above for
// the pg_attrdef catalog table.
func TestPgAttrdefRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7009
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default (id int4 PRIMARY KEY, val int4 DEFAULT 1)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other (id int4 PRIMARY KEY, val int4 DEFAULT 2)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultTbl, ok := im.LookupTable(parser.ObjectName{Name: "only_in_default"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("LookupTable(only_in_default, DefaultDBOid) not found")
	}
	otherTbl, ok := im.LookupTable(parser.ObjectName{Name: "only_in_other"}, otherDBOid)
	if !ok {
		t.Fatal("LookupTable(only_in_other, otherDBOid) not found")
	}

	defaultRows := im.PGAttrdefRowsForDBOid(catalog.DefaultDBOid)
	if pgIndexRowsContainIndrelid(defaultRows, otherTbl.OID) {
		t.Fatal("DefaultDBOid's pg_attrdef rows include only_in_other's default, a table created under a distinct dbOid")
	}
	if !pgIndexRowsContainIndrelid(defaultRows, defaultTbl.OID) {
		t.Fatal("DefaultDBOid's pg_attrdef rows are missing only_in_default's default")
	}

	otherRows := im.PGAttrdefRowsForDBOid(otherDBOid)
	if !pgIndexRowsContainIndrelid(otherRows, otherTbl.OID) {
		t.Fatal("otherDBOid's pg_attrdef rows are missing only_in_other's default")
	}
	if pgIndexRowsContainIndrelid(otherRows, defaultTbl.OID) {
		t.Fatal("otherDBOid's pg_attrdef rows include only_in_default's default, a table created under DefaultDBOid")
	}
}

// TestPgDependRowsScopedToConnectionDBOid covers M0122-0007 4e follow-up 27's
// pg_depend VirtualRows enumeration: catalog.InMemory.PGDependRowsForDBOid
// must list the GIVEN dbOid's own attrdef->sequence dependency rows, not
// always DefaultDBOid's. A SERIAL column registers a NORMAL ('n') pg_depend
// row from its pg_attrdef entry (classid=2604) to the owned sequence's own
// OID (refobjid) via attrDefRowsLockedForDBOid(dbOid) — this is the row class
// this loop's dbOid-scoping actually reaches (unlike the deptype='a'
// OWNED-BY row, which still resolves through the global,
// not-yet-dbOid-threaded SequenceParamsFunc registry — a separate,
// already-flagged "sequence ownership follow-on" gap, not fixed here; see
// the deferral ledger). Mirrors TestPgAttrdefRowsScopedToConnectionDBOid
// above for the pg_depend catalog table.
func TestPgDependRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7010
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default (id serial PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other (id serial PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultSeq, ok := im.LookupTable(parser.ObjectName{Name: "only_in_default_id_seq"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("LookupTable(only_in_default_id_seq, DefaultDBOid) not found")
	}
	otherSeq, ok := im.LookupTable(parser.ObjectName{Name: "only_in_other_id_seq"}, otherDBOid)
	if !ok {
		t.Fatal("LookupTable(only_in_other_id_seq, otherDBOid) not found")
	}

	defaultRows := im.PGDependRowsForDBOid(catalog.DefaultDBOid)
	if !pgDependRowsContainAttrdefToSeq(defaultRows, defaultSeq.OID) {
		t.Fatal("DefaultDBOid's pg_depend rows are missing only_in_default's attrdef->sequence row")
	}
	if pgDependRowsContainAttrdefToSeq(defaultRows, otherSeq.OID) {
		t.Fatal("DefaultDBOid's pg_depend rows include only_in_other's attrdef->sequence row, a table created under a distinct dbOid")
	}

	otherRows := im.PGDependRowsForDBOid(otherDBOid)
	if !pgDependRowsContainAttrdefToSeq(otherRows, otherSeq.OID) {
		t.Fatal("otherDBOid's pg_depend rows are missing only_in_other's attrdef->sequence row")
	}
	if pgDependRowsContainAttrdefToSeq(otherRows, defaultSeq.OID) {
		t.Fatal("otherDBOid's pg_depend rows include only_in_default's attrdef->sequence row, a table created under DefaultDBOid")
	}
}

// pgDependRowsContainAttrdefToSeq reports whether rows contains a
// classid=2604 (pg_attrdef) / deptype='n' row whose refobjid (index 4) is
// seqOID — the attrdef->sequence link a SERIAL column's default registers.
func pgDependRowsContainAttrdefToSeq(rows [][]string, seqOID uint32) bool {
	want := fmt.Sprintf("%d", seqOID)
	for _, r := range rows {
		if len(r) > 6 && r[0] == "2604" && r[6] == "n" && r[4] == want {
			return true
		}
	}
	return false
}

// TestPgInheritsRowsScopedToConnectionDBOid mirrors
// TestPgDependRowsScopedToConnectionDBOid above for the pg_inherits catalog
// table (M0122-0007 4e follow-up 28): a partition parent/child pair created
// under one dbOid must not leak into another dbOid's pg_inherits rows.
func TestPgInheritsRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7011
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default (id int) PARTITION BY RANGE(id)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE only_in_default_p1 PARTITION OF only_in_default FOR VALUES FROM (1) TO (100)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_default_p1: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other (id int) PARTITION BY RANGE(id)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE only_in_other_p1 PARTITION OF only_in_other FOR VALUES FROM (1) TO (100)"); err != nil {
		t.Fatalf("CREATE TABLE only_in_other_p1: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultChild, ok := im.LookupTable(parser.ObjectName{Name: "only_in_default_p1"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("LookupTable(only_in_default_p1, DefaultDBOid) not found")
	}
	otherChild, ok := im.LookupTable(parser.ObjectName{Name: "only_in_other_p1"}, otherDBOid)
	if !ok {
		t.Fatal("LookupTable(only_in_other_p1, otherDBOid) not found")
	}

	defaultRows := im.PGInheritsRowsForDBOid(catalog.DefaultDBOid)
	if !pgInheritsRowsContainInhrelid(defaultRows, defaultChild.OID) {
		t.Fatal("DefaultDBOid's pg_inherits rows are missing only_in_default_p1's parent-child row")
	}
	if pgInheritsRowsContainInhrelid(defaultRows, otherChild.OID) {
		t.Fatal("DefaultDBOid's pg_inherits rows include only_in_other_p1's parent-child row, a table created under a distinct dbOid")
	}

	otherRows := im.PGInheritsRowsForDBOid(otherDBOid)
	if !pgInheritsRowsContainInhrelid(otherRows, otherChild.OID) {
		t.Fatal("otherDBOid's pg_inherits rows are missing only_in_other_p1's parent-child row")
	}
	if pgInheritsRowsContainInhrelid(otherRows, defaultChild.OID) {
		t.Fatal("otherDBOid's pg_inherits rows include only_in_default_p1's parent-child row, a table created under DefaultDBOid")
	}
}

func pgInheritsRowsContainInhrelid(rows [][]string, childOID uint32) bool {
	want := fmt.Sprintf("%d", childOID)
	for _, r := range rows {
		if len(r) > 0 && r[0] == want {
			return true
		}
	}
	return false
}
