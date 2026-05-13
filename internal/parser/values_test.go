package parser

import (
    "testing"
    "fmt"
)

func TestParseValuesSubquery(t *testing.T) {
    sql := "SELECT x, x::int2 AS int2_value FROM (VALUES (-2.5::float8)) t(x)"
    stmts, err := Parse(sql)
    if err != nil {
        t.Fatalf("Parse error: %v", err)
    }
    if len(stmts) != 1 {
        t.Fatalf("Expected 1 statement, got %d", len(stmts))
    }
    sel, ok := stmts[0].(*SelectStmt)
    if !ok {
        t.Fatal("Not a SelectStmt")
    }
    t.Logf("Targets: %d", len(sel.Targets))
    t.Logf("From: %d", len(sel.From))
    for i, rv := range sel.From {
        t.Logf("From[%d]: alias=%q columns=%v subquery=%v", i, rv.Alias, rv.Columns, rv.Subquery != nil)
        if rv.Subquery != nil {
            t.Logf("  ValuesRows: %d", len(rv.Subquery.ValuesRows))
            for j, row := range rv.Subquery.ValuesRows {
                t.Logf("  Row[%d]: %v", j, fmt.Sprintf("%v", row))
            }
        }
    }
}
