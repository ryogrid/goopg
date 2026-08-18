package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAlterTableAddPrimaryKeyOnNoInheritNotNullFails and
// TestAlterTableAddPrimaryKeyOnNotValidNotNullFails pin M0134-0005v: PG's
// verifyNotNullPKCompatible (postgres/src/backend/commands/tablecmds.c:
// 9577-9608), called from ATPrepAddPrimaryKey (:9556) before the PK index is
// built, rejects a PK over a column whose EXISTING NOT NULL constraint is
// marked NO INHERIT or NOT VALID with 55000
// ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE. Both share the same message and
// DETAIL shape, differing only in the marking word and HINT.
// TestAlterTableAddPrimaryKeyOnOrdinaryNotNullSucceeds is the regression
// guard: an ordinary validated, inheritable NOT NULL must still let the PK
// through unchanged.

func TestAlterTableAddPrimaryKeyOnNoInheritNotNullFails(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_noinh (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE pk_noinh ADD CONSTRAINT nn NOT NULL a NO INHERIT`); err != nil {
		t.Fatalf("ADD CONSTRAINT ... NOT NULL ... NO INHERIT: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE pk_noinh ADD PRIMARY KEY (a)`)
	if err == nil {
		t.Fatal("ADD PRIMARY KEY over a NO INHERIT NOT NULL succeeded, want 55000")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "55000" {
		t.Fatalf("Code = %q, want 55000", ee.Code)
	}
	if ee.Pos != 0 {
		t.Fatalf("Pos = %d, want 0 (no errposition in PG oracle)", ee.Pos)
	}
	wantMsg := `cannot create primary key on column "a"`
	if ee.Message != wantMsg {
		t.Fatalf("Message = %q, want %q", ee.Message, wantMsg)
	}
	wantDetail := `The constraint "nn" on column "a" of table "pk_noinh", marked NO INHERIT, is incompatible with a primary key.`
	if ee.Detail != wantDetail {
		t.Fatalf("Detail = %q, want %q", ee.Detail, wantDetail)
	}
	wantHint := "You might need to make the existing constraint inheritable using ALTER TABLE ... ALTER CONSTRAINT ... INHERIT."
	if ee.Hint != wantHint {
		t.Fatalf("Hint = %q, want %q", ee.Hint, wantHint)
	}
}

func TestAlterTableAddPrimaryKeyOnNotValidNotNullFails(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_notvalid (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE pk_notvalid ADD CONSTRAINT nn NOT NULL a NOT VALID`); err != nil {
		t.Fatalf("ADD CONSTRAINT ... NOT NULL ... NOT VALID: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE pk_notvalid ADD PRIMARY KEY (a)`)
	if err == nil {
		t.Fatal("ADD PRIMARY KEY over a NOT VALID NOT NULL succeeded, want 55000")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "55000" {
		t.Fatalf("Code = %q, want 55000", ee.Code)
	}
	if ee.Pos != 0 {
		t.Fatalf("Pos = %d, want 0 (no errposition in PG oracle)", ee.Pos)
	}
	wantMsg := `cannot create primary key on column "a"`
	if ee.Message != wantMsg {
		t.Fatalf("Message = %q, want %q", ee.Message, wantMsg)
	}
	wantDetail := `The constraint "nn" on column "a" of table "pk_notvalid", marked NOT VALID, is incompatible with a primary key.`
	if ee.Detail != wantDetail {
		t.Fatalf("Detail = %q, want %q", ee.Detail, wantDetail)
	}
	wantHint := "You might need to validate it using ALTER TABLE ... VALIDATE CONSTRAINT."
	if ee.Hint != wantHint {
		t.Fatalf("Hint = %q, want %q", ee.Hint, wantHint)
	}
}

func TestAlterTableAddPrimaryKeyOnOrdinaryNotNullSucceeds(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_ordinary (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE pk_ordinary ADD CONSTRAINT nn NOT NULL a`); err != nil {
		t.Fatalf("ADD CONSTRAINT ... NOT NULL a: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE pk_ordinary ADD PRIMARY KEY (a)`); err != nil {
		t.Fatalf("ADD PRIMARY KEY over an ordinary validated, inheritable NOT NULL should succeed, got: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "pk_ordinary"})
	if !ok {
		t.Fatal("pk_ordinary not found")
	}
	if !tbl.Columns[0].NotNull {
		t.Fatalf("pk_ordinary.a NotNull = false after ADD PRIMARY KEY, want true")
	}
}

// TestAlterTableOnlyAddPrimaryKeyOnPartitionWithNotValidNotNullFails and its
// NO INHERIT sibling pin the `ALTER TABLE ONLY parent ADD PRIMARY KEY` arm
// of ATPrepAddPrimaryKey (tablecmds.c:9532-9557, constraints.sql:951-956 /
// :957-962): the parent itself carries no NOT NULL, so PG verifies every
// direct partition child's existing NOT NULL is PK-compatible instead of
// synthesizing a new one — same 55000 shape as the direct-column case, but
// the DETAIL names the CHILD relation, not the parent ALTER TABLE targets.

func TestAlterTableOnlyAddPrimaryKeyOnPartitionWithNotValidNotNullFails(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE pp_notvalid (a int) PARTITION BY HASH (a)`,
		`CREATE TABLE pp_notvalid_1 PARTITION OF pp_notvalid FOR VALUES WITH (MODULUS 2, REMAINDER 0)`,
		`ALTER TABLE pp_notvalid_1 ADD CONSTRAINT nn NOT NULL a NOT VALID`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE ONLY pp_notvalid ADD PRIMARY KEY (a)`)
	if err == nil {
		t.Fatal("ALTER TABLE ONLY ADD PRIMARY KEY over a partition with a NOT VALID NOT NULL succeeded, want 55000")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "55000" {
		t.Fatalf("Code = %q, want 55000", ee.Code)
	}
	if ee.Pos != 0 {
		t.Fatalf("Pos = %d, want 0 (no errposition in PG oracle)", ee.Pos)
	}
	wantDetail := `The constraint "nn" on column "a" of table "pp_notvalid_1", marked NOT VALID, is incompatible with a primary key.`
	if ee.Detail != wantDetail {
		t.Fatalf("Detail = %q, want %q", ee.Detail, wantDetail)
	}
}

func TestAlterTableOnlyAddPrimaryKeyOnPartitionWithNoInheritNotNullFails(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE pp_noinh (a int) PARTITION BY HASH (a)`,
		`CREATE TABLE pp_noinh_1 PARTITION OF pp_noinh FOR VALUES WITH (MODULUS 2, REMAINDER 0)`,
		`ALTER TABLE pp_noinh_1 ADD CONSTRAINT nn NOT NULL a NO INHERIT`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE ONLY pp_noinh ADD PRIMARY KEY (a)`)
	if err == nil {
		t.Fatal("ALTER TABLE ONLY ADD PRIMARY KEY over a partition with a NO INHERIT NOT NULL succeeded, want 55000")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "55000" {
		t.Fatalf("Code = %q, want 55000", ee.Code)
	}
	if ee.Pos != 0 {
		t.Fatalf("Pos = %d, want 0 (no errposition in PG oracle)", ee.Pos)
	}
	wantDetail := `The constraint "nn" on column "a" of table "pp_noinh_1", marked NO INHERIT, is incompatible with a primary key.`
	if ee.Detail != wantDetail {
		t.Fatalf("Detail = %q, want %q", ee.Detail, wantDetail)
	}
}
