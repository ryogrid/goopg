package parser

import (
	"testing"
)

// TestParseCaseExprSearchedForm pins the TPC-H Q1 shape:
// `CASE WHEN cond THEN result … ELSE result END`. Operand is
// nil for the searched form.
func TestParseCaseExprSearchedForm(t *testing.T) {
	stmts, err := Parse("SELECT CASE WHEN x = 1 THEN 'a' WHEN x = 2 THEN 'b' ELSE 'c' END FROM t")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	tgt := sel.Targets[0].Expr.(*CaseExpr)
	if tgt.Operand != nil {
		t.Errorf("searched form should have nil Operand, got %T", tgt.Operand)
	}
	if len(tgt.Whens) != 2 {
		t.Errorf("expected 2 WHEN clauses, got %d", len(tgt.Whens))
	}
	if tgt.Else == nil {
		t.Errorf("expected ELSE clause to be present")
	}
}

// TestParseCaseExprSimpleForm pins the simple form:
// `CASE expr WHEN val THEN result … END`. Operand carries the
// LHS of the implicit equality.
func TestParseCaseExprSimpleForm(t *testing.T) {
	stmts, err := Parse("SELECT CASE x WHEN 1 THEN 'a' WHEN 2 THEN 'b' END FROM t")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	tgt := sel.Targets[0].Expr.(*CaseExpr)
	if tgt.Operand == nil {
		t.Errorf("simple form should have non-nil Operand")
	}
	if len(tgt.Whens) != 2 {
		t.Errorf("expected 2 WHEN clauses, got %d", len(tgt.Whens))
	}
	if tgt.Else != nil {
		t.Errorf("ELSE should be absent, got %v", tgt.Else)
	}
}

// TestParseCaseExprRequiresWhen pins that bare `CASE END` is a
// syntax error — at least one WHEN clause is required.
func TestParseCaseExprRequiresWhen(t *testing.T) {
	_, err := Parse("SELECT CASE END FROM t")
	if err == nil {
		t.Errorf("expected syntax error for CASE without WHEN")
	}
}
