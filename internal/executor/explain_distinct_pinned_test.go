package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0146-0113: when every SELECT DISTINCT target is a constant or a column a
// WHERE `col = const` pins, PG's distinct_pathkeys is empty and the DISTINCT
// plans as a LIMIT 1 over its input (create_final_distinct_paths; regress
// select_distinct's "Ensure we get a plan with a Limit 1").
func TestExplainDistinctOnPinnedKeysIsLimitOne(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE m113d (four int, two int, v int)",
		"INSERT INTO m113d SELECT g % 4, g % 2, g FROM generate_series(1, 2000) g",
		"ANALYZE m113d",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, tc := range []struct {
		q     string
		limit bool
	}{
		{"SELECT DISTINCT four FROM m113d WHERE four = 0", true},
		{"SELECT DISTINCT four, 1, 2, 3 FROM m113d WHERE four = 0", true},
		{"SELECT DISTINCT 1 FROM m113d", true},
		{"SELECT DISTINCT four FROM m113d WHERE four = 0 OR four = 1", false},
		{"SELECT DISTINCT four, two FROM m113d WHERE four = 0", false},
	} {
		plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q, ps)), "\n")
		if got := strings.HasPrefix(plan, "Limit"); got != tc.limit {
			t.Errorf("%s: Limit-rooted = %v, want %v:\n%s", tc.q, got, tc.limit, plan)
		}
	}
	if got := renderRows(runSQL(t, ctx, "SELECT DISTINCT four, 1 FROM m113d WHERE four = 0")); len(got) != 1 {
		t.Errorf("want one row, got %v", got)
	}
}
