package executor

import (
	"strings"
	"testing"
)

// TestExplainWindowOverSortedGroupsSkipsSort pins M0146-0005ay against PG
// 18.3 (enable_hashagg = off): create_one_window_path stacks a Sort only when
// its input's ordering does not already contain PARTITION BY ++ ORDER BY. A
// sorted GroupAggregate emits its group-key order, so a window partitioned or
// ordered on those keys reads it directly (TPC-DS Q51's per-channel
// cumulative sums); a window on another key still sorts.
func TestExplainWindowOverSortedGroupsSkipsSort(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE wg (g int, x int)"); err != nil {
		t.Fatal(err)
	}
	defer hashAggSeed(false)()

	cases := []struct {
		sql   string
		sorts int
	}{
		{"SELECT g, sum(sum(x)) OVER (PARTITION BY g) FROM wg GROUP BY g", 1},
		{"SELECT g, sum(sum(x)) OVER (ORDER BY g) FROM wg GROUP BY g", 1},
		{"SELECT g, sum(sum(x)) OVER (PARTITION BY x) FROM wg GROUP BY g, x", 2},
	}
	for _, c := range cases {
		rows := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+c.sql)
		if got := countLinesContaining(rows, "Sort Key"); got != c.sorts {
			t.Errorf("%s: want %d Sort node(s) as in PG, got %d:\n%s", c.sql, c.sorts, got, strings.Join(rows, "\n"))
		}
	}
}
