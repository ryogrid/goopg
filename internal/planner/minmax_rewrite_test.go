package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// minmaxRewriteCatalog builds a single table `t(x int4, y int4)` with a btree
// index on `x` — the tenk1_unique1-like shape that lets the S6 (0134-0001 P2)
// forward/min rewrite fire, and the no-index shape that forces the SeqScan
// fallback.
func minmaxRewriteCatalog(t *testing.T) (catalog.Catalog, *catalog.Table) {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "t_x_idx"}, tbl, []string{"x"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c, tbl
}

// digMinMaxInner walks from the Result's single SubqueryExpr target down to the
// inner plan, failing the test on any shape that is not the expected rewrite.
func digMinMaxInner(t *testing.T, n Node) (*Result, *SubqueryExpr, *Limit, Node) {
	t.Helper()
	res, ok := n.(*Result)
	if !ok {
		t.Fatalf("plan root is %T, want *Result", n)
	}
	if len(res.Targets) != 1 {
		t.Fatalf("Result has %d targets, want 1", len(res.Targets))
	}
	sq, ok := res.Targets[0].(*SubqueryExpr)
	if !ok {
		t.Fatalf("Result target is %T, want *SubqueryExpr", res.Targets[0])
	}
	if !sq.IsNonCorrelated {
		t.Fatalf("SubqueryExpr.IsNonCorrelated = false, want true (InitPlan semantics)")
	}
	lim, ok := sq.Plan.(*Limit)
	if !ok {
		t.Fatalf("subquery plan is %T, want *Limit", sq.Plan)
	}
	ic, ok := lim.Limit.(*IntegerConst)
	if !ok || ic.Value != 1 {
		t.Fatalf("Limit.Limit is %#v, want IntegerConst(1)", lim.Limit)
	}
	if lim.Offset != nil {
		t.Fatalf("Limit.Offset = %T, want nil", lim.Offset)
	}
	return res, sq, lim, lim.Child
}

// TestRewriteMinMaxAggBareMinIndexOnlyScan: a bare `SELECT min(x) FROM t` over a
// btree index on x must rewrite to Result → SubqueryExpr(InitPlan) → Limit →
// IndexOnlyScan, with the `x IS NOT NULL` qual carried as the IOS Cond.
func TestRewriteMinMaxAggBareMinIndexOnlyScan(t *testing.T) {
	cat, tbl := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(x) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	ios, ok := child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Limit child is %T, want *IndexOnlyScan", child)
	}
	if ios.Index == nil || len(ios.Index.Columns) != 1 || ios.Index.Columns[0] != "x" {
		t.Fatalf("IOS index = %+v, want single-column btree on x", ios.Index)
	}
	if len(ios.Covered) != 1 || ios.Covered[0].Name != "x" {
		t.Fatalf("IOS covered = %v, want [x]", ios.Covered)
	}
	cond, ok := ios.Cond.(*IsNullExpr)
	if !ok || !cond.Negated {
		t.Fatalf("IOS Cond = %#v, want `x IS NOT NULL`", ios.Cond)
	}
	if cr, ok := cond.Operand.(*ColumnRef); !ok || cr.Name != "x" || cr.Index != 0 {
		t.Fatalf("IOS Cond operand = %#v, want ColumnRef(x) at index 0", cond.Operand)
	}
	if ios.Table != tbl {
		t.Fatalf("IOS Table = %v, want %v", ios.Table, tbl)
	}
}

// TestRewriteMinMaxAggBareMinSeqScanFallback: no index on x → the rewrite still
// fires (PG's cheapest-sorted-path can be a SeqScan — the Limit+ORDER BY
// subquery is still built), now as Limit → Sort → Filter(SeqScan).
func TestRewriteMinMaxAggBareMinSeqScanFallback(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(parseOne(t, "SELECT min(x) FROM t"), c)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, lim, child := digMinMaxInner(t, plan)
	// The SeqScan fallback needs a Project between the Limit and the Sort so
	// the InitPlan emits exactly one column (SeqScan always decodes the full
	// table row). The Project is invisible to EXPLAIN (walkPlanFiltered skips
	// Project wrappers), so the rendered shape stays Limit -> Sort -> SeqScan.
	proj, ok := child.(*Project)
	if !ok {
		t.Fatalf("Limit child is %T, want *Project", child)
	}
	if len(proj.Targets) != 1 {
		t.Fatalf("Project has %d targets, want 1", len(proj.Targets))
	}
	if cr, ok := proj.Targets[0].(*ColumnRef); !ok || cr.Name != "x" {
		t.Fatalf("Project target = %#v, want ColumnRef(x)", proj.Targets[0])
	}
	if len(proj.schema) != 1 || proj.schema[0].Name != "x" {
		t.Fatalf("Project schema = %+v, want single column x", proj.schema)
	}
	sort, ok := proj.Child.(*Sort)
	if !ok {
		t.Fatalf("Project child is %T, want *Sort", proj.Child)
	}
	if len(sort.Keys) != 1 || sort.Keys[0].Desc || sort.Keys[0].NullsFirst {
		t.Fatalf("Sort keys = %+v, want [x ASC NULLS LAST]", sort.Keys)
	}
	f, ok := sort.Child.(*Filter)
	if !ok {
		t.Fatalf("Sort child is %T, want *Filter", sort.Child)
	}
	if _, ok := f.Child.(*SeqScan); !ok {
		t.Fatalf("Filter child is %T, want *SeqScan", f.Child)
	}
	_ = lim
}

// TestRewriteMinMaxAggBareMaxRewrites: bare `max(x)` (Slice 2's Backward
// direction) MUST now rewrite — the plan root is the Result/InitPlan shape, not
// the pre-Slice-2 Project-over-Aggregate, and the Result's output column is
// named after the function.
func TestRewriteMinMaxAggBareMaxRewrites(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// digMinMaxInner walks Result → SubqueryExpr(InitPlan) → Limit and fatals on
	// any other shape (e.g. the old Aggregate path's Project → Aggregate).
	res, _, _, _ := digMinMaxInner(t, plan)
	if len(res.schema) != 1 || res.schema[0].Name != "max" {
		t.Fatalf("Result schema = %+v, want single column named max", res.schema)
	}
}

// TestRewriteMinMaxAggBareMaxIndexOnlyScan: a bare `SELECT max(x) FROM t` over a
// btree index on x rewrites to Result → SubqueryExpr(InitPlan) → Limit →
// IndexOnlyScan carrying Backward: true (PG's `Index Only Scan Backward` — the
// ASC index read in reverse delivers DESC NULLS FIRST = the max).
func TestRewriteMinMaxAggBareMaxIndexOnlyScan(t *testing.T) {
	cat, tbl := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	ios, ok := child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Limit child is %T, want *IndexOnlyScan", child)
	}
	if !ios.Backward {
		t.Fatalf("IOS.Backward = false, want true (max → Backward)")
	}
	if ios.Index == nil || len(ios.Index.Columns) != 1 || ios.Index.Columns[0] != "x" {
		t.Fatalf("IOS index = %+v, want single-column btree on x", ios.Index)
	}
	if len(ios.Covered) != 1 || ios.Covered[0].Name != "x" {
		t.Fatalf("IOS covered = %v, want [x]", ios.Covered)
	}
	if ios.Table != tbl {
		t.Fatalf("IOS Table = %v, want %v", ios.Table, tbl)
	}
}

// TestRewriteMinMaxAggBareMaxSeqScanFallback: no index on x → the max rewrite
// still fires via the SeqScan fallback, with the Sort reversed to DESC NULLS
// FIRST (max's reverse sort clause — fetch_agg_sort_op `>`, planagg.c:163-179).
func TestRewriteMinMaxAggBareMaxSeqScanFallback(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t"), c)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, lim, child := digMinMaxInner(t, plan)
	// The SeqScan fallback needs a Project between the Limit and the Sort so the
	// InitPlan emits exactly one column (SeqScan always decodes the full table
	// row); the Project is invisible to EXPLAIN.
	proj, ok := child.(*Project)
	if !ok {
		t.Fatalf("Limit child is %T, want *Project", child)
	}
	sort, ok := proj.Child.(*Sort)
	if !ok {
		t.Fatalf("Project child is %T, want *Sort", proj.Child)
	}
	if len(sort.Keys) != 1 || !sort.Keys[0].Desc || !sort.Keys[0].NullsFirst {
		t.Fatalf("Sort keys = %+v, want [x DESC NULLS FIRST]", sort.Keys)
	}
	f, ok := sort.Child.(*Filter)
	if !ok {
		t.Fatalf("Sort child is %T, want *Filter", f)
	}
	if _, ok := f.Child.(*SeqScan); !ok {
		t.Fatalf("Filter child is %T, want *SeqScan", f.Child)
	}
	_ = lim
}

// TestRewriteMinMaxAggMaxNullTrap: a table whose max column contains NULLs must
// still return the max NON-NULL. The executor's reverse loop emits rows[len-1]
// first, so the `x IS NOT NULL` Cond MUST ride on the Backward IOS — dropping it
// would leak a NULL for a NULL-containing table (the NULL-trap rule). The
// plan-level guard is asserted here; the executor keeps the Cond check verbatim
// in both directions (operators_indexonly.go Next).
func TestRewriteMinMaxAggMaxNullTrap(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	ios, ok := child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Limit child is %T, want *IndexOnlyScan", child)
	}
	if !ios.Backward {
		t.Fatalf("IOS.Backward = false, want true")
	}
	cond, ok := ios.Cond.(*IsNullExpr)
	if !ok || !cond.Negated {
		t.Fatalf("IOS Cond = %#v, want `x IS NOT NULL` on the Backward IOS", ios.Cond)
	}
	if cr, ok := cond.Operand.(*ColumnRef); !ok || cr.Name != "x" || cr.Index != 0 {
		t.Fatalf("IOS Cond operand = %#v, want ColumnRef(x) at index 0", cond.Operand)
	}
}

// TestRewriteMinMaxAggGroupedNotRewritten: `min(x) GROUP BY y` must stay on the
// grouped Aggregate path.
func TestRewriteMinMaxAggGroupedNotRewritten(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(x) FROM t GROUP BY y"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	proj, ok := plan.(*Project)
	if !ok {
		t.Fatalf("plan root is %T, want *Project", plan)
	}
	agg, ok := proj.Child.(*Aggregate)
	if !ok {
		t.Fatalf("Project child is %T, want *Aggregate", proj.Child)
	}
	if len(agg.GroupExprs) != 1 {
		t.Fatalf("Aggregate group exprs = %d, want 1 (GROUP BY y)", len(agg.GroupExprs))
	}
}

// TestRewriteMinMaxAggExpressionArgNotRewritten: `min(x + 1)` is not a plain
// column Var — the rewrite must decline and the Aggregate path stays.
func TestRewriteMinMaxAggExpressionArgNotRewritten(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(x + 1) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, ok := plan.(*Result); ok {
		t.Fatalf("expression-arg min(x+1) was rewritten to Result; must stay Aggregate")
	}
}

