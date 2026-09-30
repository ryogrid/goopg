package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestWindowInputIncrementalSort pins M0146-0005bx against PG 18.3:
// create_one_window_path gives a partially presorted window input an
// Incremental Sort (enable_incremental_sort on). PARTITION BY a, c over a
// GroupAggregate sorted on (a, b, c) shares the prefix a, and PG plans
//
//	WindowAgg
//	  ->  Incremental Sort
//	        Sort Key: x.a, x.c
//	        Presorted Key: x.a
//
// with count 1820, sum 200010000 — TPC-DS Q89's window over its sorted
// GROUP BY. goopg stacked a full Sort. (goopg flattens the derived table, so
// it prints the keys unqualified; that rendering is not what this pins.)
func TestWindowInputIncrementalSort(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE w (a int, b int, c int, v int)")
	runSQL(t, ctx, "INSERT INTO w SELECT i % 20, i % 13, i % 7, i FROM generate_series(1,20000) i")
	runSQL(t, ctx, "ANALYZE w")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashAgg = false
	const inner = "SELECT a, b, c, sum(v) s FROM w GROUP BY a, b, c"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT a, b, c, s, avg(s) OVER (PARTITION BY a, c) FROM ("+inner+") x", ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.HasPrefix(joined, "WindowAgg") || !strings.Contains(joined, "->  Incremental Sort") ||
		!(strings.Contains(joined, "Presorted Key: x.a\n") || strings.Contains(joined, "Presorted Key: a\n")) {
		t.Fatalf("want PG's Incremental Sort under the WindowAgg:\n%s", joined)
	}
	rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx,
		"SELECT count(*), sum(z)::bigint FROM (SELECT a, b, c, s, avg(s) OVER (PARTITION BY a, c) z FROM ("+inner+") x) y", ps)))
	if len(rows) != 1 || rows[0] != "1820,200010000" {
		t.Fatalf("rows = %v, want [1820,200010000]", rows)
	}
}

// TestWindowInputIncrementalSortDisplayRows pins the estimate of the
// Incremental Sort createWindowPlan stacks. The node is built without a path
// stamp, so EXPLAIN derives it (DeriveLegacyDisplayCost, EstimateRows); with
// no *IncrementalSort arm there it printed rows=1 cost=0.00, and the
// estimate collapsed into every node above (TPC-DS Q89's Limit showed
// 0.01..0.01). PG 18.3 prints `Incremental Sort (cost=1739.26..2061.71
// rows=1820 width=20)` for this fixture.
func TestWindowInputIncrementalSortDisplayRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE w (a int, b int, c int, v int)")
	runSQL(t, ctx, "INSERT INTO w SELECT i % 20, i % 13, i % 7, i FROM generate_series(1,20000) i")
	runSQL(t, ctx, "ANALYZE w")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashAgg = false
	var line string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx,
		"EXPLAIN SELECT a, b, c, s, avg(s) OVER (PARTITION BY a, c) FROM (SELECT a, b, c, sum(v) s FROM w GROUP BY a, b, c) x", ps)) {
		if len(r) > 0 && r[0].Kind == KindString && strings.Contains(r[0].StringValue(), "Incremental Sort") {
			line = r[0].StringValue()
		}
	}
	if !strings.Contains(line, " rows=1820 ") || !strings.Contains(line, "(cost=17") {
		t.Fatalf("want PG's Incremental Sort estimate (cost=1739.26..2061.71 rows=1820), got %q", line)
	}
}
