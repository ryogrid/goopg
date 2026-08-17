package executor

// operators_ddl_col_description_test.go — pg_catalog.col_description(oid,
// integer) must resolve the comment stored on a column via COMMENT ON COLUMN,
// keyed by (classoid=pg_class, objoid, objsubid=attnum). This is C15 in
// docs/design/0134-0002-alter-table-sql-divergence.md: psql's describe.c
// column-comments query calls pg_catalog.col_description(a.attrelid, a.attnum)
// (postgres/src/bin/psql/describe.c:1986) and the alter_table regress test
// SELECTs it directly. The function is declared STRICT
// (postgres/src/backend/catalog/system_functions.sql:325), so any NULL arg or
// a no-match SELECT must yield NULL.

import "testing"

func TestColDescriptionReturnsColumnComment(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	// Same table/column/comment as postgres/src/test/regress/sql/alter_table.sql:2156.
	if err := runDDL(t, ctx, "CREATE TABLE comment_test (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "COMMENT ON COLUMN comment_test.id IS 'Column ''id'' on comment_test'"); err != nil {
		t.Fatalf("COMMENT ON COLUMN: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT col_description('comment_test'::regclass, 1)")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "Column 'id' on comment_test" {
		t.Errorf("col_description('comment_test'::regclass, 1) = %q, want %q",
			got, "Column 'id' on comment_test")
	}
}

func TestColDescriptionNoMatchReturnsNull(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE comment_test (id int, note text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "COMMENT ON COLUMN comment_test.id IS 'Column ''id'' on comment_test'"); err != nil {
		t.Fatalf("COMMENT ON COLUMN: %v", err)
	}

	// (b1) objsubid 0 is the table's own comment slot; no COMMENT ON TABLE was
	// issued, so a bare-SELECT no-match yields NULL (PG system_functions.sql:322-327).
	rows := runQueryRows(t, ctx, "SELECT col_description('comment_test'::regclass, 0)")
	if len(rows) != 1 || !rows[0][0].IsNull() {
		t.Errorf("col_description('comment_test'::regclass, 0) = %+v, want NULL (no table-level comment)", rows)
	}

	// (b2) attnum 2 (column `note`) has no comment → NULL.
	rows = runQueryRows(t, ctx, "SELECT col_description('comment_test'::regclass, 2)")
	if len(rows) != 1 || !rows[0][0].IsNull() {
		t.Errorf("col_description('comment_test'::regclass, 2) = %+v, want NULL (uncommented column)", rows)
	}

	// (b3) unknown table OID → NULL.
	rows = runQueryRows(t, ctx, "SELECT col_description(999999999, 1)")
	if len(rows) != 1 || !rows[0][0].IsNull() {
		t.Errorf("col_description(999999999, 1) = %+v, want NULL (no such table)", rows)
	}
}

// TestColDescriptionIsStrict pins the STRICT declaration: any NULL argument
// returns NULL without touching the catalog.
func TestColDescriptionIsStrict(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE comment_test (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT col_description(NULL::oid, 1)")
	if len(rows) != 1 || !rows[0][0].IsNull() {
		t.Errorf("col_description(NULL::oid, 1) = %+v, want NULL (STRICT)", rows)
	}
	rows = runQueryRows(t, ctx, "SELECT col_description('comment_test'::regclass, NULL::int4)")
	if len(rows) != 1 || !rows[0][0].IsNull() {
		t.Errorf("col_description('comment_test'::regclass, NULL) = %+v, want NULL (STRICT)", rows)
	}
}
