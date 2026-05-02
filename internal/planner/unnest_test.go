package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func TestCanUnnestSubqueryBasic(t *testing.T) {
	// Directly construct an unnestable SubqueryExpr.
	outerCol := &OuterColumnRef{pos: 0, Level: 1, Index: 0, Name: "p_partkey", Type: catalog.Type{Name: "int8"}}
	subCol := &ColumnRef{pos: 0, Index: 0, Name: "ps_partkey", Type: catalog.Type{Name: "int8"}}
	eqExpr := &BinaryOp{pos: 0, Op: "=", Left: outerCol, Right: subCol}
	// Subquery plan: Aggregate over Filter over SeqScan
	agg := &Aggregate{
		pos: 0,
		Child: &Filter{
			pos:       0,
			Child:     &SeqScan{pos: 0, Table: &catalog.Table{Name: "partsupp"}},
			Predicate: eqExpr,
		},
		GroupExprs: nil,
		Aggs: []AggregateCall{
			{pos: 0, Name: "min", Arg: &ColumnRef{pos: 0, Index: 2, Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}}, Type: catalog.Type{Name: "numeric"}},
		},
	}
	sub := &SubqueryExpr{pos: 0, Plan: agg}

	if !canUnnestSubquery(sub) {
		t.Error("canUnnestSubquery returned false for unnestable subquery")
	}
	params := collectUnnestParams(agg)
	if len(params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(params))
	}
	if params[0].OuterRef != outerCol {
		t.Error("outer ref mismatch")
	}
	if params[0].SubCol != subCol {
		t.Error("sub col mismatch")
	}
}

func TestCanUnnestSubqueryWithExtraOuterRef(t *testing.T) {
	// Subquery with an OuterColumnRef in a non-equijoin context.
	outerEq := &OuterColumnRef{pos: 0, Level: 1, Index: 0, Name: "p_partkey", Type: catalog.Type{Name: "int8"}}
	subCol := &ColumnRef{pos: 0, Index: 0, Name: "ps_partkey", Type: catalog.Type{Name: "int8"}}
	outerExtra := &OuterColumnRef{pos: 0, Level: 1, Index: 1, Name: "p_size", Type: catalog.Type{Name: "int8"}}

	eqExpr := &BinaryOp{pos: 0, Op: "=", Left: outerEq, Right: subCol}
	extraExpr := &BinaryOp{pos: 0, Op: ">", Left: outerExtra, Right: &ColumnRef{pos: 0, Index: 3, Name: "ps_availqty", Type: catalog.Type{Name: "int8"}}}
	pred := &BinaryOp{pos: 0, Op: "AND", Left: eqExpr, Right: extraExpr}

	agg := &Aggregate{
		pos: 0,
		Child: &Filter{
			pos:       0,
			Child:     &SeqScan{pos: 0, Table: &catalog.Table{Name: "partsupp"}},
			Predicate: pred,
		},
		Aggs: []AggregateCall{
			{pos: 0, Name: "min", Arg: &ColumnRef{pos: 0, Index: 2, Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}}, Type: catalog.Type{Name: "numeric"}},
		},
	}
	sub := &SubqueryExpr{pos: 0, Plan: agg}

	if canUnnestSubquery(sub) {
		t.Error("canUnnestSubquery returned true for subquery with non-equijoin OuterColumnRef")
	}
}

func TestCanUnnestQ2Subquery(t *testing.T) {
	q2 := `select s_acctbal, s_name, n_name, p_partkey, p_mfgr
from part, supplier, partsupp, nation, region
where p_partkey = ps_partkey
  and s_suppkey = ps_suppkey
  and p_size = 15
  and s_nationkey = n_nationkey
  and n_regionkey = r_regionkey
  and r_name = 'EUROPE'
  and ps_supplycost = (
    select min(ps_supplycost)
    from partsupp, supplier, nation, region
    where p_partkey = ps_partkey
      and s_suppkey = ps_suppkey
      and s_nationkey = n_nationkey
      and n_regionkey = r_regionkey
      and r_name = 'EUROPE'
  )`

	cat := catalog.NewInMemory()
	for _, def := range []struct {
		name string
		cols []catalog.Column
	}{
		{"part", []catalog.Column{
			{Name: "p_partkey", Type: catalog.Type{Name: "int8"}},
			{Name: "p_name", Type: catalog.Type{Name: "text"}},
			{Name: "p_mfgr", Type: catalog.Type{Name: "text"}},
			{Name: "p_size", Type: catalog.Type{Name: "int8"}},
			{Name: "p_type", Type: catalog.Type{Name: "text"}},
		}},
		{"supplier", []catalog.Column{
			{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
			{Name: "s_name", Type: catalog.Type{Name: "text"}},
			{Name: "s_nationkey", Type: catalog.Type{Name: "int8"}},
			{Name: "s_acctbal", Type: catalog.Type{Name: "numeric"}},
		}},
		{"partsupp", []catalog.Column{
			{Name: "ps_partkey", Type: catalog.Type{Name: "int8"}},
			{Name: "ps_suppkey", Type: catalog.Type{Name: "int8"}},
			{Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}},
		}},
		{"nation", []catalog.Column{
			{Name: "n_nationkey", Type: catalog.Type{Name: "int8"}},
			{Name: "n_name", Type: catalog.Type{Name: "text"}},
			{Name: "n_regionkey", Type: catalog.Type{Name: "int8"}},
		}},
		{"region", []catalog.Column{
			{Name: "r_regionkey", Type: catalog.Type{Name: "int8"}},
			{Name: "r_name", Type: catalog.Type{Name: "text"}},
		}},
	} {
		if _, err := cat.CreateTable(parser.ObjectName{Name: def.name}, def.cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", def.name, err)
		}
	}

	stmts, err := parser.Parse(q2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Verify the plan does NOT contain a SubqueryExpr — it should
	// have been unnested into a HashJoin + Aggregate.
	if hasSubquery := findSubqueryInPlan(plan); hasSubquery {
		t.Error("plan still contains SubqueryExpr after unnesting pass")
	}

	// Verify the plan contains a HashJoin whose right child is an Aggregate.
	if !hasJoinWithAggregateChild(plan) {
		t.Error("plan does not contain a HashJoin + Aggregate (unnesting may not have fired)")
	}
}

func findSubqueryInPlan(node Node) bool {
	if node == nil {
		return false
	}
	switch n := node.(type) {
	case *Filter:
		if containsSubqueryExpr(n.Predicate) {
			return true
		}
		return findSubqueryInPlan(n.Child)
	case *Join:
		return findSubqueryInPlan(n.Left) || findSubqueryInPlan(n.Right)
	case *Project:
		for _, t := range n.Targets {
			if containsSubqueryExpr(t) {
				return true
			}
		}
		return findSubqueryInPlan(n.Child)
	case *Aggregate:
		return findSubqueryInPlan(n.Child)
	case *Sort:
		return findSubqueryInPlan(n.Child)
	}
	return false
}

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
