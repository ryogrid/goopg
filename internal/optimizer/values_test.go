package optimizer

import (
    "testing"
    "github.com/goopg/goopg/internal/catalog"
    "github.com/goopg/goopg/internal/parser"
)

func TestValuesSubquery(t *testing.T) {
    sql := "SELECT x FROM (VALUES (42)) t(x)"
    stmts, err := parser.Parse(sql)
    if err != nil {
        t.Fatalf("Parse error: %v", err)
    }
    cat := catalog.NewInMemory()
    node, err := Plan(stmts[0], cat)
    if err != nil {
        t.Fatalf("Plan error: %v", err)
    }
    t.Logf("Plan: %T, schema=%v", node, node.Output())
}
