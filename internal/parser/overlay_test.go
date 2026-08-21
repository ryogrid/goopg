package parser

import "testing"

// TestParseOverlayThreeArg covers the 3-arg SQL-standard form
// OVERLAY(str PLACING replacement FROM start), which desugars to
// overlay(str, replacement, start). M0134-0070.
func TestParseOverlayThreeArg(t *testing.T) {
	stmts, err := Parse(`SELECT overlay('abcdef' placing '45' from 4)`)
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target=%T want *FuncCall", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if fc.Name.Name != "overlay" {
		t.Errorf("name=%q want overlay", fc.Name.Name)
	}
	if len(fc.Args) != 3 {
		t.Fatalf("args=%v want 3", fc.Args)
	}
	str, ok := fc.Args[0].(*StringConst)
	if !ok || str.Value != "abcdef" {
		t.Errorf("args[0]=%#v want StringConst(abcdef)", fc.Args[0])
	}
	repl, ok := fc.Args[1].(*StringConst)
	if !ok || repl.Value != "45" {
		t.Errorf("args[1]=%#v want StringConst(45)", fc.Args[1])
	}
	start, ok := fc.Args[2].(*IntegerConst)
	if !ok || start.Value != 4 {
		t.Errorf("args[2]=%#v want IntegerConst(4)", fc.Args[2])
	}
}

// TestParseOverlayFourArg covers the 4-arg form with an explicit FOR count.
func TestParseOverlayFourArg(t *testing.T) {
	stmts, err := Parse(`SELECT OVERLAY('babosa' PLACING 'ubb' FROM 2 FOR 4)`)
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target=%T want *FuncCall", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if fc.Name.Name != "overlay" {
		t.Errorf("name=%q want overlay", fc.Name.Name)
	}
	if len(fc.Args) != 4 {
		t.Fatalf("args=%v want 4", fc.Args)
	}
	count, ok := fc.Args[3].(*IntegerConst)
	if !ok || count.Value != 4 {
		t.Errorf("args[3]=%#v want IntegerConst(4)", fc.Args[3])
	}
}

// TestParseOverlayBytea covers the bytea fixture form used by
// strings.sql:900-902, where PLACING/FROM/FOR appear lowercase.
func TestParseOverlayBytea(t *testing.T) {
	stmts, err := Parse(`SELECT overlay(E'Th\\000omas'::bytea placing E'\\002\\003'::bytea from 5 for 3)`)
	if err != nil {
		t.Fatal(err)
	}
	fc, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target=%T want *FuncCall", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if fc.Name.Name != "overlay" {
		t.Errorf("name=%q want overlay", fc.Name.Name)
	}
	if len(fc.Args) != 4 {
		t.Fatalf("args=%v want 4", fc.Args)
	}
}

// TestParseOverlayMissingPlacingError covers the syntax-error case when
// PLACING is missing.
func TestParseOverlayMissingPlacingError(t *testing.T) {
	_, err := Parse(`SELECT overlay('abcdef' '45' from 4)`)
	if err == nil {
		t.Fatal("expected parse error for missing PLACING")
	}
}
