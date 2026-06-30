package parser

import "testing"

// TestParseCreateViewWithExplicitColumns pins HammerDB Q15's
// shape: `CREATE OR REPLACE VIEW name (col_list) AS SELECT …`.
// The column-alias list, OrReplace flag, and inner SelectStmt
// all round-trip from source.
func TestParseCreateViewWithExplicitColumns(t *testing.T) {
	stmts, err := Parse("CREATE OR REPLACE VIEW revenue (supplier_no, total_revenue) AS SELECT l_suppkey, sum(l_extendedprice) FROM lineitem GROUP BY l_suppkey")
	if err != nil {
		t.Fatal(err)
	}
	cv := stmts[0].(*CreateViewStmt)
	if !cv.OrReplace {
		t.Errorf("OrReplace=false want true")
	}
	if cv.Name.Name != "revenue" {
		t.Errorf("Name=%q", cv.Name.Name)
	}
	if len(cv.Columns) != 2 || cv.Columns[0] != "supplier_no" || cv.Columns[1] != "total_revenue" {
		t.Errorf("Columns=%+v", cv.Columns)
	}
	if cv.Query == nil {
		t.Errorf("Query nil")
	}
}

// TestParseCreateViewRawDef pins that the raw view body text is captured
// verbatim into CreateViewStmt.RawDef (trimmed of surrounding whitespace and
// any trailing semicolon). pg_get_viewdef echoes RawDef so pg_dump can
// reconstruct `CREATE VIEW … AS <body>` (DU-002 slice 57).
func TestParseCreateViewRawDef(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{
			"CREATE VIEW v AS SELECT id, name FROM public.d WHERE id > 0",
			"SELECT id, name FROM public.d WHERE id > 0",
		},
		{
			// trailing semicolon and whitespace are trimmed
			"CREATE VIEW v AS   SELECT 1 ;",
			"SELECT 1",
		},
		{
			// the trailing WITH CHECK OPTION clause is NOT part of the body
			"CREATE VIEW v AS SELECT id FROM d WHERE id > 0 WITH CHECK OPTION",
			"SELECT id FROM d WHERE id > 0",
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.src, err)
		}
		cv, ok := stmts[0].(*CreateViewStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T, want *CreateViewStmt", tc.src, stmts[0])
		}
		if cv.RawDef != tc.want {
			t.Errorf("Parse(%q): RawDef=%q want %q", tc.src, cv.RawDef, tc.want)
		}
	}
}

// TestParseCreateViewCheckOption pins that the trailing
// `WITH [CASCADED|LOCAL] CHECK OPTION` clause is captured into
// CreateViewStmt.CheckOption ("cascaded" is the default when the qualifier is
// omitted), while a view with no clause leaves it empty. M0119-0004 (slice 365).
func TestParseCreateViewCheckOption(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"CREATE VIEW v AS SELECT id FROM d WHERE id > 0 WITH CHECK OPTION", "cascaded"},
		{"CREATE VIEW v AS SELECT id FROM d WHERE id > 0 WITH CASCADED CHECK OPTION", "cascaded"},
		{"CREATE VIEW v AS SELECT id FROM d WHERE id > 0 WITH LOCAL CHECK OPTION", "local"},
		{"CREATE VIEW v AS SELECT id FROM d WHERE id > 0", ""},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.src, err)
		}
		cv, ok := stmts[0].(*CreateViewStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T, want *CreateViewStmt", tc.src, stmts[0])
		}
		if cv.CheckOption != tc.want {
			t.Errorf("Parse(%q): CheckOption=%q want %q", tc.src, cv.CheckOption, tc.want)
		}
	}
}

// TestParseCreateMatViewRawDef pins that a materialized view's body text is
// captured verbatim into CreateMatViewStmt.RawDef (trimmed of surrounding
// whitespace; the trailing WITH [NO] DATA clause is NOT part of the body).
// pg_get_viewdef echoes RawDef so pg_dump can reconstruct `CREATE MATERIALIZED
// VIEW … AS <body> WITH NO DATA` (DU-002 slice 60).
func TestParseCreateMatViewRawDef(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{
			"CREATE MATERIALIZED VIEW mv AS SELECT id, name FROM public.d WHERE id > 0",
			"SELECT id, name FROM public.d WHERE id > 0",
		},
		{
			// the trailing WITH NO DATA clause is excluded from the body
			"CREATE MATERIALIZED VIEW mv AS SELECT 1 WITH NO DATA",
			"SELECT 1",
		},
		{
			// WITH DATA (the default) is likewise excluded
			"CREATE MATERIALIZED VIEW mv AS SELECT 1 WITH DATA",
			"SELECT 1",
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.src, err)
		}
		cv, ok := stmts[0].(*CreateMatViewStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T, want *CreateMatViewStmt", tc.src, stmts[0])
		}
		if cv.RawDef != tc.want {
			t.Errorf("Parse(%q): RawDef=%q want %q", tc.src, cv.RawDef, tc.want)
		}
	}
}

// TestParseCreateRecursiveViewRawDef pins that a recursive view synthesizes the
// wrapped-CTE form into CreateViewStmt.RawDef so pg_get_viewdef returns a
// non-empty body (a recursive view with no RawDef makes pg_dump abort the whole
// dump — the slice-57 blocker). PG stores a recursive view as a regular view
// over a WITH RECURSIVE CTE and pg_dump re-emits it as a plain CREATE VIEW;
// goopg mirrors that as `WITH RECURSIVE name(cols) AS (<body>) SELECT cols FROM
// name`. The verbatim body is wrapped, not echoed bare. (DU-002 slice 61.)
func TestParseCreateRecursiveViewRawDef(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{
			"CREATE RECURSIVE VIEW nums(n) AS SELECT 1 UNION ALL SELECT n + 1 FROM nums WHERE n < 5",
			"WITH RECURSIVE nums(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM nums WHERE n < 5) SELECT n FROM nums",
		},
		{
			// multi-column list round-trips in both the CTE header and the
			// outer projection
			"CREATE RECURSIVE VIEW t(a, b) AS SELECT 1, 2 UNION ALL SELECT a + 1, b + 1 FROM t WHERE a < 3",
			"WITH RECURSIVE t(a, b) AS (SELECT 1, 2 UNION ALL SELECT a + 1, b + 1 FROM t WHERE a < 3) SELECT a, b FROM t",
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.src, err)
		}
		cv, ok := stmts[0].(*CreateViewStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T, want *CreateViewStmt", tc.src, stmts[0])
		}
		if cv.RawDef != tc.want {
			t.Errorf("Parse(%q): RawDef=%q want %q", tc.src, cv.RawDef, tc.want)
		}
	}
}

// TestParseDropViewIfExists pins the DROP VIEW IF EXISTS shape
// HammerDB uses for cleanup.
func TestParseDropViewIfExists(t *testing.T) {
	stmts, err := Parse("DROP VIEW IF EXISTS revenue, revenue2")
	if err != nil {
		t.Fatal(err)
	}
	dv := stmts[0].(*DropViewStmt)
	if !dv.IfExists {
		t.Errorf("IfExists=false")
	}
	if len(dv.Names) != 2 {
		t.Errorf("Names=%+v", dv.Names)
	}
}

// TestParseCreateTempView pins M0097-0036: CREATE TEMP[ORARY] VIEW must
// dispatch to the view parser and set Temporary, not be mis-parsed as a
// CREATE TABLE. Regression for the functional_deps regress case, where
// `CREATE TEMP VIEW … AS SELECT … GROUP BY …` previously produced no view
// (a later DROP VIEW then failed with "view does not exist").
func TestParseCreateTempView(t *testing.T) {
	for _, src := range []string{
		"CREATE TEMP VIEW fdv1 AS SELECT id FROM articles GROUP BY id",
		"CREATE TEMPORARY VIEW fdv1 AS SELECT id FROM articles GROUP BY id",
		"CREATE LOCAL TEMP VIEW fdv1 AS SELECT id FROM articles GROUP BY id",
	} {
		stmts, err := Parse(src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		cv, ok := stmts[0].(*CreateViewStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T, want *CreateViewStmt", src, stmts[0])
		}
		if !cv.Temporary {
			t.Errorf("Parse(%q): Temporary=false want true", src)
		}
		if cv.Name.Name != "fdv1" {
			t.Errorf("Parse(%q): Name=%q want fdv1", src, cv.Name.Name)
		}
		if cv.Query == nil {
			t.Errorf("Parse(%q): Query nil", src)
		}
	}
}

// TestParseCreateTempSequence pins that CREATE TEMP SEQUENCE dispatches to
// the sequence parser (Temporary=true) rather than the table parser.
func TestParseCreateTempSequence(t *testing.T) {
	stmts, err := Parse("CREATE TEMP SEQUENCE s1")
	if err != nil {
		t.Fatal(err)
	}
	cs, ok := stmts[0].(*CreateSequenceStmt)
	if !ok {
		t.Fatalf("got %T, want *CreateSequenceStmt", stmts[0])
	}
	if !cs.Temporary {
		t.Errorf("Temporary=false want true")
	}
}
