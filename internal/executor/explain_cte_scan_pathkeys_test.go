package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestGroupAggOverSortedCTEScanSkipsSort pins M0146-0005bw against PG 18.3:
// set_cte_pathlist hands a CTE scan its body's pathkeys
// (convert_subquery_pathkeys, PG 17+), and add_paths_to_grouping_rel's
// is_sorted test then takes that scan as presorted input. With hash
// aggregation off PG plans
//
//	GroupAggregate
//	  Group Key: s.a
//	  ...
//	  ->  CTE Scan on s
//
// with no Sort — not below the aggregate, and not above it for the ORDER BY,
// which the sorted aggregate's emission order already satisfies (count 50,
// sum 12502500). TPC-DS Q24's outer GROUP BY/ORDER BY over the ssales CTE;
// goopg sorted the scan again.
func TestGroupAggOverSortedCTEScanSkipsSort(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE g (a int, b int, v int)")
	runSQL(t, ctx, "INSERT INTO g SELECT i % 50, i % 7, i FROM generate_series(1,5000) i")
	runSQL(t, ctx, "ANALYZE g")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashAgg = false
	const cte = "WITH s AS (SELECT a, b, sum(v) t FROM g GROUP BY a, b) "
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx,
		"EXPLAIN (COSTS OFF) "+cte+"SELECT a, sum(t) FROM s GROUP BY a HAVING sum(t) > (SELECT avg(t) FROM s) ORDER BY a", ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.HasPrefix(joined, "GroupAggregate") || lines[len(lines)-1] != "->  CTE Scan on s" {
		t.Fatalf("want the GroupAggregate directly over the presorted CTE Scan:\n%s", joined)
	}
	if strings.Count(joined, "Sort Key:") != 1 {
		t.Fatalf("want only the CTE body's Sort:\n%s", joined)
	}
	rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx,
		cte+"SELECT count(*), sum(x) FROM (SELECT a, sum(t) x FROM s GROUP BY a HAVING sum(t) > (SELECT avg(t) FROM s)) z", ps)))
	if len(rows) != 1 || rows[0] != "50,12502500" {
		t.Fatalf("rows = %v, want [50,12502500]", rows)
	}
}
