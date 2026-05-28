package parser_test

import (
	"fmt"
	"testing"
	"github.com/goopg/goopg/internal/parser"
)

func TestLateralValuesParse(t *testing.T) {
	stmts, err := parser.Parse("SELECT * FROM nocols n, LATERAL (VALUES(n.*)) v")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sel := stmts[0].(*parser.SelectStmt)
	fmt.Printf("len(From): %d\n", len(sel.From))
	fmt.Printf("len(FromExprs): %d\n", len(sel.FromExprs))
	if len(sel.From) > 0 {
		for i, rv := range sel.From {
			fmt.Printf("From[%d]: Name=%q Alias=%q Sub=%v ValuesRows=%d\n", i, rv.Name, rv.Alias, rv.Subquery != nil, func() int { if rv.Subquery != nil { return len(rv.Subquery.ValuesRows) }; return 0 }())
			if rv.Subquery != nil && len(rv.Subquery.ValuesRows) > 0 {
				row := rv.Subquery.ValuesRows[0]
				for j, e := range row {
					fmt.Printf("  row[0][%d]: %T\n", j, e)
					if star, ok := e.(*parser.StarExpr); ok {
						fmt.Printf("    StarExpr.Table=%q\n", star.Table)
					}
				}
			}
		}
	}
	if len(sel.FromExprs) > 0 {
		for i, fe := range sel.FromExprs {
			fmt.Printf("FromExprs[%d]: Base=%q SubAlias=%q\n", i, fe.Base.Name, fe.Base.Alias)
		}
	}
}
