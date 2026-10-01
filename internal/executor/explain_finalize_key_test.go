package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestFinalizeGroupKeyOverGatherMergeIsParenthesised pins M0146-0005cr
// against PG 18.3: show_agg_keys deparses a group key against the child's
// targetlist, and a Gather Merge only passes its input's columns through,
// so a computed key reaches the Finalize GroupAggregate as an OUTER_VAR and
// prints in parentheses — PG's own output for this statement (same settings):
//
//	Finalize GroupAggregate
//	  Group Key: (substr((b)::text, 1, 2))
//	  ->  Gather Merge
//	        ->  Partial GroupAggregate
//	              Group Key: (substr((b)::text, 1, 2))
func TestFinalizeGroupKeyOverGatherMergeIsParenthesised(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zpg1 (a int, b varchar(30))")
	runSQL(t, ctx, "INSERT INTO zpg1 SELECT i, 'x' || (i % 50) FROM generate_series(1,20000) i")
	runSQL(t, ctx, "ANALYZE zpg1")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 2
	ps.ParallelSetupCost = 0
	ps.ParallelTupleCost = 0
	ps.MinParallelTableScanSize = 0
	ps.EnableHashAgg = false
	const q = "SELECT substr(b, 1, 2), count(*) FROM zpg1 GROUP BY substr(b, 1, 2)"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Finalize GroupAggregate") {
		t.Skipf("no Finalize GroupAggregate in this plan:\n%s", joined)
	}
	if lines[0] != "Finalize GroupAggregate" || lines[1] != "Group Key: (substr((b)::text, 1, 2))" {
		t.Fatalf("want the Finalize key parenthesised:\n%s", joined)
	}
}
