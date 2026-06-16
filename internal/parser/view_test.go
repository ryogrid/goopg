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
