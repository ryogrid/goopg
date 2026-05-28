package parser_test

import (
	"fmt"
	"testing"
	"github.com/goopg/goopg/internal/parser"
)

func TestRowInParse(t *testing.T) {
	stmts, err := parser.Parse("select 1 from (select 1 as unique1, 2 as ten) onek where (unique1,ten) in (values (1,1))")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sel := stmts[0].(*parser.SelectStmt)
	fmt.Printf("WHERE: %T\n", sel.Where)
	inExpr, ok := sel.Where.(*parser.InExpr)
	if !ok {
		t.Fatalf("WHERE is not InExpr, got %T", sel.Where)
	}
	fmt.Printf("InExpr.Operand: %T\n", inExpr.Operand)
	fmt.Printf("InExpr.Subquery != nil: %v\n", inExpr.Subquery != nil)
	if inExpr.Subquery != nil {
		fmt.Printf("ValuesRows len: %d\n", len(inExpr.Subquery.ValuesRows))
	}
	if _, isRow := inExpr.Operand.(*parser.RowExpr); !isRow {
		t.Errorf("Operand is not *parser.RowExpr, got %T", inExpr.Operand)
	}
	if inExpr.Subquery == nil || len(inExpr.Subquery.ValuesRows) == 0 {
		t.Error("Subquery or ValuesRows not set")
	}
}
