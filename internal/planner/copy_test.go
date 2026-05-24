package planner

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanCopyFromStdin pgbench-shaped:
//
//	COPY pgbench_accounts FROM STDIN
//
// resolves the table, defaults the column list to declared order,
// and tags Direction=From + Endpoint=Stdin.
func TestPlanCopyFromStdin(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "COPY pgbench_accounts FROM STDIN")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := node.(*Copy)
	if !ok {
		t.Fatalf("root=%T want *Copy", node)
	}
	if c.Direction != CopyFrom || c.Endpoint != CopyEndpointStdin {
		t.Errorf("direction=%v endpoint=%v", c.Direction, c.Endpoint)
	}
	if c.Table == nil || c.Table.Name != "pgbench_accounts" {
		t.Errorf("table=%+v", c.Table)
	}
	if len(c.ColumnIndex) != 4 {
		t.Errorf("colindex=%v want all 4 declared columns", c.ColumnIndex)
	}
	for i, want := range []int{0, 1, 2, 3} {
		if c.ColumnIndex[i] != want {
			t.Errorf("colindex[%d]=%d want %d", i, c.ColumnIndex[i], want)
		}
	}
	if len(c.schema) != 4 || c.schema[0].Name != "aid" {
		t.Errorf("schema=%+v", c.schema)
	}
}

// TestPlanCopyTableWithColumnList: explicit column list resolves to
// the right ordinals (and the schema mirrors the listed order).
func TestPlanCopyTableWithColumnList(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "COPY pgbench_accounts (abalance, aid) TO STDOUT")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	c := node.(*Copy)
	if c.Direction != CopyTo {
		t.Errorf("direction=%v", c.Direction)
	}
	// pgbench_accounts: aid=0, bid=1, abalance=2, filler=3.
	if len(c.ColumnIndex) != 2 || c.ColumnIndex[0] != 2 || c.ColumnIndex[1] != 0 {
		t.Errorf("colindex=%v want [2 0]", c.ColumnIndex)
	}
	if c.schema[0].Name != "abalance" || c.schema[1].Name != "aid" {
		t.Errorf("schema=%+v", c.schema)
	}
}

// TestPlanCopyQueryToStdout: the wire layer's existing
// "COPY (SELECT 1) TO STDOUT" matches a query-form Copy whose
// Query subtree is a Project(Values) + the schema is "?column?".
func TestPlanCopyQueryToStdout(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "COPY (SELECT 1) TO STDOUT")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	c := node.(*Copy)
	if c.Direction != CopyTo || c.Endpoint != CopyEndpointStdout {
		t.Errorf("direction/endpoint=%v/%v", c.Direction, c.Endpoint)
	}
	if c.Table != nil {
		t.Errorf("Table should be nil for query-form, got %+v", c.Table)
	}
	if c.Query == nil {
		t.Fatal("Query nil")
	}
	if _, ok := c.Query.(*Project); !ok {
		t.Errorf("Query=%T want *Project", c.Query)
	}
	if len(c.schema) != 1 {
		t.Fatalf("schema=%+v", c.schema)
	}
}

// TestPlanCopyDMLReturningToStdout: COPY (INSERT … RETURNING) TO STDOUT
// plans the DML through the normal entry point and carries its
// RETURNING schema on the Copy node (Insert.Output() is nil, so the
// schema must come from the RETURNING list). M0097-0009.
func TestPlanCopyDMLReturningToStdout(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "COPY (INSERT INTO pgbench_accounts (aid) VALUES (1) RETURNING aid) TO STDOUT")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	c := node.(*Copy)
	if c.Direction != CopyTo || c.Endpoint != CopyEndpointStdout {
		t.Errorf("direction/endpoint=%v/%v", c.Direction, c.Endpoint)
	}
	if c.Query == nil {
		t.Fatal("Query nil")
	}
	if _, ok := c.Query.(*Insert); !ok {
		t.Errorf("Query=%T want *Insert", c.Query)
	}
	if len(c.schema) != 1 || c.schema[0].Name != "aid" {
		t.Fatalf("schema=%+v want one RETURNING column 'aid'", c.schema)
	}
}

// TestPlanCopyDMLWithoutReturningRejected: COPY (DML) with no RETURNING
// clause has no rows to stream — PostgreSQL rejects it with
// "COPY query must have a RETURNING clause".
func TestPlanCopyDMLWithoutReturningRejected(t *testing.T) {
	cat := pgbenchCatalog(t)
	for _, sql := range []string{
		"COPY (INSERT INTO pgbench_accounts (aid) VALUES (1)) TO STDOUT",
		"COPY (UPDATE pgbench_accounts SET abalance = 0) TO STDOUT",
		"COPY (DELETE FROM pgbench_accounts) TO STDOUT",
	} {
		stmt := parseOne(t, sql)
		_, err := Plan(stmt, cat)
		if err == nil {
			t.Fatalf("%q: expected error, got nil", sql)
		}
		if !strings.Contains(err.Error(), "must have a RETURNING clause") {
			t.Errorf("%q: error=%q want RETURNING-clause message", sql, err.Error())
		}
	}
}

// TestPlanCopyOptionsAcceptedAndRejected: the plan-time validator
// surfaces duplicate names, unknown FORMAT, and unknown options as
// SQLSTATE-tagged errors so the wire layer doesn't have to guess.
func TestPlanCopyOptionsAcceptedAndRejected(t *testing.T) {
	cat := pgbenchCatalog(t)

	// All-accepted: FORMAT csv, HEADER, DELIMITER '|', FORCE_QUOTE *.
	if _, err := Plan(parseOne(t, "COPY pgbench_accounts TO STDOUT WITH (FORMAT csv, HEADER, DELIMITER '|', FORCE_QUOTE *)"), cat); err != nil {
		t.Fatalf("expected accept: %v", err)
	}

	cases := []struct {
		sql  string
		code string
	}{
		{"COPY pgbench_accounts TO STDOUT WITH (FORMAT csv, FORMAT csv)", "42601"},
		{"COPY pgbench_accounts TO STDOUT WITH (FORMAT bogus)", "0A000"},
		{"COPY pgbench_accounts TO STDOUT WITH (UNKNOWNOPT)", "0A000"},
		{"COPY pgbench_accounts TO STDOUT WITH (HEADER true)", ""}, // accepted
	}
	for _, tc := range cases {
		_, err := Plan(parseOne(t, tc.sql), cat)
		if tc.code == "" {
			if err != nil {
				t.Errorf("expected accept for %q: %v", tc.sql, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("expected error for %q", tc.sql)
			continue
		}
		pe, ok := err.(*PlanError)
		if !ok {
			t.Errorf("unexpected error type for %q: %T", tc.sql, err)
			continue
		}
		if pe.Code != tc.code {
			t.Errorf("for %q: code=%q want %q", tc.sql, pe.Code, tc.code)
		}
	}
}

// TestPlanCopyTableErrors pins SQLSTATE codes for the obvious
// catalog-resolution failures.
func TestPlanCopyTableErrors(t *testing.T) {
	cat := pgbenchCatalog(t)

	cases := []struct {
		sql  string
		code string
	}{
		{"COPY no_such_table FROM STDIN", "42P01"},
		{"COPY pgbench_accounts (no_such_col) FROM STDIN", "42703"},
		{"COPY pgbench_accounts (aid, aid) FROM STDIN", "42701"},
	}
	for _, tc := range cases {
		_, err := Plan(parseOne(t, tc.sql), cat)
		if err == nil {
			t.Errorf("expected error for %q", tc.sql)
			continue
		}
		pe, ok := err.(*PlanError)
		if !ok {
			t.Errorf("unexpected error type for %q: %T", tc.sql, err)
			continue
		}
		if pe.Code != tc.code {
			t.Errorf("for %q: code=%q want %q (msg %q)", tc.sql, pe.Code, tc.code, pe.Message)
		}
	}
}

// TestPlanCopyViewRejected pins PostgreSQL's relation-kind check for
// table-form COPY: a plain view has no heap, so `COPY v TO` /
// `COPY v FROM` must fail with ERRCODE_WRONG_OBJECT_TYPE (42809) and
// the direction-specific hint. The `COPY (SELECT ...)` query form is
// the supported way to dump a view. Regression for copyselect
// (M0097-0009): goopg previously planned the view as if it were a heap
// relation.
func TestPlanCopyViewRejected(t *testing.T) {
	cat := pgbenchCatalog(t)
	viewStmts, err := parser.Parse(`SELECT aid FROM pgbench_accounts`)
	if err != nil {
		t.Fatalf("parse view body: %v", err)
	}
	innerSel := viewStmts[0].(*parser.SelectStmt)
	if _, err := cat.CreateView(parser.ObjectName{Name: "v_acc"},
		[]catalog.Column{{Name: "aid", Type: catalog.Type{Name: "int4"}}},
		[]string{"aid"}, innerSel, true); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	cases := []struct {
		sql      string
		wantMsg  string
		wantHint string
	}{
		{"COPY v_acc TO STDOUT", `cannot copy from view "v_acc"`, "Try the COPY (SELECT ...) TO variant."},
		{"COPY v_acc FROM STDIN", `cannot copy to view "v_acc"`, "To enable inserting into the view, provide an INSTEAD OF INSERT trigger."},
	}
	for _, tc := range cases {
		_, err := Plan(parseOne(t, tc.sql), cat)
		pe, ok := err.(*PlanError)
		if !ok {
			t.Errorf("for %q: expected *PlanError, got %T (%v)", tc.sql, err, err)
			continue
		}
		if pe.Code != "42809" {
			t.Errorf("for %q: code=%q want 42809", tc.sql, pe.Code)
		}
		if pe.Message != tc.wantMsg {
			t.Errorf("for %q: msg=%q want %q", tc.sql, pe.Message, tc.wantMsg)
		}
		if pe.Hint != tc.wantHint {
			t.Errorf("for %q: hint=%q want %q", tc.sql, pe.Hint, tc.wantHint)
		}
	}
}

// TestPlanCopyFileEndpointPlans: file/PROGRAM endpoints plan
// successfully (the executor will reject them later) and carry the
// filename verbatim.
func TestPlanCopyFileEndpointPlans(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "COPY pgbench_accounts TO PROGRAM 'gzip > out.gz'"), cat)
	if err != nil {
		t.Fatal(err)
	}
	c := node.(*Copy)
	if c.Endpoint != CopyEndpointProgram {
		t.Errorf("endpoint=%v", c.Endpoint)
	}
	if !strings.Contains(c.Filename, "gzip") {
		t.Errorf("filename=%q", c.Filename)
	}
}
