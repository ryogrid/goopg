package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0148-0001f: PG's ExplainNode prints VERBOSE's show_plan_tlist right after
// the node line (and `Disabled:`), before every qual and key, and qualifies
// its columns by `useprefix = es->rtable_size > 1` only — VERBOSE alone does
// not, unlike the quals and keys (explain.c). A single-relation scan prints
// `Output: a, b` above a qualified `Filter: (oa.a > 1)`; a join's scans print
// `Output: oa.a, oa.b`.
func TestExplainVerboseOutputPlacementAndPrefix(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE oa (a int, b int)")
	runSQL(t, ctx, "CREATE TABLE ob (a int, c int)")
	ps := optimizer.DefaultPlannerSettings()
	render := func(q string) []string { return renderRows(runSQLWith(t, ctx, q, ps)) }

	single := render("EXPLAIN (VERBOSE, COSTS OFF) SELECT a, b FROM oa WHERE a > 1")
	if want := []string{"Seq Scan on public.oa", "  Output: a, b", "  Filter: (oa.a > 1)"}; strings.Join(single, "\n") != strings.Join(want, "\n") {
		t.Errorf("single relation:\n got:\n%s\nwant (PG 18.3):\n%s", strings.Join(single, "\n"), strings.Join(want, "\n"))
	}

	join := strings.Join(render("EXPLAIN (VERBOSE, COSTS OFF) SELECT oa.a, ob.c FROM oa JOIN ob ON oa.a = ob.a"), "\n")
	for _, want := range []string{"Output: oa.a, oa.b", "Output: ob.a, ob.c"} {
		if !strings.Contains(join, want) {
			t.Errorf("join: want %q (PG 18.3); got:\n%s", want, join)
		}
	}
	lines := strings.Split(join, "\n")
	for i, l := range lines {
		if strings.Contains(l, "Hash Join") || strings.Contains(l, "Merge Join") || strings.Contains(l, "Nested Loop") {
			if i+1 >= len(lines) || !strings.Contains(lines[i+1], "Output:") {
				t.Errorf("join: Output must directly follow the node line:\n%s", join)
			}
		}
	}
}

// M0148-0001f: an Aggregate's VERBOSE Output: is its targetlist deparsed —
// grouping columns and aggregate calls (`y, sum((x + y)), count(*), avg(x)`),
// not the output schema's names (`y, sum, count, avg`). Verified against PG
// 18.3's show_plan_tlist for this statement.
func TestExplainVerboseAggregateOutputDeparsed(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ga (x int, y int)")
	got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (VERBOSE, COSTS OFF) SELECT y, sum(x + y), count(*), avg(x) FROM ga GROUP BY y", optimizer.DefaultPlannerSettings())), "\n")
	want := strings.Join([]string{
		"HashAggregate",
		"  Output: y, sum((x + y)), count(*), avg(x)",
		"  Group Key: ga.y",
		"  ->  Seq Scan on public.ga",
		"        Output: x, y",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
}

// M0148-0001f: a generate_series FROM item is a range-table entry PG names by
// its alias (set_rtable_names covers function RTEs), so in a multi-relation
// statement its column prints `s1.s1` in Output: and quals, as base-relation
// columns do. goopg's GenerateSeries carried no RTID and stayed bare.
func TestExplainGenerateSeriesColumnsQualified(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE oa (a int, b int)")
	got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (VERBOSE, COSTS OFF) SELECT oa.b, s1 FROM oa, generate_series(1, 3) s1 WHERE oa.a = s1", optimizer.DefaultPlannerSettings())), "\n")
	for _, want := range []string{"Function Scan on pg_catalog.generate_series s1", "Output: s1.s1", "s1.s1)"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q (PG 18.3); got:\n%s", want, got)
		}
	}
	single := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (VERBOSE, COSTS OFF) SELECT s1 FROM generate_series(1, 3) s1", optimizer.DefaultPlannerSettings())), "\n")
	if !strings.Contains(single, "Output: s1\n") && !strings.HasSuffix(single, "Output: s1") {
		t.Errorf("a lone function RTE keeps bare Output (rtable_size 1); got:\n%s", single)
	}
}

// M0148-0001f: the Agg's targetlist is the SELECT list (create_plan folds
// the projection into the projection-capable Agg) plus, as resjunk entries,
// the grouping columns the list does not name — so PG prints
// `sum((x + y)), y` at the Agg and `(sum((x + y))), y` on the Sort that
// references it. goopg holds the projection in an EXPLAIN-invisible Project
// above the chain; EXPLAIN renders the chain from it.
func TestExplainVerboseOutputFollowsSelectListOverAggregate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ga (x int, y int)")
	got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (VERBOSE, COSTS OFF) SELECT sum(x + y) FROM ga GROUP BY y ORDER BY 1", optimizer.DefaultPlannerSettings())), "\n")
	want := strings.Join([]string{
		"Sort",
		"  Output: (sum((x + y))), y",
		"  Sort Key: (sum((ga.x + ga.y)))",
		"  ->  HashAggregate",
		"        Output: sum((x + y)), y",
		"        Group Key: ga.y",
		"        ->  Seq Scan on public.ga",
		"              Output: x, y",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
}
