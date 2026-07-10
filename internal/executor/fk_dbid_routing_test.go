package executor

import (
	"fmt"
	"strconv"
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

// TestPgPolicyRowsScopedToConnectionDBOid mirrors
// TestPgInheritsRowsScopedToConnectionDBOid above for the pg_policy catalog
// table (M0122-0007 4e follow-up 29): a row-security policy created under one
// dbOid must not leak into another dbOid's pg_policy rows.
func TestPgPolicyRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7012
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE t_default (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t_default: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE POLICY pol_default ON t_default USING (a > 0)"); err != nil {
		t.Fatalf("CREATE POLICY pol_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE t_other (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t_other: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE POLICY pol_other ON t_other USING (a > 0)"); err != nil {
		t.Fatalf("CREATE POLICY pol_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultRows := im.PGPolicyRowsForDBOid(catalog.DefaultDBOid)
	if !pgPolicyRowsContainPolname(defaultRows, "pol_default") {
		t.Fatal("DefaultDBOid's pg_policy rows are missing pol_default")
	}
	if pgPolicyRowsContainPolname(defaultRows, "pol_other") {
		t.Fatal("DefaultDBOid's pg_policy rows include pol_other, a policy created under a distinct dbOid")
	}

	otherRows := im.PGPolicyRowsForDBOid(otherDBOid)
	if !pgPolicyRowsContainPolname(otherRows, "pol_other") {
		t.Fatal("otherDBOid's pg_policy rows are missing pol_other")
	}
	if pgPolicyRowsContainPolname(otherRows, "pol_default") {
		t.Fatal("otherDBOid's pg_policy rows include pol_default, a policy created under DefaultDBOid")
	}
}

func pgPolicyRowsContainPolname(rows [][]string, polname string) bool {
	for _, r := range rows {
		if len(r) > 1 && r[1] == polname {
			return true
		}
	}
	return false
}

// TestPgTriggerRowsScopedToConnectionDBOid mirrors
// TestPgPolicyRowsScopedToConnectionDBOid above for the pg_trigger catalog
// table (M0122-0007 4e follow-up 30): a trigger created under one dbOid must
// not leak into another dbOid's pg_trigger rows.
func TestPgTriggerRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7013
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE t_default (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t_default: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE FUNCTION trg_default_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$"); err != nil {
		t.Fatalf("CREATE FUNCTION trg_default_fn: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TRIGGER trg_default BEFORE INSERT ON t_default FOR EACH ROW EXECUTE FUNCTION trg_default_fn()"); err != nil {
		t.Fatalf("CREATE TRIGGER trg_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE t_other (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t_other: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE FUNCTION trg_other_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$"); err != nil {
		t.Fatalf("CREATE FUNCTION trg_other_fn: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TRIGGER trg_other BEFORE INSERT ON t_other FOR EACH ROW EXECUTE FUNCTION trg_other_fn()"); err != nil {
		t.Fatalf("CREATE TRIGGER trg_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultRows := im.PGTriggerRowsForDBOid(catalog.DefaultDBOid)
	if !pgTriggerRowsContainTgname(defaultRows, "trg_default") {
		t.Fatal("DefaultDBOid's pg_trigger rows are missing trg_default")
	}
	if pgTriggerRowsContainTgname(defaultRows, "trg_other") {
		t.Fatal("DefaultDBOid's pg_trigger rows include trg_other, a trigger created under a distinct dbOid")
	}

	otherRows := im.PGTriggerRowsForDBOid(otherDBOid)
	if !pgTriggerRowsContainTgname(otherRows, "trg_other") {
		t.Fatal("otherDBOid's pg_trigger rows are missing trg_other")
	}
	if pgTriggerRowsContainTgname(otherRows, "trg_default") {
		t.Fatal("otherDBOid's pg_trigger rows include trg_default, a trigger created under DefaultDBOid")
	}
}

func pgTriggerRowsContainTgname(rows [][]string, tgname string) bool {
	for _, r := range rows {
		if len(r) > 3 && r[3] == tgname {
			return true
		}
	}
	return false
}

// TestPgRewriteRowsScopedToConnectionDBOid mirrors
// TestPgTriggerRowsScopedToConnectionDBOid above for the pg_rewrite catalog
// table (M0122-0007 4e follow-up 31): a CREATE RULE DO-NOTHING rule created
// under one dbOid must not leak into another dbOid's pg_rewrite rows.
func TestPgRewriteRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7014
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE t_default (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t_default: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE RULE rule_default AS ON INSERT TO t_default DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE rule_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE t_other (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t_other: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE RULE rule_other AS ON INSERT TO t_other DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("CREATE RULE rule_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	defaultRows := im.PGRewriteRowsForDBOid(catalog.DefaultDBOid)
	if !pgRewriteRowsContainRulename(defaultRows, "rule_default") {
		t.Fatal("DefaultDBOid's pg_rewrite rows are missing rule_default")
	}
	if pgRewriteRowsContainRulename(defaultRows, "rule_other") {
		t.Fatal("DefaultDBOid's pg_rewrite rows include rule_other, a rule created under a distinct dbOid")
	}

	otherRows := im.PGRewriteRowsForDBOid(otherDBOid)
	if !pgRewriteRowsContainRulename(otherRows, "rule_other") {
		t.Fatal("otherDBOid's pg_rewrite rows are missing rule_other")
	}
	if pgRewriteRowsContainRulename(otherRows, "rule_default") {
		t.Fatal("otherDBOid's pg_rewrite rows include rule_default, a rule created under DefaultDBOid")
	}
}

func pgRewriteRowsContainRulename(rows [][]string, rulename string) bool {
	for _, r := range rows {
		if len(r) > 1 && r[1] == rulename {
			return true
		}
	}
	return false
}

// TestPgForeignTableRowsScopedToConnectionDBOid mirrors
// TestPgRewriteRowsScopedToConnectionDBOid above for the pg_foreign_table
// catalog table (M0122-0007 4e follow-up 32): a foreign table created under
// one dbOid must not leak into another dbOid's pg_foreign_table rows.
func TestPgForeignTableRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7015
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE SERVER srv_default FOREIGN DATA WRAPPER goopg_fdw"); err != nil {
		t.Fatalf("CREATE SERVER srv_default: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE FOREIGN TABLE ft_default (a int) SERVER srv_default"); err != nil {
		t.Fatalf("CREATE FOREIGN TABLE ft_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE SERVER srv_other FOREIGN DATA WRAPPER goopg_fdw"); err != nil {
		t.Fatalf("CREATE SERVER srv_other: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE FOREIGN TABLE ft_other (a int) SERVER srv_other"); err != nil {
		t.Fatalf("CREATE FOREIGN TABLE ft_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}

	ftDefault, ok := im.LookupTable(parser.ObjectName{Name: "ft_default"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("ft_default not found under DefaultDBOid")
	}
	ftOther, ok := im.LookupTable(parser.ObjectName{Name: "ft_other"}, otherDBOid)
	if !ok {
		t.Fatal("ft_other not found under otherDBOid")
	}

	defaultRows := im.PGForeignTableRowsForDBOid(catalog.DefaultDBOid)
	if !pgForeignTableRowsContainFtrelid(defaultRows, ftDefault.OID) {
		t.Fatal("DefaultDBOid's pg_foreign_table rows are missing ft_default")
	}
	if pgForeignTableRowsContainFtrelid(defaultRows, ftOther.OID) {
		t.Fatal("DefaultDBOid's pg_foreign_table rows include ft_other, a foreign table created under a distinct dbOid")
	}

	otherRows := im.PGForeignTableRowsForDBOid(otherDBOid)
	if !pgForeignTableRowsContainFtrelid(otherRows, ftOther.OID) {
		t.Fatal("otherDBOid's pg_foreign_table rows are missing ft_other")
	}
	if pgForeignTableRowsContainFtrelid(otherRows, ftDefault.OID) {
		t.Fatal("otherDBOid's pg_foreign_table rows include ft_default, a foreign table created under DefaultDBOid")
	}
}

func pgForeignTableRowsContainFtrelid(rows [][]string, oid uint32) bool {
	want := strconv.FormatUint(uint64(oid), 10)
	for _, r := range rows {
		if len(r) > 0 && r[0] == want {
			return true
		}
	}
	return false
}

// TestRegclassCastScopedToConnectionDBOid covers the oid::regclass /
// 'name'::regclass cast (both in internal/executor/expr.go's CastExpr arm),
// which previously resolved every lookup against DefaultDBOid regardless of
// the connection's actual dbOid (M0122-0007 4e follow-up 33): a connection to
// a distinct CREATE DATABASE'd dbOid could not resolve its own tables'
// <oid>::regclass to a name (rendered the bare numeric OID instead), and
// 'name'::regclass couldn't find that table's OID at all. This mirrors the 10
// prior VirtualRows-closure follow-ups (pg_class, pg_index, pg_trigger, …)
// but fixes a cast/output-function code path instead of a virtual table.
func TestRegclassCastScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7017
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE t_default (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE t_other (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	tDefault, ok := im.LookupTable(parser.ObjectName{Name: "t_default"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("t_default not found under DefaultDBOid")
	}
	tOther, ok := im.LookupTable(parser.ObjectName{Name: "t_other"}, otherDBOid)
	if !ok {
		t.Fatal("t_other not found under otherDBOid")
	}

	// oid::regclass, from otherDBOid's own connection: its own table's OID
	// resolves to its own name...
	ctx.CurrentDatabaseOid = otherDBOid
	rows := runQueryUnderDBOid(t, ctx, fmt.Sprintf("SELECT %d::regclass::text", tOther.OID))
	if got := rows[0][0].StringValue(); got != "t_other" {
		t.Errorf("otherDBOid: %d::regclass::text = %q, want %q", tOther.OID, got, "t_other")
	}
	// ...but t_default's OID (owned by DefaultDBOid) must NOT resolve to
	// "t_default" from this connection — that would be a cross-dbOid leak.
	rows = runQueryUnderDBOid(t, ctx, fmt.Sprintf("SELECT %d::regclass::text", tDefault.OID))
	if got := rows[0][0].StringValue(); got == "t_default" {
		t.Errorf("otherDBOid: %d::regclass::text resolved to %q, a table owned by a different database (cross-dbOid leak)", tDefault.OID, got)
	}

	// oid::regclass, from DefaultDBOid's own connection: the reverse must
	// also hold (this is the pre-existing, always-worked direction — pins it
	// against a regression from this loop's dbOid-scoping change).
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	rows = runQueryUnderDBOid(t, ctx, fmt.Sprintf("SELECT %d::regclass::text", tDefault.OID))
	if got := rows[0][0].StringValue(); got != "t_default" {
		t.Errorf("DefaultDBOid: %d::regclass::text = %q, want %q", tDefault.OID, got, "t_default")
	}
	rows = runQueryUnderDBOid(t, ctx, fmt.Sprintf("SELECT %d::regclass::text", tOther.OID))
	if got := rows[0][0].StringValue(); got == "t_other" {
		t.Errorf("DefaultDBOid: %d::regclass::text resolved to %q, a table owned by a different database (cross-dbOid leak)", tOther.OID, got)
	}

	// 'name'::regclass (string→OID direction): two IDENTICALLY-named tables in
	// the two databases (the realistic collision scenario — a bare table-name
	// mismatch can't leak by construction, since a lookup miss falls back to
	// an unresolved literal rather than another database's OID) each resolve
	// to their OWN database's OID, not the other's.
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE TABLE shared_name (a int)"); err != nil {
		t.Fatalf("CREATE TABLE shared_name (default): %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE shared_name (a int)"); err != nil {
		t.Fatalf("CREATE TABLE shared_name (other): %v", err)
	}
	sharedDefault, ok := im.LookupTable(parser.ObjectName{Name: "shared_name"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("shared_name not found under DefaultDBOid")
	}
	sharedOther, ok := im.LookupTable(parser.ObjectName{Name: "shared_name"}, otherDBOid)
	if !ok {
		t.Fatal("shared_name not found under otherDBOid")
	}
	if sharedDefault.OID == sharedOther.OID {
		t.Fatalf("test setup: shared_name got the same OID (%d) in both databases", sharedDefault.OID)
	}

	ctx.CurrentDatabaseOid = otherDBOid
	rows = runQueryUnderDBOid(t, ctx, "SELECT 'shared_name'::regclass::oid")
	if got := uint32(rows[0][0].Int); got != sharedOther.OID {
		t.Errorf("otherDBOid: 'shared_name'::regclass::oid = %d, want its own %d (got DefaultDBOid's %d instead?)", got, sharedOther.OID, sharedDefault.OID)
	}

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	rows = runQueryUnderDBOid(t, ctx, "SELECT 'shared_name'::regclass::oid")
	if got := uint32(rows[0][0].Int); got != sharedDefault.OID {
		t.Errorf("DefaultDBOid: 'shared_name'::regclass::oid = %d, want its own %d (got otherDBOid's %d instead?)", got, sharedDefault.OID, sharedOther.OID)
	}
}

// TestPgSequenceRowsScopedToConnectionDBOid mirrors
// TestPgForeignTableRowsScopedToConnectionDBOid above for the pg_sequence
// catalog table (M0122-0007 4e follow-up 34): a sequence created under one
// dbOid must not leak into another dbOid's pg_sequence rows, and each
// database's SequenceParamsFunc lookup (start/increment/etc.) must resolve
// against its OWN seqRegistry entry rather than always DefaultDBOid's.
func TestPgSequenceRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7018
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE SEQUENCE seq_default START 10 INCREMENT 2"); err != nil {
		t.Fatalf("CREATE SEQUENCE seq_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE SEQUENCE seq_other START 100 INCREMENT 5"); err != nil {
		t.Fatalf("CREATE SEQUENCE seq_other: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	seqDefault, ok := im.LookupTable(parser.ObjectName{Name: "seq_default"}, catalog.DefaultDBOid)
	if !ok {
		t.Fatal("seq_default not found under DefaultDBOid")
	}
	seqOther, ok := im.LookupTable(parser.ObjectName{Name: "seq_other"}, otherDBOid)
	if !ok {
		t.Fatal("seq_other not found under otherDBOid")
	}

	defaultRows := im.PGSequenceRowsForDBOid(catalog.DefaultDBOid)
	defaultRow, ok := pgSequenceRowByRelid(defaultRows, seqDefault.OID)
	if !ok {
		t.Fatal("DefaultDBOid's pg_sequence rows are missing seq_default")
	}
	if defaultRow[2] != "10" {
		t.Errorf("DefaultDBOid's seq_default seqstart = %q, want %q", defaultRow[2], "10")
	}
	if _, ok := pgSequenceRowByRelid(defaultRows, seqOther.OID); ok {
		t.Fatal("DefaultDBOid's pg_sequence rows include seq_other, a sequence created under a distinct dbOid")
	}

	otherRows := im.PGSequenceRowsForDBOid(otherDBOid)
	otherRow, ok := pgSequenceRowByRelid(otherRows, seqOther.OID)
	if !ok {
		t.Fatal("otherDBOid's pg_sequence rows are missing seq_other")
	}
	if otherRow[2] != "100" {
		t.Errorf("otherDBOid's seq_other seqstart = %q, want %q", otherRow[2], "100")
	}
	if _, ok := pgSequenceRowByRelid(otherRows, seqDefault.OID); ok {
		t.Fatal("otherDBOid's pg_sequence rows include seq_default, a sequence created under DefaultDBOid")
	}
}

func pgSequenceRowByRelid(rows [][]string, oid uint32) ([]string, bool) {
	want := strconv.FormatUint(uint64(oid), 10)
	for _, r := range rows {
		if len(r) > 0 && r[0] == want {
			return r, true
		}
	}
	return nil, false
}

// TestPGSequencesAndInfoSchemaSequencesRowsScopedToConnectionDBOid mirrors
// TestPgSequenceRowsScopedToConnectionDBOid above for the two sibling views
// follow-up 34 flagged as deferred: pg_catalog.pg_sequences and
// information_schema.sequences (both read straight from AllSequenceInfos, no
// catalog.InMemory indirection). A sequence created under one dbOid must not
// leak into another dbOid's rows for either view. M0122-0007 4e follow-up 35.
func TestPGSequencesAndInfoSchemaSequencesRowsScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7019
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, "CREATE SEQUENCE seqs_default START 10 INCREMENT 2"); err != nil {
		t.Fatalf("CREATE SEQUENCE seqs_default: %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE SEQUENCE seqs_other START 100 INCREMENT 5"); err != nil {
		t.Fatalf("CREATE SEQUENCE seqs_other: %v", err)
	}

	defaultPGRows := PGSequencesRows(catalog.DefaultDBOid)
	if !sequenceRowsContainName(defaultPGRows, 1, "seqs_default") {
		t.Fatal("DefaultDBOid's pg_sequences rows are missing seqs_default")
	}
	if sequenceRowsContainName(defaultPGRows, 1, "seqs_other") {
		t.Fatal("DefaultDBOid's pg_sequences rows include seqs_other, a sequence created under a distinct dbOid")
	}
	otherPGRows := PGSequencesRows(otherDBOid)
	if !sequenceRowsContainName(otherPGRows, 1, "seqs_other") {
		t.Fatal("otherDBOid's pg_sequences rows are missing seqs_other")
	}
	if sequenceRowsContainName(otherPGRows, 1, "seqs_default") {
		t.Fatal("otherDBOid's pg_sequences rows include seqs_default, a sequence created under DefaultDBOid")
	}

	defaultInfoRows := InformationSchemaSequencesRows(catalog.DefaultDBOid)
	if !sequenceRowsContainName(defaultInfoRows, 2, "seqs_default") {
		t.Fatal("DefaultDBOid's information_schema.sequences rows are missing seqs_default")
	}
	if sequenceRowsContainName(defaultInfoRows, 2, "seqs_other") {
		t.Fatal("DefaultDBOid's information_schema.sequences rows include seqs_other, a sequence created under a distinct dbOid")
	}
	otherInfoRows := InformationSchemaSequencesRows(otherDBOid)
	if !sequenceRowsContainName(otherInfoRows, 2, "seqs_other") {
		t.Fatal("otherDBOid's information_schema.sequences rows are missing seqs_other")
	}
	if sequenceRowsContainName(otherInfoRows, 2, "seqs_default") {
		t.Fatal("otherDBOid's information_schema.sequences rows include seqs_default, a sequence created under DefaultDBOid")
	}
}

func sequenceRowsContainName(rows [][]string, nameCol int, name string) bool {
	for _, r := range rows {
		if len(r) > nameCol && r[nameCol] == name {
			return true
		}
	}
	return false
}
