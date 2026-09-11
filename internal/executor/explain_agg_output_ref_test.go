package executor

import (
	"strings"
	"testing"
)

// R65 (Q11 EXPLAIN-rendering round): Sort keys and HAVING quals that name
// a child-aggregate output the child computed render PG's OUTER_VAR
// expansion (the underlying aggregate call), and a non-correlated scalar
// subquery read for its value renders `(InitPlan N).col1`.

func TestExplainSortKeyExpandsAggOutput(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r65t (g int, x int)"); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT g, sum(x) AS s FROM r65t GROUP BY g ORDER BY s DESC"), "\n")
	if !strings.Contains(joined, "Sort Key: (sum(x)) DESC") {
		t.Errorf("expected expanded `Sort Key: (sum(x)) DESC`; got:\n%s", joined)
	}
	if strings.Contains(joined, "Sort Key: s DESC") || strings.Contains(joined, "Sort Key: sum DESC") {
		t.Errorf("sort key still renders the output alias; got:\n%s", joined)
	}
	// The S18 sibling is untouched: group keys render from GroupExprs.
	if !strings.Contains(joined, "Group Key: g") {
		t.Errorf("expected `Group Key: g` unchanged; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExplainSortKeyPassThroughUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r65p (g int, x int)"); err != nil {
		t.Fatal(err)
	}

	// A sort key naming a pass-through (non-computed) output keeps
	// today's rendering — only Aggs-section hits expand.
	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT g, sum(x) AS s FROM r65p GROUP BY g ORDER BY g"), "\n")
	if !strings.Contains(joined, "Sort Key: g") {
		t.Errorf("expected bare `Sort Key: g`; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExplainHavingFilterExpandsAggOutput(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r65h (partkey int, supplycost numeric, availqty int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE r65n (x int)"); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT partkey, sum(supplycost*availqty) AS s FROM r65h GROUP BY partkey HAVING sum(supplycost*availqty) > (SELECT sum(x) FROM r65n) ORDER BY s DESC"), "\n")
	// Arm A (both halves) + Arm B compose in one Filter line. The
	// binary-op arg carries its own parens from the BinaryOp arm, so the
	// expansion is byte-identical to PG's `sum((…))` shape.
	if !strings.Contains(joined, "Filter: (sum((r65h.supplycost * r65h.availqty)) > (InitPlan 1).col1)") {
		t.Errorf("expected expanded Filter with .col1; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Sort Key: (sum((r65h.supplycost * r65h.availqty))) DESC") {
		t.Errorf("expected expanded Sort Key; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExplainInitPlanValueRendersCol1(t *testing.T) {
	ctx, cleanup := explainSubPlanFixture(t)
	defer cleanup()

	plan, _ := joinedPlan(t, ctx,
		"EXPLAIN SELECT * FROM t1 WHERE t1.b > (SELECT max(t2.b) FROM t2)")
	if !strings.Contains(plan, "(InitPlan 1).col1") {
		t.Errorf("want value-position (InitPlan 1).col1 in plan:\n%s", plan)
	}
	assertNoOpaqueExpr(t, plan)
}
