package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func containsSubqueryExpr(e Expr) bool {
	if e == nil {
		return false
	}
	if _, ok := e.(*SubqueryExpr); ok {
		return true
	}
	switch x := e.(type) {
	case *BinaryOp:
		return containsSubqueryExpr(x.Left) || containsSubqueryExpr(x.Right)
	case *UnaryOp:
		return containsSubqueryExpr(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if containsSubqueryExpr(a) {
				return true
			}
		}
	case *CaseExpr:
		if x.Operand != nil && containsSubqueryExpr(x.Operand) {
			return true
		}
		for _, w := range x.Whens {
			if containsSubqueryExpr(w.When) || containsSubqueryExpr(w.Then) {
				return true
			}
		}
		if x.Else != nil && containsSubqueryExpr(x.Else) {
			return true
		}
	case *ExtractExpr:
		return containsSubqueryExpr(x.Source)
	}
	return false
}

func hasJoinWithAggregateChild(node Node) bool {
	if node == nil {
		return false
	}
	switch n := node.(type) {
	case *Join:
		if n.Algo == JoinAlgoHash {
			if _, ok := n.Right.(*Aggregate); ok {
				return true
			}
		}
		return hasJoinWithAggregateChild(n.Left) || hasJoinWithAggregateChild(n.Right)
	case *Filter:
		return hasJoinWithAggregateChild(n.Child)
	case *Project:
		return hasJoinWithAggregateChild(n.Child)
	case *Sort:
		return hasJoinWithAggregateChild(n.Child)
	case *Aggregate:
		return hasJoinWithAggregateChild(n.Child)
	}
	return false
}

func TestCannotUnnestNonEquijoinSubquery(t *testing.T) {
	q := `select s_name from supplier
where s_suppkey = (
  select max(ps_suppkey)
  from partsupp, nation
  where s_nationkey > n_nationkey
)`

	cat := catalog.NewInMemory()
	cat.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_name", Type: catalog.Type{Name: "text"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "int8"}},
	})
	cat.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
	})
	cat.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "int8"}},
	})

	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Non-equijoin correlation should NOT be unnested.
	if hasJoinWithAggregateChild(plan) {
		t.Error("non-equijoin subquery was incorrectly unnested")
	}
}

// findInExprInPred finds the first InExpr with a Plan != nil in an expression.
func findInExprInPred(e Expr) *InExpr {
	if e == nil {
		return nil
	}
	if in, ok := e.(*InExpr); ok && in.Plan != nil {
		return in
	}
	switch x := e.(type) {
	case *BinaryOp:
		if r := findInExprInPred(x.Left); r != nil {
			return r
		}
		return findInExprInPred(x.Right)
	case *UnaryOp:
		return findInExprInPred(x.Operand)
	}
	return nil
}

func TestCannotUnnestExistsExpr(t *testing.T) {
	q := `select s_name from supplier
where exists (
  select 1 from partsupp
  where s_suppkey = ps_suppkey
)`

	cat := catalog.NewInMemory()
	cat.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_name", Type: catalog.Type{Name: "text"}},
	})
	cat.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
	})

	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// EXISTS subqueries are out of scope for v0 unnesting.
	if hasJoinWithAggregateChild(plan) {
		t.Error("EXISTS subquery was incorrectly unnested (v0 scope)")
	}
}

// outerExtra2 builds a fresh non-equijoin outer ref for the CASE
// variant in TestCanUnnestSubqueryWithExtraOuterRef (pointer identity
// matters to the collector's accounting maps).
func outerExtra2() *OuterColumnRef {
	return &OuterColumnRef{pos: 0, Level: 1, Index: 1, Name: "p_size", Type: catalog.Type{Name: "int8"}}
}

// TestNotInResidualStaysSubPlan pins the deliberate S4a non-goal: a
// correlated NOT IN with a lifted residual must stay a SubPlan. NOT IN
// carries three-valued NULL semantics (one NULL in the inner set makes
// every non-matching outer row UNKNOWN, i.e. filtered), which the anti
// join produced by residual lifting does not model. canUnnestInExprDepth
// bails on Negated+residuals; this test keeps that gate honest.
func TestNotInResidualStaysSubPlan(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x NOT IN (SELECT y FROM t2 WHERE z > t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in == nil {
		t.Fatalf("correlated NOT IN with residual must stay a SubPlan: %s", planString(node))
	}
	if findFirstJoinByType(node, JoinTypeAnti) != nil {
		t.Error("correlated NOT IN with residual was incorrectly converted to an anti join")
	}
}

// TestExprContainsColumnRefIsNullOperand pins the walkExprTree IsNullExpr arm
// (M0134-0001 S6 S3e). ExprContainsColumnRef drives CREATE INDEX partial-index
// const-folding (executor/operators_ddl.go); without the arm a `f1 IS NOT NULL`
// predicate was reported column-free and folded onto a nil slot — the aggregates
// regress `ERROR: column ref f1/0 on nil slot`. The walker must descend through
// the IsNullExpr into its *ColumnRef Operand for both polarities, and still
// report false for a bare constant.
func TestExprContainsColumnRefIsNullOperand(t *testing.T) {
	col := &ColumnRef{pos: 0, Index: 0, Name: "f1", Type: catalog.Type{Name: "int4"}}

	for _, tc := range []struct {
		name string
		expr Expr
		want bool
	}{
		{name: "is null over col", expr: &IsNullExpr{pos: 0, Operand: col}, want: true},
		{name: "is not null over col", expr: &IsNullExpr{pos: 0, Operand: col, Negated: true}, want: true},
		{name: "bare constant", expr: &BooleanConst{pos: 0, Value: true}, want: false},
	} {
		if got := ExprContainsColumnRef(tc.expr); got != tc.want {
			t.Errorf("%s: ExprContainsColumnRef(%T) = %v, want %v", tc.name, tc.expr, got, tc.want)
		}
	}
}
