package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestGroupAggIncrementalSortOverPresortedInput (M0146-0006): make_ordered_path
// (planner.c) feeds a sorted aggregate through an Incremental Sort when its
// input already delivers a leading prefix of the group keys. PG 18.3 on the
// same data, enable_hashagg = off:
//
//	GroupAggregate
//	  Group Key: is_t.a, is_t.b
//	  ->  Incremental Sort
//	        Sort Key: is_t.a, is_t.b
//	        Presorted Key: is_t.a
//	        ->  Sort  (Sort Key: is_t.a)
//
// goopg always stacked a full Sort on (a, b). The values must equal the
// hashed plan's.
func TestGroupAggIncrementalSortOverPresortedInput(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE is_t (a int, b int, c int)",
		"INSERT INTO is_t SELECT g % 1000, (g * 7) % 50, g FROM generate_series(1, 100000) g",
		"ANALYZE is_t",
	} {
		runSQL(t, ctx, q)
	}
	const q = "SELECT a, b, sum(c) FROM (SELECT * FROM is_t WHERE a < 100 ORDER BY a OFFSET 0) s GROUP BY a, b ORDER BY a, b"
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashAgg = false
	lines := renderRows(runSQLWith(t, ctx, "EXPLAIN "+q, ps))
	plan := strings.Join(lines, "\n")
	agg := -1
	for i, l := range lines {
		if strings.Contains(l, "GroupAggregate") {
			agg = i
			break
		}
	}
	if agg < 0 || agg+1 >= len(lines) {
		t.Fatalf("want a GroupAggregate\nplan:\n%s", plan)
	}
	var below string
	for _, l := range lines[agg+1:] {
		if strings.Contains(l, "->") {
			below = l
			break
		}
	}
	if !strings.Contains(below, "Incremental Sort") || !strings.Contains(plan, "Presorted Key:") {
		t.Fatalf("want the GroupAggregate fed by an Incremental Sort over the a-ordered input\nplan:\n%s", plan)
	}
	got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), ";")
	if want := strings.Join(renderRows(runSQL(t, ctx, q)), ";"); got != want {
		t.Fatalf("incremental-sort plan rows differ from the default plan's\nplan:\n%s", plan)
	}
}
