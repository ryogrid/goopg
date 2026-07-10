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

// TestCreateTableOfTypeTableConstraintAccepted verifies that a table_constraint
// entry (PRIMARY KEY/UNIQUE/CHECK/FOREIGN KEY/CONSTRAINT at table level) inside
// the OF-type-name column list now parses into the same CreateTableStmt fields
// the ordinary CREATE TABLE column list uses (PrimaryKey/TableChecks/
// TableUniques/TableForeignKeys/NamedConstraints), via the shared
// parseTableConstraintElement helper — the second half of PG's grammar
// `TypedTableElement: columnOptions | TableConstraint` (gram.y:3809-3812).
// DU-002 slice 374 follow-up (M0122-0024 deferral-ledger resume).
func TestCreateTableOfTypeTableConstraintAccepted(t *testing.T) {
	sql := `CREATE TABLE employees OF employee_type (PRIMARY KEY (name))`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if len(ct.PrimaryKey) != 1 || ct.PrimaryKey[0] != "name" {
		t.Errorf("PrimaryKey = %v, want [name]", ct.PrimaryKey)
	}

	sql = `CREATE TABLE employees OF employee_type (CHECK (salary >= 0))`
	stmts, err = Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct = stmts[0].(*CreateTableStmt)
	if len(ct.TableChecks) != 1 {
		t.Errorf("TableChecks = %v, want 1 entry", ct.TableChecks)
	}

	sql = `CREATE TABLE employees OF employee_type (UNIQUE (name))`
	stmts, err = Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct = stmts[0].(*CreateTableStmt)
	if len(ct.TableUniques) != 1 || len(ct.TableUniques[0]) != 1 || ct.TableUniques[0][0] != "name" {
		t.Errorf("TableUniques = %v, want [[name]]", ct.TableUniques)
	}

	sql = `CREATE TABLE employees OF employee_type (CONSTRAINT pk_name PRIMARY KEY (name))`
	stmts, err = Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct = stmts[0].(*CreateTableStmt)
	if len(ct.NamedConstraints) != 1 || ct.NamedConstraints[0].Name != "pk_name" || !ct.NamedConstraints[0].IsPrimary {
		t.Errorf("NamedConstraints = %+v, want [{Name:pk_name IsPrimary:true ...}]", ct.NamedConstraints)
	}
}

// TestCreateTableOfTypeMixedColumnAndTableConstraint verifies PostgreSQL's own
// canonical CREATE TABLE OF doc example — a table_constraint and a
// `column WITH OPTIONS` entry interleaved in the same list — parses both
// halves correctly (previously the table_constraint half rejected the whole
// statement outright).
func TestCreateTableOfTypeMixedColumnAndTableConstraint(t *testing.T) {
	sql := `CREATE TABLE employees OF employee_type (PRIMARY KEY (name), salary WITH OPTIONS DEFAULT 1000)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if len(ct.PrimaryKey) != 1 || ct.PrimaryKey[0] != "name" {
		t.Errorf("PrimaryKey = %v, want [name]", ct.PrimaryKey)
	}
	if len(ct.OfTypeColumnOptions) != 1 || ct.OfTypeColumnOptions[0].Name != "salary" || ct.OfTypeColumnOptions[0].DefaultExpr == nil {
		t.Errorf("OfTypeColumnOptions = %+v, want [{Name:salary DefaultExpr:non-nil}]", ct.OfTypeColumnOptions)
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
