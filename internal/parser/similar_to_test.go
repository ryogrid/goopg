package parser

import "testing"

// TestParseSimilarToConstantFold pins the M0134-0070 constant-fold
// requirement: `expr SIMILAR TO pattern [ESCAPE escape]` with a literal
// pattern/escape must desugar at PARSE time into a plain
// BinaryOp{Op: OpRegexMatch, Right: TypedStringLit{Type:"text", ...}} —
// the same shape real PG's planner constant-folding produces for
// similar_to_escape(pattern[, escape]), so EXPLAIN shows the converted POSIX
// pattern directly (postgres/src/test/regress/expected/strings.out:617-690).
func TestParseSimilarToConstantFold(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantOp  OpCode
		wantPat string
	}{
		{"plain", `SELECT 'abcdefg' SIMILAR TO '_bcd%'`, OpRegexMatch, "^(?:.bcd.*)$"},
		{"percent-and-underscore", `SELECT x SIMILAR TO '%bc_d%'`, OpRegexMatch, "^(?:.*bc.d.*)$"},
		{"custom-escape", `SELECT 'abcd%' SIMILAR TO '_bcd#%' ESCAPE '#'`, OpRegexMatch, `^(?:.bcd\%)$`},
		{"default-escape-backslash", `SELECT x SIMILAR TO '_bcd\%'`, OpRegexMatch, `^(?:.bcd\%)$`},
		{"class-underscore", `SELECT f1 SIMILAR TO '_[_[:alpha:]_]_'`, OpRegexMatch, "^(?:.[_[:alpha:]_].)$"},
		{"class-percent", `SELECT f1 SIMILAR TO '%[%[:alnum:]%]%'`, OpRegexMatch, "^(?:.*[%[:alnum:]%].*)$"},
		{"class-dot", `SELECT f1 SIMILAR TO '.[.[:alnum:].].'`, OpRegexMatch, `^(?:\.[.[:alnum:].]\.)$`},
		{"class-dollar", `SELECT f1 SIMILAR TO '$[$[:alnum:]$]$'`, OpRegexMatch, `^(?:\$[$[:alnum:]$]\$)$`},
		{"class-paren", `SELECT f1 SIMILAR TO '()[([:alnum:](]()'`, OpRegexMatch, `^(?:(?:)[([:alnum:](](?:))$`},
		{"class-caret", `SELECT f1 SIMILAR TO '^[^[:alnum:]^[^^][[^^]][\^][[\^]]\^]^'`, OpRegexMatch,
			`^(?:\^[^[:alnum:]^[^^][[^^]][\^][[\^]]\^]\^)$`},
		{"class-close-bracket", `SELECT f1 SIMILAR TO '[]%][^]%][^%]%'`, OpRegexMatch, `^(?:[]%][^]%][^%].*)$`},
		{"class-close-bracket-caret", `SELECT f1 SIMILAR TO '[^^]^'`, OpRegexMatch, `^(?:[^^]\^)$`},
		{"class-escape-then-close", `SELECT f1 SIMILAR TO '[|a]%' ESCAPE '|'`, OpRegexMatch, `^(?:[\a].*)$`},
		{"not-similar-to", `SELECT 'abcdefg' NOT SIMILAR TO 'bcd%'`, OpRegexNoMatch, "^(?:bcd.*)$"},
		{"escape-empty-no-escape", `SELECT 'abcd\efg' SIMILAR TO '_bcd\%' ESCAPE ''`, OpRegexMatch, `^(?:.bcd\\.*)$`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			bo, ok := sel.Targets[0].Expr.(*BinaryOp)
			if !ok {
				t.Fatalf("target[0]=%T(%v), want *BinaryOp", sel.Targets[0].Expr, sel.Targets[0].Expr)
			}
			if bo.Op != c.wantOp {
				t.Errorf("Op=%v, want %v", bo.Op, c.wantOp)
			}
			tsl, ok := bo.Right.(*TypedStringLit)
			if !ok {
				t.Fatalf("Right=%T(%v), want *TypedStringLit", bo.Right, bo.Right)
			}
			if tsl.Type != "text" {
				t.Errorf("Right.Type=%q, want %q", tsl.Type, "text")
			}
			if tsl.Value != c.wantPat {
				t.Errorf("Right.Value=%q, want %q", tsl.Value, c.wantPat)
			}
		})
	}
}

// TestParseSimilarToEscapeNull pins the STRICT-function NULL propagation:
// `x SIMILAR TO pattern ESCAPE NULL` folds the whole expression to NULL at
// parse time (PG oracle: similar_escape_internal is called through a STRICT
// SQL function, no PG_ARGISNULL special-casing). strings.out:611-613.
func TestParseSimilarToEscapeNull(t *testing.T) {
	stmts, err := Parse(`SELECT 'abcdefg' SIMILAR TO '_bcd%' ESCAPE NULL`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	if _, ok := sel.Targets[0].Expr.(*NullConst); !ok {
		t.Fatalf("target[0]=%T(%v), want *NullConst", sel.Targets[0].Expr, sel.Targets[0].Expr)
	}
}

// TestParseSimilarToEscapeTooLong pins ERROR 22025 for a >1-character
// ESCAPE string, raised immediately at parse time (constant-fold path).
// PG oracle: regexp.c:797-806, strings.out:614-616.
func TestParseSimilarToEscapeTooLong(t *testing.T) {
	_, err := Parse(`SELECT 'abcdefg' SIMILAR TO '_bcd#%' ESCAPE '##'`)
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
	se, ok := err.(*SyntaxError)
	if !ok {
		t.Fatalf("err=%T(%v), want *SyntaxError", err, err)
	}
	if se.Code != "22025" {
		t.Errorf("Code=%q, want 22025", se.Code)
	}
	if se.Hint != "Escape string must be empty or one character." {
		t.Errorf("Hint=%q, want the PG hint text", se.Hint)
	}
	if se.Error() != "invalid escape string" {
		t.Errorf("Error()=%q, want %q", se.Error(), "invalid escape string")
	}
}
