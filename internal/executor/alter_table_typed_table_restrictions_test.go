package executor

import "testing"

// TestAlterTableTypedTableRestrictions pins the "certain ALTER TABLE
// operations on typed tables are not allowed" block from
// postgres/src/test/regress/sql/typed_table.sql:17-23 (M0134-0183 sizing).
// A typed table (`CREATE TABLE ... OF composite_type`) refuses ADD COLUMN,
// DROP COLUMN, RENAME COLUMN, ALTER COLUMN ... TYPE, and INHERIT — five
// independent guards in PG, each the FIRST check in its respective
// ATPrep*/renameatt_check function (postgres/src/backend/commands/
// tablecmds.c:3798-3802 rename, 7200-7203 add, 9260-9263 drop, 14395-14400
// alter-type, 17237-17241 inherit) and so run before every other check in
// goopg's conflated prep+exec handlers, including the column-existence
// lookup — a typed table must reject these even when the named column
// really doesn't exist.
func TestAlterTableTypedTableRestrictions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		`CREATE TYPE tt183_person AS (id int, name text)`,
		`CREATE TABLE tt183_persons OF tt183_person`,
		`CREATE TABLE tt183_stuff (id int)`,
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE tt183_persons ADD COLUMN comment text`)
	wantAlterError(t, err, "42809", "cannot add column to typed table")

	err = runDDL(t, ctx, `ALTER TABLE tt183_persons DROP COLUMN name`)
	wantAlterError(t, err, "42809", "cannot drop column from typed table")

	err = runDDL(t, ctx, `ALTER TABLE tt183_persons RENAME COLUMN id TO num`)
	wantAlterError(t, err, "42809", "cannot rename column of typed table")

	err = runDDL(t, ctx, `ALTER TABLE tt183_persons ALTER COLUMN name TYPE varchar`)
	wantAlterError(t, err, "42809", "cannot alter column type of typed table")

	err = runDDL(t, ctx, `ALTER TABLE tt183_persons INHERIT tt183_stuff`)
	wantAlterError(t, err, "42809", "cannot change inheritance of typed table")

	// Regression guard: the same five operations remain legal on a plain
	// (never-typed) table — the OfTypeOID==0 gate must not fire generally.
	for _, s := range []string{
		`CREATE TABLE tt183_plain (id int, name text)`,
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := runDDL(t, ctx, `ALTER TABLE tt183_plain ADD COLUMN comment text`); err != nil {
		t.Errorf("plain table ADD COLUMN unexpectedly failed: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE tt183_plain RENAME COLUMN id TO num`); err != nil {
		t.Errorf("plain table RENAME COLUMN unexpectedly failed: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE tt183_plain ALTER COLUMN name TYPE varchar`); err != nil {
		t.Errorf("plain table ALTER COLUMN TYPE unexpectedly failed: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE tt183_plain INHERIT tt183_stuff`); err != nil {
		t.Errorf("plain table INHERIT unexpectedly failed: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE tt183_plain DROP COLUMN comment`); err != nil {
		t.Errorf("plain table DROP COLUMN unexpectedly failed: %v", err)
	}
}
