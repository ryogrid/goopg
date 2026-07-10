package executor

import "testing"

// TestCreateTableOfTypeWithOptionsAppliesConstraints verifies that
// `CREATE TABLE name OF type_name (col WITH OPTIONS column_constraint, ...)`
// actually applies the per-column constraint overrides (NOT NULL, DEFAULT) to
// the composite-derived columns, rather than silently no-opping them. DU-002
// slice 374 follow-up: before this fix the parser rejected any parenthesised
// list after `OF type_name` outright.
func TestCreateTableOfTypeWithOptionsAppliesConstraints(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TYPE employee_type AS (name text, salary numeric)`); err != nil {
		t.Fatalf("CREATE TYPE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE employees OF employee_type (
		name WITH OPTIONS NOT NULL,
		salary WITH OPTIONS DEFAULT 1000
	)`); err != nil {
		t.Fatalf("CREATE TABLE OF: %v", err)
	}

	// NOT NULL from the WITH OPTIONS override must be enforced at INSERT time.
	err := runDDL(t, ctx, `INSERT INTO employees (name, salary) VALUES (NULL, 500)`)
	if err == nil {
		t.Fatal("expected NOT NULL violation, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23502" {
		t.Errorf("Code=%q want 23502 (not_null_violation)", ee.Code)
	}

	// DEFAULT from the WITH OPTIONS override must apply when the column is omitted.
	if err := runDDL(t, ctx, `INSERT INTO employees (name) VALUES ('alice')`); err != nil {
		t.Fatalf("INSERT with default: %v", err)
	}
	rows := runQuery(t, ctx, `SELECT name, salary FROM employees WHERE name = 'alice'`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row for alice, got %d", len(rows))
	}
	salary := rows[0][1]
	if salary.Kind != KindNumeric && salary.Kind != KindInt {
		t.Fatalf("unexpected salary Datum kind %v", salary.Kind)
	}
}

// TestCreateTableOfTypeWithOptionsUnknownColumn verifies a WITH OPTIONS entry
// naming a column absent from the composite type is rejected with PostgreSQL's
// real error (42703 "column ... does not exist", from MergeAttributes in
// postgres/src/backend/commands/tablecmds.c), not silently ignored.
func TestCreateTableOfTypeWithOptionsUnknownColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TYPE employee_type AS (name text, salary numeric)`); err != nil {
		t.Fatalf("CREATE TYPE: %v", err)
	}
	err := runDDL(t, ctx, `CREATE TABLE bad OF employee_type (bogus WITH OPTIONS NOT NULL)`)
	if err == nil {
		t.Fatal("expected error for unknown column in WITH OPTIONS list")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "42703" {
		t.Errorf("Code=%q want 42703 (undefined_column)", ee.Code)
	}
}
