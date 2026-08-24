package parser

import "testing"

// TestParseJSONTypedLiteral pins the `json '...'` / `jsonb '...'` SQL-standard
// typed-literal grammar (M0134-0133). Before this, `json`/`jsonb` were absent
// from tryTypedLiteral's whitelist entirely, so ANY typed literal of either
// type — including the plain `json '{"a": 1}'` form with no surrounding CAST —
// fell through to parseColumnOrCall and hit "syntax error at or near '...'"
// on the string literal, e.g. `json '{ "a": "\ud83d..." }' -> 'a'`, used
// throughout postgres/src/test/regress/sql/json_encoding.sql.
func TestParseJSONTypedLiteral(t *testing.T) {
	cases := []struct {
		sql, wantType, wantValue string
	}{
		{`SELECT json '{"a": 1}' FROM t`, "json", `{"a": 1}`},
		{`SELECT jsonb '{"a": 1}' FROM t`, "jsonb", `{"a": 1}`},
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

// TestParseJSONTypedLiteralWithOperator pins the exact json_encoding.sql
// shape that motivated this fix: a `json '...'`/`jsonb '...'` typed literal
// immediately followed by an operator (`->`, `->>`) must parse — before the
// fix these all hit "syntax error at or near '<the string literal>'" because
// `json`/`jsonb` were missing from tryTypedLiteral's whitelist, so the parser
// fell back to parseColumnOrCall and choked on the following string token.
func TestParseJSONTypedLiteralWithOperator(t *testing.T) {
	sqls := []string{
		`SELECT json '{ "a":  "😄" }' -> 'a' FROM t`,
		`SELECT jsonb '{ "a":  "the Copyright © sign" }' ->> 'a' FROM t`,
	}
	for _, sql := range sqls {
		if _, err := Parse(sql); err != nil {
			t.Errorf("Parse(%q): %v", sql, err)
		}
	}
}
