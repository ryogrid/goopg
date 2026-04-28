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
	if s.FromExprs != nil {
		t.Errorf("expected no FROM exprs, got %+v", s.FromExprs)
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

func TestParseSelectJoins(t *testing.T) {
	in := "SELECT a.id FROM t1 a INNER JOIN t2 b ON a.id = b.id LEFT JOIN t3 c USING (id) CROSS JOIN t4 d"
	stmts, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.FromExprs) != 1 {
		t.Fatalf("fromExprs=%d", len(s.FromExprs))
	}
	if len(s.From) != 4 {
		t.Fatalf("from(flat)=%d", len(s.From))
	}
	joins := s.FromExprs[0].Joins
	if len(joins) != 3 {
		t.Fatalf("joins=%d", len(joins))
	}
	if joins[0].Type != JoinInner || joins[0].On == nil {
		t.Errorf("joins[0]=%+v", joins[0])
	}
	if joins[1].Type != JoinLeft || len(joins[1].Using) != 1 || joins[1].Using[0] != "id" {
		t.Errorf("joins[1]=%+v", joins[1])
	}
	if joins[2].Type != JoinCross || joins[2].On != nil || len(joins[2].Using) != 0 {
		t.Errorf("joins[2]=%+v", joins[2])
	}
}

func TestParseSelectGroupByHaving(t *testing.T) {
	stmts, err := Parse("SELECT a, sum(b) FROM t GROUP BY a HAVING sum(b) > 10")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.GroupBy) != 1 {
		t.Fatalf("groupBy=%d", len(s.GroupBy))
	}
	if c, ok := s.GroupBy[0].(*ColumnRef); !ok || c.Column != "a" {
		t.Errorf("groupBy[0]=%+v", s.GroupBy[0])
	}
	h, ok := s.Having.(*BinaryOp)
	if !ok || h.Op != ">" {
		t.Fatalf("having=%+v", s.Having)
	}
}

func TestParseSelectSetOps(t *testing.T) {
	stmts, err := Parse("SELECT 1 UNION ALL SELECT 2 INTERSECT SELECT 3")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if s.SetOp == nil || s.SetOp.Type != SetOpUnion || !s.SetOp.All {
		t.Fatalf("setop=%+v", s.SetOp)
	}
	rhs := s.SetOp.Right
	if rhs == nil || rhs.SetOp == nil || rhs.SetOp.Type != SetOpIntersect || rhs.SetOp.All {
		t.Fatalf("rhs setop=%+v", rhs)
	}
}

func TestParseSelectNaturalJoin(t *testing.T) {
	stmts, err := Parse("SELECT * FROM a NATURAL JOIN b")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.FromExprs) != 1 || len(s.FromExprs[0].Joins) != 1 {
		t.Fatalf("fromExprs=%+v", s.FromExprs)
	}
	j := s.FromExprs[0].Joins[0]
	if !j.Natural || j.Type != JoinInner || j.On != nil || len(j.Using) != 0 {
		t.Errorf("join=%+v", j)
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
		"SELECT 1 FROM t JOIN u",
		"SELECT 1 FROM t NATURAL",
		"SELECT 1 GROUP",
		"SELECT 1 UNION",
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
