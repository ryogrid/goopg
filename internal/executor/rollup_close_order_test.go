package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRollupCloseOrderMatchesComparator pins M0122-0012 C23: a sorted
// single rollup orders its groups by when they close in the input
// (rollupCloseOrder, PG's agg_retrieve_direct order), and that order is the
// one the column comparator builds. Each query runs with hash aggregation
// off — so the rollup is a GroupAggregate over a Sort — once per path, and
// the rows must agree in order. The data carries NULL keys, char(n)
// padding, duplicate rows and a numeric key with differing scales, the
// cases where a hashed group key and a sort comparator could part.
func TestRollupCloseOrderMatchesComparator(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE rco (a char(6), b text, c numeric, d int, v int)")
	runSQL(t, ctx, "INSERT INTO rco SELECT chr(97 + x % 5), 'b' || (x % 7), (x % 3)::numeric, x % 11, x FROM generate_series(1, 600) x")
	runSQL(t, ctx, "INSERT INTO rco VALUES (NULL, 'b1', 1, 1, 1), ('a', NULL, 2.0, NULL, 2), ('a  ', 'b1', 1.00, 3, 3), (NULL, NULL, NULL, NULL, 4)")
	runSQL(t, ctx, "INSERT INTO rco SELECT a, b, c, d, v FROM rco WHERE v % 13 = 0")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashAgg = false
	ps.MaxParallelWorkersPerGather = 0
	for _, q := range []string{
		"SELECT a, b, c, d, count(*), sum(v) FROM rco GROUP BY ROLLUP(a, b, c, d)",
		"SELECT b, a, count(*), max(v) FROM rco GROUP BY ROLLUP(b, a)",
		"SELECT c, sum(v) FROM rco GROUP BY ROLLUP(c)",
		"SELECT a, d, count(*) FROM rco GROUP BY GROUPING SETS ((a, d), (a), ())",
		"SELECT a, b, count(*) FROM rco WHERE v < 0 GROUP BY ROLLUP(a, b)",
		"SELECT d, a, count(*) FROM rco GROUP BY ROLLUP(d, a) ORDER BY d DESC, a",
	} {
		got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		rollupCloseOrderDisabled = true
		want := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		rollupCloseOrderDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: close order differs from the comparator (rows %d vs %d)\nclose      %v\ncomparator %v", q, len(got), len(want), got, want)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
	}
}

// TestRollupCloseOrder pins the ordering rule itself: groups end in input
// order, and the groups that end on the same row come most detailed first;
// groups still open at the end of the input end after the last row.
func TestRollupCloseOrder(t *testing.T) {
	sets := [][]int{{0, 1}, {0}, {}}
	row := func(s string) Row { return Row{NewStringDatum(s)} }
	// Input (a,b): (1,1) (1,2) (2,1). Groups in founding order:
	// (1,1)@set0 closes at row 2, (1)@set1 closes at row 3, ()@set2 open,
	// (1,2)@set0 closes at row 3, (2,1)@set0 open, (2)@set1 open.
	rows := []Row{row("11"), row("1"), row("()"), row("12"), row("21"), row("2")}
	setOf := []int{0, 1, 2, 0, 0, 1}
	closedAt := []int{2, 3, 0, 3, 0, 0}
	got := rollupCloseOrder(rows, setOf, closedAt, sets, 3)
	var names []string
	for _, r := range got {
		names = append(names, r[0].StringValue())
	}
	if want := "11 12 1 21 2 ()"; strings.Join(names, " ") != want {
		t.Fatalf("order %v, want %s", names, want)
	}
}
