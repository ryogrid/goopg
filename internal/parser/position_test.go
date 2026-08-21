package parser

import "testing"

// TestParsePositionInForm covers the SQL-standard POSITION(substring IN
// string) form, which desugars to the existing two-arg strpos(string,
// substring) FuncCall dispatch (internal/executor/expr.go, `case "strpos",
// "position":` — Args[0] is the haystack string, Args[1] is the needle
// substring). The IN form is written substring-first in source, so the
// desugared args are reordered to {str, sub} to match. M0134-0070.
func TestParsePositionInForm(t *testing.T) {
	stmts, err := Parse(`SELECT position('om' in 'Thomas')`)
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target=%T want *FuncCall", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if fc.Name.Name != "position" {
		t.Errorf("name=%q want position", fc.Name.Name)
	}
	if len(fc.Args) != 2 {
		t.Fatalf("args=%v want 2", fc.Args)
	}
	str, ok := fc.Args[0].(*StringConst)
	if !ok || str.Value != "Thomas" {
		t.Errorf("args[0]=%#v want StringConst(Thomas)", fc.Args[0])
	}
	sub, ok := fc.Args[1].(*StringConst)
	if !ok || sub.Value != "om" {
		t.Errorf("args[1]=%#v want StringConst(om)", fc.Args[1])
	}
}

// TestParsePositionCommaForm is a regression guard: the pre-existing comma
// form POSITION(sub, str) must keep parsing identically now that
// parsePositionFuncCall intercepts all `position(...)` calls.
func TestParsePositionCommaForm(t *testing.T) {
	stmts, err := Parse(`SELECT position('a', 'bar')`)
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target=%T want *FuncCall", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if fc.Name.Name != "position" {
		t.Errorf("name=%q want position", fc.Name.Name)
	}
	if len(fc.Args) != 2 {
		t.Fatalf("args=%v want 2", fc.Args)
	}
	sub, ok := fc.Args[0].(*StringConst)
	if !ok || sub.Value != "a" {
		t.Errorf("args[0]=%#v want StringConst(a)", fc.Args[0])
	}
	str, ok := fc.Args[1].(*StringConst)
	if !ok || str.Value != "bar" {
		t.Errorf("args[1]=%#v want StringConst(bar)", fc.Args[1])
	}
}

// TestParsePositionMissingCloseParenError covers the syntax-error case,
// mirroring the analogous coverage precedent for SUBSTRING/TRIM: a POSITION
// call missing its closing paren should surface a parse error rather than
// silently mis-parsing.
func TestParsePositionMissingCloseParenError(t *testing.T) {
	_, err := Parse(`SELECT position('om' in 'Thomas'`)
	if err == nil {
		t.Fatal("expected parse error for missing ')'")
	}
}
