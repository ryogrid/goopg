package parser

import "testing"

// TestParseSubstringFromForConstantFold pins the M0134-0070 obsolete-SQL99
// overload: SUBSTRING(str FROM pattern FOR escape) with literal pattern/escape
// must desugar at PARSE time into a plain 2-arg substring(str,
// <converted-pattern>) FuncCall — the SAME semantics as the SIMILAR form, since
// PG maps both grammar spellings (gram.y substr_list `a_expr FROM a_expr FOR
// a_expr` and `a_expr SIMILAR a_expr ESCAPE a_expr`) to the same 3-arg
// substring(text,text,text) SQL wrapper (system_functions.sql: `RETURN
// substring($1, similar_to_escape($2, $3))`). Expected converted patterns are
// pinned against postgres/src/test/regress/expected/strings.out:447
// ("T581 regular expression substring"). See
// docs/design/m0134-0070-substring-similar-escape.md.
func TestParseSubstringFromForConstantFold(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantPat string
	}{
		{
			"two-separators",
			`SELECT SUBSTRING('abcdefg' FROM 'a#"(b_d)#"%' FOR '#')`,
			`^(?:a){1,1}?((?:b.d)){1,1}(?:.*)$`,
		},
		{
			"one-separator",
			`SELECT SUBSTRING('abcdefg' FROM 'a#"%g' FOR '#')`,
			`^(?:a){1,1}?(.*g)$`,
		},
		{
			"zero-separators",
			`SELECT SUBSTRING('abcdefg' FROM 'a%g' FOR '#')`,
			`^(?:a.*g)$`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			fc, ok := sel.Targets[0].Expr.(*FuncCall)
			if !ok {
				t.Fatalf("target[0]=%T(%v), want *FuncCall", sel.Targets[0].Expr, sel.Targets[0].Expr)
			}
			if fc.Name.Name != "substring" {
				t.Errorf("Name=%q, want %q", fc.Name.Name, "substring")
			}
			if len(fc.Args) != 2 {
				t.Fatalf("len(Args)=%d, want 2", len(fc.Args))
			}
			tsl, ok := fc.Args[1].(*TypedStringLit)
			if !ok {
				t.Fatalf("Args[1]=%T(%v), want *TypedStringLit", fc.Args[1], fc.Args[1])
			}
			if tsl.Type != "text" {
				t.Errorf("Args[1].Type=%q, want %q", tsl.Type, "text")
			}
			if tsl.Value != c.wantPat {
				t.Errorf("Args[1].Value=%q, want %q", tsl.Value, c.wantPat)
			}
		})
	}
}

// TestParseSubstringFromForPositionForm regression: SUBSTRING(str FROM start
// FOR count) with INTEGER FROM/FOR operands must stay a plain 3-arg
// substring(str, start, count) call (position form) — integer literals fail
// similarToLiteralValue's ok check, so the string-pattern fold must not fire.
func TestParseSubstringFromForPositionForm(t *testing.T) {
	stmts, err := Parse(`SELECT SUBSTRING('abcdefg' FROM 2 FOR 3)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	fc, ok := sel.Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target[0]=%T(%v), want *FuncCall", sel.Targets[0].Expr, sel.Targets[0].Expr)
	}
	if len(fc.Args) != 3 {
		t.Fatalf("len(Args)=%d, want 3", len(fc.Args))
	}
	for i, want := range []int64{2, 3} {
		ic, ok := fc.Args[i+1].(*IntegerConst)
		if !ok {
			t.Fatalf("Args[%d]=%T(%v), want *IntegerConst", i+1, fc.Args[i+1], fc.Args[i+1])
		}
		if ic.Value != want {
			t.Errorf("Args[%d].Value=%d, want %d", i+1, ic.Value, want)
		}
		if _, isTSL := fc.Args[i+1].(*TypedStringLit); isTSL {
			t.Errorf("Args[%d] is *TypedStringLit, want integer const", i+1)
		}
	}
}

// TestParseSubstringFromForPatternOnly: the FROM-only regex form
// SUBSTRING(str FROM pattern) (no FOR) must stay a plain 2-arg
// substring(str, pattern) call — it is the regex-substring form resolved by
// overload at runtime (evalSubstr → evalSubstrRegex), not the SQL99 fold.
func TestParseSubstringFromForPatternOnly(t *testing.T) {
	stmts, err := Parse(`SELECT SUBSTRING('abcdefg' FROM 'c.e')`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	fc, ok := sel.Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target[0]=%T(%v), want *FuncCall", sel.Targets[0].Expr, sel.Targets[0].Expr)
	}
	if len(fc.Args) != 2 {
		t.Fatalf("len(Args)=%d, want 2", len(fc.Args))
	}
	sc, ok := fc.Args[1].(*StringConst)
	if !ok {
		t.Fatalf("Args[1]=%T(%v), want *StringConst", fc.Args[1], fc.Args[1])
	}
	if sc.Value != "c.e" {
		t.Errorf("Args[1].Value=%q, want %q", sc.Value, "c.e")
	}
	if _, isTSL := fc.Args[1].(*TypedStringLit); isTSL {
		t.Errorf("Args[1] is *TypedStringLit, want plain *StringConst")
	}
}
