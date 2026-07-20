package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Stage S2b (design bundle correlated-subquery-planning, D4.1): the
// PARAM_EXEC lowering pass. These tests pin the plan-side contract —
// slot assignment, Arg recording, Level≥2 forwarding through the
// intermediate sublink, exclusion of the stack-path shapes, and the
// IsNonCorrelated invariant.
//
// Probe shapes park sublinks under an OR arm (or use nested sublinks)
// so the S1a guards keep them as SubPlans — a top-level correlated
// sublink over these fixtures would be decorrelated away and leave
// nothing to lower.

// threeTablesCatalog: t1(x), t2(y,z), t3(a,b) — index-less so plans
// stay Filter(SeqScan)-shaped and firmly inside the lowering walker's
// modelled node set.
func threeTablesCatalog(t *testing.T) catalog.Catalog {
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
	if _, err := c.CreateTable(parser.ObjectName{Name: "t3"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// findExistsExprIn returns the first ExistsExpr found in the plan's
// expressions (host level only — walkPlanExprs does not descend into
// sublink plans, mirroring how the lowering pass discovers hosts).
func findExistsExprIn(node Node) *ExistsExpr {
	var found *ExistsExpr
	walkPlanExprs(node, func(e Expr) {
		if x, ok := e.(*ExistsExpr); ok && found == nil {
			found = x
		}
	})
	return found
}

func findSubqueryExprIn(node Node) *SubqueryExpr {
	var found *SubqueryExpr
	walkPlanExprs(node, func(e Expr) {
		if x, ok := e.(*SubqueryExpr); ok && found == nil {
			found = x
		}
	})
	return found
}

func findArraySubqueryIn(node Node) *ArraySubqueryExpr {
	var found *ArraySubqueryExpr
	walkPlanExprs(node, func(e Expr) {
		if x, ok := e.(*ArraySubqueryExpr); ok && found == nil {
			found = x
		}
	})
	return found
}

// countRefsInPlan counts OuterColumnRef / ExecParamRef nodes reachable
// in a sublink's inner plan via the lowering traversal itself, so the
// assertion sees exactly what the executor's modelled paths see.
func countRefsInPlan(t *testing.T, plan Node) (outer, execParam int) {
	t.Helper()
	ok := lowerTraverseNode(plan, func(e Expr) (Expr, bool, bool) {
		switch e.(type) {
		case *OuterColumnRef:
			outer++
			return e, true, true
		case *ExecParamRef:
			execParam++
			return e, true, true
		case *SubqueryExpr, *ExistsExpr, *InExpr, *ArraySubqueryExpr, *MultiAssignSubqRow:
			// Do not descend into nested sublink plans here; the
			// per-sublink assertions handle each level explicitly.
			return e, true, true
		}
		return e, false, true
	})
	if !ok {
		t.Fatalf("lowering traversal bailed on the probe plan — fixture drifted outside the modelled node set")
	}
	return outer, execParam
}

func TestLowerSingleLevelExists(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = -1 OR EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	ex := findExistsExprIn(node)
	if ex == nil {
		t.Fatalf("probe lost its ExistsExpr (unnested?):\n%s", planString(node))
	}
	if len(ex.ParParam) != 1 || len(ex.Args) != 1 {
		t.Fatalf("ParParam/Args = %v/%v, want one slot", ex.ParParam, ex.Args)
	}
	arg, ok := ex.Args[0].(*ColumnRef)
	if !ok {
		t.Fatalf("Args[0] = %T, want *ColumnRef (immediate-parent ref)", ex.Args[0])
	}
	if arg.Name != "x" {
		t.Errorf("Args[0].Name = %q, want x", arg.Name)
	}
	outer, execp := countRefsInPlan(t, ex.Plan)
	if outer != 0 {
		t.Errorf("%d OuterColumnRef survive in the lowered plan (want 0)", outer)
	}
	if execp == 0 {
		t.Errorf("no ExecParamRef in the lowered plan — correlation lost")
	}
	if ex.IsNonCorrelated {
		t.Errorf("IsNonCorrelated flipped true on a correlated sublink")
	}
}

// TestLowerTwoLevelForwarding is the M8 shape: the innermost sublink
// references the outermost query (Level 2), which must forward through
// the intermediate sublink's ParParam/Args chain — the intermediate
// grows a param whose Arg is itself an ExecParamRef.
func TestLowerTwoLevelForwarding(t *testing.T) {
	cat := threeTablesCatalog(t)
	// The inner correlation is deliberately NON-equi (`t3.a > t1.x`):
	// an equijoin inner would be decorrelated inside the outer subplan
	// into a semi join carrying the Level-2 ref on a join predicate —
	// the LATERAL shape the lowering analysis conservatively refuses.
	// Non-equi-only correlation bails the unnest (M14), so both levels
	// survive as a true SubPlan chain.
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.y = 1 AND EXISTS (SELECT 1 FROM t3 WHERE t3.a > t1.x))"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	outerEx := findExistsExprIn(node)
	if outerEx == nil {
		t.Fatalf("outer ExistsExpr missing (hasNestedSub guard should keep it):\n%s", planString(node))
	}
	if len(outerEx.ParParam) != 1 {
		t.Fatalf("outer ParParam = %v, want exactly one forwarded slot", outerEx.ParParam)
	}
	if _, ok := outerEx.Args[0].(*ColumnRef); !ok {
		t.Fatalf("outer Args[0] = %T, want *ColumnRef (fills the forwarded slot from t1.x)", outerEx.Args[0])
	}
	innerEx := findExistsExprIn(outerEx.Plan)
	if innerEx == nil {
		t.Fatalf("nested ExistsExpr missing inside the outer plan:\n%s", planString(outerEx.Plan))
	}
	if len(innerEx.ParParam) != 1 {
		t.Fatalf("inner ParParam = %v, want one slot", innerEx.ParParam)
	}
	fwd, ok := innerEx.Args[0].(*ExecParamRef)
	if !ok {
		t.Fatalf("inner Args[0] = %T, want *ExecParamRef (forwarded through the intermediate)", innerEx.Args[0])
	}
	if fwd.ID != outerEx.ParParam[0] {
		t.Errorf("forwarded Arg reads slot %d, want the outer sublink's slot %d", fwd.ID, outerEx.ParParam[0])
	}
	if innerEx.ParParam[0] == outerEx.ParParam[0] {
		t.Errorf("inner and outer sublinks share slot %d — the flat space must not collide", innerEx.ParParam[0])
	}
	outer, execp := countRefsInPlan(t, innerEx.Plan)
	if outer != 0 || execp == 0 {
		t.Errorf("innermost plan: %d OuterColumnRef / %d ExecParamRef, want 0 / >0", outer, execp)
	}
	// Bonus property: the outer sublink's own expressions contain no
	// refs (they all sit two levels down), so the pre-lowering
	// IsNonCorrelated computation — blind to nested sublink plans —
	// called it non-correlated and would have cached it under a
	// constant key. The forwarded param corrects the flag; the derived
	// value is what the eval sites now trust.
	if outerEx.IsNonCorrelated {
		t.Errorf("outer sublink still marked non-correlated despite the forwarded Level-2 dependency")
	}
}

// TestLowerArraySubqueryUntouched: excluded eval sites keep the stack
// path — their plans must still contain OuterColumnRef and gain no
// params anywhere.
func TestLowerArraySubqueryUntouched(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT ARRAY(SELECT z FROM t2 WHERE t2.y = t1.x) FROM t1"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	arr := findArraySubqueryIn(node)
	if arr == nil {
		t.Fatalf("ArraySubqueryExpr missing:\n%s", planString(node))
	}
	hasOuter := false
	walkPlanExprs(arr.Plan, func(e Expr) {
		if _, ok := e.(*OuterColumnRef); ok {
			hasOuter = true
		}
	})
	if !hasOuter {
		t.Errorf("excluded ArraySubqueryExpr lost its OuterColumnRef — it must stay on the stack path")
	}
}

// TestLowerNonCorrelatedInvariant: a surviving non-correlated sublink
// gains no params and keeps IsNonCorrelated (no mismatch counted).
func TestLowerNonCorrelatedInvariant(t *testing.T) {
	cat := threeTablesCatalog(t)
	before := SubplanLowerMismatches()
	// Two-column inner suppresses the non-correlated IN unnest
	// (isUnnestableNonCorrelatedIn needs a single output column).
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y, z FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	in := findInExpr(node)
	if in == nil {
		t.Fatalf("InExpr missing:\n%s", planString(node))
	}
	if len(in.ParParam) != 0 {
		t.Errorf("non-correlated sublink gained params: %v", in.ParParam)
	}
	if !in.IsNonCorrelated {
		t.Errorf("IsNonCorrelated lost on a non-correlated sublink")
	}
	if got := SubplanLowerMismatches(); got != before {
		t.Errorf("mismatch counter moved %d -> %d on an agreeing sublink", before, got)
	}
}

// TestLowerScalarUnderOR: the guard-parked correlated scalar lowers
// like EXISTS and its inner Filter now compares against $N.
func TestLowerScalarUnderOR(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = -1 OR x > (SELECT min(z) FROM t2 WHERE t2.y = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	sub := findSubqueryExprIn(node)
	if sub == nil {
		t.Fatalf("SubqueryExpr missing (unnested?):\n%s", planString(node))
	}
	if len(sub.ParParam) != 1 {
		t.Fatalf("ParParam = %v, want one slot", sub.ParParam)
	}
	outer, execp := countRefsInPlan(t, sub.Plan)
	if outer != 0 || execp == 0 {
		t.Errorf("lowered scalar plan: %d OuterColumnRef / %d ExecParamRef, want 0 / >0", outer, execp)
	}
}
