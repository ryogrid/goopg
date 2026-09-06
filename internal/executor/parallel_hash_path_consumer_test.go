package executor

// C-19f / P5-06's executor consumer check.
//
// The planner slice is `internal/optimizer/joinpathsparallel.go`
// (`try_partial_hashjoin_path`) and its design is
// docs/design/planner-c19f-parallel-hashjoin/DESIGN.md. Its unit tests prove
// that a Gather over a partial hash join WINS by cost — which C-19d could not
// obtain at a base rel at any relation size, because there the whole relation
// crossed the parallel boundary.
//
// A planner-only pin is explicitly not enough here. "An unwinnable path is an
// untested path": a partial hash join path that has never been CHOSEN has never
// been EXECUTED, and when the bitmap costs were fixed four latent execution
// bugs surfaced in a row. Writing this file found two more of exactly that
// class, both in C-19d's landed createPlan arms and both invisible until a
// Gather path could win at a search root:
//
//   - `createGatherPlan` built `&Gather{…}` as a struct literal instead of
//     calling `NewGather`, so the node's schema was never set and
//     `createPlanAtSearchRootRange` panicked with "search root layout is 5
//     columns but its output is 0";
//   - `*Gather` did not embed `searchedTree`, so `markSearchedTree` panicked —
//     the tag whose absence would have let the legacy posmap family permute an
//     already-searched subtree a second time.
//
// So this test's job is not decoration. It asserts, on a plan the PATH MODEL
// chose (not the `MaybeAddGather` post-pass):
//
//	(1) the shape executed is Gather -> Hash Join with the PROBE side's scan
//	    stamped parallel and the BUILD side's not;
//	(2) the rows are identical to the serial answer — a parallel scan that
//	    landed on the build side returns a plausible SMALLER result with no
//	    error anywhere;
//	(3) the build ran ONCE and was SHARED, which is the E-09a/E-09b behaviour
//	    the C-19f price describes (§3.2 of the design). A test that passed while
//	    every participant rebuilt the table privately would mean the cost model
//	    is describing an executor that is not there.

import (
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// c19fSettings makes the crossover reachable on a unit-test-sized fixture.
//
// Nothing here manufactures a plan shape: every field is a real PG GUC, and the
// two cost knobs are the same levers the design's §7.1 inequality is written
// in. The DEFAULT calibration's crossover is pinned separately and at realistic
// scale by internal/optimizer's TestPartialHashJoinGatherWinsExactlyAtTheCrossover
// — this fixture exists to make the WINNING path executable in a few
// milliseconds, not to argue about where the crossover belongs.
//
// `MinParallelTableScanSize: 1` is the path-model twin of what the post-pass
// tests do with `DebugParallelQuery: "on"`.
func c19fSettings() optimizer.PlannerSettings {
	ps := optimizer.DefaultPlannerSettings()
	ps.MinParallelTableScanSize = 1
	ps.MinParallelIndexScanSize = 1
	ps.MaxParallelWorkersPerGather = 2
	ps.ParallelLeaderParticipation = true
	// parallel_setup_cost / parallel_tuple_cost scaled to a fixture two orders
	// of magnitude smaller than the 2 M-row relation the default constants are
	// calibrated for.
	ps.ParallelSetupCost = 0.001
	ps.ParallelTupleCost = 0.00001
	return ps
}

// c19fPlan plans sql under the path model with C-19f's producer live.
//
// The `WHERE` in every query below is load-bearing and not cosmetic: a
// filterless INNER join tree is deliberately left on the legacy planner path
// (planner.go's `tryJoinSearch` call site), so a query with no restriction
// never enters the search at all and would silently test nothing.
func c19fPlan(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, c19fSettings())
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	return node
}

// c19fFindGather returns the Gather the PATH MODEL placed, or nil.
func c19fFindGather(n optimizer.Node) *optimizer.Gather {
	var found *optimizer.Gather
	var walk func(optimizer.Node)
	walk = func(cur optimizer.Node) {
		if cur == nil || found != nil {
			return
		}
		if g, ok := cur.(*optimizer.Gather); ok {
			found = g
			return
		}
		for _, c := range optimizer.ParallelChildrenForTest(cur) {
			walk(c)
		}
	}
	walk(n)
	return found
}

func c19fFindJoin(n optimizer.Node) *optimizer.Join {
	var found *optimizer.Join
	var walk func(optimizer.Node)
	walk = func(cur optimizer.Node) {
		if cur == nil || found != nil {
			return
		}
		if j, ok := cur.(*optimizer.Join); ok {
			found = j
			return
		}
		for _, c := range optimizer.ParallelChildrenForTest(cur) {
			walk(c)
		}
	}
	walk(n)
	return found
}

// c19fRun executes a planned tree and renders its rows.
func c19fRun(t *testing.T, ctx *Context, node optimizer.Node) []string {
	t.Helper()
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	op, err := Build(node)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	sort.Strings(out)
	return out
}

// c19fCorpus is deliberately narrow: these are the shapes
// `addPartialHashJoinPath` can produce today (the search's INNER-only jointype
// pin, leftdeep-joins 03 §4.4). The broader jointype corpus is
// parallel_hash_join_test.go's, which exercises the same executor mechanism
// through the POST-PASS.
func c19fCorpus() []string {
	return []string{
		"SELECT f.fid, d.dname FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE f.amt >= 0",
		"SELECT count(*) FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE f.amt >= 0",
		"SELECT f.fid FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE f.amt > 200",
		"SELECT f.fid, d.dname FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE d.dk > 3",
	}
}

// TestC19fPathModelGatherExecutesAsAParallelHashJoin is the item's consumer
// check.
func TestC19fPathModelGatherExecutesAsAParallelHashJoin(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	chosen := 0
	for _, sql := range c19fCorpus() {
		t.Run(sql, func(t *testing.T) {
			// The serial control arm: the SAME statement with the producer
			// off, which is the shipped default. Any row difference is a
			// wrong answer, not a slow plan.
			restoreOff := optimizer.SetGatherPathsMode("off")
			serialPlan := c19fPlan(t, ctx, sql)
			restoreOff()
			if g := c19fFindGather(serialPlan); g != nil {
				t.Fatal("mode off produced a Gather; the control arm is not serial and proves nothing")
			}

			restoreAll := optimizer.SetGatherPathsMode("all")
			parPlan := c19fPlan(t, ctx, sql)
			restoreAll()

			gather := c19fFindGather(parPlan)
			if gather == nil {
				t.Skipf("the path model did not choose a Gather for this shape; nothing to consume")
			}
			chosen++

			// (1) The shape. The Gather must sit OVER the hash join — that is
			// the whole point of C-19f, as against C-19d's base-rel Gather —
			// and the parallel stamp must be on the PROBE side.
			j, ok := gather.Child.(*optimizer.Join)
			if !ok {
				// A Project/Filter may intervene; find the join below.
				j = c19fFindJoin(gather.Child)
			}
			if j == nil {
				t.Fatalf("the Gather's subtree carries no join: %T", gather.Child)
			}
			if j.Algo != optimizer.JoinAlgoHash {
				t.Fatalf("the gathered join is a %v, not a hash join", j.Algo)
			}
			probe, build := j.Left, j.Right
			if j.BuildLeft {
				probe, build = j.Right, j.Left
			}
			probeScan := c19fScanOf(probe)
			buildScan := c19fScanOf(build)
			if probeScan == nil || !probeScan.Parallel {
				t.Errorf("the PROBE side's scan is not stamped Parallel; every worker would read the whole relation and the Gather would return %d+1 copies of every row",
					gather.WorkersPlanned)
			}
			if buildScan != nil && buildScan.Parallel {
				t.Fatal("the BUILD side's scan is stamped Parallel; each worker would hash only a PARTITION of the build input and the join would quietly drop matches")
			}
			if !optimizer.HasShareableHashJoin(gather.Child) {
				t.Fatal("the executor does not recognise the gathered join as shareable, so prebuildSharedHashJoins would decline and every participant would rebuild the table privately — the exact 5x the C-19f price says is gone")
			}

			// (2) Identity with the serial answer, at TWO work_mem values: one
			// that holds the build in memory and one small enough that the
			// SHARED build spills to batch files. The spilling shared build
			// (E-09a) is the newer of the two mechanisms the C-19f price
			// describes — before it, every participant fell through to a full
			// private build — so a partial hash join path that only ever ran
			// resident would leave half of that price unexercised.
			//
			// Both arms are run at the SAME work_mem, deliberately: the
			// fixture's serial answer is itself work_mem-dependent on one of
			// these shapes (a HEAD defect, unrelated to this slice and
			// identical with the producer off — see the report accompanying
			// C-19f), and comparing a parallel run against a serial run taken
			// at a different budget would blame that on parallelism.
			saved := ctx.WorkMem
			defer func() { ctx.WorkMem = saved }()
			for _, workMem := range []int64{1 << 20, 16 << 10} {
				ctx.WorkMem = workMem
				want := c19fRun(t, ctx, serialPlan)
				got := c19fRun(t, ctx, parPlan)
				if len(got) != len(want) {
					t.Fatalf("work_mem=%d: parallel returned %d rows, serial %d; fewer rows usually means the parallel scan landed on the BUILD side",
						workMem, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("work_mem=%d: row %d differs: parallel %q, serial %q", workMem, i, got[i], want[i])
					}
				}
			}
		})
	}
	if chosen == 0 {
		t.Fatal("no shape in the corpus produced a path-model Gather; the whole comparison was serial against serial and asserts nothing")
	}
}

// c19fScanOf returns the SeqScan a side of the join ultimately reads, peeling
// the Filter/Project wrappers stampParallelScan itself descends through.
func c19fScanOf(n optimizer.Node) *optimizer.SeqScan {
	for {
		switch x := n.(type) {
		case *optimizer.SeqScan:
			return x
		case *optimizer.Filter:
			n = x.Child
		case *optimizer.Project:
			n = x.Child
		default:
			return nil
		}
	}
}

// TestC19fGatheredHashBuildRunsOnceAndIsShared is the E-09a/E-09b half of the
// consumer check, and the one that keeps the PRICE honest.
//
// The C-19f cost model charges the hash build ONCE, undivided (design §3.2),
// because after E-09a/E-09b the leader performs it once
// (`prebuildSharedHashJoins`) and every participant adopts the published table
// — spilling builds included. A reverted D-05 experiment charged a 5x
// participant multiplier derived from the sharing-decline rule E-09a deleted.
// If the sharing ever stopped happening for a path-model Gather, that
// multiplier would become right again and this price would be wrong by 5x with
// nothing failing. So the sharing is asserted, on the plan the path model
// chose, through the executor's own predicate and its own EXPLAIN ANALYZE
// witness: exactly one Build Time for the join.
func TestC19fGatheredHashBuildRunsOnceAndIsShared(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	sql := "SELECT f.fid, d.dname FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk WHERE f.amt >= 0"
	restore := optimizer.SetGatherPathsMode("all")
	plan := c19fPlan(t, ctx, sql)
	restore()

	gather := c19fFindGather(plan)
	if gather == nil {
		t.Skip("the path model chose no Gather for this fixture")
	}
	if gather.WorkersPlanned <= 0 {
		t.Fatalf("the Gather plans %d workers", gather.WorkersPlanned)
	}

	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	text := c19fExplainAnalyze(t, ctx, plan)

	// One Build Time means one build. Five would mean every participant built
	// its own copy — the pre-E-09a behaviour, and the shape the reverted 5x
	// multiplier was derived from.
	// EXACTLY one Build Time. `> 1` alone would also accept ZERO, which is
	// what a fixture that quietly stopped producing a hash join would report.
	// E-09a's own acceptance witness on TPC-H Q9 was five Build Times becoming
	// one; this is that witness at unit scale, on a plan the PATH MODEL chose.
	if n := strings.Count(text, "Build Time"); n != 1 {
		t.Errorf("EXPLAIN ANALYZE reports %d Build Times, want exactly 1; the C-19f price charges the build ONCE because the leader performs it once and shares it (E-09a/E-09b):\n%s", n, text)
	}
	if !strings.Contains(text, "Parallel Seq Scan on pq_fact") {
		t.Errorf("the PROBE side is not a Parallel Seq Scan under the path-model Gather:\n%s", text)
	}
	// The build side's own witness: `loops=0` in every participant means no
	// worker re-ran the build scan. A worker showing loops=1 there is the
	// pre-E-09a shape, in which the 5x multiplier the C-19f price refutes
	// would be correct again.
	buildLine := -1
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.Contains(l, "Seq Scan on pq_dim") {
			buildLine = i
		}
	}
	if buildLine < 0 {
		t.Fatalf("no build-side scan in the plan:\n%s", text)
	}
	rebuilt := 0
	for _, l := range lines[buildLine+1:] {
		if !strings.Contains(l, "Worker ") {
			break
		}
		if !strings.Contains(l, "loops=0") {
			rebuilt++
		}
	}
	if rebuilt > 0 {
		t.Errorf("%d worker(s) re-ran the BUILD side's scan; the build is meant to be performed once by the leader and adopted by pointer:\n%s", rebuilt, text)
	}
}

// c19fExplainAnalyze renders EXPLAIN ANALYZE over an already-planned tree.
func c19fExplainAnalyze(t *testing.T, ctx *Context, plan optimizer.Node) string {
	t.Helper()
	ex := &optimizer.Explain{Child: plan, Options: parser.ExplainOptions{Analyze: true}}
	op, err := Build(ex)
	if err != nil {
		t.Fatalf("build explain: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open explain: %v", err)
	}
	var sb strings.Builder
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("explain next: %v", err)
		}
		for _, r := range renderRows([]Row{slot.Row()}) {
			sb.WriteString(r)
			sb.WriteString("\n")
		}
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close explain: %v", err)
	}
	return sb.String()
}
