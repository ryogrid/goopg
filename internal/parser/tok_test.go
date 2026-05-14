package parser_test

import (
	"testing"
	"github.com/goopg/goopg/internal/parser"
	"fmt"
)

func TestTimeToken(t *testing.T) {
	sql := "SELECT EXTRACT(DAY FROM TIME '13:30:25')"
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sel := stmts[0].(*parser.SelectStmt)
	target := sel.Targets[0]
	extract, ok := target.Expr.(*parser.ExtractExpr)
	if !ok {
		t.Fatalf("expected ExtractExpr, got %T", target.Expr)
	}
	fmt.Printf("Extract.Field = %q\n", extract.Field)
	fmt.Printf("Extract.Source type = %T\n", extract.Source)
	if tsl, ok := extract.Source.(*parser.TypedStringLit); ok {
		fmt.Printf("TypedStringLit.Type = %q, Value = %q\n", tsl.Type, tsl.Value)
		t.Logf("TypedStringLit.Type = %q", tsl.Type)
	} else {
		t.Logf("Source is NOT TypedStringLit: %T", extract.Source)
	}
}
