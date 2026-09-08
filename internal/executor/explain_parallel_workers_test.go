package executor

// EX0-03 (a)+(b) — per-worker hash counters merged into the leader map with
// PG's independent-field MAX rule, and `Workers Launched:` rendered on
// Gather/GatherMerge under EXPLAIN ANALYZE from a Context-keyed carrier.
//
// Design: docs/design/executor-ex0-03-workers/DESIGN.md §§2, 4. No timing
// claim anywhere; no assertion on per-worker row distribution (schedule-
// dependent) — only presence of the launched line and self-consistency of
// the merged leader hash line.

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestMergeWorkerContextMaxMergesHashJoinStats is the pure unit half of (a):
// two worker Contexts publishing different HashJoinStats for the same plan
// node max-merge per key into the leader, each of the 6 fields independently
// — including PG's cross-worker field-mixing quirk (explain.c:3398-3422),
// asserted here as MAX per field rather than "improved".
func TestMergeWorkerContextMaxMergesHashJoinStats(t *testing.T) {
	j1 := &optimizer.Join{}
	j2 := &optimizer.Join{}

	leader := NewContext()
	w1 := NewContext()
	w1.HashJoinStats = map[*optimizer.Join]*HashJoinStats{
		j1: {NBuckets: 1024, OrigNBuckets: 1024, NBatch: 1, OrigNBatch: 1, SpacePeak: 100, BuildTimeNs: 50},
		j2: {NBuckets: 64, OrigNBuckets: 64, NBatch: 1, OrigNBatch: 1, SpacePeak: 10, BuildTimeNs: 5},
	}
	w2 := NewContext()
	w2.HashJoinStats = map[*optimizer.Join]*HashJoinStats{
		// The quirk: this worker saw a SMALLER table (fewer buckets) but a
		// LARGER spill (more batches, higher peak). PG's independent-field
		// Max reports buckets from w1 alongside batches from w2.
		j1: {NBuckets: 512, OrigNBuckets: 512, NBatch: 8, OrigNBatch: 8, SpacePeak: 700, BuildTimeNs: 30},
	}

	MergeWorkerContext(leader, w1)
	MergeWorkerContext(leader, w2)

	got, ok := leader.HashJoinStats[j1]
	if !ok {
		t.Fatalf("leader map has no entry for the joined key after merging two workers")
	}
	want := HashJoinStats{NBuckets: 1024, OrigNBuckets: 1024, NBatch: 8, OrigNBatch: 8, SpacePeak: 700, BuildTimeNs: 50}
	if *got != want {
		t.Errorf("merged stats = %+v, want %+v (MAX per field, mixed across workers)", *got, want)
	}

	// A key only one worker touched survives verbatim.
	if got2, ok := leader.HashJoinStats[j2]; !ok || *got2 != *w1.HashJoinStats[j2] {
		t.Errorf("untouched key = %+v, want %+v", got2, *w1.HashJoinStats[j2])
	}

	// A leader value already LARGER than every worker's wins (MAX, not last).
	leader2 := NewContext()
	leader2.HashJoinStats = map[*optimizer.Join]*HashJoinStats{
		j1: {NBuckets: 4096, OrigNBuckets: 4096, NBatch: 16, OrigNBatch: 16, SpacePeak: 9000, BuildTimeNs: 500},
	}
	MergeWorkerContext(leader2, w1)
	MergeWorkerContext(leader2, w2)
	if *leader2.HashJoinStats[j1] != (HashJoinStats{NBuckets: 4096, OrigNBuckets: 4096, NBatch: 16, OrigNBatch: 16, SpacePeak: 9000, BuildTimeNs: 500}) {
		t.Errorf("leader-larger value must survive the merge, got %+v", *leader2.HashJoinStats[j1])
	}

	// A nil stats entry in the worker is skipped, not copied.
	w3 := NewContext()
	w3.HashJoinStats = map[*optimizer.Join]*HashJoinStats{j1: nil}
	MergeWorkerContext(leader, w3)
	if *leader.HashJoinStats[j1] != want {
		t.Errorf("nil worker entry must not disturb the merge, got %+v", *leader.HashJoinStats[j1])
	}

	// A worker that built nothing leaves a fresh leader map nil, and nil
	// contexts are no-ops rather than panics.
	empty := NewContext()
	MergeWorkerContext(empty, NewContext())
	if empty.HashJoinStats != nil {
		t.Errorf("merging empty worker maps must not allocate the leader map")
	}
	MergeWorkerContext(nil, w1)
	MergeWorkerContext(leader, nil)
}

// findGatherNode locates the Gather/GatherMerge MaybeAddGather inserted and
// reports its planned worker count.
func findGatherNode(n optimizer.Node) (optimizer.Node, int, bool) {
	switch g := n.(type) {
	case *optimizer.Gather:
		return g, g.WorkersPlanned, true
	case *optimizer.GatherMerge:
		return g, g.WorkersPlanned, true
	}
	for _, c := range optimizer.ParallelChildrenForTest(n) {
		if found, planned, ok := findGatherNode(c); ok {
			return found, planned, true
		}
	}
	return nil, 0, false
}

// planGatheredJoin plans sql and wraps the planner's partial subtree in a
// Gather with the given worker count, following runJoinGathered's forced
// size-gate pattern so placement itself stays under test.
func planGatheredJoin(t *testing.T, ctx *Context, sql string, workers int) optimizer.Node {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	gathered := optimizer.MaybeAddGather(node, optimizer.ParallelSettings{
		MaxWorkersPerGather: workers,
		MinTableScanBlocks:  1,
		DebugParallelQuery:  "on", // force past the size gate; fixtures are small
		BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true },
	})
	if !planTreeHasParallelNode(gathered) {
		t.Fatalf("planner declined to parallelise %q; the carrier/merge assertions need a Gather", sql)
	}
	return gathered
}

// runExplainOverChild runs an EXPLAIN statement whose inner plan is the
// caller-supplied (possibly Gather-wrapped) node, and returns the QUERY PLAN
// lines. Plain EXPLAIN never executes the inner plan; ANALYZE drains it.
func runExplainOverChild(t *testing.T, ctx *Context, explainSQL string, child optimizer.Node) []string {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(explainSQL)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ex, ok := plan.(*optimizer.Explain)
	if !ok {
		t.Fatalf("EXPLAIN did not plan to *optimizer.Explain, got %T", plan)
	}
	ex.Child = child
	op, err := Build(ex)
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
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 && r[0].Kind == KindString {
			out = append(out, r[0].StringValue())
		}
	}
	return out
}

// TestExplainAnalyzeParallelWorkersLaunchedAndHashMerge is the integration
// half of (a)+(b): a small parallel plan that forces WORKER-SIDE hash builds
// (tiny work_mem, so the leader's shared prebuild declines the share per
// parallel_hash_build.go and every worker builds privately), run under
// EXPLAIN (ANALYZE, TIMING OFF).
//
// Asserted: `Workers Launched: N` is present, the carrier holds the count
// under the Gather node, and the leader hash line matches the merged map.
func TestExplainAnalyzeParallelWorkersLaunchedAndHashMerge(t *testing.T) {
	ctx := spillFixture(t, 1200, 4000, 400)
	ctx.WorkMem = 64 << 10 // force the spill that declines the shared prebuild
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true

	gathered := planGatheredJoin(t, ctx, spillJoinSQL, 2)
	gatherNode, planned, ok := findGatherNode(gathered)
	if !ok {
		t.Fatalf("gathered plan has no Gather node")
	}
	launched := planned
	if launched > ctx.MaxParallelWorkers {
		launched = ctx.MaxParallelWorkers
	}

	lines := runExplainOverChild(t, ctx, "EXPLAIN (ANALYZE, TIMING OFF) "+spillJoinSQL, gathered)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "Gather") {
		t.Fatalf("no Gather node in ANALYZE output:\n%s", joined)
	}
	if want := fmt.Sprintf("Workers Launched: %d", launched); !strings.Contains(joined, want) {
		t.Errorf("missing %q in ANALYZE output:\n%s", want, joined)
	}
	if got, ok := ctx.GatherLaunched[gatherNode]; !ok || got != launched {
		t.Errorf("carrier GatherLaunched[%T] = %d, %v; want %d, present", gatherNode, got, ok, launched)
	}

	m := hashJoinInfoRe.FindStringSubmatch(joined)
	if m == nil {
		t.Fatalf("no hash-table line in ANALYZE output:\n%s", joined)
	}
	hs := lastHashJoinStats(t, ctx)
	if hs.NBatch < 2 {
		t.Fatalf("precondition: NBatch=%d — the spill never engaged, so the "+
			"shared prebuild was not declined and no worker-side build ran", hs.NBatch)
	}
	if m[1] != fmt.Sprint(hs.NBuckets) || m[3] != fmt.Sprint(hs.NBatch) {
		t.Errorf("leader hash line %q inconsistent with merged max (Buckets=%d Batches=%d)",
			m[0], hs.NBuckets, hs.NBatch)
	}
}

// TestExplainSerialPlanHasNoWorkersLaunched pins the negative: serial plans
// emit no `Workers Launched:` line, and neither does a plain (non-ANALYZE)
// EXPLAIN over a parallel plan — the carrier only exists for executed nodes.
func TestExplainSerialPlanHasNoWorkersLaunched(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE wl_serial (id int, pad text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO wl_serial VALUES (1, 'a'), (2, 'b'), (3, 'c')"); err != nil {
		t.Fatal(err)
	}

	serial := strings.Join(runExplainRows(t, ctx, "EXPLAIN (ANALYZE, TIMING OFF) SELECT * FROM wl_serial"), "\n")
	if strings.Contains(serial, "Workers Launched:") {
		t.Errorf("serial ANALYZE plan must not render Workers Launched::\n%s", serial)
	}

	// Plain EXPLAIN over a Gather plan: never executed, so no carrier entry.
	ctx2 := spillFixture(t, 50, 200, 40)
	gathered := planGatheredJoin(t, ctx2, spillJoinSQL, 2)
	plain := strings.Join(runExplainOverChild(t, ctx2, "EXPLAIN "+spillJoinSQL, gathered), "\n")
	if strings.Contains(plain, "Workers Launched:") {
		t.Errorf("plain EXPLAIN must not render Workers Launched: (nothing executed):\n%s", plain)
	}
	if !strings.Contains(plain, "Gather") {
		t.Errorf("precondition: plain EXPLAIN lost the Gather node:\n%s", plain)
	}
}

// TestFoldGatherWorkerStatsCopiesFourFields is the deterministic unit half
// of EX0-03b (design §4): synthetic per-site tables fold post-join into the
// Context-keyed carrier with exact Worker/rows/loops/time contents.
//
// The fold copies ONLY rowsOut/loops/startupNs/totalNs (B4 collection
// invariant) — the source stats carry buffer/WAL/memory noise to prove it
// travels nowhere (workerNodeStat has no such fields by construction).
// Nil slots (a site that never built) and nil entries contribute nothing,
// and a nil Context is a no-op.
func TestFoldGatherWorkerStatsCopiesFourFields(t *testing.T) {
	n1 := &optimizer.SeqScan{}
	n2 := &optimizer.SeqScan{}
	mk := func(rows, loops, startup, total int64) *nodeStats {
		return &nodeStats{
			rowsOut: rows, loops: loops, startupNs: startup, totalNs: total,
			bufHit: 7, bufRead: 8, walRecords: 9, walBytes: 10,
			memAllocated: 11, memPeak: 12,
		}
	}
	w0 := nodeStatsTable{n1: mk(10, 1, 100, 200), n2: mk(5, 2, 11, 22)}
	w1 := nodeStatsTable{n1: mk(30, 1, 300, 400)}
	var nilSlot nodeStatsTable
	leader := nodeStatsTable{n1: mk(4, 1, 40, 50), n2: nil}

	ctx := NewContext()
	foldGatherWorkerStats(ctx, []nodeStatsTable{w0, w1, nilSlot, leader})

	wantN1 := []workerNodeStat{
		{Worker: 0, RowsOut: 10, Loops: 1, StartupNs: 100, TotalNs: 200},
		{Worker: 1, RowsOut: 30, Loops: 1, StartupNs: 300, TotalNs: 400},
		{Worker: 3, RowsOut: 4, Loops: 1, StartupNs: 40, TotalNs: 50},
	}
	if got := ctx.GatherWorkerStats[n1]; !reflect.DeepEqual(got, wantN1) {
		t.Errorf("carrier[n1] = %+v, want %+v", got, wantN1)
	}
	wantN2 := []workerNodeStat{
		{Worker: 0, RowsOut: 5, Loops: 2, StartupNs: 11, TotalNs: 22},
	}
	if got := ctx.GatherWorkerStats[n2]; !reflect.DeepEqual(got, wantN2) {
		t.Errorf("carrier[n2] = %+v, want %+v", got, wantN2)
	}
	if len(ctx.GatherWorkerStats) != 2 {
		t.Errorf("carrier holds %d keys, want 2 (nil slot/entry must add none)", len(ctx.GatherWorkerStats))
	}

	// All-nil slots leave the carrier nil, and a nil Context is a no-op
	// rather than a panic.
	empty := NewContext()
	foldGatherWorkerStats(empty, []nodeStatsTable{nil, nil})
	if empty.GatherWorkerStats != nil {
		t.Errorf("folding nil slots must not allocate the carrier, got %+v", empty.GatherWorkerStats)
	}
	foldGatherWorkerStats(nil, []nodeStatsTable{w0})
}

// workerLineRe matches a rendered per-worker line without matching
// `Workers Planned:` / `Workers Launched:` (`Worker` followed by `s`).
var workerLineRe = regexp.MustCompile(`Worker \d+:`)

// workerShapeRes normalize the schedule-dependent numbers (per-worker
// rows=, actual time= ranges, summary times) so two runs can be compared
// for SHAPE identity. Loops and line structure stay literal.
var workerShapeRes = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`rows=\d+\.\d+`), "rows=N"},
	{regexp.MustCompile(`actual time=\d+\.\d+\.\.\d+\.\d+`), "actual time=T..T"},
	{regexp.MustCompile(`(Planning Time|Execution Time): \d+\.\d+ ms`), "$1: M ms"},
}

func workerShape(lines []string) string {
	s := strings.Join(lines, "\n")
	for _, nr := range workerShapeRes {
		s = nr.re.ReplaceAllString(s, nr.repl)
	}
	return s
}

// TestExplainAnalyzeParallelWorkerLinesShape is the golden half of EX0-03b
// (design §4): a forced small parallel plan (single SeqScan under Gather,
// so exactly one visited inner node carries entries) run under EXPLAIN
// (ANALYZE, TIMING OFF) twice.
//
// Asserted on BOTH runs: rendered `Worker \d+:` line count == launched
// count, with `Worker 0:` … `Worker N-1:` each present, and identical
// SHAPE. Deliberately NOT asserted: exact per-worker rows= (schedule-
// dependent). Leader participation is off so the leader builds no table —
// the count pins workers only.
func TestExplainAnalyzeParallelWorkerLinesShape(t *testing.T) {
	ctx := spillFixture(t, 200, 200, 40)
	ctx.WorkMem = 0
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = false

	const scanSQL = `SELECT * FROM sp_probe`
	gathered := planGatheredJoin(t, ctx, scanSQL, 2)
	gatherNode, planned, ok := findGatherNode(gathered)
	if !ok {
		t.Fatalf("gathered plan has no Gather node")
	}
	launched := planned
	if launched > ctx.MaxParallelWorkers {
		launched = ctx.MaxParallelWorkers
	}
	if launched <= 0 {
		t.Fatalf("precondition: launched=%d — the count assertion needs workers", launched)
	}

	run := func() []string {
		// Production hands every statement a fresh Context; the reused
		// test context must drop the previous run's fold (the fold
		// appends — same precedent as ctx.HashJoinStats = nil in the
		// spill-identity test).
		ctx.GatherWorkerStats = nil
		return runExplainOverChild(t, ctx, "EXPLAIN (ANALYZE, TIMING OFF) "+scanSQL, gathered)
	}

	for runIdx := 0; runIdx < 2; runIdx++ {
		lines := run()
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "Gather") {
			t.Fatalf("run %d: no Gather node in ANALYZE output:\n%s", runIdx, joined)
		}
		if want := fmt.Sprintf("Workers Launched: %d", launched); !strings.Contains(joined, want) {
			t.Fatalf("run %d: missing %q in ANALYZE output:\n%s", runIdx, want, joined)
		}
		if got := len(workerLineRe.FindAllString(joined, -1)); got != launched {
			t.Errorf("run %d: %d Worker lines, want launched count %d:\n%s", runIdx, got, launched, joined)
		}
		for w := 0; w < launched; w++ {
			if want := fmt.Sprintf("Worker %d:", w); !strings.Contains(joined, want) {
				t.Errorf("run %d: missing %q in ANALYZE output:\n%s", runIdx, want, joined)
			}
		}
		if got, ok := ctx.GatherLaunched[gatherNode]; !ok || got != launched {
			t.Errorf("run %d: carrier GatherLaunched = %d, %v; want %d, present", runIdx, got, ok, launched)
		}
	}

	// SHAPE identity across reruns: reset, run twice more, compare the
	// normalized outputs for byte equality.
	first := workerShape(run())
	second := workerShape(run())
	if first != second {
		t.Errorf("worker-line SHAPE differs between runs:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestExplainAnalyzeEmptyWorkerCarrierByteIdentical is the item-class pin
// for EX0-03b (design §4): the walker change is unreachable when the
// carrier is empty, so a render with a nil carrier and one with an empty
// (but non-nil) carrier — even beside a populated stats table — must be
// byte-identical. Serial plans (no Gather ever folds) take the same path.
func TestExplainAnalyzeEmptyWorkerCarrierByteIdentical(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	root := optimizer.MaybeAddGather(&optimizer.SeqScan{Table: tbl}, parallelLabelTestSettings())
	g, ok := root.(*optimizer.Gather)
	if !ok {
		t.Fatalf("expected a Gather at the root, got %T", root)
	}
	stats := nodeStatsTable{g.Child: &nodeStats{rowsOut: 5, loops: 1}}
	render := func(carrier workerNodeStatsTable) string {
		var rows []Row
		walkPlanAnalyzeFiltered(root, 0, &rows, parser.ExplainOptions{Analyze: true},
			stats, nil, nil, nil, nil, carrier, nil, nil, 0,
			&subPlanReg{rel: newExplainNames(root), cte: collectCTEHoist(root)})
		return joinRowText(rows)
	}
	if nilOut, emptyOut := render(nil), render(workerNodeStatsTable{}); nilOut != emptyOut {
		t.Errorf("empty-carrier render differs:\n--- nil ---\n%s\n--- empty ---\n%s", nilOut, emptyOut)
	}
}
