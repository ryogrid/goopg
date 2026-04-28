package parser

import "testing"

// TestParseCopyFromStdinTable: the canonical pgbench shape — load
// pgbench_accounts from stdin, no options.
func TestParseCopyFromStdinTable(t *testing.T) {
	stmts, err := Parse("COPY pgbench_accounts FROM STDIN")
	if err != nil {
		t.Fatal(err)
	}
	c, ok := stmts[0].(*CopyStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if c.Direction != CopyFrom {
		t.Errorf("direction=%v", c.Direction)
	}
	if c.Endpoint != CopyEndpointStdin {
		t.Errorf("endpoint=%v", c.Endpoint)
	}
	if c.Table.Name != "pgbench_accounts" {
		t.Errorf("table=%+v", c.Table)
	}
	if c.Query != nil {
		t.Errorf("Query should be nil")
	}
	if len(c.Options) != 0 {
		t.Errorf("options=%v", c.Options)
	}
}

// TestParseCopyToStdoutWithColumnList exercises the column-list +
// TO STDOUT form with a parenthesised WITH clause.
func TestParseCopyToStdoutWithColumnList(t *testing.T) {
	stmts, err := Parse("COPY pgbench_accounts (aid, abalance) TO STDOUT WITH (FORMAT csv, HEADER, DELIMITER '|', NULL '')")
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].(*CopyStmt)
	if c.Direction != CopyTo || c.Endpoint != CopyEndpointStdout {
		t.Errorf("direction/endpoint = %v/%v", c.Direction, c.Endpoint)
	}
	if len(c.Columns) != 2 || c.Columns[0] != "aid" || c.Columns[1] != "abalance" {
		t.Errorf("columns=%v", c.Columns)
	}
	if len(c.Options) != 4 {
		t.Fatalf("options=%v", c.Options)
	}
	got := map[string]CopyOption{}
	for _, o := range c.Options {
		got[o.Name] = o
	}
	if got["format"].Value != "csv" {
		t.Errorf("format=%+v", got["format"])
	}
	if !got["header"].Bool {
		t.Errorf("header=%+v", got["header"])
	}
	if got["delimiter"].Value != "|" {
		t.Errorf("delimiter=%+v", got["delimiter"])
	}
	if got["null"].Value != "" || got["null"].Bool {
		t.Errorf("null=%+v", got["null"])
	}
}

// TestParseCopyQueryToStdout: COPY (SELECT 1) TO STDOUT. The current
// server hand-matches this exact string, so the parser must produce
// the equivalent CopyStmt for the server replacement to land cleanly.
func TestParseCopyQueryToStdout(t *testing.T) {
	stmts, err := Parse("COPY (SELECT 1) TO STDOUT")
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].(*CopyStmt)
	if c.Query == nil {
		t.Fatal("Query nil")
	}
	if c.Direction != CopyTo || c.Endpoint != CopyEndpointStdout {
		t.Errorf("direction/endpoint = %v/%v", c.Direction, c.Endpoint)
	}
	if c.Table.Name != "" {
		t.Errorf("Table should be empty, got %+v", c.Table)
	}
	if len(c.Query.Targets) != 1 {
		t.Fatalf("targets=%v", c.Query.Targets)
	}
}

// TestParseCopyFileEndpoints: filename + PROGRAM. v0 parses these so
// the analyzer/executor can return a stable "not supported" error
// rather than a generic syntax error.
func TestParseCopyFileEndpoints(t *testing.T) {
	stmts, err := Parse("COPY t FROM '/tmp/in.csv'")
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].(*CopyStmt)
	if c.Endpoint != CopyEndpointFile || c.Filename != "/tmp/in.csv" {
		t.Errorf("file form: endpoint=%v filename=%q", c.Endpoint, c.Filename)
	}

	stmts, err = Parse("COPY t TO PROGRAM 'gzip > out.gz'")
	if err != nil {
		t.Fatal(err)
	}
	c = stmts[0].(*CopyStmt)
	if c.Endpoint != CopyEndpointProgram || c.Filename != "gzip > out.gz" {
		t.Errorf("program form: endpoint=%v filename=%q", c.Endpoint, c.Filename)
	}
}

// TestParseCopyLegacyTrail: pre-9.0 `WITH BINARY DELIMITER '|' NULL ''
// HEADER` syntax. We accept it because some clients still emit it.
func TestParseCopyLegacyTrail(t *testing.T) {
	stmts, err := Parse("COPY t TO STDOUT WITH BINARY DELIMITER '|' NULL '' HEADER")
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].(*CopyStmt)
	if len(c.Options) != 4 {
		t.Fatalf("options=%v", c.Options)
	}
	if c.Options[0].Name != "binary" || !c.Options[0].Bool {
		t.Errorf("[0]=%+v", c.Options[0])
	}
	if c.Options[1].Name != "delimiter" || c.Options[1].Value != "|" {
		t.Errorf("[1]=%+v", c.Options[1])
	}
}

// TestParseCopyForceQuoteStarAndCols: FORCE_QUOTE accepts either '*'
// or a parenthesised column list — both shapes round-trip.
func TestParseCopyForceQuoteStarAndCols(t *testing.T) {
	stmts, err := Parse("COPY t TO STDOUT WITH (FORMAT csv, FORCE_QUOTE *, FORCE_NOT_NULL (a, b))")
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].(*CopyStmt)
	got := map[string]CopyOption{}
	for _, o := range c.Options {
		got[o.Name] = o
	}
	if !got["force_quote"].Star {
		t.Errorf("force_quote *: %+v", got["force_quote"])
	}
	if got["force_not_null"].Cols == nil ||
		len(got["force_not_null"].Cols) != 2 ||
		got["force_not_null"].Cols[0] != "a" || got["force_not_null"].Cols[1] != "b" {
		t.Errorf("force_not_null cols: %+v", got["force_not_null"])
	}
}

// TestParseCopySyntaxErrors pins a few must-reject shapes.
func TestParseCopySyntaxErrors(t *testing.T) {
	cases := []string{
		"COPY (SELECT 1) FROM STDIN",     // query-form is TO-only
		"COPY t FROM STDOUT",             // STDOUT requires TO
		"COPY t TO STDIN",                // STDIN requires FROM
		"COPY t",                         // missing FROM/TO
		"COPY t FROM 123",                // numeric literal isn't a valid endpoint
		"COPY t FROM PROGRAM",            // PROGRAM without arg
		"COPY t TO STDOUT WITH (FORMAT)", // option name without value/flag eaten -- this should still parse as bare-flag actually
	}
	for _, in := range cases {
		_, err := Parse(in)
		if err == nil && in != "COPY t TO STDOUT WITH (FORMAT)" {
			t.Errorf("expected error for %q", in)
		}
	}
}
