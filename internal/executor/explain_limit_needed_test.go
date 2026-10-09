package executor

import (
	"strings"
	"testing"
)

// M0146-0110: PG's limit_needed (planner.c) plans no Limit node when OFFSET
// is a constant 0 (or NULL) and LIMIT is absent or a constant NULL — the
// `OFFSET 0` subquery fence costs nothing at run time. goopg printed a
// `Limit` there (`select 1 offset 0` → `Limit -> Result`).
func TestExplainLimitNeeded(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE m110l (a int, b int)"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		q     string
		limit bool
	}{
		{"SELECT 1 OFFSET 0", false},
		{"SELECT * FROM (SELECT a FROM m110l OFFSET 0) s", false},
		{"SELECT * FROM m110l LIMIT NULL", false},
		{"SELECT * FROM m110l LIMIT NULL OFFSET 0", false},
		{"SELECT DISTINCT b FROM m110l OFFSET 0", false},
		{"SELECT * FROM m110l OFFSET 2", true},
		{"SELECT * FROM m110l LIMIT 3 OFFSET 0", true},
	} {
		plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q), "\n")
		if got := strings.Contains(plan, "Limit"); got != tc.limit {
			t.Errorf("%s: Limit planned = %v, want %v:\n%s", tc.q, got, tc.limit, plan)
		}
	}
}
