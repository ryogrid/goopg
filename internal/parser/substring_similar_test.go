package parser

import "testing"

// TestParseSubstringSimilarConstantFold pins the M0134-0070 constant-fold
// requirement: SUBSTRING(str SIMILAR pattern ESCAPE escape) with literal
// str/pattern/escape must desugar at PARSE time into a plain 2-arg
// substring(str, <converted-pattern>) FuncCall — mirroring real PG's planner
// constant-folding of the SQL wrapper
// `substring(text,text,text) RETURN substring($1, similar_to_escape($2, $3))`
// (postgres/src/backend/catalog/system_functions.sql). Expected converted
// patterns and results are pinned against
// postgres/src/test/regress/expected/strings.out ("T581 regular expression
// substring"). See docs/design/m0134-0070-substring-similar-escape.md.
func TestParseSubstringSimilarConstantFold(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantPat string
	}{
		{
			"two-separators",
			`SELECT SUBSTRING('abcdefg' SIMILAR 'a#"(b_d)#"%' ESCAPE '#')`,
			`^(?:a){1,1}?((?:b.d)){1,1}(?:.*)$`,
		},
		{
			"two-separators-leading-quote",
			`SELECT SUBSTRING('abcdefg' SIMILAR '#"(b_d)#"%' ESCAPE '#')`,
			`^(?:){1,1}?((?:b.d)){1,1}(?:.*)$`,
		},
		{
			"two-separators-star-parts",
			`SELECT SUBSTRING('abcdefg' SIMILAR 'a#"%#"g' ESCAPE '#')`,
			`^(?:a){1,1}?(.*){1,1}(?:g)$`,
		},
		{
			"two-separators-star-star-parts",
			`SELECT SUBSTRING('abcdefg' SIMILAR 'a*#"%#"g*' ESCAPE '#')`,
			`^(?:a*){1,1}?(.*){1,1}(?:g*)$`,
		},
		{
			"two-separators-alt-part1",
			`SELECT SUBSTRING('abcdefg' SIMILAR 'a|b#"%#"g' ESCAPE '#')`,
			`^(?:a|b){1,1}?(.*){1,1}(?:g)$`,
		},
		{
			"two-separators-alt-part3",
			`SELECT SUBSTRING('abcdefg' SIMILAR 'a#"%#"x|g' ESCAPE '#')`,
			`^(?:a){1,1}?(.*){1,1}(?:x|g)$`,
		},
		{
			"two-separators-alt-part2",
			`SELECT SUBSTRING('abcdefg' SIMILAR 'a#"%|ab#"g' ESCAPE '#')`,
			`^(?:a){1,1}?(.*|ab){1,1}(?:g)$`,
		},
		{
			"one-separator",
			`SELECT SUBSTRING('abcdefg' SIMILAR 'a#"%g' ESCAPE '#')`,
			`^(?:a){1,1}?(.*g)$`,
		},
		{
			"zero-separators",
			`SELECT SUBSTRING('abcdefg' SIMILAR 'a%g' ESCAPE '#')`,
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

// TestParseSubstringSimilarTooManySeparators pins ERROR 2200C for a pattern
// with more than two escape-double-quote separators (PG oracle:
// regexp.c:940-944), surfaced at parse time via the constant-fold path with
// no errposition (Pos: -1), matching buildSimilarTo's 22025 convention.
func TestParseSubstringSimilarTooManySeparators(t *testing.T) {
	_, err := Parse(`SELECT SUBSTRING('abcdefg' SIMILAR 'a*#"%#"g*#"x' ESCAPE '#')`)
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
	se, ok := err.(*SyntaxError)
	if !ok {
		t.Fatalf("err=%T(%v), want *SyntaxError", err, err)
	}
	if se.Code != "2200C" {
		t.Errorf("Code=%q, want 2200C", se.Code)
	}
	want := "SQL regular expression may not contain more than two escape-double-quote separators"
	if se.Error() != want {
		t.Errorf("Error()=%q, want %q", se.Error(), want)
	}
}

// TestParseSubstringSimilarNullPropagation pins the STRICT 3-arg NULL
// propagation: ANY of str/pattern/escape being SQL NULL folds the whole
// SUBSTRING call to NULL at parse time (PG oracle: system_functions.sql's
// substring(text,text,text) wrapper is declared STRICT).
func TestParseSubstringSimilarNullPropagation(t *testing.T) {
	cases := []string{
		`SELECT SUBSTRING('abcdefg' SIMILAR '%' ESCAPE NULL)`,
		`SELECT SUBSTRING(NULL SIMILAR '%' ESCAPE '#')`,
		`SELECT SUBSTRING('abcdefg' SIMILAR NULL ESCAPE '#')`,
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			stmts, err := Parse(sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", sql, err)
			}
			sel := stmts[0].(*SelectStmt)
			if _, ok := sel.Targets[0].Expr.(*NullConst); !ok {
				t.Fatalf("target[0]=%T(%v), want *NullConst", sel.Targets[0].Expr, sel.Targets[0].Expr)
			}
		})
	}
}
