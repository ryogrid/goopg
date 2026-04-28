package planner

import (
	"strings"
	"testing"
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
