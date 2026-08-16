package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// fkAddFixture sets up the regress alter_table.sql:339-356 shape — a PK
// referenced table with rows 1..4 and a child table with rows (1,10),(1,20),
// (5,50) — so the FK-block ADD tests run against PG's exact starting state.
func fkAddFixture(t *testing.T, ctx *Context) {
	t.Helper()
	if err := runDDL(t, ctx, `CREATE TABLE attmp2 (a int primary key)`); err != nil {
		t.Fatalf("CREATE TABLE attmp2: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO attmp2 VALUES (1),(2),(3),(4)`); err != nil {
		t.Fatalf("INSERT attmp2: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE attmp3 (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE attmp3: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO attmp3 VALUES (1,10),(1,20),(5,50)`); err != nil {
		t.Fatalf("INSERT attmp3: %v", err)
	}
}

// TestAlterTableAddForeignKeyDuplicateName verifies the 42710 duplicate-name
// guard (M0134-0002 C4 item 1): an explicit CONSTRAINT name that already
// exists on the table — here from a named CHECK, exercising the cross-kind
// enumeration (FK + CHECK + PK/UNIQUE/EXCLUDE index + NOT NULL, mirroring
// execAlterTableDropConstraint) — is refused byte-exact with Pos 0
// (ATExecAddConstraint CONSTR_FOREIGN, tablecmds.c:9824-9833 →
// ConstraintNameIsUsed, pg_constraint.c:412).
func TestAlterTableAddForeignKeyDuplicateName(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	fkAddFixture(t, ctx)

	if err := runDDL(t, ctx, `ALTER TABLE attmp3 ADD CONSTRAINT attmpconstr CHECK (a > 0)`); err != nil {
		t.Fatalf("ADD CHECK attmpconstr: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE attmp3 ADD CONSTRAINT attmpconstr FOREIGN KEY (a) REFERENCES attmp2`),
		"42710", `constraint "attmpconstr" for relation "attmp3" already exists`)
	if ee.Message != `constraint "attmpconstr" for relation "attmp3" already exists` {
		t.Errorf("Message = %q, want the byte-exact PG text", ee.Message)
	}
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0 (PG emits no errposition)", ee.Pos)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "attmp3"})
	if !ok {
		t.Fatal("attmp3 table not found")
	}
	if len(tbl.ForeignKeys) != 0 {
		t.Errorf("ForeignKeys after failed ADD FK = %+v, want none", tbl.ForeignKeys)
	}
}

// TestAlterTableAddForeignKeyMissingSourceColumn verifies the 42703
// source-column check (M0134-0002 C4 item 2): `foreign key(c)` on a table
// with no column c raises PG's byte-exact message (transformColumnNameList,
// tablecmds.c:13327-13346), case-sensitive, Pos 0.
func TestAlterTableAddForeignKeyMissingSourceColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	fkAddFixture(t, ctx)

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE attmp3 ADD CONSTRAINT attmpconstr FOREIGN KEY (c) REFERENCES attmp2`),
		"42703", `column "c" referenced in foreign key constraint does not exist`)
	if ee.Message != `column "c" referenced in foreign key constraint does not exist` {
		t.Errorf("Message = %q, want the byte-exact PG text", ee.Message)
	}
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0 (PG emits no errposition)", ee.Pos)
	}
}

// TestAlterTableAddForeignKeyMissingRefColumn verifies the 42703 ref-column
// check (M0134-0002 C4 item 3): `references attmp2(b)` on a referenced table
// with no column b raises the same byte-exact message, Pos 0. Source-column
// resolution precedes ref-column resolution, so a bad source column would win
// over a bad ref column; this fixture has a valid source (a) and only the ref
// column wrong.
func TestAlterTableAddForeignKeyMissingRefColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	fkAddFixture(t, ctx)

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE attmp3 ADD CONSTRAINT attmpconstr FOREIGN KEY (a) REFERENCES attmp2(b)`),
		"42703", `column "b" referenced in foreign key constraint does not exist`)
	if ee.Message != `column "b" referenced in foreign key constraint does not exist` {
		t.Errorf("Message = %q, want the byte-exact PG text", ee.Message)
	}
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0 (PG emits no errposition)", ee.Pos)
	}
}

// TestAlterTableAddForeignKeyDanglingRow verifies the 23503 existing-row scan
// (M0134-0002 C4 item 4): a plain ADD FOREIGN KEY (no NOT VALID) validates
// existing rows — the dangling (5,50) child row raises PG's byte-exact error +
// DETAIL, with no ref columns named (exercising assertParentExists's
// pkColumns inference), Pos 0. A failed ADD must not register a ghost FK.
func TestAlterTableAddForeignKeyDanglingRow(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	fkAddFixture(t, ctx)

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE attmp3 ADD CONSTRAINT attmpconstr FOREIGN KEY (a) REFERENCES attmp2`),
		"23503", `insert or update on table "attmp3" violates foreign key constraint "attmpconstr"`)
	if ee.Message != `insert or update on table "attmp3" violates foreign key constraint "attmpconstr"` {
		t.Errorf("Message = %q, want the byte-exact PG text", ee.Message)
	}
	if ee.Detail != `Key (a)=(5) is not present in table "attmp2".` {
		t.Errorf("Detail = %q, want %q", ee.Detail, `Key (a)=(5) is not present in table "attmp2".`)
	}
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0 (FK-violation ereports carry no errposition, ri_triggers.c:2778)", ee.Pos)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "attmp3"})
	if !ok {
		t.Fatal("attmp3 table not found")
	}
	if len(tbl.ForeignKeys) != 0 {
		t.Errorf("ForeignKeys after failed ADD FK = %+v, want none", tbl.ForeignKeys)
	}
}

// TestAlterTableAddForeignKeyNotValidThenValidate verifies the NOT VALID arm
// skips the existing-row scan (the dangling row is accepted and the FK is
// registered convalidated='f'), and that the later VALIDATE CONSTRAINT raises
// the same 23503 with Pos 0 (M0134-0002 C4 item 5 — the wrap at the old
// :7757-7761 put a spurious LINE 1 caret on the anchor; PG's FK ereports carry
// no errposition, ri_triggers.c:2778).
func TestAlterTableAddForeignKeyNotValidThenValidate(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	fkAddFixture(t, ctx)

	if err := runDDL(t, ctx, `ALTER TABLE attmp3 ADD CONSTRAINT attmpconstr FOREIGN KEY (a) REFERENCES attmp2 NOT VALID`); err != nil {
		t.Fatalf("ADD FK NOT VALID should not scan existing rows: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "attmp3"})
	if !ok {
		t.Fatal("attmp3 table not found")
	}
	if len(tbl.ForeignKeys) != 1 || tbl.ForeignKeys[0].Name != "attmpconstr" {
		t.Fatalf("expected 1 registered FK attmpconstr, got %+v", tbl.ForeignKeys)
	}
	if !tbl.ForeignKeys[0].NotValid {
		t.Errorf("ForeignKeys[0].NotValid = false after NOT VALID, want true")
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE attmp3 VALIDATE CONSTRAINT attmpconstr`),
		"23503", `insert or update on table "attmp3" violates foreign key constraint "attmpconstr"`)
	if ee.Detail != `Key (a)=(5) is not present in table "attmp2".` {
		t.Errorf("Detail = %q, want %q", ee.Detail, `Key (a)=(5) is not present in table "attmp2".`)
	}
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0 (VALIDATE FK 23503 must stay Pos 0)", ee.Pos)
	}
}
