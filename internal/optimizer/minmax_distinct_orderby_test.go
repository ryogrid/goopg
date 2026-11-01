package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestRewriteMinMaxAggDistinct — S19 acceptance (1): `SELECT DISTINCT
// max(unique2) FROM tenk1`-shape (`select distinct max(x) from t`) must still
// rewrite to the InitPlan shape, now wrapped in a Distinct (rendered "Unique"
// in EXPLAIN) node on top of the Result. M0134-0001 S19.
func TestRewriteMinMaxAggDistinct(t *testing.T) {
	cat, tbl := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT DISTINCT max(x) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	dist, ok := plan.(*Distinct)
	if !ok {
		t.Fatalf("plan root is %T, want *Distinct", plan)
	}
	_, _, _, child := digMinMaxInner(t, dist.Child)
	ios, ok := child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Limit child is %T, want *IndexOnlyScan", child)
	}
	if !ios.Backward {
		t.Fatalf("IOS.Backward = false, want true (max → Backward)")
	}
	if ios.Table != tbl {
		t.Fatalf("IOS Table = %v, want %v", ios.Table, tbl)
	}
}

// TestRewriteMinMaxAggOrderByOrdinal — S19 acceptance (2): `ORDER BY 1`
// resolves positionally against the sole rewritten target; the plan root is a
// Sort over the Result/InitPlan shape.
func TestRewriteMinMaxAggOrderByOrdinal(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t ORDER BY 1"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	sort, ok := plan.(*Sort)
	if !ok {
		t.Fatalf("plan root is %T, want *Sort", plan)
	}
	if len(sort.Keys) != 1 {
		t.Fatalf("Sort has %d keys, want 1", len(sort.Keys))
	}
	cr, ok := sort.Keys[0].Expr.(*ColumnRef)
	if !ok || cr.Index != 0 || cr.Name != "max" {
		t.Fatalf("Sort key = %#v, want ColumnRef(max) at index 0", sort.Keys[0].Expr)
	}
	digMinMaxInner(t, sort.Child)
}

// TestRewriteMinMaxAggOrderByBareAggregate — S19 acceptance (3): `ORDER BY
// max(x)` echoes the SELECT list's aggregate call verbatim; it must resolve
// via structural match (parserExprKey) against the rewritten output, not stay
// on the un-rewritten Aggregate path.
func TestRewriteMinMaxAggOrderByBareAggregate(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t ORDER BY max(x)"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	sort, ok := plan.(*Sort)
	if !ok {
		t.Fatalf("plan root is %T, want *Sort", plan)
	}
	if len(sort.Keys) != 1 {
		t.Fatalf("Sort has %d keys, want 1", len(sort.Keys))
	}
	cr, ok := sort.Keys[0].Expr.(*ColumnRef)
	if !ok || cr.Index != 0 || cr.Name != "max" {
		t.Fatalf("Sort key = %#v, want ColumnRef(max) at index 0", sort.Keys[0].Expr)
	}
	digMinMaxInner(t, sort.Child)
}

// TestRewriteMinMaxAggOrderByExpressionOverAggregate — S19 acceptance (4):
// `ORDER BY max(x)+1` contains the aggregate as a sub-expression; the
// FuncCall must be substituted with a ColumnRef into the rewritten output,
// leaving the `+1` intact.
func TestRewriteMinMaxAggOrderByExpressionOverAggregate(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t ORDER BY max(x)+1"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	sort, ok := plan.(*Sort)
	if !ok {
		t.Fatalf("plan root is %T, want *Sort", plan)
	}
	if len(sort.Keys) != 1 {
		t.Fatalf("Sort has %d keys, want 1", len(sort.Keys))
	}
	bin, ok := sort.Keys[0].Expr.(*BinaryOp)
	if !ok || bin.Op != parser.OpAdd {
		t.Fatalf("Sort key = %#v, want (max + 1)", sort.Keys[0].Expr)
	}
	cr, ok := bin.Left.(*ColumnRef)
	if !ok || cr.Index != 0 || cr.Name != "max" {
		t.Fatalf("Sort key left = %#v, want ColumnRef(max) at index 0", bin.Left)
	}
	if ic, ok := bin.Right.(*IntegerConst); !ok || ic.Value != 1 {
		t.Fatalf("Sort key right = %#v, want IntegerConst(1)", bin.Right)
	}
	digMinMaxInner(t, sort.Child)
}

// TestRewriteMinMaxAggMinOrderByOrdinal — sibling-path check (Hard-won Rule
// #2): the forward/min half must go through the same wrap path as max.
func TestRewriteMinMaxAggMinOrderByOrdinal(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(x) FROM t ORDER BY 1"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	sort, ok := plan.(*Sort)
	if !ok {
		t.Fatalf("plan root is %T, want *Sort", plan)
	}
	_, _, _, child := digMinMaxInner(t, sort.Child)
	ios, ok := child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Limit child is %T, want *IndexOnlyScan", child)
	}
	if ios.Backward {
		t.Fatalf("IOS.Backward = true, want false (min → forward)")
	}
}

// TestRewriteMinMaxAggMinDistinct — sibling-path check for DISTINCT.
func TestRewriteMinMaxAggMinDistinct(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT DISTINCT min(x) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	dist, ok := plan.(*Distinct)
	if !ok {
		t.Fatalf("plan root is %T, want *Distinct", plan)
	}
	digMinMaxInner(t, dist.Child)
}

// TestRewriteMinMaxAggOrderByUnresolvedDeclines — escape hatch: an ORDER BY
// item that is neither an ordinal, the bare aggregate call, nor an
// expression containing it (here, a DIFFERENT aggregate call, `min(x)`, on an
// `ORDER BY` for a `max(x)` SELECT target — valid SQL that PostgreSQL accepts
// by simply evaluating both aggregates over the single output row) must
// decline the rewrite and fall through to today's non-rewritten Aggregate
// path, exactly as before S19.
func TestRewriteMinMaxAggOrderByUnresolvedDeclines(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t ORDER BY min(x)"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Must NOT be the InitPlan rewrite shape (Result/Sort over it) — assert
	// the fallback explicitly, not merely "no crash".
	if _, ok := plan.(*Result); ok {
		t.Fatalf("plan root is *Result — rewrite fired despite unresolved ORDER BY item y")
	}
	sort, ok := plan.(*Sort)
	if ok {
		if _, isResult := sort.Child.(*Result); isResult {
			t.Fatalf("plan is Sort over *Result — rewrite fired despite unresolved ORDER BY item y")
		}
	}
	// The un-rewritten path is Project(Sort(Aggregate(...))) or similar —
	// assert an Aggregate node is present somewhere, proving the Aggregate
	// path (not InitPlan) executed.
	if !planContainsAggregate(plan) {
		t.Fatalf("plan has no *Aggregate node; want the ordinary Aggregate path")
	}
}

// TestRewriteMinMaxAggDistinctOnDeclines — escape hatch: DISTINCT ON is not
// plain SELECT DISTINCT and must decline the rewrite, even when its key
// matches the sole aggregate (so it would otherwise be eligible under the
// bare-column DISTINCT gate).
func TestRewriteMinMaxAggDistinctOnDeclines(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT DISTINCT ON (max(x)) max(x) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !planContainsAggregate(plan) {
		t.Fatalf("plan has no *Aggregate node; want the ordinary Aggregate path (DISTINCT ON must decline the rewrite)")
	}
}

// TestRewriteMinMaxAggMultiTargetProjectSet — M0148-0001c slice (b):
// `select max(x), generate_series(1,3) as g from t order by g desc`
// (aggregates.sql, expected block at
// postgres/src/test/regress/expected/aggregates.out:1169-1180) is no longer a
// decline: planagg.c gates on the AGGS, not the tlist length (planagg.c:160-183),
// and create_minmaxagg_plan's `make_result(tlist, quals, NULL)` keeps the whole
// surviving tlist (createplan.c:2588-2612), with the ProjectSet above expanding
// the tlist SRF. The plan must be Sort → ProjectSet → childless Result/InitPlan
// with NO Aggregate node.
func TestRewriteMinMaxAggMultiTargetProjectSet(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x), generate_series(1,3) AS g FROM t ORDER BY g DESC"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if planContainsAggregate(plan) {
		t.Fatalf("plan holds an *Aggregate; want the min/max rewrite (Sort → ProjectSet → Result):\n%s", planShapeString(plan))
	}
	sort, ok := plan.(*Sort)
	if !ok {
		t.Fatalf("plan root is %T, want *Sort", plan)
	}
	if len(sort.Keys) != 1 {
		t.Fatalf("Sort has %d keys, want 1", len(sort.Keys))
	}
	if cr, isCol := sort.Keys[0].Expr.(*ColumnRef); !isCol || cr.Index != 1 || cr.Name != "g" {
		t.Fatalf("Sort key = %#v, want ColumnRef(g) at index 1 (the SRF slot)", sort.Keys[0].Expr)
	}
	ps, ok := sort.Child.(*ProjectSet)
	if !ok {
		t.Fatalf("Sort child is %T, want *ProjectSet", sort.Child)
	}
	if len(ps.SrfCols) != 1 || ps.SrfCols[0].ColIdx != 1 {
		t.Fatalf("ProjectSet SrfCols = %+v, want one slot at ColIdx 1", ps.SrfCols)
	}
	if len(ps.OtherExprs) != 2 || ps.OtherExprs[1] != nil {
		t.Fatalf("ProjectSet OtherExprs = %+v, want 2 slots (nil at the SRF slot)", ps.OtherExprs)
	}
	if cr, isCol := ps.OtherExprs[0].(*ColumnRef); !isCol || cr.Index != 0 || cr.Name != "max" {
		t.Fatalf("agg passthrough = %#v, want ColumnRef(max) at index 0 (the Result row)", ps.OtherExprs[0])
	}
	res, ok := ps.Child.(*Result)
	if !ok {
		t.Fatalf("ProjectSet child is %T, want *Result", ps.Child)
	}
	if res.Child != nil {
		t.Fatalf("Result has a child; want the childless nodeResult.c shape")
	}
	if len(res.Targets) != 1 {
		t.Fatalf("Result has %d targets, want 1 (the SRF is expanded above, not evaluated here)", len(res.Targets))
	}
	sq, ok := res.Targets[0].(*SubqueryExpr)
	if !ok || !SublinkIsInitPlan(sq) {
		t.Fatalf("Result target is %T, want the InitPlan SubqueryExpr", res.Targets[0])
	}
	if len(res.Output()) != 1 || res.Output()[0].Name != "max" {
		t.Fatalf("Result output = %v, want the single max column", res.Output())
	}
	if len(ps.Output()) != 2 || ps.Output()[0].Name != "max" || ps.Output()[1].Name != "g" {
		t.Fatalf("ProjectSet output = %v, want (max, g)", ps.Output())
	}
}

// TestRewriteMinMaxAggMultiInitPlans — M0148-0001c slice (c): the two-agg
// `select min(x), max(x) from t` shape (aggregates.sql's minmaxtest block,
// postgres/src/test/regress/expected/aggregates.out:1069-1092) is no longer a
// decline. planagg.c preprocess_minmax_aggregates LOOPS root->agginfos
// (planagg.c:160-183) building ONE InitPlan per strict min/max agg, and
// create_minmaxagg_plan's tlist carries one Param per InitPlan over the single
// childless Result (createplan.c:2546-2612). goopg mirrors that: Result with
// two InitPlan targets — InitPlan 1 (min, forward IOS, ASC NULLS LAST) and
// InitPlan 2 (max, Backward IOS, DESC NULLS FIRST), each Limit 1.
func TestRewriteMinMaxAggMultiInitPlans(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(x), max(x) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if planContainsAggregate(plan) {
		t.Fatalf("plan holds an *Aggregate; want the multi-InitPlan rewrite:\n%s", planShapeString(plan))
	}
	res, ok := plan.(*Result)
	if !ok {
		t.Fatalf("plan root is %T, want *Result", plan)
	}
	if res.Child != nil {
		t.Fatalf("Result has a child; want the childless nodeResult.c shape")
	}
	if len(res.Targets) != 2 {
		t.Fatalf("Result has %d targets, want 2 (one InitPlan Param per agg)", len(res.Targets))
	}
	for k, wantName := range []string{"min", "max"} {
		sq, isSQ := res.Targets[k].(*SubqueryExpr)
		if !isSQ || !SublinkIsInitPlan(sq) {
			t.Fatalf("Result target %d is %T, want the InitPlan SubqueryExpr", k, res.Targets[k])
		}
		lim, isLim := sq.Plan.(*Limit)
		if !isLim {
			t.Fatalf("InitPlan %d plan is %T, want *Limit", k+1, sq.Plan)
		}
		ios, isIOS := lim.Child.(*IndexOnlyScan)
		if !isIOS {
			t.Fatalf("InitPlan %d Limit child is %T, want *IndexOnlyScan", k+1, lim.Child)
		}
		if wantBackward := wantName == "max"; ios.Backward != wantBackward {
			t.Fatalf("InitPlan %d IOS.Backward = %v, want %v (%s → %s)",
				k+1, ios.Backward, wantBackward, wantName,
				map[bool]string{true: "Backward", false: "forward"}[wantBackward])
		}
	}
	out := res.Output()
	if len(out) != 2 || out[0].Name != "min" || out[1].Name != "max" {
		t.Fatalf("Result output = %v, want (min, max)", out)
	}
}

// TestRewriteMinMaxAggMultiTargetDeclines — slice (b)/(c) scope bounds
// (deferral ledger): the rewrite stays bounded to strict min/max aggs and
// jointree-surviving extras. An aggregate extra, a non-generate_series SRF
// extra, a relation-Var extra, an expression mixing two min/max aggs, and an
// ordinal ORDER BY over a multi-target tlist all keep the ordinary Aggregate
// path. (`min(x), max(x)` left this list in slice (c): it lands as the
// multi-InitPlan rewrite above.)
func TestRewriteMinMaxAggMultiTargetDeclines(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	for _, q := range []string{
		"SELECT max(x), count(*) FROM t",                             // aggregate extra
		"SELECT max(x), unnest(ARRAY[1,2]) FROM t",                   // SRF kind we do not zip
		"SELECT min(x) + max(x) FROM t",                              // no bare min/max tlist entry
		"SELECT max(x), generate_series(1,3) AS g FROM t ORDER BY 1", // ordinal over multi-target
	} {
		plan, err := Plan(parseOne(t, q), cat)
		if err != nil {
			t.Fatalf("%s: plan: %v", q, err)
		}
		if !planContainsAggregate(plan) {
			t.Errorf("%s: want the ordinary Aggregate path (scope bound), got:\n%s", q, planShapeString(plan))
		}
	}
}

// planContainsAggregate walks the plan tree looking for an *Aggregate node,
// proving the ordinary (non-InitPlan-rewritten) Aggregate path was taken.
func planContainsAggregate(n Node) bool {
	found := false
	walkPlanNodes(n, func(child Node) {
		if _, ok := child.(*Aggregate); ok {
			found = true
		}
	})
	return found
}

// walkPlanNodes is a minimal single-child-aware plan walker sufficient for
// the shapes this test file needs (Result/Sort/Distinct/Project/Aggregate/
// Filter/SeqScan/IndexOnlyScan/Limit/ProjectSet chains all have exactly one
// Child field, reached generically below).
func walkPlanNodes(n Node, visit func(Node)) {
	if n == nil {
		return
	}
	visit(n)
	switch x := n.(type) {
	case *Result:
		walkPlanNodes(x.Child, visit)
	case *Sort:
		walkPlanNodes(x.Child, visit)
	case *Distinct:
		walkPlanNodes(x.Child, visit)
	case *DistinctOn:
		walkPlanNodes(x.Child, visit)
	case *Project:
		walkPlanNodes(x.Child, visit)
	case *ProjectSet:
		walkPlanNodes(x.Child, visit)
	case *Aggregate:
		walkPlanNodes(x.Child, visit)
	case *Filter:
		walkPlanNodes(x.Child, visit)
	case *Limit:
		walkPlanNodes(x.Child, visit)
	}
}
