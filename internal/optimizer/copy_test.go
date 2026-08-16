package optimizer

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
		{"COPY pgbench_accounts TO STDOUT WITH (FORMAT bogus)", "22023"}, // PG: COPY format "bogus" not recognized
		{"COPY pgbench_accounts TO STDOUT WITH (UNKNOWNOPT)", "42601"},   // PG: option "unknownopt" not recognized
		{"COPY pgbench_accounts TO STDOUT WITH (HEADER true)", ""},       // accepted
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

// TestPlanCopyIncorrectOptions pins the PostgreSQL-exact ERROR
// messages for the copy2 "incorrect options" block. These must fire at
// plan time so a COPY FROM STDIN with a bad option never enters
// copy-in mode (otherwise the following statements get slurped as data
// and the rest of the batch desyncs). Messages mirror
// ProcessCopyOptions in src/backend/commands/copy.c byte-for-byte; the
// regress diff compares the message text (psql does not print SQLSTATE).
// M0097-0024 (copy2).
func TestPlanCopyIncorrectOptions(t *testing.T) {
	cat := pgbenchCatalog(t)

	cases := []struct {
		sql  string
		want string
	}{
		// Redundant options → "conflicting or redundant options".
		{"COPY pgbench_accounts FROM STDIN (format csv, FORMAT csv)", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (freeze off, freeze on)", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (delimiter ',', delimiter ',')", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (null ' ', null ' ')", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (header off, header on)", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (quote ':', quote ':')", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (escape ':', escape ':')", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (force_quote (aid), force_quote *)", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (force_not_null (aid), force_not_null (bid))", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (force_null (aid), force_null (bid))", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (convert_selectively (aid), convert_selectively (bid))", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (encoding 'sql_ascii', encoding 'sql_ascii')", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (on_error ignore, on_error ignore)", "conflicting or redundant options"},
		{"COPY pgbench_accounts FROM STDIN (log_verbosity default, log_verbosity verbose)", "conflicting or redundant options"},
		// Incompatible combinations.
		{"COPY pgbench_accounts FROM STDIN (format BINARY, delimiter ',')", "cannot specify DELIMITER in BINARY mode"},
		{"COPY pgbench_accounts FROM STDIN (format BINARY, null 'x')", "cannot specify NULL in BINARY mode"},
		{"COPY pgbench_accounts FROM STDIN (format BINARY, on_error ignore)", "only ON_ERROR STOP is allowed in BINARY mode"},
		{"COPY pgbench_accounts FROM STDIN (on_error unsupported)", "COPY ON_ERROR \"unsupported\" not recognized"},
		{"COPY pgbench_accounts FROM STDIN (format TEXT, force_quote(aid))", "COPY FORCE_QUOTE requires CSV mode"},
		{"COPY pgbench_accounts FROM STDIN (format TEXT, force_quote *)", "COPY FORCE_QUOTE requires CSV mode"},
		{"COPY pgbench_accounts FROM STDIN (format CSV, force_quote(aid))", "COPY FORCE_QUOTE cannot be used with COPY FROM"},
		{"COPY pgbench_accounts FROM STDIN (format CSV, force_quote *)", "COPY FORCE_QUOTE cannot be used with COPY FROM"},
		{"COPY pgbench_accounts FROM STDIN (format TEXT, force_not_null(aid))", "COPY FORCE_NOT_NULL requires CSV mode"},
		{"COPY pgbench_accounts FROM STDIN (format TEXT, force_not_null *)", "COPY FORCE_NOT_NULL requires CSV mode"},
		{"COPY pgbench_accounts TO STDOUT (format CSV, force_not_null(aid))", "COPY FORCE_NOT_NULL cannot be used with COPY TO"},
		{"COPY pgbench_accounts TO STDOUT (format CSV, force_not_null *)", "COPY FORCE_NOT_NULL cannot be used with COPY TO"},
		{"COPY pgbench_accounts FROM STDIN (format TEXT, force_null(aid))", "COPY FORCE_NULL requires CSV mode"},
		{"COPY pgbench_accounts FROM STDIN (format TEXT, force_null *)", "COPY FORCE_NULL requires CSV mode"},
		{"COPY pgbench_accounts TO STDOUT (format CSV, force_null(aid))", "COPY FORCE_NULL cannot be used with COPY TO"},
		{"COPY pgbench_accounts TO STDOUT (format CSV, force_null *)", "COPY FORCE_NULL cannot be used with COPY TO"},
		{"COPY pgbench_accounts TO STDOUT (format BINARY, on_error unsupported)", "COPY ON_ERROR cannot be used with COPY TO"},
		{"COPY pgbench_accounts FROM STDIN (log_verbosity unsupported)", "COPY LOG_VERBOSITY \"unsupported\" not recognized"},
		{"COPY pgbench_accounts FROM STDIN with (reject_limit 1)", "COPY REJECT_LIMIT requires ON_ERROR to be set to IGNORE"},
		{"COPY pgbench_accounts FROM STDIN with (on_error ignore, reject_limit 0)", "REJECT_LIMIT (0) must be greater than zero"},
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
		if pe.Message != tc.want {
			t.Errorf("for %q:\n got  %q\n want %q", tc.sql, pe.Message, tc.want)
		}
	}

	// Valid combinations must still plan (no false positives).
	ok := []string{
		"COPY pgbench_accounts TO STDOUT (format CSV, force_quote *)",
		"COPY pgbench_accounts TO STDOUT (format CSV, force_quote (aid))",
		"COPY pgbench_accounts FROM STDIN (format CSV, force_not_null (aid))",
		"COPY pgbench_accounts FROM STDIN (format CSV, force_null (aid))",
		"COPY pgbench_accounts FROM STDIN (format BINARY)",
		"COPY pgbench_accounts FROM STDIN (on_error ignore)",
		"COPY pgbench_accounts FROM STDIN (on_error ignore, reject_limit 5)",
		"COPY pgbench_accounts FROM STDIN (log_verbosity verbose)",
		"COPY pgbench_accounts TO STDOUT (format CSV, header)",
	}
	for _, sql := range ok {
		if _, err := Plan(parseOne(t, sql), cat); err != nil {
			t.Errorf("expected accept for %q: %v", sql, err)
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

// TestPlanCopySelectIntoRejected: COPY (SELECT … INTO …) is rejected with
// the PG-compatible feature-not-supported error (copyto.c). The parser
// flags it (SelectInto) and stops before resolving the INTO target, so
// planCopy short-circuits before touching the catalog. M0097-0024.
func TestPlanCopySelectIntoRejected(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "COPY (SELECT 1 INTO test3) TO STDOUT")
	if !stmt.(*parser.CopyStmt).SelectInto {
		t.Fatal("parser did not flag SelectInto")
	}
	_, err := Plan(stmt, cat)
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("expected *PlanError, got %T (%v)", err, err)
	}
	if pe.Code != "0A000" {
		t.Errorf("code=%q want 0A000", pe.Code)
	}
	if pe.Message != "COPY (SELECT INTO) is not supported" {
		t.Errorf("msg=%q want %q", pe.Message, "COPY (SELECT INTO) is not supported")
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
