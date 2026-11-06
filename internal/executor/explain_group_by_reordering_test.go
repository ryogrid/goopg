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

// M0148-0001e: with a presorted aggregate (adjust_group_pathkeys_for_groupagg
// appends array_agg(DISTINCT w)'s key behind the GROUP BY keys),
// group_keys_reorder_by_pathkeys still reorders the GROUP BY prefix
// (num_groupby_pathkeys) to the input's pathkeys and keeps the aggregate key
// behind it. aggregates.out: `GROUP BY x, y` with array_agg(DISTINCT w) over
// btg_y_x_w_idx groups as `y, x` straight off the Index Only Scan — no Sort.
func TestGroupByReorderKeepsPresortedAggregateKeys(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE btg (x int NOT NULL, y int NOT NULL, z text, w int NOT NULL)",
		"INSERT INTO btg SELECT i % 10, i % 10, 'abc' || i % 10, i FROM generate_series(1, 100) AS i",
		"CREATE INDEX btg_y_x_w_idx ON btg(y, x, w)",
		"ANALYZE btg",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashAgg = false
	ps.EnableSeqScan = false
	q := "EXPLAIN (COSTS OFF) SELECT y, x, array_agg(distinct w) FROM btg WHERE y < 0 GROUP BY x, y"
	plan := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n")
	lines := strings.Split(plan, "\n")
	if len(lines) < 3 || lines[0] != "GroupAggregate" || lines[1] != "  Group Key: y, x" ||
		lines[2] != "  ->  Index Only Scan using btg_y_x_w_idx on btg" || strings.Contains(plan, "Sort") {
		t.Errorf("want PG 18.3's GroupAggregate (Group Key: y, x) directly over the Index Only Scan:\n%s", plan)
	}
	vq := "SELECT y, x, array_agg(distinct w ORDER BY w) FROM btg WHERE y < 2 GROUP BY x, y ORDER BY 1, 2"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, vq, ps)), ";"); got != "0|0|{10,20,30,40,50,60,70,80,90,100};1|1|{1,11,21,31,41,51,61,71,81,91}" {
		t.Errorf("values: %s", got)
	}
	ps.EnableGroupByReordering = false
	if plan := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n"); !strings.Contains(plan, "Group Key: x, y") {
		t.Errorf("enable_group_by_reordering = off must keep the written order:\n%s", plan)
	}
}

// M0148-0001e: cost_agg (costsize.c) starts an Agg path's disabled_nodes from
// its input's, so a GroupAggregate over a Sort that enable_sort = off disabled
// counts one disabled node and loses to an enabled HashAggregate however much
// the hash spills. aggregates.out: `group by g%10000` with work_mem = 64kB,
// enable_hashagg on and enable_sort off is a HashAggregate in PG 18.3; goopg's
// sorted arms carried no count and the GroupAggregate won on cost.
func TestSortedAggInheritsDisabledSort(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE agg_data_20k AS SELECT g FROM generate_series(0, 19999) g",
		"ANALYZE agg_data_20k",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.WorkMem = 64 * 1024
	ps.EnableSort = false
	q := "EXPLAIN (COSTS OFF) SELECT g%10000 AS c1, sum(g::numeric) AS c2, count(*) AS c3 FROM agg_data_20k GROUP BY g%10000"
	want := strings.Join([]string{
		"HashAggregate",
		"  Group Key: (g % 10000)",
		"  ->  Seq Scan on agg_data_20k",
	}, "\n")
	if got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n"); got != want {
		t.Errorf("%s:\n got:\n%s\nwant (PG 18.3):\n%s", q, got, want)
	}
	vq := "SELECT count(*), sum(c3) FROM (SELECT g%10000 AS c1, count(*) AS c3 FROM agg_data_20k GROUP BY g%10000) s"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, vq, ps)), ";"); got != "10000|20000" {
		t.Errorf("values: %s", got)
	}
}

// M0148-0001e: EXPLAIN VERBOSE qualifies every qual and key column, even for
// one relation — show_scan_qual uses `useprefix = IsA(plan, SubqueryScan) ||
// es->verbose`, show_upper_qual / show_sort_group_keys `es->rtable_size > 1
// || es->verbose` (explain.c). The plain form stays bare.
func TestExplainVerboseQualifiesKeysAndQuals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE vq (a int, b int)")
	ps := optimizer.DefaultPlannerSettings()
	verbose := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (VERBOSE, COSTS OFF) SELECT a FROM vq WHERE b > 1 ORDER BY a", ps)), "\n")
	for _, want := range []string{"Sort Key: vq.a", "Filter: (vq.b > 1)", "Seq Scan on public.vq"} {
		if !strings.Contains(verbose, want) {
			t.Errorf("VERBOSE: want %q (PG 18.3); got:\n%s", want, verbose)
		}
	}
	plain := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) SELECT a FROM vq WHERE b > 1 ORDER BY a", ps)), "\n")
	for _, want := range []string{"Sort Key: a", "Filter: (b > 1)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain: want %q; got:\n%s", want, plain)
		}
	}
}
