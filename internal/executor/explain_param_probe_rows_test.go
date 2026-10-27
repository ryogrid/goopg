package executor

import (
	"strings"
	"testing"
)

// TestExplainAnalyzeParamProbeRowsAfterMovedFilter pins M0122-0017: a
// parameterized nested loop's inner probe prints the residual conjunct it
// owns as its `Filter:` (M0146-0005aj), and its `actual rows` must count only
// the tuples that pass that filter — PG evaluates it inside the scan
// (ExecScan's qual) and InstrStopNode counts what the scan returns. goopg
// evaluates the conjunct at the join, so the probe's raw count is
// pre-filter; the renderer subtracts the attributed rejections.
//
// Fixture: 100 outer rows (id = k); inner keys 0..49 carry v = 2k. The probe
// returns 50 candidates over 100 loops; `v > o.id + 40` keeps k = 41..49 (9)
// and rejects 41. Scratch PG 18.3 with the same nested-loop shape prints
// `Index Scan … (actual rows=0.09 loops=100)` / `Rows Removed by Filter: 0`.
func TestExplainAnalyzeParamProbeRowsAfterMovedFilter(t *testing.T) {
	ctx, cleanup := nliJointypeFixture(t)
	defer cleanup()

	rows, err := runQueryWithErr(ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) "+
		"SELECT count(*) FROM nlij_outer o JOIN nlij_inner i ON i.k = o.k AND i.v > o.id + 40")
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(renderRows(rows), "\n")
	for _, want := range []string{
		"Nested Loop (actual rows=9.00 loops=1)",
		"Index Scan using nlij_inner_k on nlij_inner i (actual rows=0.09 loops=100)",
		"Filter: (v > (o.id + 40))",
		"Rows Removed by Filter: 0",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan lacks %q:\n%s", want, plan)
		}
	}
}
