package executor

import (
	"github.com/goopg/goopg/internal/parser"
	"testing"
)

// TestCreateIndexTablespaceResolvesAndStores mirrors
// TestCreateTableTablespaceResolvesAndStores (create_table_tablespace_test.go)
// for the real-btree CREATE INDEX path: CREATE INDEX ... TABLESPACE name now
// resolves the name and stores the OID on catalog.Index.Tablespace (rendered
// as pg_class.reltablespace), instead of being a syntax error. M0122-0007.
func TestCreateIndexTablespaceResolvesAndStores(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	wantOID, ok := ctx.Catalog.LookupTablespaceOID("ts1")
	if !ok {
		t.Fatalf("LookupTablespaceOID(ts1): not found")
	}
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx1 ON t1 (a) TABLESPACE ts1"); err != nil {
		t.Fatalf("CREATE INDEX ... TABLESPACE ts1: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "idx1"})
	if !ok {
		t.Fatalf("index idx1 not found after CREATE")
	}
	if idx.Tablespace != wantOID {
		t.Errorf("idx1.Tablespace = %d, want %d", idx.Tablespace, wantOID)
	}
}

// TestCreateIndexTablespaceCatalogOnlyMethod covers the separate gist/spgist/
// gin/brin catalog-only creation path (a different code path from the btree
// builder above — see execCreateIndex's early-return branch).
func TestCreateIndexTablespaceCatalogOnlyMethod(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	wantOID, _ := ctx.Catalog.LookupTablespaceOID("ts1")
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx1 ON t1 USING gist (a) TABLESPACE ts1"); err != nil {
		t.Fatalf("CREATE INDEX ... USING gist ... TABLESPACE ts1: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "idx1"})
	if !ok {
		t.Fatalf("index idx1 not found after CREATE")
	}
	if idx.Tablespace != wantOID {
		t.Errorf("idx1.Tablespace = %d, want %d", idx.Tablespace, wantOID)
	}
}

// TestCreateIndexTablespaceUnknownErrors42704 mirrors
// TestCreateTableTablespaceUnknownErrors42704: an unresolvable tablespace
// name must reject the CREATE INDEX (no partially-built index left behind).
func TestCreateIndexTablespaceUnknownErrors42704(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	err := runDDL(t, ctx, "CREATE INDEX idx1 ON t1 (a) TABLESPACE nope")
	if got := execErrCode(err); got != "42704" {
		t.Fatalf("unknown tablespace: want 42704, got %q (%v)", got, err)
	}
	if _, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "idx1"}); ok {
		t.Fatalf("idx1 should not exist after a rejected CREATE INDEX")
	}
}

// TestAlterTableSetTablespaceUpdatesTable pins the ALTER TABLE ... SET
// TABLESPACE follow-up: previously a full syntax error (fell through to
// "expected ADD or DROP"). Catalog metadata only, matching the CREATE TABLE
// TABLESPACE precedent — no physical relocation of the table's file.
func TestAlterTableSetTablespaceUpdatesTable(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	wantOID, _ := ctx.Catalog.LookupTablespaceOID("ts1")
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE t1 SET TABLESPACE ts1"); err != nil {
		t.Fatalf("ALTER TABLE ... SET TABLESPACE ts1: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: "t1"})
	if !ok {
		t.Fatalf("t1 not found")
	}
	if tbl.Tablespace != wantOID {
		t.Errorf("t1.Tablespace = %d, want %d", tbl.Tablespace, wantOID)
	}
}

// TestAlterIndexSetTablespaceUpdatesIndex mirrors
// TestAlterTableSetTablespaceUpdatesTable for `ALTER INDEX ... SET
// TABLESPACE`, and also confirms the pre-existing `ALTER INDEX ... SET
// (fastupdate=...)` reloptions path (a sibling branch in the same parser
// dispatch) still works unaffected.
func TestAlterIndexSetTablespaceUpdatesIndex(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	wantOID, _ := ctx.Catalog.LookupTablespaceOID("ts1")
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx1 ON t1 (a)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER INDEX idx1 SET TABLESPACE ts1"); err != nil {
		t.Fatalf("ALTER INDEX ... SET TABLESPACE ts1: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "idx1"})
	if !ok {
		t.Fatalf("idx1 not found")
	}
	if idx.Tablespace != wantOID {
		t.Errorf("idx1.Tablespace = %d, want %d", idx.Tablespace, wantOID)
	}

	// The non-TABLESPACE ALTER INDEX SET arm must still work. It is asserted on
	// a GIN index, not on idx1: `fastupdate` is RELOPT_KIND_GIN, and since
	// M0134-0160 goopg validates ALTER INDEX SET against the access method's
	// admissible set the way ATExecSetRelOptions -> index_reloptions() does, so
	// `ALTER INDEX <btree> SET (fastupdate = off)` now raises the same
	// `unrecognized parameter "fastupdate"` real PG 18.3 raises (verified
	// against the oracle).
	if err := runDDL(t, ctx, "CREATE INDEX gidx1 ON t1 USING gin (a)"); err != nil {
		t.Fatalf("CREATE INDEX gidx1: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER INDEX gidx1 SET (fastupdate = off)"); err != nil {
		t.Fatalf("ALTER INDEX ... SET (fastupdate=off) regressed: %v", err)
	}
	gidx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "gidx1"})
	if !ok {
		t.Fatalf("gidx1 not found")
	}
	if gidx.FastUpdate == nil || *gidx.FastUpdate {
		t.Errorf("gidx1.FastUpdate = %v, want *false", gidx.FastUpdate)
	}
	// ... and the btree index rejects it, matching PG.
	if err := runDDL(t, ctx, "ALTER INDEX idx1 SET (fastupdate = off)"); err == nil {
		t.Error("ALTER INDEX <btree> SET (fastupdate=off): expected 22023, got nil")
	} else if ee, isExec := err.(*ExecError); !isExec || ee.Code != "22023" ||
		ee.Message != `unrecognized parameter "fastupdate"` {
		t.Errorf("ALTER INDEX <btree> SET (fastupdate=off): error = %v, want 22023 unrecognized parameter", err)
	}
}

// TestPGClassRendersIndexReltablespace confirms the live pg_class query path
// (registerSystemTables' index-row loop, catalog.go) surfaces the stored
// Index.Tablespace instead of the previous hardcoded 0.
func TestPGClassRendersIndexReltablespace(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	wantOID, _ := ctx.Catalog.LookupTablespaceOID("ts1")
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx1 ON t1 (a) TABLESPACE ts1"); err != nil {
		t.Fatalf("CREATE INDEX ... TABLESPACE ts1: %v", err)
	}
	rows := runQuery(t, ctx, "SELECT reltablespace FROM pg_catalog.pg_class WHERE relname = 'idx1'")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != int64(wantOID) {
		t.Errorf("pg_class.reltablespace = %+v, want %d", rows[0][0], wantOID)
	}
}
