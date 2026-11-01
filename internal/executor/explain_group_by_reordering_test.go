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

// M0148-0001e: EXPLAIN qualifies upper-node columns when the statement's
// range table holds more than one entry (explain.c `useprefix =
// es->rtable_size > 1`), counting RTEs that left no plan node. aggregates.out's
// q1 case: the FROM subquery's Subquery Scan is stripped as trivial, so only
// btg is visible, yet PG prints `btg.x` because q1 is still an RTE. The
// planner reports the size on the Explain node (Explain.RtableSize); a bare
// single-table query (the test above) keeps bare names.
func TestExplainQualifiesForStrippedSubqueryRTE(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
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
	q := "EXPLAIN (COSTS OFF) SELECT count(*) FROM (SELECT * FROM btg ORDER BY x, y, w, z) AS q1 GROUP BY w, x, z, y"
	want := strings.Join([]string{
		"GroupAggregate",
		"  Group Key: btg.x, btg.y, btg.w, btg.z",
		"  ->  Incremental Sort",
		"        Sort Key: btg.x, btg.y, btg.w, btg.z",
		"        Presorted Key: btg.x, btg.y",
		"        ->  Index Scan using btg_x_y_idx on btg",
	}, "\n")
	if got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n"); got != want {
		t.Errorf("%s:\n got:\n%s\nwant (PG 18.3):\n%s", q, got, want)
	}
}

// M0148-0001e: PG 18's try_mergejoin_path sorts a merge join's outer with an
// Incremental Sort when the outer path already delivers a leading prefix of
// the merge ordering (outer_presorted_keys; initial_cost_mergejoin prices
// cost_incremental_sort). sort_inner_and_outer offers the `x`-first rotation
// of the merge keys, t1's (x, y) index scan is presorted on x, and the
// GroupAggregate above takes the join's (x, w, z) order with no Sort —
// aggregates.out's "Utilize the ordering of merge join" case, verified
// against PG 18.3. enable_incremental_sort = off falls back to full Sorts.
func TestMergeJoinOuterIncrementalSort(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
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
	ps.EnableHashJoin = false
	ps.EnableNestLoop = false
	q := "EXPLAIN (COSTS OFF) SELECT count(*) FROM btg t1 JOIN btg t2 ON t1.w = t2.w AND t1.x = t2.x AND t1.z = t2.z GROUP BY t1.w, t1.z, t1.x"
	want := strings.Join([]string{
		"GroupAggregate",
		"  Group Key: t1.x, t1.w, t1.z",
		"  ->  Merge Join",
		"        Merge Cond: ((t1.x = t2.x) AND (t1.w = t2.w) AND (t1.z = t2.z))",
		"        ->  Incremental Sort",
		"              Sort Key: t1.x, t1.w, t1.z",
		"              Presorted Key: t1.x",
		"              ->  Index Scan using btg_x_y_idx on btg t1",
		"        ->  Sort",
		"              Sort Key: t2.x, t2.w, t2.z",
		"              ->  Index Scan using btg_x_y_idx on btg t2",
	}, "\n")
	if got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n"); got != want {
		t.Errorf("%s:\n got:\n%s\nwant (PG 18.3):\n%s", q, got, want)
	}
	vq := "SELECT t1.x, t1.w, count(*) FROM btg t1 JOIN btg t2 ON t1.w = t2.w AND t1.x = t2.x AND t1.z = t2.z GROUP BY t1.w, t1.z, t1.x ORDER BY t1.w LIMIT 3"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, vq, ps)), ";"); got != "1|1|1;2|2|1;3|3|1" {
		t.Errorf("values: %s", got)
	}
	ps.EnableIncrementalSort = false
	if plan := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n"); strings.Contains(plan, "Incremental Sort") {
		t.Errorf("enable_incremental_sort = off must not build an Incremental Sort:\n%s", plan)
	}
}
