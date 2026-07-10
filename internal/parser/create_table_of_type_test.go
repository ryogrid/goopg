package parser

import "testing"

// TestCreateTableOfTypeColumnWithOptions verifies that `CREATE TABLE name OF
// type_name ( column_name WITH OPTIONS column_constraint [, ...] )` parses
// each entry into CreateTableStmt.OfTypeColumnOptions, sharing the same
// constraint grammar as a normal column definition (parseColumnConstraintList)
// but with no type of its own. DU-002 slice 374 follow-up: this list used to
// be rejected outright with "typed-table column option list is not
// supported".
func TestCreateTableOfTypeColumnWithOptions(t *testing.T) {
	sql := `CREATE TABLE employees OF employee_type (
		name WITH OPTIONS NOT NULL,
		salary WITH OPTIONS DEFAULT 1000
	)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct, ok := stmts[0].(*CreateTableStmt)
	if !ok {
		t.Fatalf("want *CreateTableStmt, got %T", stmts[0])
	}
	if ct.OfType == nil || ct.OfType.Name != "employee_type" {
		t.Fatalf("OfType = %v, want employee_type", ct.OfType)
	}
	if len(ct.OfTypeColumnOptions) != 2 {
		t.Fatalf("len(OfTypeColumnOptions) = %d, want 2", len(ct.OfTypeColumnOptions))
	}
	nameOpt := ct.OfTypeColumnOptions[0]
	if nameOpt.Name != "name" || !nameOpt.NotNull {
		t.Errorf("OfTypeColumnOptions[0] = %+v, want Name=name NotNull=true", nameOpt)
	}
	salaryOpt := ct.OfTypeColumnOptions[1]
	if salaryOpt.Name != "salary" || salaryOpt.DefaultExpr == nil {
		t.Errorf("OfTypeColumnOptions[1] = %+v, want Name=salary with a DefaultExpr", salaryOpt)
	}
}

// TestCreateTableOfTypeEmptyColumnList verifies the parenthesised list may be
// empty (`OF type_name ()`), matching the grammar's optional element list.
func TestCreateTableOfTypeEmptyColumnList(t *testing.T) {
	stmts, err := Parse(`CREATE TABLE employees OF employee_type ()`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if len(ct.OfTypeColumnOptions) != 0 {
		t.Errorf("OfTypeColumnOptions = %v, want empty", ct.OfTypeColumnOptions)
	}
}

// TestCreateTableOfTypeTableConstraintRejected verifies that a table_constraint
// entry (PRIMARY KEY/UNIQUE/CHECK/FOREIGN KEY/CONSTRAINT at table level) inside
// the OF-type-name column list is rejected with a clear parse error rather
// than silently dropped or misparsed as a column. This half of the PG grammar
// (`{ column_name WITH OPTIONS column_constraint | table_constraint }`) is
// still out of scope — see the deferral ledger.
func TestCreateTableOfTypeTableConstraintRejected(t *testing.T) {
	cases := []string{
		`CREATE TABLE employees OF employee_type (PRIMARY KEY (name))`,
		`CREATE TABLE employees OF employee_type (CHECK (salary >= 0))`,
		`CREATE TABLE employees OF employee_type (UNIQUE (name))`,
		`CREATE TABLE employees OF employee_type (CONSTRAINT pk_name PRIMARY KEY (name))`,
	}
	for _, sql := range cases {
		if _, err := Parse(sql); err == nil {
			t.Errorf("Parse(%q): expected rejection error, got nil", sql)
		}
	}
}

// TestCreateTableOfTypeUnknownColumnRequiresWithOptions verifies a bare
// column name without a trailing WITH OPTIONS is a syntax error (the OF-type
// column list has no form for a column reference without it).
func TestCreateTableOfTypeUnknownColumnRequiresWithOptions(t *testing.T) {
	if _, err := Parse(`CREATE TABLE employees OF employee_type (name NOT NULL)`); err == nil {
		t.Error("expected error for column entry missing WITH OPTIONS")
	}
}
