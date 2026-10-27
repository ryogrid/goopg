package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestExplainPlanningTimeIsPlannerDuration pins M0122-0019: "Planning Time"
// is the planner's own duration — PG's ExplainOneQuery times pg_plan_query
// alone (explain.c:350-356) — stamped on the Explain node where the inner
// statement was planned. goopg used to time only the executor's operator
// construction (buildNode, PG's ExecInitNode, which PG counts inside
// Execution Time), so TPC-DS Q64 printed 0.17 ms for a ~1.6 s join search.
//
// A sentinel planning duration on the node must reach every renderer
// verbatim: ANALYZE text and JSON, and plain EXPLAIN (SUMMARY ON), which PG
// also prints (summary defaults to analyze; explicit ON wins).
func TestExplainPlanningTimeIsPlannerDuration(t *testing.T) {
	ctx, cleanup := nliJointypeFixture(t)
	defer cleanup()

	run := func(sql string) string {
		t.Helper()
		advanceStmtCounter(ctx)
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			t.Fatal(err)
		}
		ex, ok := plan.(*optimizer.Explain)
		if !ok {
			t.Fatalf("plan is %T, want *optimizer.Explain", plan)
		}
		if ex.PlanningNs <= 0 {
			t.Fatalf("planner did not stamp its duration: PlanningNs = %d", ex.PlanningNs)
		}
		ex.PlanningNs = 1234567890
		op, err := Build(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		defer op.Close()
		var lines []string
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, renderRows([]Row{slot.Materialize().Row()})...)
		}
		return strings.Join(lines, "\n")
	}

	const q = " SELECT count(*) FROM nlij_outer o JOIN nlij_inner i ON i.k = o.k"
	for _, c := range []struct {
		sql, want string
	}{
		{"EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF)" + q, "Planning Time: 1234.568 ms"},
		{"EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, FORMAT JSON)" + q, `"Planning Time": 1234.56789`},
		{"EXPLAIN (SUMMARY ON, COSTS OFF)" + q, "Planning Time: 1234.568 ms"},
		{"EXPLAIN (SUMMARY ON, COSTS OFF, FORMAT JSON)" + q, `"Planning Time": 1234.56789`},
	} {
		if out := run(c.sql); !strings.Contains(out, c.want) {
			t.Fatalf("%s: output lacks %q:\n%s", c.sql, c.want, out)
		}
	}
	// Plain EXPLAIN keeps SUMMARY off by default.
	if out := run("EXPLAIN (COSTS OFF)" + q); strings.Contains(out, "Planning Time") {
		t.Fatalf("plain EXPLAIN printed a summary:\n%s", out)
	}
}
