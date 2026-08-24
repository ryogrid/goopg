package parser

import "testing"

// TestParseIsnullNotnullPostfix pins the historical postfix synonyms
// `expr ISNULL` / `expr NOTNULL` for `expr IS [NOT] NULL`
// (M0134-0112, kwlist.h: ISNULL/NOTNULL are TYPE_FUNC_NAME_KEYWORD;
// gram.y: `a_expr ISNULL` / `a_expr NOTNULL`). Both desugar to the same
// *IsNullExpr node the `IS [NOT] NULL` spelling produces, just with
// Negated set directly instead of via a NOT keyword.
func TestParseIsnullNotnullPostfix(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x ISNULL")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	isNull, ok := sel.Where.(*IsNullExpr)
	if !ok {
		t.Fatalf("WHERE=%T(%v), want *IsNullExpr", sel.Where, sel.Where)
	}
	if isNull.Negated {
		t.Errorf("ISNULL: Negated=true, want false")
	}
}

func TestParseNotnullPostfix(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x NOTNULL")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	isNull, ok := sel.Where.(*IsNullExpr)
	if !ok {
		t.Fatalf("WHERE=%T(%v), want *IsNullExpr", sel.Where, sel.Where)
	}
	if !isNull.Negated {
		t.Errorf("NOTNULL: Negated=false, want true")
	}
}

// TestParseIsnullNotnullEquivalentToIsNull checks the postfix forms
// produce the same shape as the canonical `IS [NOT] NULL` spelling.
func TestParseIsnullNotnullEquivalentToIsNull(t *testing.T) {
	stmtsA, err := Parse("SELECT * FROM t WHERE x ISNULL")
	if err != nil {
		t.Fatal(err)
	}
	stmtsB, err := Parse("SELECT * FROM t WHERE x IS NULL")
	if err != nil {
		t.Fatal(err)
	}
	a := stmtsA[0].(*SelectStmt).Where.(*IsNullExpr)
	b := stmtsB[0].(*SelectStmt).Where.(*IsNullExpr)
	if a.Negated != b.Negated {
		t.Errorf("ISNULL.Negated=%v, IS NULL.Negated=%v, want equal", a.Negated, b.Negated)
	}
}
