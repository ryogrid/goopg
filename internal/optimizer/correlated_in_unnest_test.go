package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// threeColCorrelationCatalog builds t1(x, w) / t2(y, z) so a
// correlation predicate can be written on a column (w) distinct from
// the IN operand/select column (x/y).
func threeColCorrelationCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t1"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "w", Type: catalog.Type{Name: "int4"}},
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

// TestUnnestCorrelatedIn_SafeSelfReferencing verifies the one shape
// where unnestInExpr's single-correlation-pair join key is provably
// equivalent to the original predicate: the subquery's WHERE clause
// correlates on exactly the column it also SELECTs, and that column
// is exactly the IN operand (`x IN (SELECT y FROM t2 WHERE y = t1.x)`
// — every surviving row's y equals t1.x by construction, so the plain
// hash Semi join is correct.
func TestUnnestCorrelatedIn_SafeSelfReferencing(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in != nil {
		t.Fatalf("InExpr survived unnesting: %#v", in)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found after unnesting: %s", planString(node))
	}
	if j.Algo != JoinAlgoHash {
		t.Errorf("Semi join algo = %d, want JoinAlgoHash", j.Algo)
	}
}

// TestUnnestCorrelatedNotIn_SafeSelfReferencing is the NOT IN sibling
// of the above: the correlation predicate's equality guarantees the
// subquery's per-row output is either empty or exactly {t1.x}, never
// NULL, so a plain (non-NullAware) Anti join already implements NOT
// IN's three-valued semantics correctly for this shape.
func TestUnnestCorrelatedNotIn_SafeSelfReferencing(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x NOT IN (SELECT y FROM t2 WHERE y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in != nil {
		t.Fatalf("InExpr survived unnesting: %#v", in)
	}
	j := findFirstJoinByType(node, JoinTypeAnti)
	if j == nil {
		t.Fatalf("no JoinTypeAnti found after unnesting: %s", planString(node))
	}
	if j.Algo != JoinAlgoHash {
		t.Errorf("Anti join algo = %d, want JoinAlgoHash", j.Algo)
	}
}

// TestUnnestCorrelatedIn_RejectsOperandNotCorrelationColumn is a
// regression test for a real (previously undetected) correctness bug:
// unnestInExpr always keyed the join on the correlation pair found in
// the subquery's WHERE clause, never on in.Operand. When the
// correlation is on a DIFFERENT column than the IN operand
// (`x IN (SELECT y FROM t2 WHERE z = t1.w)`), the old code silently
// built `Join(t1, t2, on w = y)` — checking "does a z=w-correlated row
// exist" instead of "is x among the y values of z=w-correlated rows".
// This must now refuse to unnest and leave the InExpr in place so the
// (always-correct) runtime per-row evaluation path handles it.
func TestUnnestCorrelatedIn_RejectsOperandNotCorrelationColumn(t *testing.T) {
	cat := threeColCorrelationCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z = t1.w)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in == nil {
		t.Fatalf("InExpr was unnested despite operand != correlation column: %s", planString(node))
	}
}

// TestUnnestCorrelatedIn_RejectsSelectNotCorrelationColumn covers the
// second half of the same bug class: the correlation is on the IN
// operand's own column (`z = t1.x`), but the subquery SELECTs a
// different column (`y`) than it correlates on. clonePlanReplacingOuter
// folds the correlation predicate into a tautology, so unnesting here
// would silently drop the `z = t1.x` filter entirely and match on
// `x = y` alone, wrong whenever a t2 row has y = x but z != x. Must
// refuse to unnest.
func TestUnnestCorrelatedIn_RejectsSelectNotCorrelationColumn(t *testing.T) {
	cat := threeColCorrelationCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in == nil {
		t.Fatalf("InExpr was unnested despite selected column != correlation column: %s", planString(node))
	}
}
