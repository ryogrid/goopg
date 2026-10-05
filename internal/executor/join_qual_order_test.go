package executor

import (
	"strings"
	"testing"
)

// TestJoinFilterOrderedByQualCost pins M0146-0013: every create_*join_plan
// runs order_qual_clauses over its join quals, a stable sort by
// cost_qual_eval's per-tuple cost, so the one-operator `a.z < b.z` is
// evaluated (and printed) before the two-operator OR written ahead of it.
// goopg kept the restriction list's written order. Plan and value are PG
// 18.3's.
func TestJoinFilterOrderedByQualCost(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE oq_a (k int, x int, z int)",
		"CREATE TABLE oq_b (k int, y int, z int)",
		"INSERT INTO oq_a SELECT g % 50, g % 7, g % 13 FROM generate_series(1, 2000) g",
		"INSERT INTO oq_b SELECT g % 50, g % 5, g % 11 FROM generate_series(1, 1000) g",
		"ANALYZE oq_a",
		"ANALYZE oq_b",
	} {
		runSQL(t, ctx, q)
	}
	const q = `SELECT count(*) FROM oq_a a JOIN oq_b b ON a.k = b.k AND (a.x = 1 OR b.y = 2) AND a.z < b.z`
	plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+q))
	if countLinesContaining(plan, "Join Filter: ((a.z < b.z) AND ((a.x = 1) OR (b.y = 2)))") != 1 {
		t.Errorf("want PG's cost-ordered Join Filter:\n%s", strings.Join(plan, "\n"))
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, q)), ";"); got != "4840" {
		t.Errorf("rows %q, want PG's 4840", got)
	}
}
