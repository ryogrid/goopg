package executor

import (
	"strings"
	"testing"
)

// TestCorrelatedScalarSublinkStaysSubPlan pins M0145-0008y. PG 18.3 never
// decorrelates a scalar sublink (pull_up_sublinks converts only ANY/EXISTS),
// so a body with no index probe still runs as a per-row SubPlan. goopg's
// post-planning unnest rewrote such a body into a grouped hash join. Plan and
// value are PG's.
func TestCorrelatedScalarSublinkStaysSubPlan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE su_t (id int, cat int, v int)",
		"CREATE TABLE su_u (cat int, v int)",
		"INSERT INTO su_t SELECT g, g % 7, g % 23 FROM generate_series(1, 300) g",
		"INSERT INTO su_u SELECT g % 7, g % 31 FROM generate_series(1, 2000) g",
		"ANALYZE su_t",
		"ANALYZE su_u",
	} {
		runSQL(t, ctx, q)
	}
	const q = `SELECT count(*) FROM su_t t WHERE t.v > (SELECT avg(u.v) FROM su_u u WHERE u.cat = t.cat)`
	plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+q))
	joined := strings.Join(plan, "\n")
	for _, w := range []string{"Filter: ((v)::numeric > (SubPlan 1))", "Seq Scan on su_u u", "Filter: (cat = t.cat)"} {
		if countLinesContaining(plan, w) != 1 {
			t.Errorf("want %q (PG 18.3):\n%s", w, joined)
		}
	}
	if countLinesContaining(plan, "Hash Join") != 0 || countLinesContaining(plan, "HashAggregate") != 0 {
		t.Errorf("the scalar sublink was decorrelated into a join:\n%s", joined)
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, q)), ";"); got != "104" {
		t.Errorf("rows %q, want PG's 104", got)
	}
}
