package postmaster

import (
	"strings"
	"testing"
)

// TestExplainIncrementalSortOverGrouping pins M0146-0177 against PG 18.3.
//
// An ORDER BY whose leading key is the GROUP BY key of a sorted aggregate is
// presorted on that key, and create_ordered_paths (planner.c) then builds an
// Incremental Sort only — never a full Sort beside it. goopg's grouping
// election offers node-less aggregate paths, and the presorted arm used to
// require the input's node, so they got the full Sort alone (TPC-DS Q60).
//
// The second half is the sibling it exposed: the upper narrowing descent
// (upperNarrowChildren) listed Sort but not Incremental Sort, so with an
// Incremental Sort on top the aggregate below kept its full input width and
// its input Sort printed the Subquery Scan column bare. PG prints it through
// the scan's alias (TPC-DS Q54's `my_revenue.revenue`).
func TestExplainIncrementalSortOverGrouping(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE r77 (c int, x numeric)",
		"INSERT INTO r77 SELECT g % 1000, (g % 97)::numeric FROM generate_series(1, 20000) g",
		"ANALYZE r77",
		"SET enable_hashagg = off",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF) SELECT CAST(rev/50 AS int) AS seg, count(*)
  FROM (SELECT c, sum(x) AS rev FROM r77 GROUP BY c) my_revenue
 GROUP BY CAST(rev/50 AS int) ORDER BY seg, count(*) LIMIT 100`)
	if e != "" {
		t.Fatal(e)
	}
	plan := strings.Join(got, "\n")
	for _, want := range []string{
		"->  Incremental Sort",
		"Presorted Key: (((my_revenue.rev / '50'::numeric))::integer)",
		"Sort Key: (((my_revenue.rev / '50'::numeric))::integer)\n",
	} {
		if !strings.Contains(plan+"\n", want) {
			t.Errorf("missing PG 18.3's %q:\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "->  Sort\n         Sort Key: (((my_revenue.rev") {
		t.Errorf("a full Sort was elected for the presorted ORDER BY:\n%s", plan)
	}
}
