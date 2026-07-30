package planner

import (
	"testing"
)

// M0125-0036 (C3). The pins below are organised around the one question the
// conversion has to keep answering: does the rewritten form select the same
// rows as the EXISTS it replaced? Everything that could make the answer "no"
// — a NOT above the sublink, a NULL-visible position, a body whose row set the
// projection would change, a correlation the operand cannot carry — gets a
// decline pin, because a decline is always safe (the SubPlan path is the
// correctness reference) and a wrong conversion is silent.

// findInExprIn returns the first InExpr carrying a subquery plan, at the host
// level only — the same discovery rule findExistsExprIn uses.
func findInExprIn(node Node) *InExpr {
	var found *InExpr
	walkPlanExprs(node, func(e Expr) {
		if x, ok := e.(*InExpr); ok && x.Plan != nil && found == nil {
			found = x
		}
	})
	return found
}

// TestExistsToAnyConvertsOrEXISTS is the acceptance shape: TPC-DS Q10/Q35's
// `… OR EXISTS (…)`, which unnestExistsExpr cannot touch (an OR-ed sublink is
// not a top-level conjunct) and which therefore re-executed its body once per
// outer row.
func TestExistsToAnyConvertsOrEXISTS(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = -1 OR EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if ex := findExistsExprIn(node); ex != nil {
		t.Fatalf("ExistsExpr survived the conversion:\n%s", planString(node))
	}
	in := findInExprIn(node)
	if in == nil {
		t.Fatalf("no InExpr with a subquery plan:\n%s", planString(node))
	}
	// Uncorrelated is the whole point: it is what lets
	// executor/subplan_hash.go build the value set once instead of per row.
	if !in.IsNonCorrelated {
		t.Errorf("IsNonCorrelated = false, want true — the body still correlates")
	}
	if in.Negated || in.NotEqualAny || in.AnyOp != 0 {
		t.Errorf("not the plain-equality form: Negated=%v NotEqualAny=%v AnyOp=%v — "+
			"evalInHashProbe only serves plain equality",
			in.Negated, in.NotEqualAny, in.AnyOp)
	}
	// The operand must be the OUTER column, read from the host row the qual
	// is evaluated against.
	op, ok := in.Operand.(*ColumnRef)
	if !ok {
		t.Fatalf("Operand = %T, want *ColumnRef", in.Operand)
	}
	if op.Name != "x" {
		t.Errorf("Operand.Name = %q, want x", op.Name)
	}
	// collectInValues rejects any width but one.
	if got := len(in.Plan.Output()); got != 1 {
		t.Errorf("inner plan width = %d, want 1:\n%s", got, planString(node))
	}
	if got := in.Plan.Output()[0].Name; got != "z" {
		t.Errorf("inner plan column = %q, want z (the correlation's sub-side)", got)
	}
	// No PARAM_EXEC slots: lowering runs after this pass and must find
	// nothing left to bind.
	if len(in.ParParam) != 0 || len(in.Args) != 0 {
		t.Errorf("ParParam/Args = %v/%v, want empty — an uncorrelated sublink is not lowered",
			in.ParParam, in.Args)
	}
	// The lifted equality must be GONE from the body; left behind it would
	// be an unbound OuterColumnRef at build time.
	if planHasOuterRefRemaining(in.Plan) {
		t.Errorf("an OuterColumnRef survived in the body:\n%s", planString(node))
	}
}

// TestExistsToAnyDeclinesNotExists is the NULL-semantics pin. EXISTS is
// two-valued; `IN` is three-valued, and the two differ exactly when the
// operand does not match a value set that contains a NULL (FALSE vs NULL).
// Under a qual that difference is invisible — but `NOT FALSE` is TRUE while
// `NOT NULL` is NULL, so a negated EXISTS must keep its SubPlan.
func TestExistsToAnyDeclinesNotExists(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = -1 OR NOT EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findExistsExprIn(node) == nil {
		t.Fatalf("NOT EXISTS was converted — `NOT NULL` is NULL, not TRUE:\n%s", planString(node))
	}
}

// TestExistsToAnyDeclinesTopLevelConjunct pins the scope decision recorded in
// rewriteExistsToAnyQual: an AND-ed EXISTS is left to unnestExistsExpr, which
// turns it into a streaming semi-join rather than a materialised value set.
// M0125-0026 §C3 names the OR as the trigger and Q69 (all-AND) as the control
// that completes without this pass.
func TestExistsToAnyDeclinesTopLevelConjunct(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x > 0 AND EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if in := findInExprIn(node); in != nil {
		t.Fatalf("a top-level-conjunct EXISTS was converted; the semi-join pull-up owns that shape:\n%s",
			planString(node))
	}
}

// TestExistsToAnyDeclinesCompositeCorrelation: goopg's IN test expression is
// single-column (executor/subplan_hash.go), so a two-equality correlation has
// no operand to become. Upstream expresses the same shape as a ROW testexpr.
func TestExistsToAnyDeclinesCompositeCorrelation(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT a FROM t3 WHERE a = -1 OR EXISTS (SELECT 1 FROM t2 WHERE t2.z = t3.a AND t2.y = t3.b)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findExistsExprIn(node) == nil {
		t.Fatalf("a composite correlation was converted to a single-column IN:\n%s", planString(node))
	}
}

// TestExistsToAnyDeclinesNonEqualityCorrelation: the conversion lifts an
// equality into the operand position. An inequality correlation cannot be
// carried by `IN` at all, and leaving it in the body would strand an unbound
// reference.
func TestExistsToAnyDeclinesNonEqualityCorrelation(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = -1 OR EXISTS (SELECT 1 FROM t2 WHERE t2.z > t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findExistsExprIn(node) == nil {
		t.Fatalf("an inequality correlation was converted:\n%s", planString(node))
	}
}

// TestExistsToAnyDeclinesAggregatingBody: the conversion replaces "at least
// one row" with "the set of all values". A body whose spine aggregates or
// de-duplicates does not have the same row set under that change, so it keeps
// the SubPlan (upstream's simplify_EXISTS_query refuses the same shapes).
func TestExistsToAnyDeclinesAggregatingBody(t *testing.T) {
	cat := threeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = -1 OR EXISTS (SELECT count(*) FROM t2 WHERE t2.z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findExistsExprIn(node) == nil {
		t.Fatalf("an aggregating body was converted:\n%s", planString(node))
	}
}

// TestExistsToAnyKillSwitch pins the operational escape. GOOPG_EXISTS_TO_ANY=off
// must restore the pre-M0125-0036 plan exactly, which is what makes the pass
// revertible without a rebuild.
func TestExistsToAnyKillSwitch(t *testing.T) {
	SetExistsToAnyEnabled(false)
	defer SetExistsToAnyEnabled(true)
	cat := threeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x = -1 OR EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findExistsExprIn(node) == nil {
		t.Fatalf("switch off but the EXISTS was still converted:\n%s", planString(node))
	}
}
