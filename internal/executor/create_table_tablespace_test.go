package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCreateTableTablespaceResolvesAndStores pins the M0122-0007 fix: CREATE
// TABLE ... TABLESPACE name now resolves the name against the tablespace
// registry and stores the OID on catalog.Table.Tablespace (rendered as
// pg_class.reltablespace), instead of silently discarding the clause.
func TestCreateTableTablespaceResolvesAndStores(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	wantOID, ok := ctx.Catalog.LookupTablespaceOID("ts1")
	if !ok {
		t.Fatalf("LookupTablespaceOID(ts1): not found")
	}

	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int) TABLESPACE ts1"); err != nil {
		t.Fatalf("CREATE TABLE ... TABLESPACE ts1: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "t1"})
	if !ok {
		t.Fatalf("table t1 not found after CREATE")
	}
	if tbl.Tablespace != wantOID {
		t.Errorf("t1.Tablespace = %d, want %d", tbl.Tablespace, wantOID)
	}
}

// TestCreateTableTablespaceDefaultNormalizesToZero mirrors PG's own
// convention (heap_create): an explicit `TABLESPACE pg_default` — same as the
// implicit default — normalizes to reltablespace=0, not pg_default's own OID
// (1663).
func TestCreateTableTablespaceDefaultNormalizesToZero(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int) TABLESPACE pg_default"); err != nil {
		t.Fatalf("CREATE TABLE ... TABLESPACE pg_default: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "t1"})
	if !ok {
		t.Fatalf("table t1 not found after CREATE")
	}
	if tbl.Tablespace != 0 {
		t.Errorf("t1.Tablespace = %d, want 0 (pg_default normalizes to InvalidOid)", tbl.Tablespace)
	}
}

// TestCreateTableTablespaceUnknownErrors42704 mirrors get_tablespace_oid's
// missing_ok=false error (tablespace.c:1420).
func TestCreateTableTablespaceUnknownErrors42704(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, "CREATE TABLE t1 (a int) TABLESPACE nope")
	if got := execErrCode(err); got != "42704" {
		t.Fatalf("unknown tablespace: want 42704, got %q (%v)", got, err)
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "t1"}); ok {
		t.Fatalf("t1 should not exist after a rejected CREATE TABLE")
	}
}

// TestCreateTableTablespacePgGlobalErrors22023 mirrors tablecmds.c's "In all
// cases disallow placing user relations in pg_global" check.
func TestCreateTableTablespacePgGlobalErrors22023(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, "CREATE TABLE t1 (a int) TABLESPACE pg_global")
	if got := execErrCode(err); got != "22023" {
		t.Fatalf("pg_global: want 22023, got %q (%v)", got, err)
	}
}

// TestCreateTablePartitionOfChildTablespace covers the PARTITION OF child
// parse/exec site (a separate code path from the main CREATE TABLE body).
func TestCreateTablePartitionOfChildTablespace(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	wantOID, _ := ctx.Catalog.LookupTablespaceOID("ts1")
	if err := runDDL(t, ctx, "CREATE TABLE parent (a int) PARTITION BY RANGE (a)"); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE child PARTITION OF parent FOR VALUES FROM (1) TO (10) TABLESPACE ts1"); err != nil {
		t.Fatalf("CREATE TABLE child PARTITION OF ... TABLESPACE ts1: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "child"})
	if !ok {
		t.Fatalf("child partition not found after CREATE")
	}
	if tbl.Tablespace != wantOID {
		t.Errorf("child.Tablespace = %d, want %d", tbl.Tablespace, wantOID)
	}
}

// TestPGClassRendersReltablespace confirms buildUserPGClassRow (the pg_class
// heap-row builder used both by the catalog-heap sync and the live virtual
// query path) surfaces the stored Tablespace instead of a hardcoded 0.
func TestPGClassRendersReltablespace(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	wantOID, _ := ctx.Catalog.LookupTablespaceOID("ts1")
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int) TABLESPACE ts1"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "t1"})
	if !ok {
		t.Fatalf("t1 not found")
	}
	row := buildUserPGClassRow(ctx.Catalog, tbl)
	const reltablespaceOrdinal = 8
	got := row[reltablespaceOrdinal].Int
	if got != int64(wantOID) {
		t.Errorf("pg_class.reltablespace = %d, want %d", got, wantOID)
	}
}
