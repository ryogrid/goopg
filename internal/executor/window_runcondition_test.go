package executor

import (
	"strings"
	"testing"
)

// TestWindowRunCondition pins M0146-0005dn against PG 18.3: a monotonic
// window function bounded by a constant over a subquery becomes the
// WindowAgg's Run Condition (the Filter keeps only what PG keeps), and the
// rows equal those of the same query whose bound the planner cannot see
// through (`rk + 0`), with and without PARTITION BY — the executor's
// skip-to-next-partition and stop-the-scan arms.
func TestWindowRunCondition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE rc (g int, v int)")
	runSQL(t, ctx, "INSERT INTO rc SELECT x % 4, (x * 37) % 101 FROM generate_series(1, 400) x")
	runSQL(t, ctx, "INSERT INTO rc VALUES (1, NULL), (2, NULL)")

	for _, c := range []struct {
		name, sql, opaque, runCond, filter string
	}{
		{"no partition, <",
			"SELECT count(*), sum(v), sum(rk) FROM (SELECT v, rank() OVER (ORDER BY v) rk FROM rc) s WHERE rk < 5",
			"SELECT count(*), sum(v), sum(rk) FROM (SELECT v, rank() OVER (ORDER BY v) rk FROM rc) s WHERE rk + 0 < 5",
			"Run Condition: (rank() OVER w1 < 5)", ""},
		{"partitioned, <=",
			"SELECT count(*), sum(v), sum(rn) FROM (SELECT g, v, row_number() OVER (PARTITION BY g ORDER BY v DESC) rn FROM rc) s WHERE rn <= 3",
			"SELECT count(*), sum(v), sum(rn) FROM (SELECT g, v, row_number() OVER (PARTITION BY g ORDER BY v DESC) rn FROM rc) s WHERE rn + 0 <= 3",
			"Run Condition: (row_number() OVER w1 <= 3)", ""},
		{"constant on the left, extra qual kept",
			"SELECT count(*), sum(v) FROM (SELECT v, dense_rank() OVER (PARTITION BY g ORDER BY v) dr FROM rc) s WHERE 4 > dr AND v > 2",
			"SELECT count(*), sum(v) FROM (SELECT v, dense_rank() OVER (PARTITION BY g ORDER BY v) dr FROM rc) s WHERE 4 > dr + 0 AND v > 2",
			"Run Condition: (4 > dense_rank() OVER w1)", "Filter: (s.v > 2)"},
		{"= keeps its qual, runs as <=",
			"SELECT count(*), sum(v) FROM (SELECT v, rank() OVER (ORDER BY v) rk FROM rc) s WHERE rk = 3",
			"SELECT count(*), sum(v) FROM (SELECT v, rank() OVER (ORDER BY v) rk FROM rc) s WHERE rk + 0 = 3",
			"Run Condition: (rank() OVER w1 <= 3)", "Filter: (s.rk = 3)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := explainText(t, ctx, c.sql)
			if !strings.Contains(plan, c.runCond) {
				t.Fatalf("missing %q:\n%s", c.runCond, plan)
			}
			if c.filter == "" && strings.Contains(plan, "Filter:") {
				t.Fatalf("the run condition's qual is still a Filter:\n%s", plan)
			}
			if c.filter != "" && !strings.Contains(plan, c.filter) {
				t.Fatalf("missing %q:\n%s", c.filter, plan)
			}
			got := renderRows(runSQL(t, ctx, c.sql))
			want := renderRows(runSQL(t, ctx, c.opaque))
			if strings.Join(got, ";") != strings.Join(want, ";") {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
	// A non-monotonic function gets no run condition.
	if plan := explainText(t, ctx, "SELECT * FROM (SELECT v, sum(v) OVER (ORDER BY v) sm FROM rc) s WHERE sm < 50"); strings.Contains(plan, "Run Condition") {
		t.Fatalf("sum() got a run condition:\n%s", plan)
	}
}
