package parser

import "testing"

// TestParseNormalizeFuncCall covers NORMALIZE(str [, form]) — gram.y
// :15896ff, COERCE_SQL_SYNTAX rewrite to normalize(text[, text]).
// M0134-0184 (unicode.sql).
func TestParseNormalizeFuncCall(t *testing.T) {
	stmts, err := Parse(`SELECT NORMALIZE('abc')`)
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target=%T want *FuncCall", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if fc.Name.Name != "normalize" {
		t.Errorf("name=%q want normalize", fc.Name.Name)
	}
	if len(fc.Args) != 1 {
		t.Fatalf("args=%v want 1", fc.Args)
	}
	if fc.Variadic != nil {
		t.Errorf("Variadic=%v want nil (specialFormCall)", fc.Variadic)
	}
}

// TestParseNormalizeFuncCallWithForm covers the 2-arg spelling with an
// explicit normalization form (bare identifier, not a string literal).
func TestParseNormalizeFuncCallWithForm(t *testing.T) {
	stmts, err := Parse(`SELECT NORMALIZE('abc', NFKC)`)
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target=%T want *FuncCall", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if fc.Name.Name != "normalize" {
		t.Errorf("name=%q want normalize", fc.Name.Name)
	}
	if len(fc.Args) != 2 {
		t.Fatalf("args=%v want 2", fc.Args)
	}
	form, ok := fc.Args[1].(*StringConst)
	if !ok || form.Value != "NFKC" {
		t.Errorf("args[1]=%#v want StringConst(NFKC)", fc.Args[1])
	}
}

// TestParseIsNormalized covers the SQL-standard predicate spelling
// `a_expr IS [NOT] [form] NORMALIZED` — gram.y :15364-15393, rewritten to
// is_normalized(...) (NOT-wrapped for the negated forms). M0134-0184.
func TestParseIsNormalized(t *testing.T) {
	cases := []struct {
		sql      string
		wantArgs int
		wantForm string // "" if no form arg expected
		wantNot  bool
	}{
		{`SELECT x IS NORMALIZED FROM t`, 1, "", false},
		{`SELECT x IS NFC NORMALIZED FROM t`, 2, "NFC", false},
		{`SELECT x IS NOT NORMALIZED FROM t`, 1, "", true},
		{`SELECT x IS NOT NFD NORMALIZED FROM t`, 2, "NFD", true},
	}
	for _, c := range cases {
		stmts, err := Parse(c.sql)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		expr := stmts[0].(*SelectStmt).Targets[0].Expr
		if c.wantNot {
			un, ok := expr.(*UnaryOp)
			if !ok || un.Op != OpNot {
				t.Fatalf("%s: expr=%#v want *UnaryOp(OpNot)", c.sql, expr)
			}
			expr = un.Operand
		}
		fc, ok := expr.(*FuncCall)
		if !ok {
			t.Fatalf("%s: expr=%T want *FuncCall", c.sql, expr)
		}
		if fc.Name.Name != "is_normalized" {
			t.Errorf("%s: name=%q want is_normalized", c.sql, fc.Name.Name)
		}
		if len(fc.Args) != c.wantArgs {
			t.Fatalf("%s: args=%v want %d", c.sql, fc.Args, c.wantArgs)
		}
		if c.wantForm != "" {
			form, ok := fc.Args[1].(*StringConst)
			if !ok || form.Value != c.wantForm {
				t.Errorf("%s: args[1]=%#v want StringConst(%s)", c.sql, fc.Args[1], c.wantForm)
			}
		}
	}
}

// TestParseUnicodeNormalizeParity pins the golden AST dumps for the forms
// above, plus the two 0-arg/1-arg builtins that need no grammar changes at
// all (they parse as ordinary function calls).
func TestParseUnicodeNormalizeParity(t *testing.T) {
	for _, q := range []string{
		`SELECT NORMALIZE('abc')`,
		`SELECT NORMALIZE('abc', NFC)`,
		`SELECT NORMALIZE('abc', NFD)`,
		`SELECT NORMALIZE('abc', NFKC)`,
		`SELECT NORMALIZE('abc', NFKD)`,
		`SELECT x IS NORMALIZED FROM t`,
		`SELECT x IS NFC NORMALIZED FROM t`,
		`SELECT x IS NOT NORMALIZED FROM t`,
		`SELECT x IS NOT NFKD NORMALIZED FROM t`,
		`SELECT unicode_version()`,
		`SELECT unicode_assigned('abc')`,
		`SELECT is_normalized('abc', 'def')`,
	} {
		assertParity(t, q)
	}
}
