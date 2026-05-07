package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanHasOuterRef_NonCorrelated verifies that a plan tree with no
// OuterColumnRef nodes is reported as non-correlated. Q22's
// `SELECT avg(c_acctbal) FROM customer WHERE c_acctbal > 0` is the
// canonical case. (M0058-0001.)
func TestPlanHasOuterRef_NonCorrelated(t *testing.T) {
	plan := &Aggregate{
		pos: 0,
		Child: &Filter{
			pos:   0,
			Child: &SeqScan{pos: 0, Table: &catalog.Table{Name: "customer"}},
			Predicate: &BinaryOp{
				pos: 0, Op: ">",
				Left:  &ColumnRef{pos: 0, Index: 5, Name: "c_acctbal", Type: catalog.Type{Name: "numeric"}},
				Right: &IntegerConst{pos: 0, Value: 0},
			},
		},
		Aggs: []AggregateCall{
			{pos: 0, Name: "avg", Arg: &ColumnRef{pos: 0, Index: 5, Name: "c_acctbal", Type: catalog.Type{Name: "numeric"}}, Type: catalog.Type{Name: "numeric"}},
		},
	}
	if planHasOuterRef(plan) {
		t.Error("planHasOuterRef returned true for non-correlated subquery plan")
	}
}

// TestPlanHasOuterRef_Correlated verifies that a plan tree containing
// an OuterColumnRef is reported as correlated. Q17's correlated
// `avg(l_quantity) WHERE l_partkey = p_partkey` is the canonical case.
// (M0058-0001.)
func TestPlanHasOuterRef_Correlated(t *testing.T) {
	outer := &OuterColumnRef{pos: 0, Level: 1, Index: 0, Name: "p_partkey", Type: catalog.Type{Name: "int8"}}
	plan := &Filter{
		pos:   0,
		Child: &SeqScan{pos: 0, Table: &catalog.Table{Name: "lineitem"}},
		Predicate: &BinaryOp{
			pos: 0, Op: "=",
			Left:  &ColumnRef{pos: 0, Index: 1, Name: "l_partkey", Type: catalog.Type{Name: "int8"}},
			Right: outer,
		},
	}
	if !planHasOuterRef(plan) {
		t.Error("planHasOuterRef returned false for plan containing OuterColumnRef")
	}
}

// TestPlanHasOuterRef_NestedSubquery verifies that an OuterColumnRef
// inside a nested SubqueryExpr's plan is reported. The conservative
// behaviour treats the outer subquery as correlated even when the
// nested OuterColumnRef refers to a level deeper than the outer plan
// — a false negative would be a correctness bug; a false positive is
// only a missed cache optimisation.
func TestPlanHasOuterRef_NestedSubquery(t *testing.T) {
	outer := &OuterColumnRef{pos: 0, Level: 1, Index: 0, Name: "x", Type: catalog.Type{Name: "int8"}}
	innerPlan := &Filter{
		pos:   0,
		Child: &SeqScan{pos: 0, Table: &catalog.Table{Name: "t2"}},
		Predicate: &BinaryOp{
			pos: 0, Op: "=",
			Left:  &ColumnRef{pos: 0, Index: 0, Name: "y", Type: catalog.Type{Name: "int8"}},
			Right: outer,
		},
	}
	nested := &SubqueryExpr{pos: 0, Plan: innerPlan}
	plan := &Filter{
		pos:       0,
		Child:     &SeqScan{pos: 0, Table: &catalog.Table{Name: "t1"}},
		Predicate: nested,
	}
	if !planHasOuterRef(plan) {
		t.Error("planHasOuterRef did not descend into nested SubqueryExpr.Plan")
	}
}

// TestPlanHasOuterRef_NestedNonCorrelatedSubquery verifies that a
// subquery containing only nested non-correlated subqueries is itself
// reported as non-correlated.
func TestPlanHasOuterRef_NestedNonCorrelatedSubquery(t *testing.T) {
	innerPlan := &SeqScan{pos: 0, Table: &catalog.Table{Name: "t2"}}
	nested := &SubqueryExpr{pos: 0, Plan: innerPlan}
	plan := &Filter{
		pos:       0,
		Child:     &SeqScan{pos: 0, Table: &catalog.Table{Name: "t1"}},
		Predicate: nested,
	}
	if planHasOuterRef(plan) {
		t.Error("planHasOuterRef returned true for non-correlated nested subquery")
	}
}

// twoTablesCatalog provides two unrelated tables so subquery tests
// can compose `WHERE x IN (SELECT y FROM other)` without dragging in
// the pgbench schema.
func twoTablesCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t1"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "t2"}, []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}},
		{Name: "z", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestPlanInExpr_NonCorrelatedFlag verifies that planInExpr sets
// IsNonCorrelated=true for a subquery with no outer references.
// (M0058-0001 acceptance: Q18 IN-SubPlan picks up the constant-key cache.)
func TestPlanInExpr_NonCorrelatedFlag(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	in := findInExpr(node)
	if in == nil {
		t.Fatalf("no InExpr found in plan: %T", node)
	}
	if !in.IsNonCorrelated {
		t.Error("expected IsNonCorrelated=true for non-correlated IN subquery")
	}
}

// TestPlanInExpr_CorrelatedUnnested verifies that a correlated IN
// subquery is unnested (M0040), so no InExpr survives in the plan.
// (If unnesting were ever bypassed and an InExpr survived, its
// IsNonCorrelated must be false — the unit test for planHasOuterRef
// covers that case directly.)
func TestPlanInExpr_CorrelatedUnnested(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in != nil {
		if in.IsNonCorrelated {
			t.Error("correlated IN subquery survived unnesting AND was flagged as non-correlated — incorrect")
		}
	}
	// otherwise: unnested away, which is the desired M0040 behaviour
}

// TestPlanSubquery_NonCorrelatedFlag verifies SubqueryExpr is flagged
// when the inner SELECT has no outer references. (Q22 avg.)
func TestPlanSubquery_NonCorrelatedFlag(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x > (SELECT max(y) FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	sub := findSubqueryExpr(node)
	if sub == nil {
		t.Fatalf("no SubqueryExpr found in plan: %T", node)
	}
	if !sub.IsNonCorrelated {
		t.Error("expected IsNonCorrelated=true for non-correlated scalar subquery")
	}
}

// TestPlanExists_NonCorrelatedFlag verifies ExistsExpr is flagged
// for a non-correlated EXISTS predicate.
func TestPlanExists_NonCorrelatedFlag(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	ex := findExistsExpr(node)
	if ex == nil {
		t.Fatalf("no ExistsExpr found in plan: %T", node)
	}
	if !ex.IsNonCorrelated {
		t.Error("expected IsNonCorrelated=true for non-correlated EXISTS")
	}
}

func findInExpr(node Node) *InExpr {
	var found *InExpr
	walkPlanExprs(node, func(e Expr) {
		if found != nil {
			return
		}
		walkExprTree(e, func(inner Expr) {
			if found != nil {
				return
			}
			if x, ok := inner.(*InExpr); ok && x.Plan != nil {
				found = x
			}
		})
	})
	return found
}

func findSubqueryExpr(node Node) *SubqueryExpr {
	var found *SubqueryExpr
	walkPlanExprs(node, func(e Expr) {
		if found != nil {
			return
		}
		walkExprTree(e, func(inner Expr) {
			if found != nil {
				return
			}
			if x, ok := inner.(*SubqueryExpr); ok {
				found = x
			}
		})
	})
	return found
}

func findExistsExpr(node Node) *ExistsExpr {
	var found *ExistsExpr
	walkPlanExprs(node, func(e Expr) {
		if found != nil {
			return
		}
		walkExprTree(e, func(inner Expr) {
			if found != nil {
				return
			}
			if x, ok := inner.(*ExistsExpr); ok {
				found = x
			}
		})
	})
	return found
}
