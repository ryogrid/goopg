package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestKeylessGroupedAggregate pins M0146-0102. A GROUP BY whose every key is
// pinned by a WHERE constant groups on nothing (PG's processed_groupClause is
// empty, standard_qp_callback), but the query is still grouped: PG runs
// AGG_SORTED with zero columns, labelled GroupAggregate (Group without
// aggregates), with no Group Key and no Sort, and it returns NO row over
// empty input — unlike an ungrouped aggregate, which returns one. Every
// expected plan and result is PG 18.3's for the same statements (TPC-DS
// Q44's InitPlan is the corpus witness).
func TestKeylessGroupedAggregate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ck (k int, v int)")

	plan := func(sql string) string { return strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+sql), "\n") }
	for _, tc := range []struct{ sql, head string }{
		{"SELECT avg(v) FROM ck WHERE k = 4 GROUP BY k", "GroupAggregate"},
		{"SELECT avg(v), k FROM ck WHERE k = 4 GROUP BY k", "GroupAggregate"},
		{"SELECT k FROM ck WHERE k = 4 GROUP BY k", "Group"},
	} {
		p := plan(tc.sql)
		lines := strings.Split(p, "\n")
		if strings.TrimSpace(lines[0]) != tc.head || strings.Contains(p, "Group Key") || strings.Contains(p, "Sort") {
			t.Errorf("%s: want PG's keyless `%s` with no Group Key and no Sort, got:\n%s", tc.sql, tc.head, p)
		}
	}

	// Empty input: no row, as PG's grouped aggregate — an ungrouped one
	// would return a single NULL row.
	for _, sql := range []string{
		"SELECT avg(v) FROM ck WHERE k = 4 GROUP BY k",
		"SELECT count(*) FROM ck WHERE k = 4 GROUP BY k",
		"SELECT k FROM ck WHERE k = 4 GROUP BY k",
	} {
		if rows := runSQL(t, ctx, sql); len(rows) != 0 {
			t.Errorf("%s over empty input: %d rows, want 0", sql, len(rows))
		}
	}
	runSQL(t, ctx, "INSERT INTO ck VALUES (4, 10), (4, 20), (5, 1)")
	if got := renderRows(runSQL(t, ctx, "SELECT count(*), k FROM ck WHERE k = 4 GROUP BY k")); len(got) != 1 || got[0] != "2|4" {
		t.Errorf("one group of two rows: got %v", got)
	}
	if rows := runSQL(t, ctx, "SELECT k, count(*) FROM ck WHERE k = 4 GROUP BY k HAVING count(*) > 5"); len(rows) != 0 {
		t.Errorf("HAVING filters the only group: got %v", rows)
	}
}

// TestKeylessGroupedAggregateParallel: the same aggregate split across a
// Gather. PG 18.3 (same settings) prints `Finalize GroupAggregate -> Gather
// -> Partial GroupAggregate`, and a filter matching no row still returns no
// row — no worker contributes a pre-created empty group.
func TestKeylessGroupedAggregateParallel(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE cp (k int, v int)")
	runSQL(t, ctx, "INSERT INTO cp SELECT i % 50, i FROM generate_series(1, 20000) i")
	runSQL(t, ctx, "ANALYZE cp")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 2
	ps.ParallelSetupCost = 0
	ps.ParallelTupleCost = 0
	ps.MinParallelTableScanSize = 0
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) SELECT avg(v) FROM cp WHERE k = 4 GROUP BY k", ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Finalize GroupAggregate") || !strings.Contains(joined, "Partial GroupAggregate") || strings.Contains(joined, "Group Key") {
		t.Fatalf("want PG's keyless Finalize/Partial GroupAggregate pair, got:\n%s", joined)
	}
	if rows := drainPlanRows(t, ctx, planWithSettings(t, ctx, "SELECT avg(v) FROM cp WHERE k = 77 GROUP BY k", ps)); len(rows) != 0 {
		t.Errorf("empty filter under the split: %d rows, want 0", len(rows))
	}
	if got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, "SELECT count(*) FROM cp WHERE k = 4 GROUP BY k", ps))); len(got) != 1 || got[0] != "400" {
		t.Errorf("one group under the split: got %v", got)
	}
}
