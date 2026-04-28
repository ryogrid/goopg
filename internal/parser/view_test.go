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
