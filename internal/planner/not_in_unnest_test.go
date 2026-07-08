package planner

import "testing"

// TestUnnestNonCorrelatedNotIn verifies that a non-correlated
// `x NOT IN (subquery)` is unnested to a NullAware JoinTypeAnti
// hash join (M0122-0011 — previously NOT IN was blanket-rejected
// by isUnnestableNonCorrelatedIn and fell back to the slower
// runtime-cache execution path).
func TestUnnestNonCorrelatedNotIn(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x NOT IN (SELECT y FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in != nil {
		t.Errorf("InExpr survived unnesting: %#v", in)
	}
	j := findFirstJoinByType(node, JoinTypeAnti)
	if j == nil {
		t.Fatalf("no JoinTypeAnti found after unnesting: %s", planString(node))
	}
	if j.Algo != JoinAlgoHash {
		t.Errorf("Anti join algo = %d, want JoinAlgoHash", j.Algo)
	}
	if !j.NullAware {
		t.Error("expected NullAware=true on the Anti join built from NOT IN")
	}
	if j.LeftKey == nil || j.RightKey == nil {
		t.Errorf("Anti join missing LeftKey/RightKey: left=%v right=%v", j.LeftKey, j.RightKey)
	}
}

// TestUnnestNonCorrelatedIn_NotNullAware verifies plain (non-negated)
// non-correlated IN still unnests to a plain JoinTypeSemi with
// NullAware left false — NullAware is specific to the NOT IN /
// JoinTypeAnti combination.
func TestUnnestNonCorrelatedIn_NotNullAware(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found after unnesting: %s", planString(node))
	}
	if j.NullAware {
		t.Error("expected NullAware=false on a plain (non-negated) Semi join")
	}
}

// TestUnnestNonCorrelatedIn_NonColumnRefOperand verifies that a
// non-correlated IN whose LHS is a general scalar expression (not a
// bare column) still unnests to a Semi join (M0122-0011 follow-up —
// isUnnestableNonCorrelatedIn previously hard-required a *ColumnRef
// operand and fell back to the slower runtime-cache path for
// anything else, e.g. an arithmetic expression). LeftKey/RightKey
// are general Expr and the hash-join executor evaluates them with
// evalExpr, so no ColumnRef reconstruction is needed.
func TestUnnestNonCorrelatedIn_NonColumnRefOperand(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x + 1 IN (SELECT y FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in != nil {
		t.Errorf("InExpr survived unnesting: %#v", in)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found after unnesting: %s", planString(node))
	}
	if j.Algo != JoinAlgoHash {
		t.Errorf("Semi join algo = %d, want JoinAlgoHash", j.Algo)
	}
	if _, ok := j.LeftKey.(*ColumnRef); ok {
		t.Errorf("LeftKey = %#v, want a non-ColumnRef expression (x + 1)", j.LeftKey)
	}
	if _, ok := j.LeftKey.(*BinaryOp); !ok {
		t.Errorf("LeftKey = %#v (%T), want *BinaryOp for `x + 1`", j.LeftKey, j.LeftKey)
	}
}
