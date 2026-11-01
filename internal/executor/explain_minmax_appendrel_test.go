package executor

import (
	"strings"
	"testing"
)

// M0148-0001c slice (c2): min/max over an inheritance parent plans PG's
// appendrel shape — one InitPlan per aggregate, each a Limit over a Merge
// Append of the members' ordered Index Only Scans
// (postgres/src/test/regress/expected/aggregates.out:1225-1246). The block
// pins every rendering rule the slice ports: the parent RTE claims the bare
// name and the members `_1`…`_4`, numbering continuing into InitPlan 2
// (`minmaxtest_5`, members `_6`…`_9`); the Merge Append key deparses as the
// parent's Var; a DESC index reads Backward for min and forward for max; the
// partial index whose predicate IS the `f1 IS NOT NULL` qual prints no Index
// Cond.
func TestExplainMinMaxAppendRelMergeAppend(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table minmaxtest(f1 int)",
		"create table minmaxtest1() inherits (minmaxtest)",
		"create table minmaxtest2() inherits (minmaxtest)",
		"create table minmaxtest3() inherits (minmaxtest)",
		"create index minmaxtesti on minmaxtest(f1)",
		"create index minmaxtest1i on minmaxtest1(f1)",
		"create index minmaxtest2i on minmaxtest2(f1 desc)",
		"create index minmaxtest3i on minmaxtest3(f1) where f1 is not null",
		"insert into minmaxtest values(11), (12)",
		"insert into minmaxtest1 values(13), (14)",
		"insert into minmaxtest2 values(15), (16)",
		"insert into minmaxtest3 values(17), (18)",
	} {
		runSQL(t, ctx, q)
	}
	want := strings.Join([]string{
		"Result",
		"  InitPlan 1",
		"    ->  Limit",
		"          ->  Merge Append",
		"                Sort Key: minmaxtest.f1",
		"                ->  Index Only Scan using minmaxtesti on minmaxtest minmaxtest_1",
		"                      Index Cond: (f1 IS NOT NULL)",
		"                ->  Index Only Scan using minmaxtest1i on minmaxtest1 minmaxtest_2",
		"                      Index Cond: (f1 IS NOT NULL)",
		"                ->  Index Only Scan Backward using minmaxtest2i on minmaxtest2 minmaxtest_3",
		"                      Index Cond: (f1 IS NOT NULL)",
		"                ->  Index Only Scan using minmaxtest3i on minmaxtest3 minmaxtest_4",
		"  InitPlan 2",
		"    ->  Limit",
		"          ->  Merge Append",
		"                Sort Key: minmaxtest_5.f1 DESC",
		"                ->  Index Only Scan Backward using minmaxtesti on minmaxtest minmaxtest_6",
		"                      Index Cond: (f1 IS NOT NULL)",
		"                ->  Index Only Scan Backward using minmaxtest1i on minmaxtest1 minmaxtest_7",
		"                      Index Cond: (f1 IS NOT NULL)",
		"                ->  Index Only Scan using minmaxtest2i on minmaxtest2 minmaxtest_8",
		"                      Index Cond: (f1 IS NOT NULL)",
		"                ->  Index Only Scan Backward using minmaxtest3i on minmaxtest3 minmaxtest_9",
	}, "\n")
	got := strings.Join(renderRows(runSQL(t, ctx,
		"explain (costs off) select min(f1), max(f1) from minmaxtest")), "\n")
	if got != want {
		t.Errorf("appendrel min/max plan:\n got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
	for _, tc := range []struct{ q, want string }{
		{"select min(f1), max(f1) from minmaxtest", "11|18"},
		{"select distinct min(f1), max(f1) from minmaxtest", "11|18"},
		// FROM ONLY: rte->inh = false, the parent's own rows only.
		{"select min(f1), max(f1) from only minmaxtest", "11|12"},
		// A DESC member alone decides the max once the others are empty.
		{"select max(f1) from minmaxtest where false", "NULL"},
	} {
		if g := strings.Join(renderRows(runSQL(t, ctx, tc.q)), ";"); g != tc.want {
			t.Errorf("%s: got %s, want %s", tc.q, g, tc.want)
		}
	}
	runSQL(t, ctx, "delete from minmaxtest1")
	runSQL(t, ctx, "insert into minmaxtest2 values (99), (NULL)")
	runSQL(t, ctx, "insert into minmaxtest3 values (NULL), (1)")
	if g := strings.Join(renderRows(runSQL(t, ctx, "select min(f1), max(f1) from minmaxtest")), ";"); g != "1|99" {
		t.Errorf("after member changes: got %s, want 1|99", g)
	}
	// FROM ONLY keeps the plain single-relation shape (no Merge Append), and
	// a FROM alias names the appendrel parent and its members.
	only := strings.Join(renderRows(runSQL(t, ctx,
		"explain (costs off) select max(f1) from only minmaxtest")), "\n")
	if strings.Contains(only, "Merge Append") || !strings.Contains(only, "Index Only Scan Backward using minmaxtesti on minmaxtest") {
		t.Errorf("FROM ONLY plan:\n%s", only)
	}
	aliased := strings.Join(renderRows(runSQL(t, ctx,
		"explain (costs off) select min(f1) from minmaxtest m")), "\n")
	for _, w := range []string{"Sort Key: m.f1", "on minmaxtest m_1", "on minmaxtest3 m_4"} {
		if !strings.Contains(aliased, w) {
			t.Errorf("aliased plan lacks %q:\n%s", w, aliased)
		}
	}
}

// M0148-0001c slice (c2), wrong-result fix: build_minmax_path's index path
// reads a DESC key column in the opposite direction (build_index_pathkeys),
// so `max(x)` over `(x desc)` is a FORWARD Index Only Scan. goopg set
// Backward = isMax regardless of the column's ordering, so max() returned
// the minimum and min() the maximum.
func TestMinMaxOverDescIndexDirection(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table mmdesc(x int, y int)",
		"create index mmdesc_x on mmdesc(x desc)",
		"create index mmdesc_yx on mmdesc(y, x desc)",
		"insert into mmdesc select g, g % 3 from generate_series(1, 100) g",
		"insert into mmdesc values (NULL, 1)",
	} {
		runSQL(t, ctx, q)
	}
	for _, tc := range []struct{ q, want string }{
		{"select max(x) from mmdesc", "100"},
		{"select min(x) from mmdesc", "1"},
		{"select min(x), max(x) from mmdesc", "1|100"},
		{"select max(x) from mmdesc where y = 1", "100"},
		{"select min(x) from mmdesc where y = 2", "2"},
	} {
		if g := strings.Join(renderRows(runSQL(t, ctx, tc.q)), ";"); g != tc.want {
			t.Errorf("%s: got %s, want %s", tc.q, g, tc.want)
		}
	}
	plan := strings.Join(renderRows(runSQL(t, ctx, "explain (costs off) select max(x) from mmdesc")), "\n")
	if !strings.Contains(plan, "->  Index Only Scan using mmdesc_x on mmdesc") {
		t.Errorf("max over a DESC index must scan forward, as PG prints:\n%s", plan)
	}
	plan = strings.Join(renderRows(runSQL(t, ctx, "explain (costs off) select min(x) from mmdesc")), "\n")
	if !strings.Contains(plan, "->  Index Only Scan Backward using mmdesc_x on mmdesc") {
		t.Errorf("min over a DESC index must scan backward, as PG prints:\n%s", plan)
	}
}
