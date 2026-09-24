package executor

// M0140-0006c-3: the end-to-end acceptance for a partial SetOp path through
// the REAL planner — not a synthesized node. With stamped page stats the
// comma-join branches below produce searched join rels with partial paths,
// the pure arm files, generateUpperRelGatherPaths wraps it in a PathGather,
// and that Gather WINS the serial tournament — the first shape where
// createSetOpPaths' winner is a *Gather rather than the *SetOp itself.
//
// The two assertions together pin both halves of the feature:
//
//   - shape: *Gather > *SetOp whose branches keep their boundary *Project
//     wrappers over the spliced searched emissions — a wholesale child
//     substitution would emit the join rel's internal schema (2 cols)
//     where the union declared 1;
//   - identity: the Gather's rows are the serial SetOp's rows, on the nose
//     — each branch partitioned by its own claim set, neither replayed.

import (
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planPartialSetOpWinner plans sql with the parallel-cost floors dropped so
// the partial SetOp's Gather can win on a small fixture (eligibility still
// reads stats.Pages, which the caller stamps high — the shape is
// deterministic, not timing-dependent).
func planPartialSetOpWinner(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MinParallelTableScanSize = 0
	ps.MinParallelIndexScanSize = 0
	ps.ParallelSetupCost = 0
	ps.ParallelTupleCost = 0
	ps.MaxParallelWorkersPerGather = 8
	// M0146-0002: a partial SetOp branch cannot carry a Parallel Hash yet (the
	// branch claim sets hold no per-join build state; ledgered), so with it on
	// the planner prefers a serial SetOp over per-branch Gathers of Parallel
	// Hash joins. This test's subject is the Gather over a partial SetOp.
	ps.EnableParallelHash = false
	node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return node
}

// drainNode executes a plan tree serially and returns its rendered rows —
// the multiset baseline the Gather must reproduce.
func drainNode(t *testing.T, ctx *Context, n optimizer.Node) []string {
	t.Helper()
	advanceStmtCounter(ctx)
	op, err := Build(n)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var rows []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			op.Close()
			t.Fatalf("next: %v", err)
		}
		rows = append(rows, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return rows
}

func TestGatherOverSetOpPlannerWinnerIdentity(t *testing.T) {
	ctx, cleanup := parallelSetOpFixture(t)
	defer cleanup()

	// Stamp big page counts so the planner's parallel-eligibility yields
	// partial paths on the join rels (eligibility reads stats.Pages, not
	// real row count).
	for _, name := range []string{"pq_setop_a", "pq_setop_b", "pq_setop_c"} {
		if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name}); ok {
			tbl.Stats = &catalog.TableStats{RowCount: 100000, Pages: 40000, Analyzed: true}
		}
	}

	// Left branch yields 40 rows (a.id in 0..39 via c.aid = a.id); the
	// right yields 40 rows in a disjoint range (b.id in 100000..100039 via
	// c.aid + 100000 = b.id), so a branch dropped wholesale or replayed
	// per-worker both change the multiset.
	const sql = "SELECT a.id FROM pq_setop_a a, pq_setop_c c WHERE c.aid = a.id " +
		"UNION ALL SELECT b.id FROM pq_setop_b b, pq_setop_c c WHERE c.aid + 100000 = b.id"

	gathered := planPartialSetOpWinner(t, ctx, sql)
	g, ok := gathered.(*optimizer.Gather)
	if !ok {
		t.Fatalf("winner is %T, want *Gather over a partial SetOp (the arm did not win; check the stats fixture)", gathered)
	}
	so, ok := g.Child.(*optimizer.SetOp)
	if !ok {
		t.Fatalf("Gather child is %T, want *SetOp", g.Child)
	}
	if so.LeftNonPartial || so.RightNonPartial {
		t.Fatalf("markers = (L=%v, R=%v); the pure arm filed — both branches are partial",
			so.LeftNonPartial, so.RightNonPartial)
	}
	// The splice preserves each branch's boundary projection over the
	// searched emission — 1 column out, not the join rel's internal 2.
	for i, child := range []optimizer.Node{so.Left, so.Right} {
		if len(child.Output()) != 1 {
			t.Fatalf("branch %d emits %d columns, want 1 — a wholesale child "+
				"substitution dropped the boundary projection", i, len(child.Output()))
		}
		if _, isProject := child.(*optimizer.Project); !isProject {
			t.Fatalf("branch %d is %T, want *Project over the spliced emission", i, child)
		}
	}

	// Serial baseline: the SetOp node's own branches, drained without the
	// Gather — 80 rows, both ranges present.
	advanceStmtCounter(ctx)
	want := drainNode(t, ctx, so)
	if len(want) != 80 {
		t.Fatalf("serial baseline = %d rows, want 80 (40 join + 40 join); the fixture moved", len(want))
	}

	got := drainNode(t, ctx, gathered)
	sort.Strings(want)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d — a worker replayed a whole branch or a claim starved one", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d differs: got %q want %q", i, got[i], want[i])
		}
	}
}
