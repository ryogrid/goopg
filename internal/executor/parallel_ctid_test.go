package executor

import (
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCTIDSurvivesGather pins M0146-0051 against PG 18.3: `ctid` read above
// a Gather or Gather Merge reports each row's own tid. A row's tid is a slot
// property, not a column, so a worker row used to reach the leader without
// it: `count(DISTINCT ctid)` over Gather Merge → Sort → Parallel Seq Scan
// counted 0 (PG: every row), and a ctid projected above a Gather read NULL
// for every worker row. Each want is PG's answer on the same table. Both
// builders run every query: the slab project reaches the Gather through an
// OpAdapter, the legacy one directly.
func TestCTIDSurvivesGather(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE pq_ctid (a int, b text)")
	runSQL(t, ctx, "INSERT INTO pq_ctid SELECT g, repeat('x', 1000) FROM generate_series(1, 3000) g")
	ps := optimizer.DefaultPlannerSettings()
	ps.ParallelSetupCost, ps.ParallelTupleCost, ps.MinParallelTableScanSize = 0, 0, 0
	ps.MaxParallelWorkersPerGather = 2
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	for _, c := range []struct{ sql, node, want string }{
		{"SELECT count(DISTINCT ctid) FROM pq_ctid", "Gather Merge", "3000"},
		{"SELECT ctid FROM pq_ctid WHERE a % 1000 = 0 ORDER BY a", "Gather Merge", "(142,6)|(285,5)|(428,4)"},
		{"SELECT ctid FROM pq_ctid WHERE a % 1000 = 0", "Gather", "(142,6)|(285,5)|(428,4)"},
	} {
		stmts, err := parser.Parse(c.sql)
		if err != nil {
			t.Fatalf("parse %q: %v", c.sql, err)
		}
		plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatalf("plan %q: %v", c.sql, err)
		}
		text := renderPlanText(plan)
		if !strings.Contains(text, c.node) {
			t.Fatalf("%s: plan has no %s, so the test would prove nothing:\n%s", c.sql, c.node, text)
		}
		for _, slab := range []bool{false, true} {
			advanceStmtCounter(ctx)
			var op Operator
			if slab {
				op, err = BuildFastIterator(plan)
			} else {
				op, err = Build(plan)
			}
			if err != nil {
				t.Fatalf("build %q: %v", c.sql, err)
			}
			rows, err := Run(op, ctx)
			if err != nil {
				t.Fatalf("run %q (slab=%v): %v", c.sql, slab, err)
			}
			got := renderRows(rows)
			if c.node == "Gather" && len(got) > 1 {
				sort.Strings(got)
			}
			if g := strings.Join(got, "|"); g != c.want {
				t.Errorf("%s (slab=%v)\ngot  %s\nwant PG's %s\nplan:\n%s", c.sql, slab, g, c.want, text)
			}
		}
	}
}
