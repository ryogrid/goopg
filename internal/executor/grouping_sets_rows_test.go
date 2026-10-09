package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestGroupingSetsRowEstimateSumsSets (M0146-0009o): a GROUPING SETS /
// ROLLUP aggregate's row estimate is get_number_of_groups' sum of
// estimate_num_groups over every set (planner.c), not one estimate of the
// sets' union, and its hashed path is priced rollup by rollup as
// create_groupingsets_path does. PG 18.3 on the same data (ANALYZEd,
// serial):
//
//	GROUP BY ROLLUP (a, b, c)              MixedAggregate (cost=0.00..849.41 rows=4041)
//	GROUP BY GROUPING SETS ((a), (b), ())  MixedAggregate (cost=0.00..562.41 rows=341)
//	GROUP BY GROUPING SETS ((a, b), (a))   HashAggregate  (cost=459.00..579.40 rows=2040)
//
// Before the fix the path search sized the grouped rel from the union
// (rows=2000 for the rollup) and priced the hashed path as one hash table.
func TestGroupingSetsRowEstimateSumsSets(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE gs_t (a int, b int, c int, v int)",
		"INSERT INTO gs_t SELECT g % 40, g % 300, g % 7, g FROM generate_series(1, 20000) g",
		"ANALYZE gs_t",
	} {
		runSQL(t, ctx, q)
	}
	for _, c := range []struct {
		sql, head, rows string
	}{
		{"SELECT a, b, c, sum(v) FROM gs_t GROUP BY ROLLUP (a, b, c)", "MixedAggregate  (cost=0.00..", "rows=4041 "},
		{"SELECT a, b, sum(v) FROM gs_t GROUP BY GROUPING SETS ((a), (b), ())", "MixedAggregate  (cost=0.00..", "rows=341 "},
		{"SELECT a, b, sum(v) FROM gs_t GROUP BY GROUPING SETS ((a, b), (a))", "HashAggregate  (cost=459.00..", "rows=2040 "},
	} {
		plan := explainText(t, ctx, c.sql)
		first := strings.SplitN(plan, "\n", 2)[0]
		if !strings.Contains(first, c.head) || !strings.Contains(first, c.rows) {
			t.Fatalf("%s: want PG's %s… %s\nplan:\n%s", c.sql, c.head, c.rows, plan)
		}
	}
	// A sorted rollup carries group pathkeys marked as grouping-nullable:
	// they keep the path alive in add_path but never satisfy an ORDER BY on
	// the plain columns (PG's RTE_GROUP Vars), so a Sort stays above it even
	// when the ORDER BY is the rollup order (PG 18.3 with enable_hashagg =
	// off: Limit -> Sort -> GroupAggregate -> Sort).
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashAgg = false
	const ordered = "SELECT a, b, sum(v) FROM gs_t WHERE c = 3 AND a < 3 GROUP BY ROLLUP (a, b) ORDER BY a, b LIMIT 8"
	lines := renderRows(runSQLWith(t, ctx, "EXPLAIN "+ordered, ps))
	agg := -1
	for i, l := range lines {
		if strings.Contains(l, "GroupAggregate") {
			agg = i
			break
		}
	}
	if agg < 1 || !strings.Contains(lines[agg-1], "Sort") {
		t.Fatalf("want a Sort above the sorted rollup\nplan:\n%s", strings.Join(lines, "\n"))
	}
	if got, want := strings.Join(renderRows(runSQLWith(t, ctx, ordered, ps)), ";"), strings.Join(renderRows(runSQL(t, ctx, ordered)), ";"); got != want {
		t.Fatalf("sorted rollup rows %s, want %s", got, want)
	}
}
