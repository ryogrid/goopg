package executor

// operators_ddl_system_column_test.go — M0134-0002 C8: a user column whose name
// collides with a PostgreSQL system column must be rejected with SQLSTATE 42701
// and the PG-exact message `column name %q conflicts with a system column name`.
// PG oracle:
//   - CREATE TABLE (incl. CTAS): CheckAttributeNamesTypes
//     (postgres/src/backend/catalog/heap.c:478-483) over SysAtt[]
//     (heap.c:144-228).
//   - ADD COLUMN / RENAME COLUMN: check_for_column_name_collision
//     (postgres/src/backend/commands/tablecmds.c:7670-7674).
//   - The match is exact and case-sensitive (SystemAttributeByName → strcmp,
//     heap.c:248), so a quoted mixed-case "XMIN" is a legal user column and
//     "oid" has been legal since PG 12 (dropped from the system-attribute set).

import "testing"

// systemColumnNames is the six-element SysAtt[] name set (heap.c:144-228).
var systemColumnNames = []string{"ctid", "xmin", "cmin", "xmax", "cmax", "tableoid"}

// wantDDL42701 runs sql via runDDL and asserts it fails with the PG
// system-column-collision error: SQLSTATE 42701 + the exact PG message.
func wantDDL42701(t *testing.T, ctx *Context, sql, wantName string) {
	t.Helper()
	err := runDDL(t, ctx, sql)
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("%s: err = %v (%T), want *ExecError{Code: 42701}", sql, err, err)
	}
	if ee.Code != "42701" {
		t.Fatalf("%s: Code = %q (message %q), want 42701", sql, ee.Code, ee.Message)
	}
	if ee.Pos != 0 {
		t.Errorf("%s: Pos = %d, want 0 (the PG ereport for this error carries no errposition, so psql renders no LINE 1)", sql, ee.Pos)
	}
	wantMsg := "column name \"" + wantName + "\" conflicts with a system column name"
	if ee.Message != wantMsg {
		t.Errorf("%s: Message = %q, want %q", sql, ee.Message, wantMsg)
	}
}

func TestSystemColumnRejectedAddColumn(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for _, name := range systemColumnNames {
		t.Run(name, func(t *testing.T) {
			wantDDL42701(t, ctx, "ALTER TABLE t ADD COLUMN "+name+" int", name)
		})
	}
	// The rejected ALTERs must not have mutated the table.
	if err := runDDL(t, ctx, "ALTER TABLE t ADD COLUMN b int"); err != nil {
		t.Fatalf("ALTER TABLE ADD COLUMN b after failed adds: %v", err)
	}
}

func TestSystemColumnRejectedCreateTable(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	for _, name := range systemColumnNames {
		t.Run(name, func(t *testing.T) {
			wantDDL42701(t, ctx, "CREATE TABLE ct_"+name+" ("+name+" int)", name)
		})
	}
	// A normal CREATE TABLE still works after the rejections.
	if err := runDDL(t, ctx, "CREATE TABLE ct_ok (a int)"); err != nil {
		t.Fatalf("CREATE TABLE ct_ok after rejected creates: %v", err)
	}
}

func TestSystemColumnRejectedRenameColumn(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	for _, name := range systemColumnNames {
		t.Run(name, func(t *testing.T) {
			if err := runDDL(t, ctx, "CREATE TABLE rn_"+name+" (a int)"); err != nil {
				t.Fatalf("CREATE TABLE: %v", err)
			}
			wantDDL42701(t, ctx, "ALTER TABLE rn_"+name+" RENAME COLUMN a TO "+name, name)
		})
	}
	// The rejected renames must not have mutated their tables.
	if err := runDDL(t, ctx, "ALTER TABLE rn_xmin RENAME COLUMN a TO b"); err != nil {
		t.Fatalf("ALTER TABLE RENAME after failed rename: %v", err)
	}
}

// TestSystemColumnRejectedCreateTableAs pins the CTAS entry point: PG validates
// CTAS column names — both the SELECT output names and the alias list — through
// DefineRelation → heap_create_with_catalog → CheckAttributeNamesTypes
// (createas.c:166-188 builds the ColumnDefs from tlist + colNames; createas.c:114).
func TestSystemColumnRejectedCreateTableAs(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE t1: %v", err)
	}
	// Column-alias list names a system column.
	wantDDL42701(t, ctx, "CREATE TABLE ctas_alias (xmin) AS SELECT 1", "xmin")
	// A normal CTAS still works.
	if err := runDDL(t, ctx, "CREATE TABLE ctas_ok AS SELECT a FROM t1"); err != nil {
		t.Fatalf("CREATE TABLE ctas_ok should succeed, got: %v", err)
	}
}

func TestSystemColumnQuotedMixedCaseAccepted(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	// The system-column match is exact and case-sensitive (heap.c:248 strcmp),
	// and quoted identifiers preserve case through the lexer, so "XMIN",
	// "TABLEOID", and "CTID" are all legal user column names.
	if err := runDDL(t, ctx, `CREATE TABLE q_ct ("XMIN" int)`); err != nil {
		t.Fatalf(`CREATE TABLE ("XMIN" int) should succeed, got: %v`, err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE q_add (a int)"); err != nil {
		t.Fatalf("CREATE TABLE q_add: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE q_add ADD COLUMN "TABLEOID" text`); err != nil {
		t.Fatalf(`ALTER TABLE ADD COLUMN "TABLEOID" should succeed, got: %v`, err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE q_rn (a int)"); err != nil {
		t.Fatalf("CREATE TABLE q_rn: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE q_rn RENAME COLUMN a TO "CTID"`); err != nil {
		t.Fatalf(`ALTER TABLE RENAME COLUMN a TO "CTID" should succeed, got: %v`, err)
	}
}

func TestSystemColumnOidAccepted(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	// "oid" has been an ordinary user column since PG 12; none of the C8
	// rejection sites may fire on it.
	if err := runDDL(t, ctx, "CREATE TABLE oid_ct (oid int)"); err != nil {
		t.Fatalf("CREATE TABLE (oid int) should succeed, got: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE oid_add (a int)"); err != nil {
		t.Fatalf("CREATE TABLE oid_add: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE oid_add ADD COLUMN oid int"); err != nil {
		t.Fatalf("ALTER TABLE ADD COLUMN oid should succeed, got: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE oid_rn (a int)"); err != nil {
		t.Fatalf("CREATE TABLE oid_rn: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE oid_rn RENAME COLUMN a TO oid"); err != nil {
		t.Fatalf("ALTER TABLE RENAME COLUMN a TO oid should succeed, got: %v", err)
	}
}
