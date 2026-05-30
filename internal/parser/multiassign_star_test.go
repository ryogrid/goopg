package parser

import (
	"fmt"
	"testing"
)

func TestMultiAssignStarExpr(t *testing.T) {
	for _, sql := range []string{
		`UPDATE t SET (a,b) = (v.*) FROM (VALUES(21, 101)) AS v(i, j) WHERE t.a = v.i`,
		`UPDATE t SET (a,b) = ROW(v.*) FROM (VALUES(21, 100)) AS v(i, j) WHERE t.a = v.i`,
	} {
		stmts, err := Parse(sql)
		if err != nil {
			t.Logf("sql=%q parse error: %v", sql, err)
			continue
		}
		for _, stmt := range stmts {
			upd, ok := stmt.(*UpdateStmt)
			if !ok {
				continue
			}
			for i, a := range upd.Set {
				t.Logf("sql=%q assign[%d]: columns=%v expr type=%T", sql, i, a.Columns, a.Expr)
				if r, ok := a.Expr.(*RowExpr); ok {
					for j, e := range r.Elems {
						t.Logf("  elem[%d]: %T = %v", j, e, fmt.Sprintf("%+v", e))
					}
				}
			}
		}
	}
}
