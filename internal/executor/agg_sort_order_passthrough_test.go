package executor

import (
	"strings"
	"testing"
)

// TestAggSortOrderPassthroughOrderBy pins M0145-0008h: the upstream
// aggregates regress case (aggregates.out:3158, agg_sort_order) — `GROUP BY`
// a primary key, `ORDER BY` a functionally dependent column — panicked the
// planner (assertSortInputTargetCoversKeys), dropping the session.
// The ORDER BY key appends the dependent column as an aggregate passthrough
// AFTER the grouping paths were snapshotted, and electOrderedGrouping rebuilt
// its winner from the snapshot, which lacks it. The loop now declines in that
// case and the normal ordered arm sorts over the aggregate that carries the
// column. PG 18.3's plan is Sort c2 / GroupAggregate c1 / Sort c1, c2; its
// rows are pinned below.
func TestAggSortOrderPassthroughOrderBy(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE agg_sort_order (c1 int PRIMARY KEY, c2 int)",
		"CREATE UNIQUE INDEX agg_sort_order_c2_idx ON agg_sort_order(c2)",
		"INSERT INTO agg_sort_order SELECT i, i FROM generate_series(1,100) i",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	const q = "SELECT array_agg(c1 ORDER BY c2), c2 FROM agg_sort_order WHERE c2 < 100 GROUP BY c1 ORDER BY 2"
	plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+q), "\n")
	if !strings.Contains(plan, "Sort Key: c2") {
		t.Errorf("expected the ORDER BY Sort on c2 over the aggregate; got:\n%s", plan)
	}
	rows, err := runQueryWithErr(ctx, q+" LIMIT 3")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r[0].Format()+"|"+r[1].Format())
	}
	want := []string{"{1}|1", "{2}|2", "{3}|3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want PG 18.3's %v", got, want)
	}
}
