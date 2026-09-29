package executor

import (
	"strings"
	"testing"
)

// TestExplainMergeJoinMaterializesCTEInner pins M0146-0005bd against PG 18.3:
// a merge join rewinds its inner with mark/restore, and a CTE Scan cannot, so
// final_cost_mergejoin elects materialize_inner for a presorted CTE inner and
// create_mergejoin_plan puts a Material above it, costed at the scan plus
// cpu_operator_cost per row. PG 18.3 on this query (empty table, default
// estimates) prints
//
//	Merge Join  (cost=54.04..65.54 rows=200 width=24)
//	  Merge Cond: (x.a = y.a)
//	  ->  CTE Scan on v x  (cost=0.00..4.00 rows=200 width=12)
//	  ->  Materialize  (cost=0.00..4.50 rows=200 width=12)
//	        ->  CTE Scan on v y  (cost=0.00..4.00 rows=200 width=12)
//
// (TPC-DS Q47/Q57's v1_lag / v1_lead self-joins).
func TestExplainMergeJoinMaterializesCTEInner(t *testing.T) {
	lines := cteExplainLines(t,
		`WITH v AS MATERIALIZED (SELECT a, count(*) AS c FROM t GROUP BY a ORDER BY a)
		 SELECT * FROM v x JOIN v y ON x.a = y.a`)
	joined := strings.Join(lines, "\n")
	if countLinesContaining(lines, "Merge Join") != 1 {
		t.Skipf("the join is not a merge join here; nothing to pin:\n%s", joined)
	}
	idx := -1
	for i, l := range lines {
		if strings.Contains(l, "Materialize") {
			idx = i
		}
	}
	if idx < 0 || idx+1 >= len(lines) || !strings.Contains(lines[idx+1], "CTE Scan on v") {
		t.Fatalf("want a Materialize directly above the merge join's inner CTE Scan:\n%s", joined)
	}
	if got := explainTotalCost(t, lines, "Materialize"); got != 4.50 {
		t.Errorf("Materialize total %.2f, want PG's 4.50 (the scan's 4.00 + cpu_operator_cost x 200):\n%s", got, joined)
	}
	// The run cost below the join's startup is PG's 11.50: outer 4.00, the
	// materialized inner 4.50, one comparison per tuple read from either
	// side (400 x 0.0025) and cpu_tuple_cost per merged tuple (200 x 0.01).
	// (The startup differs: goopg does not charge the CTE's initPlan there.)
	if got := explainTotalCost(t, lines, "Merge Join"); got != 11.50 {
		t.Errorf("Merge Join total %.2f, want the 11.50 PG's run cost adds up to:\n%s", got, joined)
	}
}

