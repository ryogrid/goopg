package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planWithSettings plans sql under ps.
func planWithSettings(t *testing.T, ctx *Context, sql string, ps optimizer.PlannerSettings) optimizer.Node {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return plan
}

// drainPlanRows builds, opens and drains a plan.
func drainPlanRows(t *testing.T, ctx *Context, plan optimizer.Node) []Row {
	t.Helper()
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	rows, err := drainScan(op)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	_ = op.Close()
	return rows
}

// TestSortedRollupMatchesPG pins M0146-0020a against PG 18.3 with hash
// aggregation off: a ROLLUP is one sorted pass — `GroupAggregate` with a
// `Group Key:` per set over a Sort on the rollup order — and its rows come
// out in that pass's order:
//
//	GroupAggregate
//	  Group Key: k1, k2
//	  Group Key: k1
//	  Group Key: ()
//	  ->  Sort
//	        Sort Key: k1, k2
//
//	(1,1,2,30) (1,2,1,5) (1,NULL,3,35) (2,1,1,7) (2,NULL,1,7) (NULL,NULL,4,42)
func TestSortedRollupMatchesPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE r0 (k1 int, k2 int, v int)")
	runSQL(t, ctx, "INSERT INTO r0 VALUES (1,1,10),(1,1,20),(1,2,5),(2,1,7)")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashAgg = false
	ps.MaxParallelWorkersPerGather = 0
	const q = "SELECT k1, k2, count(*), sum(v) FROM r0 GROUP BY ROLLUP(k1, k2)"

	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	joined := strings.Join(lines, "\n")
	want := []string{"GroupAggregate", "Group Key: k1, k2", "Group Key: k1", "Group Key: ()"}
	for i, w := range want {
		if i >= len(lines) || !strings.HasPrefix(lines[i], w) {
			t.Fatalf("line %d: want %q:\n%s", i, w, joined)
		}
	}
	if !strings.Contains(joined, "Sort Key: k1, k2") {
		t.Errorf("want the rollup's input sorted on k1, k2:\n%s", joined)
	}

	rows := drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps))
	checkRollupRows(t, rows, rollupWantSorted())
}
