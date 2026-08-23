package parser

import "testing"

// TestParseBitStringLiteral pins the B'...'/X'...' bit-string literal syntax
// (M0134-0092; PG scan.l xbstart/xhstart, gram.y AexprConst BCONST/XCONST →
// makeBitStringConst). goopg decodes the literal to canonical binary-digit
// text at parse time (decodeBitStringLit, expr.go) and represents it as a
// plain *StringConst so it flows through the existing bit(n)/varbit(n)
// column coercion (internal/executor/codec.go's coerceTextLikeDatum) —
// deliberately NOT a dedicated bit-typed AST node; see decodeBitStringLit's
// doc comment for the tracked simplification.
func TestParseBitStringLiteral(t *testing.T) {
	cases := []struct {
		sql, want string
	}{
		{"SELECT B'1010'", "1010"},
		{"SELECT b'1010'", "1010"},
		{"SELECT B''", ""},
		{"SELECT X'A'", "1010"},
		{"SELECT x'a'", "1010"},
		{"SELECT X'FF'", "11111111"},
		{"SELECT X'0'", "0000"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		expr := stmts[0].(*SelectStmt).Targets[0].Expr
		sc, ok := expr.(*StringConst)
		if !ok {
			t.Errorf("%q: expr=%T, want *StringConst", tc.sql, expr)
			continue
		}
		if sc.Value != tc.want {
			t.Errorf("%q: Value=%q want %q", tc.sql, sc.Value, tc.want)
		}
	}
}

// TestParseBitStringLiteralInvalidDigit pins the eager digit-set validation
// PG's bit_in/varbit_in perform when the parser builds the A_Const
// (parse_node.c transformExprRecurse T_BitString) — an invalid digit errors
// immediately, with SQLSTATE 22P02 and an errposition at the literal's
// start, regardless of what (if anything) the literal is later assigned to.
func TestParseBitStringLiteralInvalidDigit(t *testing.T) {
	cases := []struct {
		sql, wantMsg string
		wantPos      int
	}{
		{"SELECT b' 0'", `" " is not a valid binary digit`, 7},
		{"SELECT b'2'", `"2" is not a valid binary digit`, 7},
		{"SELECT x' 0'", `" " is not a valid hexadecimal digit`, 7},
		{"SELECT x'Z'", `"Z" is not a valid hexadecimal digit`, 7},
	}
	for _, tc := range cases {
		_, err := Parse(tc.sql)
		if err == nil {
			t.Errorf("Parse(%q): want error, got nil", tc.sql)
			continue
		}
		se, ok := err.(*SyntaxError)
		if !ok {
			t.Errorf("Parse(%q): err=%T, want *SyntaxError", tc.sql, err)
			continue
		}
		if se.Code != "22P02" {
			t.Errorf("Parse(%q): Code=%q want 22P02", tc.sql, se.Code)
		}
		if se.Message != tc.wantMsg {
			t.Errorf("Parse(%q): Message=%q want %q", tc.sql, se.Message, tc.wantMsg)
		}
		if se.Pos != tc.wantPos {
			t.Errorf("Parse(%q): Pos=%d want %d", tc.sql, se.Pos, tc.wantPos)
		}
		if !se.Raw {
			t.Errorf("Parse(%q): Raw=false, want true (message must not get the generic \"syntax error at or near\" wrapper)", tc.sql)
		}
	}
}
