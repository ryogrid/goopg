package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// R66 Slice 1 (alias → source Sort keys): group-section Sort keys render
// the grouping expression, and Star / DISTINCT aggregate outputs expand
// to the underlying call. Group Key arm untouched.

func TestExplainSortKeyGroupSectionRendersSource(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE r66g (brand text, typ text, sz int)",
		"CREATE TABLE r66h (id int, gbrand text)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	// Q16 shape: group keys from one table, DISTINCT aggregate over
	// another, ORDER BY all four.
	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT g.brand, g.typ, g.sz, count(DISTINCT h.id) AS c FROM r66g g, r66h h WHERE h.gbrand = g.brand GROUP BY g.brand, g.typ, g.sz ORDER BY c DESC, g.brand, g.typ, g.sz"), "\n")
	if !strings.Contains(joined, "Sort Key: (count(DISTINCT h.id)) DESC, g.brand, g.typ, g.sz") {
		t.Errorf("expected sourced+expanded Sort Key; got:\n%s", joined)
	}
	if strings.Contains(joined, "Sort Key: count DESC") || strings.Contains(joined, "Sort Key: (count(DISTINCT h.id)) DESC, brand,") {
		t.Errorf("sort key still renders an output alias; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExplainSortKeyCountStarExpands(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r66s (g int)"); err != nil {
		t.Fatal(err)
	}

	// Q13-key1 shape: Sort key naming a count(*) Aggs output.
	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT g, count(*) AS c FROM r66s GROUP BY g ORDER BY c DESC"), "\n")
	if !strings.Contains(joined, "Sort Key: (count(*)) DESC") {
		t.Errorf("expected expanded `Sort Key: (count(*)) DESC`; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExplainHavingCountStarExpands(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r66v (a int)"); err != nil {
		t.Fatal(err)
	}

	// The shared expansion helper also serves the Aggregate HAVING arm:
	// a Star HAVING qual renders PG's call text (P3-advertised move).
	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT a, count(*) FROM r66v GROUP BY a HAVING count(*) > 1"), "\n")
	if !strings.Contains(joined, "Filter: (count(*) > 1)") {
		t.Errorf("expected expanded HAVING `Filter: (count(*) > 1)`; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExplainSortKeyAliasGroupRendersUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r66u (n_name text, v int)"); err != nil {
		t.Fatal(err)
	}

	// Q7-shape miniature (subquery): GroupExprs is itself alias-named,
	// so Arm S re-renders identical text — the whole Slice-1 behaviour
	// on Q7/Q9/Q13-outer. Guards "zero alias lines moved".
	rows := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT s.supp AS supp_nation, sum(s.v) FROM (SELECT n_name AS supp, v FROM r66u) s GROUP BY s.supp ORDER BY supp_nation")
	var sortLine string
	for _, r := range rows {
		if strings.Contains(r, "Sort Key:") {
			sortLine = strings.TrimSpace(r)
		}
	}
	if sortLine != "Sort Key: supp" {
		t.Errorf("expected byte-identical `Sort Key: supp`; got:\n%s", strings.Join(rows, "\n"))
	}
	assertNoOpaqueExpr(t, strings.Join(rows, "\n"))
}

func TestExplainSortKeySubqueryGroupByUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE r66q1 (x int)",
		"CREATE TABLE r66q2 (x int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	// End-to-end sublink guard: GROUP BY a scalar subquery. The Sort key
	// names the SELECT alias over the sublink and must NOT expand through
	// it (subplan numbering belongs to the main plan walk) — while the
	// Group Key keeps R65's InitPlan value form with numbering intact.
	rows := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT (SELECT max(x) FROM r66q2) AS s, sum(x) FROM r66q1 GROUP BY (SELECT max(x) FROM r66q2) ORDER BY s")
	var sortLine string
	for _, r := range rows {
		if strings.Contains(r, "Sort Key:") {
			sortLine = strings.TrimSpace(r)
			break // the top Sort; deeper subplan Sorts are not this pin
		}
	}
	if sortLine != "Sort Key: max" {
		t.Errorf("expected byte-identical `Sort Key: max`; got:\n%s", strings.Join(rows, "\n"))
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "Group Key: (InitPlan 1).col1") {
		t.Errorf("expected Group Key InitPlan numbering intact; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExprHasSubplanOrOuterRef(t *testing.T) {
	// The Arm-S fail-closed guard: any sublink or outer reference in a
	// grouping expression declines key-line expansion (numbering belongs
	// to the main plan walk).
	blocked := []optimizer.Expr{
		&optimizer.SubqueryExpr{},
		&optimizer.ExistsExpr{},
		&optimizer.InExpr{},
		&optimizer.ArraySubqueryExpr{},
		&optimizer.OuterColumnRef{},
		&optimizer.ExecParamRef{},
	}
	for i, e := range blocked {
		if !exprHasSubplanOrOuterRef(e) {
			t.Errorf("blocked[%d] %T: want true", i, e)
		}
	}
	plain := []optimizer.Expr{
		&optimizer.ColumnRef{},
		&optimizer.FuncCall{Name: "count", Star: true},
		&optimizer.FuncCall{Name: "count", Args: []optimizer.Expr{&optimizer.ColumnRef{}}, Distinct: true},
	}
	for i, e := range plain {
		if exprHasSubplanOrOuterRef(e) {
			t.Errorf("plain[%d] %T: want false", i, e)
		}
	}
}
