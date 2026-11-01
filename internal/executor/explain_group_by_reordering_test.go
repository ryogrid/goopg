package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0148-0001e: get_useful_group_keys_orderings (pathkeys.c) offers the GROUP
// BY keys reordered to an input path's pathkeys (group_keys_reorder_by_pathkeys)
// beside their written order, and make_ordered_path builds no Sort, an
// Incremental Sort or a Sort per ordering (add_paths_to_grouping_rel).
// aggregates.out's btg block: `GROUP BY y, x` over the (x, y) index groups as
// `x, y` with no Sort, and `GROUP BY z, y, w, x` sorts incrementally on the
// presorted `x, y` prefix. Verified against PG 18.3; the
// enable_group_by_reordering = off arm keeps the written order.
func TestGroupByKeysReorderedToInputPathkeys(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		// NOT NULL keys: this fixture's cluster stores no NULL-keyed index
		// entries, so an ordered full scan over a nullable key column is
		// guarded off here (indexUnboundKeysNotNull); PG's plans are the same.
		"CREATE TABLE btg (x int NOT NULL, y int NOT NULL, z text, w int)",
		"INSERT INTO btg SELECT i % 10, i % 10, 'abc' || i % 10, i FROM generate_series(1, 100) AS i",
		"CREATE INDEX btg_x_y_idx ON btg(x, y)",
		"ANALYZE btg",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashAgg = false
	ps.EnableSeqScan = false
	for _, tc := range []struct{ q, want string }{
		{"EXPLAIN (COSTS OFF) SELECT count(*) FROM btg GROUP BY y, x", strings.Join([]string{
			"GroupAggregate",
			"  Group Key: x, y",
			"  ->  Index Only Scan using btg_x_y_idx on btg",
		}, "\n")},
		{"EXPLAIN (COSTS OFF) SELECT count(*) FROM btg GROUP BY z, y, w, x", strings.Join([]string{
			"GroupAggregate",
			"  Group Key: x, y, z, w",
			"  ->  Incremental Sort",
			"        Sort Key: x, y, z, w",
			"        Presorted Key: x, y",
			"        ->  Index Scan using btg_x_y_idx on btg",
		}, "\n")},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, tc.q, ps)), "\n"); got != tc.want {
			t.Errorf("%s:\n got:\n%s\nwant (PG 18.3):\n%s", tc.q, got, tc.want)
		}
	}
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT y, x, count(*) FROM btg GROUP BY y, x ORDER BY 1, 2 LIMIT 2", ps)), ";"); got != "0|0|10;1|1|10" {
		t.Errorf("values: %s", got)
	}
	ps.EnableGroupByReordering = false
	plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) SELECT count(*) FROM btg GROUP BY y, x", ps)), "\n")
	if !strings.Contains(plan, "Group Key: y, x") {
		t.Errorf("enable_group_by_reordering = off must keep the written order:\n%s", plan)
	}
}
