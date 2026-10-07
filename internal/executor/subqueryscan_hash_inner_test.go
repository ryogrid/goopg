package executor

import (
	"strings"
	"testing"
)

// TestSubqueryScanOnHashJoinInner pins M0146-0097 against PG 18.3
// (analysis/m0146/m0146-0097/): create_hashjoin_plan plans the hashed input
// with CP_SMALL_TLIST, so a subquery leaf under the Hash keeps `Subquery Scan`
// unless the join reads its columns whole and in order (and it has no
// resjunk column) — the pathtarget regime, not the physical one a join's
// outer side gets.
func TestSubqueryScanOnHashJoinInner(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE hjt (a int, b int)",
		"INSERT INTO hjt SELECT g % 50, g FROM generate_series(1, 5000) g",
		"CREATE TABLE hjbig (k int, v int)",
		"INSERT INTO hjbig SELECT g % 50, g FROM generate_series(1, 40000) g",
		"ANALYZE hjt", "ANALYZE hjbig",
	} {
		runSQL(t, ctx, q)
	}
	const x = "(select a, sum(b) s from hjt group by a) x"
	for _, tc := range []struct {
		sql  string
		keep bool
	}{
		{"select hjbig.v from hjbig, " + x + " where hjbig.k = x.a", true},
		{"select hjbig.v, x.a, x.s from hjbig, " + x + " where hjbig.k = x.a", false},
		{"select hjbig.v, x.s, x.a from hjbig, " + x + " where hjbig.k = x.a and x.s > 0", true},
		{"select hjbig.v from hjbig, (select sum(b) s from hjt group by a) x where hjbig.v = x.s", true},
	} {
		plan := strings.Join(renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+tc.sql)), "\n")
		if !strings.Contains(plan, "Hash Join") {
			t.Fatalf("%s: no hash join to test:\n%s", tc.sql, plan)
		}
		if got := strings.Contains(plan, "Subquery Scan on x"); got != tc.keep {
			t.Errorf("%s: Subquery Scan kept=%v, PG keeps=%v\n%s", tc.sql, got, tc.keep, plan)
		}
	}
}
