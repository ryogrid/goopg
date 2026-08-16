package optimizer

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

// TestRewriteMinMaxAggConstArg — S6 Slice 3d acceptance: `SELECT max(100) FROM t`
// (aggregates.sql block 14) must rewrite to the const-arg shape:
// Result → SubqueryExpr(InitPlan) → Limit →
// Result{OneTimeFilter: (100 IS NOT NULL), Child: SeqScan, Targets: [100]}.
// The inner Result is goopg's first Result-with-child (nodeResult.c
// outerPlan(plan) variant); its schema type is int4 — ExprResultType's
// make_const answer — NOT the int8 the normal aggregate path would type a
// bare integer literal.
func TestRewriteMinMaxAggConstArg(t *testing.T) {
	cat, tbl := minmaxRewriteCatalog(t)

	// max(100): the Limit's inner is a *Result carrying the constant target,
	// the one-time filter, and the SeqScan child.
	plan, err := Plan(parseOne(t, "SELECT max(100) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, _, _, child := digMinMaxInner(t, plan)
	if len(res.schema) != 1 || res.schema[0].Name != "max" {
		t.Fatalf("Result schema = %+v, want single column named max", res.schema)
	}
	if res.schema[0].Type.Name != "int4" {
		t.Fatalf("Result schema type = %+v, want int4 (make_const small-literal typing)", res.schema[0].Type)
	}
	inner, ok := child.(*Result)
	if !ok {
		t.Fatalf("Limit child is %T, want *Result (const-arg inner)", child)
	}
	if inner.Child == nil {
		t.Fatalf("inner Result has nil Child, want *SeqScan")
	}
	seq, ok := inner.Child.(*SeqScan)
	if !ok {
		t.Fatalf("inner Result Child is %T, want *SeqScan", inner.Child)
	}
	if seq.Table != tbl {
		t.Fatalf("inner SeqScan Table = %v, want %v", seq.Table, tbl)
	}
	otf, ok := inner.OneTimeFilter.(*IsNullExpr)
	if !ok || !otf.Negated {
		t.Fatalf("inner OneTimeFilter = %#v, want `100 IS NOT NULL`", inner.OneTimeFilter)
	}
	if ic, ok := otf.Operand.(*IntegerConst); !ok || ic.Value != 100 {
		t.Fatalf("inner OneTimeFilter operand = %#v, want IntegerConst(100)", otf.Operand)
	}
	if len(inner.Targets) != 1 {
		t.Fatalf("inner Result has %d targets, want 1", len(inner.Targets))
	}
	if ic, ok := inner.Targets[0].(*IntegerConst); !ok || ic.Value != 100 {
		t.Fatalf("inner Result target = %#v, want IntegerConst(100)", inner.Targets[0])
	}

	// min(100): same const-arg shape, forward label min.
	plan, err = Plan(parseOne(t, "SELECT min(100) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, _, _, child = digMinMaxInner(t, plan)
	if len(res.schema) != 1 || res.schema[0].Name != "min" {
		t.Fatalf("Result schema = %+v, want single column named min", res.schema)
	}
	inner, ok = child.(*Result)
	if !ok {
		t.Fatalf("Limit child is %T, want *Result (const-arg inner)", child)
	}
	if _, ok := inner.OneTimeFilter.(*IsNullExpr); !ok {
		t.Fatalf("inner OneTimeFilter = %#v, want *IsNullExpr", inner.OneTimeFilter)
	}
	if _, ok := inner.Child.(*SeqScan); !ok {
		t.Fatalf("inner Result Child is %T, want *SeqScan", inner.Child)
	}
}

// TestRewriteMinMaxAggFilteredMaxIOS — S6 Slice 3b acceptance (a): a filtered
// `SELECT max(x) FROM t WHERE x < 42` must push the qual into the IOS Cond as
// `((x IS NOT NULL) AND (x < 42))` — IS NOT NULL first (planagg.c:385-396
// lcons-prepends; formatExprQual renders strict left-then-right) — with every
// agg-column ref at Index 0 (the 1-wide covered-row position), on a Backward IOS.
func TestRewriteMinMaxAggFilteredMaxIOS(t *testing.T) {
	cat, tbl := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(x) FROM t WHERE x < 42"), cat)
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
	if ios.Table != tbl {
		t.Fatalf("IOS Table = %v, want %v", ios.Table, tbl)
	}
	cond, ok := ios.Cond.(*BinaryOp)
	if !ok || cond.Op != parser.OpAnd {
		t.Fatalf("IOS Cond = %#v, want AND of is-null + qual", ios.Cond)
	}
	// LEFT conjunct must be `x IS NOT NULL` (planagg.c lcons-prepend).
	ln, ok := cond.Left.(*IsNullExpr)
	if !ok || !ln.Negated {
		t.Fatalf("Cond left = %#v, want `x IS NOT NULL`", cond.Left)
	}
	if cr, ok := ln.Operand.(*ColumnRef); !ok || cr.Name != "x" || cr.Index != 0 {
		t.Fatalf("Cond left operand = %#v, want ColumnRef(x) at index 0", ln.Operand)
	}
	// RIGHT conjunct must be the pushed `x < 42` with its agg ref at Index 0.
	lt, ok := cond.Right.(*BinaryOp)
	if !ok || lt.Op != parser.OpLt {
		t.Fatalf("Cond right = %#v, want `x < 42`", cond.Right)
	}
	if cr, ok := lt.Left.(*ColumnRef); !ok || cr.Name != "x" || cr.Index != 0 {
		t.Fatalf("Cond right operand = %#v, want ColumnRef(x) at index 0", lt.Left)
	}
}

// TestRewriteMinMaxAggFilteredMinIOS — S6 Slice 3b acceptance (b): the forward
// (min) filtered case mirrors the max shape on a non-Backward IOS.
func TestRewriteMinMaxAggFilteredMinIOS(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(x) FROM t WHERE x < 42"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	ios, ok := child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Limit child is %T, want *IndexOnlyScan", child)
	}
	if ios.Backward {
		t.Fatalf("IOS.Backward = true, want false (min → forward)")
	}
	cond, ok := ios.Cond.(*BinaryOp)
	if !ok || cond.Op != parser.OpAnd {
		t.Fatalf("IOS Cond = %#v, want AND of is-null + qual", ios.Cond)
	}
	if ln, ok := cond.Left.(*IsNullExpr); !ok || !ln.Negated {
		t.Fatalf("Cond left = %#v, want `x IS NOT NULL`", cond.Left)
	}
	lt, ok := cond.Right.(*BinaryOp)
	if !ok || lt.Op != parser.OpLt {
		t.Fatalf("Cond right = %#v, want `x < 42`", cond.Right)
	}
	if cr, ok := lt.Left.(*ColumnRef); !ok || cr.Name != "x" || cr.Index != 0 {
		t.Fatalf("Cond right operand = %#v, want ColumnRef(x) at index 0", lt.Left)
	}
}

// TestRewriteMinMaxAggFilteredNonCoveredFallback — S6 Slice 3b acceptance (c):
// `min(x) WHERE y = 3` references a NON-covered column, so the qual MUST NOT be
// pushed into the IOS Cond; the rewrite stays on the SeqScan fallback, where the
// wherePred refs keep their full-table positions.
func TestRewriteMinMaxAggFilteredNonCoveredFallback(t *testing.T) {
	cat, _ := minmaxRewriteCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(x) FROM t WHERE y = 3"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	if _, ok := child.(*IndexOnlyScan); ok {
		t.Fatalf("qual on non-covered column y rewrote to IndexOnlyScan; must stay SeqScan fallback")
	}
	proj, ok := child.(*Project)
	if !ok {
		t.Fatalf("Limit child is %T, want *Project (SeqScan fallback)", child)
	}
	sort, ok := proj.Child.(*Sort)
	if !ok {
		t.Fatalf("Project child is %T, want *Sort", proj.Child)
	}
	f, ok := sort.Child.(*Filter)
	if !ok {
		t.Fatalf("Sort child is %T, want *Filter", sort.Child)
	}
	if _, ok := f.Child.(*SeqScan); !ok {
		t.Fatalf("Filter child is %T, want *SeqScan", f.Child)
	}
	// Predicate = andExpr(wherePred, isNotNull): left is `y = 3` with y at its
	// TABLE position (Index 1); right is `x IS NOT NULL` at x's table position
	// (Index 0).
	pred, ok := f.Predicate.(*BinaryOp)
	if !ok || pred.Op != parser.OpAnd {
		t.Fatalf("Filter predicate = %#v, want AND of qual + is-null", f.Predicate)
	}
	eq, ok := pred.Left.(*BinaryOp)
	if !ok || eq.Op != parser.OpEq {
		t.Fatalf("Filter predicate left = %#v, want `y = 3`", pred.Left)
	}
	if cr, ok := eq.Left.(*ColumnRef); !ok || cr.Name != "y" || cr.Index != 1 {
		t.Fatalf("Filter predicate left operand = %#v, want ColumnRef(y) at table index 1", eq.Left)
	}
	rn, ok := pred.Right.(*IsNullExpr)
	if !ok || !rn.Negated {
		t.Fatalf("Filter predicate right = %#v, want `x IS NOT NULL`", pred.Right)
	}
	if cr, ok := rn.Operand.(*ColumnRef); !ok || cr.Name != "x" || cr.Index != 0 {
		t.Fatalf("Filter predicate right operand = %#v, want ColumnRef(x) at table index 0", rn.Operand)
	}
}

// TestRewriteMinMaxAggFilteredNonLeadingColumnIOS — S6 Slice 3b acceptance (d):
// an agg column at table position != 0 (index on `y`) with a WHERE qual must
// still rewrite to IOS, and EVERY agg-column ref in the Cond (is-not-null AND
// pushed qual) must be at Index 0 — the 1-wide covered-row position, NOT
// argCR.Index. This regresses the latent bug where the shared isNotNull used the
// table position on the IOS (correct only for a position-0 agg column).
func TestRewriteMinMaxAggFilteredNonLeadingColumnIOS(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "t_y_idx"}, tbl, []string{"y"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(parseOne(t, "SELECT min(y) FROM t WHERE y < 42"), c)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	ios, ok := child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Limit child is %T, want *IndexOnlyScan", child)
	}
	if ios.Index == nil || len(ios.Index.Columns) != 1 || ios.Index.Columns[0] != "y" {
		t.Fatalf("IOS index = %+v, want single-column btree on y", ios.Index)
	}
	if len(ios.Covered) != 1 || ios.Covered[0].Name != "y" {
		t.Fatalf("IOS covered = %v, want [y]", ios.Covered)
	}
	cond, ok := ios.Cond.(*BinaryOp)
	if !ok || cond.Op != parser.OpAnd {
		t.Fatalf("IOS Cond = %#v, want AND of is-null + qual", ios.Cond)
	}
	ln, ok := cond.Left.(*IsNullExpr)
	if !ok || !ln.Negated {
		t.Fatalf("Cond left = %#v, want `y IS NOT NULL`", cond.Left)
	}
	if cr, ok := ln.Operand.(*ColumnRef); !ok || cr.Name != "y" || cr.Index != 0 {
		t.Fatalf("Cond left operand = %#v, want ColumnRef(y) at index 0 (covered row)", ln.Operand)
	}
	lt, ok := cond.Right.(*BinaryOp)
	if !ok || lt.Op != parser.OpLt {
		t.Fatalf("Cond right = %#v, want `y < 42`", cond.Right)
	}
	if cr, ok := lt.Left.(*ColumnRef); !ok || cr.Name != "y" || cr.Index != 0 {
		t.Fatalf("Cond right operand = %#v, want ColumnRef(y) at index 0 (covered row)", lt.Left)
	}
}

// minmaxCompositeCatalog builds a single table `t(x int4, y int4, z int4)` with
// a composite btree index on [x, y] — the tenk1_thous_tenthous shape that lets
// the S6 Slice 3c composite-prefix rewrite fire (prefix col x bound by a WHERE
// equality, agg col y next-and-last). z is the non-index column for the
// fallback probes.
func minmaxCompositeCatalog(t *testing.T) (catalog.Catalog, *catalog.Table) {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
		{Name: "z", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "t_xy_idx"}, tbl, []string{"x", "y"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c, tbl
}

// digCompositeIos asserts the Limit child is the slice-down Project above the
// composite IndexOnlyScan (S6 Slice 3c shape) and returns the IOS.
func digCompositeIos(t *testing.T, child Node) *IndexOnlyScan {
	t.Helper()
	proj, ok := child.(*Project)
	if !ok {
		t.Fatalf("Limit child is %T, want *Project (composite IOS needs a slice-down)", child)
	}
	ios, ok := proj.Child.(*IndexOnlyScan)
	if !ok {
		t.Fatalf("Project child is %T, want *IndexOnlyScan", proj.Child)
	}
	if len(proj.Targets) != 1 {
		t.Fatalf("Project has %d targets, want 1", len(proj.Targets))
	}
	if cr, ok := proj.Targets[0].(*ColumnRef); !ok || cr.Name != "y" || cr.Index != 1 {
		t.Fatalf("Project target = %#v, want ColumnRef(y) at covered index 1", proj.Targets[0])
	}
	if len(proj.schema) != 1 || proj.schema[0].Name != "y" {
		t.Fatalf("Project schema = %+v, want single column y", proj.schema)
	}
	return ios
}

// TestRewriteMinMaxAggCompositePrefixMinIOS — S6 Slice 3c acceptance (a): a
// `SELECT min(y) FROM t WHERE x = 33` over a composite btree [x, y] (the
// tenk1_thous_tenthous shape) must rewrite to Result → SubqueryExpr(InitPlan) →
// Limit → Project → IndexOnlyScan with Covered = [x, y], Backward = false, and
// Cond = `((x = 33) AND (y IS NOT NULL))` — prefix qual LEFT, the agg col's
// IS NOT NULL at its covered position 1 RIGHT (the reverse of Slice 3b's
// single-col order), matching `aggregates.out:1052-1057`.
func TestRewriteMinMaxAggCompositePrefixMinIOS(t *testing.T) {
	cat, tbl := minmaxCompositeCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(y) FROM t WHERE x = 33"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	ios := digCompositeIos(t, child)
	if ios.Index == nil || ios.Index.Name != "t_xy_idx" ||
		len(ios.Index.Columns) != 2 || ios.Index.Columns[0] != "x" || ios.Index.Columns[1] != "y" {
		t.Fatalf("IOS index = %+v, want composite btree [x, y]", ios.Index)
	}
	if ios.Backward {
		t.Fatalf("IOS.Backward = true, want false (min → forward)")
	}
	if len(ios.Covered) != 2 || ios.Covered[0].Name != "x" || ios.Covered[1].Name != "y" {
		t.Fatalf("IOS covered = %v, want [x, y] in index order", ios.Covered)
	}
	if len(ios.schema) != 2 || ios.schema[0].Name != "x" || ios.schema[1].Name != "y" {
		t.Fatalf("IOS schema = %+v, want [x, y] in index order", ios.schema)
	}
	if ios.Table != tbl {
		t.Fatalf("IOS Table = %v, want %v", ios.Table, tbl)
	}
	cond, ok := ios.Cond.(*BinaryOp)
	if !ok || cond.Op != parser.OpAnd {
		t.Fatalf("IOS Cond = %#v, want AND of prefix qual + is-not-null", ios.Cond)
	}
	// LEFT conjunct must be the remapped `x = 33` with x at covered position 0.
	eq, ok := cond.Left.(*BinaryOp)
	if !ok || eq.Op != parser.OpEq {
		t.Fatalf("Cond left = %#v, want `x = 33`", cond.Left)
	}
	if cr, ok := eq.Left.(*ColumnRef); !ok || cr.Name != "x" || cr.Index != 0 {
		t.Fatalf("Cond left operand = %#v, want ColumnRef(x) at index 0", eq.Left)
	}
	// RIGHT conjunct must be `y IS NOT NULL` with y at its covered position 1.
	rn, ok := cond.Right.(*IsNullExpr)
	if !ok || !rn.Negated {
		t.Fatalf("Cond right = %#v, want `y IS NOT NULL`", cond.Right)
	}
	if cr, ok := rn.Operand.(*ColumnRef); !ok || cr.Name != "y" || cr.Index != 1 {
		t.Fatalf("Cond right operand = %#v, want ColumnRef(y) at index 1", rn.Operand)
	}
}

// TestRewriteMinMaxAggCompositePrefixMaxIOS — S6 Slice 3c acceptance (b): the
// max twin — same shape but Backward = true (PG's `Index Only Scan Backward`).
func TestRewriteMinMaxAggCompositePrefixMaxIOS(t *testing.T) {
	cat, _ := minmaxCompositeCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT max(y) FROM t WHERE x = 33"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	ios := digCompositeIos(t, child)
	if !ios.Backward {
		t.Fatalf("IOS.Backward = false, want true (max → Backward)")
	}
	if len(ios.Covered) != 2 || ios.Covered[0].Name != "x" || ios.Covered[1].Name != "y" {
		t.Fatalf("IOS covered = %v, want [x, y] in index order", ios.Covered)
	}
	cond, ok := ios.Cond.(*BinaryOp)
	if !ok || cond.Op != parser.OpAnd {
		t.Fatalf("IOS Cond = %#v, want AND of prefix qual + is-not-null", ios.Cond)
	}
	if eq, ok := cond.Left.(*BinaryOp); !ok || eq.Op != parser.OpEq {
		t.Fatalf("Cond left = %#v, want `x = 33`", cond.Left)
	}
	if rn, ok := cond.Right.(*IsNullExpr); !ok || !rn.Negated {
		t.Fatalf("Cond right = %#v, want `y IS NOT NULL`", cond.Right)
	}
}

// TestRewriteMinMaxAggCompositePrefixNonPrefixQualFallback — S6 Slice 3c
// acceptance (c) first half: `min(y) WHERE z = 5` puts the equality on a
// NON-prefix column, so neither the leading-column nor the composite branch can
// fire and the rewrite stays on the SeqScan fallback (no IOS).
func TestRewriteMinMaxAggCompositePrefixNonPrefixQualFallback(t *testing.T) {
	cat, _ := minmaxCompositeCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(y) FROM t WHERE z = 5"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	if _, ok := child.(*IndexOnlyScan); ok {
		t.Fatalf("qual on non-prefix column z rewrote to IndexOnlyScan; must stay SeqScan fallback")
	}
	proj, ok := child.(*Project)
	if !ok {
		t.Fatalf("Limit child is %T, want *Project (SeqScan fallback)", child)
	}
	if _, ok := proj.Child.(*IndexOnlyScan); ok {
		t.Fatalf("Project child is *IndexOnlyScan; must stay SeqScan fallback")
	}
	sort, ok := proj.Child.(*Sort)
	if !ok {
		t.Fatalf("Project child is %T, want *Sort (SeqScan fallback)", proj.Child)
	}
	if _, ok := sort.Child.(*Filter); !ok {
		t.Fatalf("Sort child is %T, want *Filter", sort.Child)
	}
}

// TestRewriteMinMaxAggCompositePrefixNoWhereFallback — S6 Slice 3c acceptance
// (c) second half: `min(y)` with NO WHERE over only the composite [x, y] index
// stays on the SeqScan fallback — the composite branch needs a WHERE equality
// to bind the leading prefix, and no single-column index on y exists for the
// leading-column branch.
func TestRewriteMinMaxAggCompositePrefixNoWhereFallback(t *testing.T) {
	cat, _ := minmaxCompositeCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(y) FROM t"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	proj, ok := child.(*Project)
	if !ok {
		t.Fatalf("Limit child is %T, want *Project (SeqScan fallback)", child)
	}
	if _, ok := proj.Child.(*IndexOnlyScan); ok {
		t.Fatalf("no-WHERE min(y) rewrote to IndexOnlyScan over the composite index; must stay SeqScan fallback (no leading-column index on y)")
	}
	if _, ok := proj.Child.(*Sort); !ok {
		t.Fatalf("Project child is %T, want *Sort (SeqScan fallback)", proj.Child)
	}
}

// TestRewriteMinMaxAggCompositePrefixRangeReject — S6 Slice 3c acceptance (d):
// `min(y) WHERE x = 33 AND y < 500` mixes a bound prefix equality with a range
// conjunct on the agg column — a non-equality conjunct, so allEq is false and
// the composite branch declines to the SeqScan fallback (the Cond cannot be
// pushed without reading a column in a non-covered shape).
func TestRewriteMinMaxAggCompositePrefixRangeReject(t *testing.T) {
	cat, _ := minmaxCompositeCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(y) FROM t WHERE x = 33 AND y < 500"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	proj, ok := child.(*Project)
	if !ok {
		t.Fatalf("Limit child is %T, want *Project (SeqScan fallback)", child)
	}
	if _, ok := proj.Child.(*IndexOnlyScan); ok {
		t.Fatalf("range conjunct `y < 500` rewrote to IndexOnlyScan; must stay SeqScan fallback")
	}
	sort, ok := proj.Child.(*Sort)
	if !ok {
		t.Fatalf("Project child is %T, want *Sort (SeqScan fallback)", proj.Child)
	}
	f, ok := sort.Child.(*Filter)
	if !ok {
		t.Fatalf("Sort child is %T, want *Filter", sort.Child)
	}
	if _, ok := f.Child.(*SeqScan); !ok {
		t.Fatalf("Filter child is %T, want *SeqScan", f.Child)
	}
}

// TestRewriteMinMaxAggCompositePrefixDuplicateEqualityFallback — S6 Slice 3c
// safety: `min(y) WHERE x = 33 AND x = 44` puts TWO equality conjuncts on the
// same prefix column. Under last-write-wins map semantics one would be silently
// dropped (returning min(y) over x=44 where PG's full-qual evaluation returns
// NULL for the contradiction), so collectEqualityConjuncts must set allEq=false
// and the composite branch must decline to the SeqScan fallback, which evaluates
// the full `x = 33 AND x = 44` qual verbatim.
func TestRewriteMinMaxAggCompositePrefixDuplicateEqualityFallback(t *testing.T) {
	cat, _ := minmaxCompositeCatalog(t)
	plan, err := Plan(parseOne(t, "SELECT min(y) FROM t WHERE x = 33 AND x = 44"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	_, _, _, child := digMinMaxInner(t, plan)
	proj, ok := child.(*Project)
	if !ok {
		t.Fatalf("Limit child is %T, want *Project (SeqScan fallback)", child)
	}
	if _, ok := proj.Child.(*IndexOnlyScan); ok {
		t.Fatalf("duplicate same-column equality rewrote to IndexOnlyScan; must stay SeqScan fallback")
	}
	sort, ok := proj.Child.(*Sort)
	if !ok {
		t.Fatalf("Project child is %T, want *Sort (SeqScan fallback)", proj.Child)
	}
	f, ok := sort.Child.(*Filter)
	if !ok {
		t.Fatalf("Sort child is %T, want *Filter", sort.Child)
	}
	if _, ok := f.Child.(*SeqScan); !ok {
		t.Fatalf("Filter child is %T, want *SeqScan", f.Child)
	}
	// The fallback's Filter predicate must carry BOTH equality conjuncts
	// (andExpr(wherePred, isNotNull): left is the AND of x=33 AND x=44).
	pred, ok := f.Predicate.(*BinaryOp)
	if !ok || pred.Op != parser.OpAnd {
		t.Fatalf("Filter predicate = %#v, want AND of full qual + is-null", f.Predicate)
	}
	both, ok := pred.Left.(*BinaryOp)
	if !ok || both.Op != parser.OpAnd {
		t.Fatalf("Filter predicate left = %#v, want `x = 33 AND x = 44`", pred.Left)
	}
	if eq, ok := both.Left.(*BinaryOp); !ok || eq.Op != parser.OpEq {
		t.Fatalf("Filter predicate left-left = %#v, want `x = 33`", both.Left)
	}
	if eq, ok := both.Right.(*BinaryOp); !ok || eq.Op != parser.OpEq {
		t.Fatalf("Filter predicate left-right = %#v, want `x = 44`", both.Right)
	}
}
