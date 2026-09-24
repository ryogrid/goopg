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

// TestCorrelatedNotInStaysSubPlan: the correlated NOT IN is declined too —
// PG converts no ALL_SUBLINK, correlated or not (M0146-0002c).
func TestCorrelatedNotInStaysSubPlan(t *testing.T) {
	assertNotInStaysSubPlan(t, "SELECT x FROM t1 WHERE x NOT IN (SELECT y FROM t2 WHERE y = t1.x)")
}

// assertCorrelatedInKeepsBothConjuncts pins the correctness property the
// legacy unnest's key-selection bug broke: a correlated IN whose
// correlation is NOT the operand/select pair. PG 18.3 pulls such an IN up
// as a LATERAL semi join (convert_ANY_sublink_to_join, subselect.c:1356 —
// `use_lateral`), and so does the jointree pipeline; the semi join is
// correct only if it carries BOTH the IN equality and the correlation
// qual. The legacy unnest once keyed the join on the correlation pair
// alone (checking "does a correlated row exist" instead of "is x among
// its y values"), and refused these shapes after that fix; the legacy
// pipeline, and with it that refusal, was deleted in M0145-0008.
func assertCorrelatedInKeepsBothConjuncts(t *testing.T, sql string) {
	t.Helper()
	cat := threeColCorrelationCatalog(t)
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExpr(node); in != nil {
		return // kept as a SubPlan: per-row evaluation is always correct
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("IN neither kept nor decorrelated to a semi join: %s", planString(node))
	}
	conj := 0
	var count func(e Expr)
	count = func(e Expr) {
		if b, ok := e.(*BinaryOp); ok && b.Op == parser.OpAnd {
			count(b.Left)
			count(b.Right)
			return
		}
		if e != nil {
			conj++
		}
	}
	count(j.Predicate)
	if j.LeftKey != nil {
		conj++
	}
	if conj < 2 {
		t.Fatalf("semi join carries %d conjunct(s), want the IN equality AND the correlation qual: %s",
			conj, planString(node))
	}
}

// TestUnnestCorrelatedIn_RejectsOperandNotCorrelationColumn: correlation on
// a different column than the IN operand (`z = t1.w`).
func TestUnnestCorrelatedIn_RejectsOperandNotCorrelationColumn(t *testing.T) {
	assertCorrelatedInKeepsBothConjuncts(t, "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z = t1.w)")
}

// TestUnnestCorrelatedIn_RejectsSelectNotCorrelationColumn: correlation on
// the operand's own column (`z = t1.x`) while the subquery SELECTs a
// different column (`y`). Folding the correlation into a tautology would
// silently match on `x = y` alone — wrong whenever a t2 row has y = x but
// z != x — so the semi join must keep `z = t1.x` beside the IN equality.
func TestUnnestCorrelatedIn_RejectsSelectNotCorrelationColumn(t *testing.T) {
	assertCorrelatedInKeepsBothConjuncts(t, "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z = t1.x)")
}
