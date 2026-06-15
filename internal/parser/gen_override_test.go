package parser

import (
	"testing"
)

func TestPartitionColGeneratedExprParsed(t *testing.T) {
	sql := `CREATE TABLE parttbl2 PARTITION OF parttbl
  (d WITH OPTIONS GENERATED ALWAYS AS (a + b + 1000) STORED)
  FOR VALUES IN (2)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(stmts) == 0 {
		t.Fatal("no stmts")
	}
	ct := stmts[0].(*CreateTableStmt)
	poc := ct.PartitionOf
	if poc == nil {
		t.Fatal("no PartitionOf")
	}
	if len(poc.ColGeneratedExprs) != 1 {
		t.Fatalf("expected 1 ColGeneratedExprs, got %d: %v", len(poc.ColGeneratedExprs), poc.ColGeneratedExprs)
	}
	cg := poc.ColGeneratedExprs[0]
	if cg.ColName != "d" {
		t.Errorf("expected ColName=d, got %q", cg.ColName)
	}
	if cg.Expr != "a + b + 1000" {
		t.Errorf("expected Expr='a + b + 1000', got %q", cg.Expr)
	}
}
