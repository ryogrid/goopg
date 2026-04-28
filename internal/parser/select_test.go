package parser

import (
	"testing"
)

// TestParseSelectConstant pins the simplest form: SELECT 1, mirroring
// the libpq smoke-test query.
func TestParseSelectConstant(t *testing.T) {
	stmts, err := Parse("SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := stmts[0].(*SelectStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(s.Targets) != 1 {
		t.Fatalf("targets=%d", len(s.Targets))
	}
	ic, ok := s.Targets[0].Expr.(*IntegerConst)
	if !ok || ic.Value != 1 {
		t.Errorf("target=%+v", s.Targets[0].Expr)
	}
	if s.From != nil || s.Where != nil {
		t.Errorf("expected no FROM/WHERE, got %+v / %+v", s.From, s.Where)
	}
}

// TestParseSelectPgbenchSelectOnly: the canonical query the
// pgbench `--select-only` script issues.
func TestParseSelectPgbenchSelectOnly(t *testing.T) {
	stmts, err := Parse("SELECT abalance FROM pgbench_accounts WHERE aid = $1")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.Targets) != 1 {
		t.Fatalf("targets=%d", len(s.Targets))
	}
	cr, ok := s.Targets[0].Expr.(*ColumnRef)
	if !ok || cr.Column != "abalance" {
		t.Fatalf("target=%+v", s.Targets[0].Expr)
	}
	if len(s.From) != 1 || s.From[0].Name != "pgbench_accounts" {
		t.Fatalf("from=%+v", s.From)
	}
	bo, ok := s.Where.(*BinaryOp)
	if !ok || bo.Op != "=" {
		t.Fatalf("where=%+v", s.Where)
	}
	if l, ok := bo.Left.(*ColumnRef); !ok || l.Column != "aid" {
		t.Errorf("where.Left=%+v", bo.Left)
	}
	if pr, ok := bo.Right.(*ParamRef); !ok || pr.Number != 1 {
		t.Errorf("where.Right=%+v", bo.Right)
	}
}

// TestParseSelectStarTargetAndAlias: SELECT *, qualified star, AS alias.
func TestParseSelectStarTargetAndAlias(t *testing.T) {
	stmts, err := Parse("SELECT a.*, b.x AS bx FROM t1 AS a, t2 b")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.Targets) != 2 {
		t.Fatalf("targets=%d", len(s.Targets))
	}
	star, ok := s.Targets[0].Expr.(*StarExpr)
	if !ok || star.Table != "a" {
		t.Fatalf("targets[0]=%+v", s.Targets[0].Expr)
	}
	if s.Targets[1].Alias != "bx" {
		t.Errorf("targets[1].Alias=%q", s.Targets[1].Alias)
	}
	if len(s.From) != 2 || s.From[0].Alias != "a" || s.From[1].Alias != "b" {
		t.Errorf("from=%+v", s.From)
	}
}

// TestParseSelectExpressionPrecedence verifies that operator
// precedence binds AND tighter than OR, and arithmetic tighter than
// comparisons. Tree shape is checked with a small string formatter so
// the assertion is compact.
func TestParseSelectExpressionPrecedence(t *testing.T) {
	stmts, err := Parse("SELECT 1 WHERE a = 1 + 2 * 3 OR b > 4 AND c < 5")
	if err != nil {
		t.Fatal(err)
	}
	got := exprString(stmts[0].(*SelectStmt).Where)
	want := "((a = (1 + (2 * 3))) OR ((b > 4) AND (c < 5)))"
	if got != want {
		t.Errorf("expr = %s\nwant   %s", got, want)
	}
}

// TestParseSelectOrderLimitOffset: trailing clauses parse as the right
// node types and integers attach to LIMIT/OFFSET.
func TestParseSelectOrderLimitOffset(t *testing.T) {
	stmts, err := Parse("SELECT x FROM t ORDER BY x DESC, y LIMIT 10 OFFSET 5")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.OrderBy) != 2 || !s.OrderBy[0].Desc || s.OrderBy[1].Desc {
		t.Errorf("orderby=%+v", s.OrderBy)
	}
	if li, ok := s.Limit.(*IntegerConst); !ok || li.Value != 10 {
		t.Errorf("limit=%+v", s.Limit)
	}
	if oi, ok := s.Offset.(*IntegerConst); !ok || oi.Value != 5 {
		t.Errorf("offset=%+v", s.Offset)
	}
}

// TestParseSelectFunctionCall: count(*) and sum(distinct x) shapes.
func TestParseSelectFunctionCall(t *testing.T) {
	stmts, err := Parse("SELECT count(*), sum(DISTINCT x) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	c, ok := s.Targets[0].Expr.(*FuncCall)
	if !ok || c.Name.Name != "count" || !c.Star {
		t.Fatalf("count target=%+v", s.Targets[0].Expr)
	}
	d, ok := s.Targets[1].Expr.(*FuncCall)
	if !ok || d.Name.Name != "sum" || !d.Distinct || len(d.Args) != 1 {
		t.Fatalf("sum target=%+v", s.Targets[1].Expr)
	}
}

// TestParseSelectSyntaxErrors pins error positions for the canonical
// "missing piece" cases.
func TestParseSelectSyntaxErrors(t *testing.T) {
	cases := []string{
		"SELECT",            // no target
		"SELECT 1 FROM",     // no table after FROM
		"SELECT 1 WHERE",    // no expression after WHERE
		"SELECT 1 ORDER BY", // no expression after ORDER BY
	}
	for _, in := range cases {
		_, err := Parse(in)
		if err == nil {
			t.Errorf("Parse(%q) expected error", in)
			continue
		}
		if _, ok := err.(*SyntaxError); !ok {
			t.Errorf("Parse(%q) err type=%T", in, err)
		}
	}
}

// exprString is a tiny helper for the precedence test — it prints the
// expression tree fully parenthesised so the test asserts shape, not
// string formatting choices.
func exprString(e Expr) string {
	switch x := e.(type) {
	case *IntegerConst:
		return itoa(x.Value)
	case *StringConst:
		return "'" + x.Value + "'"
	case *NullConst:
		return "NULL"
	case *BooleanConst:
		if x.Value {
			return "TRUE"
		}
		return "FALSE"
	case *ParamRef:
		return "$" + itoa(int64(x.Number))
	case *ColumnRef:
		s := ""
		if x.Schema != "" {
			s += x.Schema + "."
		}
		if x.Table != "" {
			s += x.Table + "."
		}
		return s + x.Column
	case *BinaryOp:
		return "(" + exprString(x.Left) + " " + x.Op + " " + exprString(x.Right) + ")"
	case *UnaryOp:
		return "(" + x.Op + " " + exprString(x.Operand) + ")"
	case *FuncCall:
		args := ""
		if x.Star {
			args = "*"
		} else {
			for i, a := range x.Args {
				if i > 0 {
					args += ", "
				}
				args += exprString(a)
			}
		}
		return x.Name.String() + "(" + args + ")"
	}
	return "?"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
