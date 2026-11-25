package parser

import (
	"strings"
	"testing"
)

// TestTypedLiteralPositionIsTheString pins makeStringConstCast: a typed literal's
// location is its string, which is where an input error's caret goes.
func TestTypedLiteralPositionIsTheString(t *testing.T) {
	for _, sql := range []string{
		"select date 'x'",
		"select timestamp with time zone 'x'",
		"select time without time zone 'x'",
		"select char(3) 'x'",
	} {
		stmts, err := Parse(sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		lit, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*TypedStringLit)
		if !ok {
			t.Fatalf("%s: target is %T", sql, stmts[0].(*SelectStmt).Targets[0].Expr)
		}
		if want := strings.Index(sql, "'"); lit.Pos() != want {
			t.Errorf("%s: position %d, want %d", sql, lit.Pos(), want)
		}
	}
}
