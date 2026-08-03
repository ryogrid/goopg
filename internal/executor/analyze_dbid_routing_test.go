package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0125-0028 pins: ANALYZE (and VACUUM's named-target twin) must resolve
// relation names in the CONNECTION's database, the way every SELECT does via
// ctxPlanCatalog. Before this fix expandAnalyzeTargets called a raw
// ctx.Catalog.LookupTable — always DefaultDBOid's namespace — so in any
// non-default database `ANALYZE <table>` raised 42P01 for a table SELECT could
// read (measured in db tpch 2026-07-27, ledger `bench-reorg ANALYZE-scope`),
// and `VACUUM <table>` silently skipped its target. relationStillExists had
// the same pin (LookupTableByOID keys off DefaultDBOid), which would have
// turned every per-DB target into a silent "concurrently dropped" skip right
// after resolution was fixed.
//
// The bare `ANALYZE;` form is pinned separately below: upstream analyzes every
// relation in the CURRENT database (get_all_vacuum_rels), and until this task
// the form was a silent no-op returning nil targets.

// TestAnalyzeNamedTargetResolvesConnectionDBOid: ANALYZE <name> on a
// distinct-dbOid connection finds the table and publishes stats onto the live
// per-DB catalog object.
func TestAnalyzeNamedTargetResolvesConnectionDBOid(t *testing.T) {
	const otherDBOid = 6161
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4, label text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO widgets VALUES (1,'a'),(2,'b'),(3,'b')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// ANALYZE samples under a FRESH snapshot; commit the seeding tx so the
	// rows are visible to it (same reason as TestAnalyzeRelationPopulatesStats).
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := runDMLUnderDBOid(t, ctx, "ANALYZE widgets"); err != nil {
		t.Fatalf("ANALYZE of a table in the connection's own database: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "widgets"}, otherDBOid)
	if !ok {
		t.Fatal("fixture: widgets not found under otherDBOid")
	}
	if tbl.Stats == nil {
		t.Fatal("ANALYZE succeeded but published no stats onto the live per-DB table")
	}
	if tbl.Stats.RowCount != 3 {
		t.Fatalf("Stats.RowCount=%d want 3", tbl.Stats.RowCount)
	}
	if !tbl.Stats.Analyzed {
		t.Fatal("Stats.Analyzed not set")
	}
}

// TestBareAnalyzeCoversCurrentDatabaseOnly: `ANALYZE;` analyzes every
// relation of the CURRENT database — and nothing outside it.
func TestBareAnalyzeCoversCurrentDatabaseOnly(t *testing.T) {
	const otherDBOid = 6262
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// One table in the DEFAULT database…
	if err := runDDL(t, ctx, "CREATE TABLE default_db_tbl (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE (default db): %v", err)
	}
	// …and two in a genuinely distinct database.
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4, label text)"); err != nil {
		t.Fatalf("CREATE TABLE widgets: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE gadgets (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE gadgets: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO widgets VALUES (1,'a'),(2,'b')"); err != nil {
		t.Fatalf("INSERT widgets: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO gadgets VALUES (10)"); err != nil {
		t.Fatalf("INSERT gadgets: %v", err)
	}
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := runDMLUnderDBOid(t, ctx, "ANALYZE"); err != nil {
		t.Fatalf("bare ANALYZE: %v", err)
	}

	widgets, _ := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "widgets"}, otherDBOid)
	if widgets == nil || widgets.Stats == nil || widgets.Stats.RowCount != 2 {
		t.Fatalf("bare ANALYZE did not populate widgets stats: %+v", statsOrNil(widgets))
	}
	gadgets, _ := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "gadgets"}, otherDBOid)
	if gadgets == nil || gadgets.Stats == nil || gadgets.Stats.RowCount != 1 {
		t.Fatalf("bare ANALYZE did not populate gadgets stats: %+v", statsOrNil(gadgets))
	}
	// The other database's table must be untouched: bare ANALYZE is
	// current-database-scoped, not cluster-wide.
	defTbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "default_db_tbl"})
	if defTbl == nil {
		t.Fatal("fixture: default_db_tbl not found under DefaultDBOid")
	}
	if defTbl.Stats != nil {
		t.Fatalf("bare ANALYZE in another database analyzed DefaultDBOid's table: %+v", defTbl.Stats)
	}
}

// TestVacuumNamedTargetResolvesConnectionDBOid: the VACUUM twin —
// `VACUUM <name>` on a distinct-dbOid connection processes the table
// (vac_update_relstats publishes reltuples) instead of silently skipping it.
func TestVacuumNamedTargetResolvesConnectionDBOid(t *testing.T) {
	const otherDBOid = 6363
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE widgets (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO widgets VALUES (1),(2)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := runDMLUnderDBOid(t, ctx, "VACUUM widgets"); err != nil {
		t.Fatalf("VACUUM of a table in the connection's own database: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "widgets"}, otherDBOid)
	if !ok {
		t.Fatal("fixture: widgets not found under otherDBOid")
	}
	if tbl.Stats == nil || tbl.Stats.RowCount != 2 {
		t.Fatalf("VACUUM did not publish reltuples onto the live per-DB table: %+v", statsOrNil(tbl))
	}
}

// statsOrNil renders a table's Stats for failure messages without
// nil-dereferencing when the lookup itself came back empty.
func statsOrNil(tbl *catalog.Table) any {
	if tbl == nil {
		return "<table not found>"
	}
	return tbl.Stats
}
