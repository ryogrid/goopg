package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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

// M0125-0042. A hand-written OR-ed `col IN (subquery)` cannot unnest, so its
// operand ColumnRef keeps whatever Index the binder assigned against a
// partial join layout — nothing else re-resolves it by Name the way an
// unnested single IN's semi-join keys are (rebind/predRebind). Reaching this
// naturally needs a specific binding history through a MultiHashJoin OID
// re-sort; the design doc's synthetic 6-table case reaches the same plan
// SHAPE without that history and still answers correctly, so this pins the
// resolution mechanism (fixInExprOperandIndex) directly rather than a query
// result that could pass for the wrong reason.
func stubSubqueryPlan(t *testing.T) Node {
	t.Helper()
	tbl := &catalog.Table{Name: "t2", Columns: []catalog.Column{
		{Name: "z", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	}}
	return &SeqScan{Table: tbl, schema: Schema{{Name: "z", Type: catalog.Type{Name: "int4"}}}}
}

func TestFixInExprOperandIndexReResolvesStaleIndex(t *testing.T) {
	// hostRow mirrors the filed defect: index 0 is a same-typed DIFFERENT
	// column left behind by an earlier partial-layout binding (goopg's
	// ca_zip), index 1 is c_customer_sk's real, final position.
	hostRow := Schema{
		{Name: "other_col", Type: catalog.Type{Name: "text"}},
		{Name: "c_customer_sk", Type: catalog.Type{Name: "int4"}},
	}
	in := &InExpr{
		Operand: &ColumnRef{Name: "c_customer_sk", Index: 0, Type: catalog.Type{Name: "int4"}},
		Plan:    stubSubqueryPlan(t),
	}
	fixInExprOperandIndex(in, hostRow)
	op := in.Operand.(*ColumnRef)
	if op.Index != 1 {
		t.Fatalf("Operand.Index = %d, want 1 (c_customer_sk's position in hostRow); "+
			"a stale index left at 0 reads other_col instead", op.Index)
	}
}

// An ambiguous name must decline, not guess: M0125-0039 recorded that a
// confidently wrong index is worse than a stale one left alone.
func TestFixInExprOperandIndexDeclinesAmbiguousName(t *testing.T) {
	hostRow := Schema{
		{Name: "c_customer_sk", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		{Name: "c_customer_sk", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2},
	}
	in := &InExpr{
		Operand: &ColumnRef{Name: "c_customer_sk", Index: 5, Type: catalog.Type{Name: "int4"}},
		Plan:    stubSubqueryPlan(t),
	}
	fixInExprOperandIndex(in, hostRow)
	op := in.Operand.(*ColumnRef)
	if op.Index != 5 {
		t.Fatalf("Operand.Index = %d, want unchanged 5 — an ambiguous Name-only match must decline", op.Index)
	}
}

// SourceTableIdx must disambiguate a self-join before falling back to Name
// alone, matching resolveHostOperandIdx / resolveSide.
func TestFixInExprOperandIndexDisambiguatesSelfJoinBySourceTableIdx(t *testing.T) {
	hostRow := Schema{
		{Name: "c_customer_sk", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		{Name: "c_customer_sk", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2},
	}
	in := &InExpr{
		Operand: &ColumnRef{Name: "c_customer_sk", Index: 5, Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2},
		Plan:    stubSubqueryPlan(t),
	}
	fixInExprOperandIndex(in, hostRow)
	op := in.Operand.(*ColumnRef)
	if op.Index != 1 {
		t.Fatalf("Operand.Index = %d, want 1 (SourceTableIdx=2's position)", op.Index)
	}
}

// TestRewriteExistsToAnyNodeFixesOrEdInOperandUnderFilter wires the fix into
// the actual walk: a Filter's hostRow is n.Child.Output(), and an OR-ed
// InExpr (which cannot unnest and so is never visited by rewriteExistsToAny's
// own conversion) must still have its operand re-resolved by the fallback
// branch of rewriteExistsToAnyQual.
func TestRewriteExistsToAnyNodeFixesOrEdInOperandUnderFilter(t *testing.T) {
	tbl := &catalog.Table{Name: "t1", Columns: []catalog.Column{
		{Name: "other_col", Type: catalog.Type{Name: "text"}, Ordinal: 0},
		{Name: "x", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	}}
	child := &SeqScan{Table: tbl, schema: Schema{
		{Name: "other_col", Type: catalog.Type{Name: "text"}},
		{Name: "x", Type: catalog.Type{Name: "int4"}},
	}}
	filt := &Filter{
		Child: child,
		Predicate: &BinaryOp{
			Op: parser.OpOr,
			Left: &InExpr{
				Operand: &ColumnRef{Name: "x", Index: 0, Type: catalog.Type{Name: "int4"}},
				Plan:    stubSubqueryPlan(t),
			},
			Right: &InExpr{
				Operand: &ColumnRef{Name: "x", Index: 0, Type: catalog.Type{Name: "int4"}},
				Plan:    stubSubqueryPlan(t),
			},
		},
	}
	rewriteExistsToAnyNode(filt)
	bin := filt.Predicate.(*BinaryOp)
	for _, side := range []Expr{bin.Left, bin.Right} {
		in, ok := side.(*InExpr)
		if !ok {
			t.Fatalf("side = %T, want *InExpr", side)
		}
		op := in.Operand.(*ColumnRef)
		if op.Index != 1 {
			t.Errorf("Operand.Index = %d, want 1 (x's position in child.Output())", op.Index)
		}
	}
}
