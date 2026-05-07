package planner

import (
	"testing"
)

// TestUnnestCorrelatedExists verifies that a correlated EXISTS
// predicate is rewritten as a JoinTypeSemi hash join.
// (M0061-0001 acceptance: Q4-shape EXISTS now becomes a Semi Join.)
func TestUnnestCorrelatedExists(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if ex := findExistsExpr(node); ex != nil {
		t.Errorf("ExistsExpr survived unnesting: %#v", ex)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found after unnesting: %s", planString(node))
	}
	if j.Algo != JoinAlgoHash {
		t.Errorf("Semi join algo = %d, want JoinAlgoHash", j.Algo)
	}
	if j.LeftKey == nil || j.RightKey == nil {
		t.Errorf("Semi join missing LeftKey/RightKey: left=%v right=%v", j.LeftKey, j.RightKey)
	}
}

// TestUnnestCorrelatedNotExists verifies NOT EXISTS becomes a
// JoinTypeAnti hash join. (M0061-0001 acceptance: Q22-shape.)
func TestUnnestCorrelatedNotExists(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE NOT EXISTS (SELECT 1 FROM t2 WHERE z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if ex := findExistsExpr(node); ex != nil {
		t.Errorf("ExistsExpr survived unnesting: %#v", ex)
	}
	j := findFirstJoinByType(node, JoinTypeAnti)
	if j == nil {
		t.Fatalf("no JoinTypeAnti found after unnesting: %s", planString(node))
	}
	if j.Algo != JoinAlgoHash {
		t.Errorf("Anti join algo = %d, want JoinAlgoHash", j.Algo)
	}
}

// TestUnnestExistsNonCorrelatedStays verifies that a non-correlated
// EXISTS is NOT converted to a join — it stays as ExistsExpr so the
// M0058-0001 constant-key cache can collapse repeated evaluations
// across outer rows.
func TestUnnestExistsNonCorrelatedStays(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	ex := findExistsExpr(node)
	if ex == nil {
		t.Fatalf("non-correlated EXISTS should NOT be unnested; want survivor")
	}
	if !ex.IsNonCorrelated {
		t.Error("non-correlated EXISTS missing IsNonCorrelated flag")
	}
	if findFirstJoinByType(node, JoinTypeSemi) != nil {
		t.Error("non-correlated EXISTS was incorrectly converted to Semi join")
	}
}

// TestUnnestExistsMixedEquiAndNonEqui pins M0062-0005: an EXISTS
// whose correlation combines an equi-pair AND a non-equi residual
// (e.g. Q21's `l2.l_orderkey = l1.l_orderkey AND l2.l_suppkey <>
// l1.l_suppkey`) is unnested as a hash semi-join keyed on the
// equi-pair, with the `<>` lifted to the join's Predicate.
func TestUnnestExistsMixedEquiAndNonEqui(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := `SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z = t1.x AND y <> t1.x)`
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if ex := findExistsExpr(node); ex != nil {
		t.Errorf("ExistsExpr survived: %#v", ex)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi: %s", planString(node))
	}
	if j.Predicate == nil {
		t.Errorf("Semi join missing residual Predicate (the `<>` should have been lifted)")
	}
}

// TestUnnestExistsNonEquijoinStays verifies that a correlated EXISTS
// whose correlation is NOT an equijoin (range predicate) falls
// through to the existing SubPlan path; canUnnestExistsExpr's
// equijoin gate must reject it.
func TestUnnestExistsNonEquijoinStays(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z > t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if ex := findExistsExpr(node); ex == nil {
		t.Errorf("non-equijoin EXISTS should NOT be unnested; got Semi join: %s", planString(node))
	}
	if findFirstJoinByType(node, JoinTypeSemi) != nil {
		t.Error("non-equijoin EXISTS was incorrectly converted to Semi join")
	}
}

// findFirstJoinByType walks the plan tree returning the first Join
// whose Type matches `want`, or nil.
func findFirstJoinByType(node Node, want JoinType) *Join {
	if node == nil {
		return nil
	}
	if j, ok := node.(*Join); ok && j.Type == want {
		return j
	}
	switch n := node.(type) {
	case *Join:
		if j := findFirstJoinByType(n.Left, want); j != nil {
			return j
		}
		return findFirstJoinByType(n.Right, want)
	case *Filter:
		return findFirstJoinByType(n.Child, want)
	case *Project:
		return findFirstJoinByType(n.Child, want)
	case *Aggregate:
		return findFirstJoinByType(n.Child, want)
	case *Sort:
		return findFirstJoinByType(n.Child, want)
	case *Limit:
		return findFirstJoinByType(n.Child, want)
	}
	return nil
}

// planString is a tiny stringifier for test failure messages —
// avoids relying on a full EXPLAIN implementation.
func planString(node Node) string {
	if node == nil {
		return "<nil>"
	}
	switch n := node.(type) {
	case *Project:
		return "Project(" + planString(n.Child) + ")"
	case *Filter:
		return "Filter(" + planString(n.Child) + ")"
	case *Aggregate:
		return "Aggregate(" + planString(n.Child) + ")"
	case *Join:
		t := "Join"
		switch n.Type {
		case JoinTypeInner:
			t = "InnerJoin"
		case JoinTypeSemi:
			t = "SemiJoin"
		case JoinTypeAnti:
			t = "AntiJoin"
		case JoinTypeLeft:
			t = "LeftJoin"
		case JoinTypeRight:
			t = "RightJoin"
		case JoinTypeFull:
			t = "FullJoin"
		case JoinTypeCross:
			t = "CrossJoin"
		}
		return t + "(" + planString(n.Left) + ", " + planString(n.Right) + ")"
	case *SeqScan:
		if n.Table != nil {
			return "SeqScan(" + n.Table.Name + ")"
		}
		return "SeqScan(?)"
	default:
		return "<other>"
	}
}
