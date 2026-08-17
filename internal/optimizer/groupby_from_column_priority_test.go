package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// usingJoinGroupByCatalog builds t1(f1,f2)/t2(f1,f2) — the reduced shape of
// PG's `aggregates.sql` "GROUP BY matching of join columns that are
// type-coerced due to USING" block (postgres/src/test/regress/sql/
// aggregates.sql, expected/aggregates.out:1544-1547).
func usingJoinGroupByCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t1"}, []catalog.Column{
		{Name: "f1", Type: catalog.Type{Name: "int4"}},
		{Name: "f2", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "t2"}, []catalog.Column{
		{Name: "f1", Type: catalog.Type{Name: "int4"}},
		{Name: "f2", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestGroupByUsingMergedColumnRejectsQualifiedSelectRef is the M0134-0001
// S13 guard test (docs/design/0134-0001-p5-groupby-name-resolution.md).
//
// `GROUP BY f1` over a `t1 LEFT JOIN t2 USING (f1)` join names the USING
// merged pseudo-column (PG: parse_clause.c:2056-2076
// findTargetlistEntrySQL92, EXPR_KIND_GROUP_BY gate — a FROM-clause input
// column always outranks a target-list alias/output-name match for GROUP
// BY). The SELECT list's qualified `t1.f1` is a DIFFERENT Var and is not
// grouped, so PG rejects it with 42803
// (postgres/src/backend/parser/parse_agg.c check_ungrouped_columns).
//
// Before this fix, goopg's `resolveOrderBySubstitution` ran unconditionally
// on the GROUP BY item, rewriting bare `f1` into the target list's `t1.f1`
// before the USING-merge tracking loop (groupByMergedByName,
// planner.go:6554-6568 pre-fix line numbers) ever saw it — starving the
// M0097-0155 guard in resolveExprAfterAggregate and silently accepting the
// query (verified FAIL-pre: returned `(0 rows)` instead of erroring).
func TestGroupByUsingMergedColumnRejectsQualifiedSelectRef(t *testing.T) {
	cat := usingJoinGroupByCatalog(t)
	stmt := parseOne(t, `select t1.f1 from t1 left join t2 using (f1) group by f1`)
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected 42803, query planned successfully")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("error type = %T, want *PlanError", err)
	}
	if pe.Code != "42803" {
		t.Errorf("error code = %q, want 42803", pe.Code)
	}
	const want = `column "t1.f1" must appear in the GROUP BY clause or be used in an aggregate function`
	if pe.Message != want {
		t.Errorf("error message = %q, want %q", pe.Message, want)
	}
}

// TestGroupByUsingMergedColumnStillAcceptsUnqualifiedSelectRef pins the
// first of the three statements PG accepts immediately before the failing
// one in the same aggregates.sql block: an unqualified `f1` in the SELECT
// list DOES resolve to the USING-merged GROUP BY key.
func TestGroupByUsingMergedColumnStillAcceptsUnqualifiedSelectRef(t *testing.T) {
	cat := usingJoinGroupByCatalog(t)
	stmt := parseOne(t, `select f1 from t1 left join t2 using (f1) group by f1`)
	if _, err := Plan(stmt, cat); err != nil {
		t.Fatalf("unqualified f1 should satisfy GROUP BY f1 over the USING merge: %v", err)
	}
}

// TestGroupByQualifiedGroupItemStillAcceptsQualifiedSelectRef pins the
// second preceding statement: `GROUP BY t1.f1` (already qualified — never
// took the alias-substitution path) still satisfies `SELECT t1.f1`.
func TestGroupByQualifiedGroupItemStillAcceptsQualifiedSelectRef(t *testing.T) {
	cat := usingJoinGroupByCatalog(t)
	stmt := parseOne(t, `select t1.f1 from t1 left join t2 using (f1) group by t1.f1`)
	if _, err := Plan(stmt, cat); err != nil {
		t.Fatalf("GROUP BY t1.f1 should satisfy SELECT t1.f1: %v", err)
	}
}

// aliasShadowsFromColumnCatalog builds t(a,b) — both columns present — for
// the alias-shadowing case in the design doc: a target-list alias that
// coincides with a real FROM-clause column name.
func aliasShadowsFromColumnCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestGroupByAliasShadowingPrefersFromColumn is acceptance criterion 2:
// `SELECT a AS b FROM t GROUP BY b` (t has columns a and b) must group by
// the FROM column `t.b`, not the aliased `t.a` — PG's colNameToVar-first
// rule (parse_clause.c:2056-2076). Before the fix, goopg's ungated
// resolveOrderBySubstitution matched the alias and grouped by `t.a`,
// silently accepting the query (SELECT target and GROUP BY key were the
// same expression, t.a).
//
// Once GROUP BY resolves to the FROM column t.b instead, the SELECT target
// `a AS b` is a Var over the DIFFERENT column t.a — ungrouped, not an
// aggregate — so PG (and goopg after this fix) reject the whole query with
// 42803, naming the ungrouped column t.a. That rejection is itself the
// proof the GROUP BY key is t.b, not t.a: under the old (buggy)
// alias-wins behaviour this exact query planned successfully.
func TestGroupByAliasShadowingPrefersFromColumn(t *testing.T) {
	cat := aliasShadowsFromColumnCatalog(t)
	stmt := parseOne(t, `SELECT a AS b FROM t GROUP BY b`)
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("expected 42803 (t.a is ungrouped once GROUP BY b binds to the FROM column t.b), query planned successfully")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("error type = %T, want *PlanError", err)
	}
	if pe.Code != "42803" {
		t.Errorf("error code = %q, want 42803", pe.Code)
	}
	const want = `column "t.a" must appear in the GROUP BY clause or be used in an aggregate function`
	if pe.Message != want {
		t.Errorf("error message = %q, want %q", pe.Message, want)
	}
}

// TestGroupByAliasShadowingFromColumnAcceptsMatchingSelectRef is the
// success-side complement: with the same alias-shadowing GROUP BY (`b`
// resolves to the FROM column t.b, not the `a AS b` alias), selecting the
// FROM column `b` itself groups cleanly — confirming the group key really
// is t.b, not merely that any reference to `a` errors.
func TestGroupByAliasShadowingFromColumnAcceptsMatchingSelectRef(t *testing.T) {
	cat := aliasShadowsFromColumnCatalog(t)
	stmt := parseOne(t, `SELECT b FROM t GROUP BY b`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	agg := findAggregate(t, plan)
	if len(agg.GroupExprs) != 1 {
		t.Fatalf("group exprs = %d, want 1", len(agg.GroupExprs))
	}
	cr, ok := agg.GroupExprs[0].(*ColumnRef)
	if !ok {
		t.Fatalf("group expr = %T, want *ColumnRef", agg.GroupExprs[0])
	}
	if cr.Name != "b" {
		t.Errorf("group expr binds to column %q, want %q (t.b)", cr.Name, "b")
	}
}

// TestGroupByPositionalUnaffected is acceptance criterion 3 (positional
// half): `GROUP BY 1` is an *parser.IntegerConst, not a ColumnRef, so the
// new FROM-column gate never sees it — resolveOrderBySubstitution still
// performs the positional rewrite unconditionally.
func TestGroupByPositionalUnaffected(t *testing.T) {
	cat := aliasShadowsFromColumnCatalog(t)
	stmt := parseOne(t, `SELECT a AS b FROM t GROUP BY 1`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	agg := findAggregate(t, plan)
	if len(agg.GroupExprs) != 1 {
		t.Fatalf("group exprs = %d, want 1", len(agg.GroupExprs))
	}
	cr, ok := agg.GroupExprs[0].(*ColumnRef)
	if !ok {
		t.Fatalf("group expr = %T, want *ColumnRef", agg.GroupExprs[0])
	}
	if cr.Name != "a" {
		t.Errorf("group expr binds to column %q, want %q (positional 1 → SELECT a AS b's underlying column a)", cr.Name, "a")
	}
}

// TestGroupByGenuineAliasStillResolvesViaSubstitution is acceptance
// criterion 3 (alias half): when the GROUP BY name is NOT a FROM-clause
// column at all, the target-list alias substitution still applies — TPC-H
// Q7 relies on exactly this shape (`GROUP BY x` for a computed `AS x`).
func TestGroupByGenuineAliasStillResolvesViaSubstitution(t *testing.T) {
	cat := aliasShadowsFromColumnCatalog(t)
	stmt := parseOne(t, `SELECT a+b AS x FROM t GROUP BY x`)
	if _, err := Plan(stmt, cat); err != nil {
		t.Fatalf("GROUP BY x should resolve via target-list alias substitution (no FROM column named x): %v", err)
	}
}

// findAggregate expects the plan root to be a bare `*Project` wrapping the
// `*Aggregate` directly — true for these no-ORDER-BY, no-LIMIT queries,
// matching projectTargets' assumption in groupby_alias_collapse_test.go.
func findAggregate(t *testing.T, n Node) *Aggregate {
	t.Helper()
	p, ok := n.(*Project)
	if !ok {
		t.Fatalf("plan root is %T, want *Project", n)
	}
	a, ok := p.Child.(*Aggregate)
	if !ok {
		t.Fatalf("Project.Child is %T, want *Aggregate", p.Child)
	}
	return a
}
