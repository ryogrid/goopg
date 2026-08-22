package parser

import (
	"testing"
)

// TestParseCharTypmodLiteral pins the `typename ( typmod ) 'string'`
// typed-literal grammar for the single-int-typmod character types (M0134-0070):
// char(N), varchar(N), bpchar(N) parse to a TypedStringLit holding the raw
// string — the typmod is deliberately ignored (no bpchar blank-padding or
// truncation; the only fixture lines this matters for, strings.sql:550/552,
// concatenate, where PG's expected output is unpadded). Mirrors the
// `interval ( p ) 'lit'` arm; PG grammar `character '(' Iconst ')' Sconst` →
// makeStringConstCast (gram.y), padding applied later at coercion.
func TestParseCharTypmodLiteral(t *testing.T) {
	cases := []struct {
		sql, wantType, wantValue string
	}{
		{"SELECT char(20) 'characters' FROM t", "char", "characters"},
		{"SELECT varchar(10) 'foo' FROM t", "varchar", "foo"},
		{"SELECT bpchar(5) 'x' FROM t", "bpchar", "x"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		expr := stmts[0].(*SelectStmt).Targets[0].Expr
		tsl, ok := expr.(*TypedStringLit)
		if !ok {
			t.Errorf("%q: expr=%T, want *TypedStringLit", tc.sql, expr)
			continue
		}
		if tsl.Type != tc.wantType || tsl.Value != tc.wantValue {
			t.Errorf("%q: typed=%+v want Type=%q Value=%q", tc.sql, tsl, tc.wantType, tc.wantValue)
		}
	}
}

// Negative guard: the paren-typmod form is restricted to char/bpchar/varchar
// and to a single integer literal between the parens. text(5) / numeric(5,2) /
// char(x) must fall through to the normal parse (function call or a parse
// error) and never mis-consume tokens as a TypedStringLit.
func TestParseCharTypmodLiteralNegative(t *testing.T) {
	sqls := []string{
		"SELECT text(5) 'foo' FROM t",      // text must NOT take the paren form
		"SELECT numeric(5,2) 'foo' FROM t", // multi-arg numeric typmod
		"SELECT char(x) 'foo' FROM t",      // non-int typmod
	}
	for _, sql := range sqls {
		stmts, err := Parse(sql)
		if err != nil {
			continue // fell through to normal parse and errored — acceptable
		}
		expr := stmts[0].(*SelectStmt).Targets[0].Expr
		if _, ok := expr.(*TypedStringLit); ok {
			t.Errorf("%q: parsed as TypedStringLit %+v, want fall-through", sql, expr)
		}
	}
}
