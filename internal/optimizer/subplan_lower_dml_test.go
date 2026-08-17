package optimizer

import (
	"testing"
)

// R3-5: correlated sublinks in UPDATE / DELETE / INSERT…SELECT WHERE
// clauses are lowered to param slots like their SELECT counterparts.
//
// Host discovery starts at Plan()'s root, and lowerSubPlanParams has
// always run for DML roots — but its walker had no *Update / *Delete /
// *Insert case, so a DML root fell through the type switch without ever
// descending into Child, where planUpdate hangs the WHERE. Those sublinks
// therefore kept full-outer-row cache keys and scoped clearing: correct,
// but with far weaker reuse than a projected param key.
//
// The fix is a host-discovery-local wrapper rather than new cases inside
// walkPlanExprs — see walkPlanExprsIncludingDML's comment for why the
// shared walker must not grow them (two of its callers mutate/harvest
// rather than detect, and DML is already reachable there via CTEDMLPrefix).

// TestLowerUpdateWhereExists pins that an UPDATE's correlated EXISTS gets
// param slots and its inner plan is rewritten to read them.
func TestLowerUpdateWhereExists(t *testing.T) {
	cat := threeTablesCatalog(t)
	node, err := Plan(parseOne(t, `update t1 set x = 0 where exists (select 1 from t2 where t2.y = t1.x)`), cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ex := findExistsExprInDML(node)
	if ex == nil {
		t.Fatalf("no ExistsExpr survived in the UPDATE plan")
	}
	if len(ex.ParParam) != 1 {
		t.Fatalf("UPDATE WHERE EXISTS not lowered: ParParam = %v, want one slot", ex.ParParam)
	}
	if len(ex.Args) != 1 {
		t.Fatalf("expected one host-scope Arg, got %v", ex.Args)
	}
	if _, ok := ex.Args[0].(*ColumnRef); !ok {
		t.Fatalf("Arg[0] = %T, want *ColumnRef resolved in the DML target's scope", ex.Args[0])
	}
	outer, execParam := countRefsInPlan(t, ex.Plan)
	if outer != 0 {
		t.Fatalf("inner plan still has %d OuterColumnRef(s) after lowering", outer)
	}
	if execParam == 0 {
		t.Fatalf("inner plan has no ExecParamRef — the rewrite phase did not run")
	}
}

// TestLowerDeleteWhereIn pins the same for DELETE with a correlated IN.
func TestLowerDeleteWhereIn(t *testing.T) {
	cat := threeTablesCatalog(t)
	node, err := Plan(parseOne(t, `delete from t2 where t2.y in (select t3.a from t3 where t3.b = t2.z)`), cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	in := findInExprInDML(node)
	if in == nil {
		t.Fatalf("no InExpr survived in the DELETE plan")
	}
	if len(in.ParParam) != 1 {
		t.Fatalf("DELETE WHERE IN not lowered: ParParam = %v, want one slot", in.ParParam)
	}
	outer, execParam := countRefsInPlan(t, in.Plan)
	if outer != 0 {
		t.Fatalf("inner plan still has %d OuterColumnRef(s) after lowering", outer)
	}
	if execParam == 0 {
		t.Fatalf("inner plan has no ExecParamRef — the rewrite phase did not run")
	}
}

// TestUpdateFromPredicateStaysUnlowered pins the deliberate scope limit:
// UPDATE … FROM keeps its predicate in FromPred, which the wrapper does
// NOT descend into, so that sublink stays on the legacy stack path. This
// is the guard on the exclusion, not an aspiration — if a later change
// starts lowering it, the combined-schema SourceTableIdx question must be
// answered first and this test is where that conversation starts.
func TestUpdateFromPredicateStaysUnlowered(t *testing.T) {
	cat := threeTablesCatalog(t)
	node, err := Plan(parseOne(t,
		`update t1 set x = t2.y from t2 where t1.x = t2.y and exists (select 1 from t3 where t3.a = t2.z)`), cat)
	if err != nil {
		t.Skipf("UPDATE … FROM with a sublink did not plan here: %v", err)
	}
	upd, ok := node.(*Update)
	if !ok {
		t.Skipf("plan root = %T, want *Update", node)
	}
	if upd.FromPred == nil {
		t.Skipf("predicate did not land in FromPred; shape changed")
	}
	// Any sublink reachable only through FromPred must be unlowered.
	var sawSublink, sawLowered bool
	walkExprTreeForDMLTest(upd.FromPred, func(e Expr) {
		if x, ok := e.(*ExistsExpr); ok && x.Plan != nil {
			sawSublink = true
			if len(x.ParParam) > 0 {
				sawLowered = true
			}
		}
	})
	if !sawSublink {
		t.Skipf("no sublink found in FromPred; shape changed")
	}
	if sawLowered {
		t.Fatalf("FROM-clause sublink was lowered — R3-5 deliberately excludes FromPred/UsingPred " +
			"until lowered Args carry combined-schema SourceTableIdx values")
	}
}

// TestWalkPlanExprsStillSkipsDML protects the two shared-walker callers
// that mutate (remapOuterRefsInSubplan) or harvest (collectUnnestParams)
// rather than merely detect: walkPlanExprs itself must NOT descend into a
// DML node's child, or those passes would newly see CTE-DML bodies.
func TestWalkPlanExprsStillSkipsDML(t *testing.T) {
	cat := threeTablesCatalog(t)
	node, err := Plan(parseOne(t, `update t1 set x = 0 where exists (select 1 from t2 where t2.y = t1.x)`), cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, ok := node.(*Update); !ok {
		t.Fatalf("plan root = %T, want *Update", node)
	}
	var shared int
	walkPlanExprs(node, func(Expr) { shared++ })
	if shared != 0 {
		t.Fatalf("walkPlanExprs descended into the UPDATE (%d exprs visited); the DML cases belong to "+
			"walkPlanExprsIncludingDML only", shared)
	}
	var viaWrapper int
	walkPlanExprsIncludingDML(node, func(Expr) { viaWrapper++ })
	if viaWrapper == 0 {
		t.Fatalf("walkPlanExprsIncludingDML visited nothing — host discovery would miss DML sublinks")
	}
}

// findExistsExprInDML / findInExprInDML mirror the SELECT-side helpers but
// use the DML-aware wrapper so they can see into an Update/Delete child.
func findExistsExprInDML(node Node) *ExistsExpr {
	var found *ExistsExpr
	walkPlanExprsIncludingDML(node, func(e Expr) {
		if x, ok := e.(*ExistsExpr); ok && found == nil {
			found = x
		}
	})
	return found
}

func findInExprInDML(node Node) *InExpr {
	var found *InExpr
	walkPlanExprsIncludingDML(node, func(e Expr) {
		if x, ok := e.(*InExpr); ok && found == nil {
			found = x
		}
	})
	return found
}

// walkExprTreeForDMLTest visits an expression tree's nodes. Kept local to
// the test so it cannot drift into production traversal semantics.
func walkExprTreeForDMLTest(e Expr, fn func(Expr)) {
	if e == nil {
		return
	}
	fn(e)
	switch x := e.(type) {
	case *BinaryOp:
		walkExprTreeForDMLTest(x.Left, fn)
		walkExprTreeForDMLTest(x.Right, fn)
	case *UnaryOp:
		walkExprTreeForDMLTest(x.Operand, fn)
	case *FuncCall:
		for _, a := range x.Args {
			walkExprTreeForDMLTest(a, fn)
		}
	}
}
