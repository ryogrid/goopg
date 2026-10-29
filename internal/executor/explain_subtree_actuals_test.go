package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplainAnalyzeCTEBodyAndSubPlanActuals pins M0122-0021 slice 3: the
// subtrees goopg builds outside the top-level operator tree report their own
// (actual …), as PG's ExplainNode does for every plan node (explain.c recurses
// into initPlan / subPlan lists and CTE subplans, each with its own
// Instrumentation). Before the fix none of these lines carried actuals:
//
//   - a materialized CTE's body, built by cteScanOp with Build() (no scope).
//     Two references share the body node; the replaying one never opens it,
//     so the body still reports exactly one loop;
//   - an InitPlan's inner plan, built lazily by acquireSubPlanOp during
//     execution, long after the EXPLAIN build — one loop;
//   - a correlated SubPlan's inner plan — one loop per call (3 outer rows).
func TestExplainAnalyzeCTEBodyAndSubPlanActuals(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	cases := []struct {
		name, sql string
		want      []string
	}{
		{
			name: "materialized CTE referenced twice",
			sql:  "WITH c AS MATERIALIZED (SELECT k FROM mo WHERE pad > 5) SELECT count(*) FROM c a JOIN c b ON a.k = b.k",
			want: []string{"Seq Scan on mo (actual rows=34.00 loops=1)", "Rows Removed by Filter: 6"},
		},
		{
			name: "InitPlan",
			sql:  "SELECT count(*) FROM mo WHERE k > (SELECT avg(id) FROM mi)",
			want: []string{"->  Aggregate (actual rows=1.00 loops=1)", "Seq Scan on mi (actual rows=10.00 loops=1)"},
		},
		{
			// An empty outer never reaches the inner: PG prints
			// ` (never executed)` for a node with nloops == 0 (explain.c
			// ExplainNode), not `(actual rows=0.00 loops=0)`.
			name: "inner never executed",
			sql:  "SELECT count(*), sum(mi.v) FROM mo JOIN mi ON mi.id = mo.k WHERE mo.pad < 0",
			want: []string{"Memoize (never executed)", "Index Scan using mi_pkey on mi (never executed)"},
		},
		{
			name: "correlated SubPlan",
			sql:  "SELECT k, (SELECT v FROM mi WHERE mi.id = mo.k) FROM mo WHERE pad < 3",
			want: []string{"Index Scan using mi_pkey on mi (actual rows=1.00 loops=3)"},
		},
	}
	for _, tc := range cases {
		out := strings.Join(runExplainRows(t, ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) "+tc.sql), "\n")
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q:\n%s", tc.name, w, out)
			}
		}
	}
}

// TestExplainAnalyzeCoopBuildInputActuals pins the cooperative-build half of
// M0122-0021 slice 3: a hash join whose build goopg splits across producer
// goroutines (E-18) reports its build input as PG reports a serial build —
// one loop carrying every row. Before the fold the input printed
// `(actual rows=0.00 loops=0)`: the tree buildNode made for it is never
// opened and the producers built theirs uninstrumented.
func TestExplainAnalyzeCoopBuildInputActuals(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE cb_dim (dk int, dname text)`)
	runSQL(t, ctx, `CREATE TABLE cb_fact (fid int, fk int)`)
	runSQL(t, ctx, `INSERT INTO cb_dim SELECT x, 'name-' || x FROM generate_series(1, 3000) x`)
	runSQL(t, ctx, `INSERT INTO cb_fact SELECT x, (x * 7) % 3300 FROM generate_series(1, 12000) x`)
	runSQL(t, ctx, `ANALYZE cb_dim`)
	runSQL(t, ctx, `ANALYZE cb_fact`)
	ctx.MinParallelTableScanBlocks = 0
	ctx.MaxParallelWorkers = 4

	before := coopBorrowedProducers.Load()
	var lines []string
	for _, r := range runSQL(t, ctx, `EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) SELECT f.fid FROM cb_fact f JOIN cb_dim d ON f.fk = d.dk`) {
		lines = append(lines, r[0].StringValue())
	}
	out := strings.Join(lines, "\n")
	if coopBorrowedProducers.Load() == before {
		t.Skipf("precondition: the join did not take the cooperative build:\n%s", out)
	}
	if !strings.Contains(out, "Seq Scan on cb_dim d (actual rows=3000.00 loops=1)") {
		t.Errorf("cooperative build input does not report one loop of 3000 rows:\n%s", out)
	}
	if strings.Contains(out, "loops=0)") {
		t.Errorf("a node still reports loops=0:\n%s", out)
	}
}

// TestExplainAnalyzeExpressionSubqueryActuals pins M0122-0021 residual 2: the
// subqueries goopg evaluates from inside expression evaluation report their
// subtree's (actual …), as PG's do (every SubPlan / InitPlan has its own
// Instrumentation, explain.c ExplainSubPlans). Before the fix these sites
// built the inner plan with Build() — no instrumentation scope — and the
// subtree printed bare:
//
//   - ARRAY(SELECT …), correlated and not (evalArraySubquery). The
//     correlated line is byte-identical to PG 18.3's;
//   - the correlated value hash map (buildCorrSubqHashMap). goopg scans the
//     inner table once and probes the map per outer row, so the scan reports
//     one loop carrying every row; PG re-runs the SubPlan per call (loops=12,
//     Rows Removed by Filter: 10). That is an execution difference, recorded
//     in the deferral ledger, not a rendering one.
func TestExplainAnalyzeExpressionSubqueryActuals(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	cases := []struct {
		name, sql string
		want      []string
	}{
		{
			name: "correlated ARRAY()",
			sql:  "SELECT k, ARRAY(SELECT v FROM mi WHERE mi.id = mo.k) FROM mo WHERE pad < 3",
			want: []string{"Index Scan using mi_pkey on mi (actual rows=1.00 loops=3)"},
		},
		{
			name: "uncorrelated ARRAY()",
			sql:  "SELECT ARRAY(SELECT v FROM mi WHERE v > 500)",
			want: []string{"Seq Scan on mi (actual rows=5.00 loops=1)", "Rows Removed by Filter: 5"},
		},
		{
			name: "correlated value hash map",
			sql:  "SELECT k, (SELECT id FROM mi WHERE mi.v = mo.pad) FROM mo WHERE pad < 12",
			want: []string{"Seq Scan on mi (actual rows=10.00 loops=1)"},
		},
	}
	for _, tc := range cases {
		out := strings.Join(runExplainRows(t, ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) "+tc.sql), "\n")
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q:\n%s", tc.name, w, out)
			}
		}
	}
}

// TestExplainAnalyzeBitmapProducerActuals pins the bitmap half of M0122-0021
// residual 2: Bitmap Index Scan (and BitmapAnd / BitmapOr above it) report
// actuals as PG's do. PG runs these through MultiExecProcNode — one loop per
// call, rows = the TIDs the index scan added (MultiExecBitmapIndexScan's
// InstrStopNode(…, nTuples)); 0 for BitmapAnd / BitmapOr. goopg built the
// producer subtree inside the heap scan's Open with no scope, so it printed
// no actuals anywhere: serially, as a nested-loop probe, and below a Gather,
// where the leader builds the shared bitmap once (prebuildBitmap) and PG's
// parallel bitmap heap scan likewise runs the index scan once.
func TestExplainAnalyzeBitmapProducerActuals(t *testing.T) {
	ctx := spillFixture(t, 2000, 10, 400)
	if err := runDDL(t, ctx, "CREATE INDEX sp_probe_k ON sp_probe (k)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	const sql = "SELECT pad FROM sp_probe WHERE k = 7"
	const want = "Bitmap Index Scan on sp_probe_k (actual rows=5.00 loops=1)"
	explain := "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) " + sql

	serial := strings.Join(runExplainOverChild(t, ctx, explain, planBitmapForced(t, ctx, sql)), "\n")
	if !strings.Contains(serial, want) {
		t.Errorf("serial: missing %q:\n%s", want, serial)
	}

	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	ctx.GatherWorkerStats = nil
	gathered := &optimizer.Gather{Child: planBitmapForced(t, ctx, sql), WorkersPlanned: 2}
	lines := runExplainOverChild(t, ctx, explain, gathered)
	par := strings.Join(lines, "\n")
	if !strings.Contains(par, "Gather") || !strings.Contains(par, want) {
		t.Errorf("below a Gather: missing %q:\n%s", want, par)
	}
	if strings.Contains(par, "loops=0)") {
		t.Errorf("a node reports loops=0:\n%s", par)
	}
}

// TestExplainAnalyzeProbeCondRowsRemoved pins M0122-0021 residual 4: a
// nested-loop index probe that evaluates its own Cond (the relation's local
// quals, IndexScan.Cond) reports the tuples it rejected as `Rows Removed by
// Filter`, per loop with PG's %.0f rounding — PG keeps the count as the scan
// node's nfiltered1 (execScan.h ExecScan, InstrCountFiltered1;
// explain.c show_instrumentation_count). indexScanOp rejected with a bare
// `continue` and counted nothing, so the line was missing. Here 3 of the 10
// probes are rejected: 3 / 10 loops prints `0`, exactly as PG 18.3 prints for
// the same data. The index-only and bitmap heap probes count the same way.
func TestExplainAnalyzeProbeCondRowsRemoved(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) SELECT count(*) FROM mo JOIN mi ON mi.id = mo.k AND mi.v > 300")
	out := strings.Join(lines, "\n")
	want := []string{
		"->  Index Scan using mi_pkey on mi (actual rows=0.70 loops=10)",
		"Filter: (v > 300)",
		"Rows Removed by Filter: 0",
	}
	at := -1
	for i, l := range lines {
		if strings.Contains(l, want[0]) {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("no memoized index probe in:\n%s", out)
	}
	for i, w := range want[1:] {
		got := ""
		if at+2+i < len(lines) {
			got = strings.TrimSpace(lines[at+2+i])
		}
		if got != w {
			t.Errorf("probe detail line %d = %q, want %q:\n%s", i+2, got, w, out)
		}
	}
}

// TestExplainAnalyzeBitmapHeapBlocks pins M0122-0021's `Heap Blocks:` line:
// PG prints the pages a Bitmap Heap Scan fetched, split exact / lossy
// (explain.c show_tidbitmap_info from BitmapHeapScanState.stats, counted once
// per page in heapam_handler.c BitmapHeapScanNextBlock). goopg counted the
// pages already (exactPages / lossyPages) but nothing reported them. Below a
// Gather the main line is the leader's own pages and each worker that fetched
// one prints a `Worker N:  Heap Blocks:` line, so the main line plus the
// worker lines account for the serial scan's pages exactly.
func TestExplainAnalyzeBitmapHeapBlocks(t *testing.T) {
	ctx := spillFixture(t, 2000, 10, 400)
	if err := runDDL(t, ctx, "CREATE INDEX sp_probe_k ON sp_probe (k)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	const sql = "SELECT pad FROM sp_probe WHERE k < 40"
	explain := "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) " + sql
	exactPages := func(lines []string, workerLines bool) (sum int64, n int) {
		for _, l := range lines {
			l = strings.TrimSpace(l)
			var w, e int64
			if workerLines {
				if c, _ := fmt.Sscanf(l, "Worker %d:  Heap Blocks: exact=%d", &w, &e); c == 2 {
					sum += e
					n++
				}
			} else if c, _ := fmt.Sscanf(l, "Heap Blocks: exact=%d", &e); c == 1 {
				sum += e
				n++
			}
		}
		return sum, n
	}

	serialLines := runExplainOverChild(t, ctx, explain, planBitmapForced(t, ctx, sql))
	serial, n := exactPages(serialLines, false)
	if n != 1 || serial <= 1 {
		t.Fatalf("serial: want one `Heap Blocks: exact=N` line over several pages, got %d line(s) totalling %d:\n%s",
			n, serial, strings.Join(serialLines, "\n"))
	}

	ctx.MaxParallelWorkers = 8
	for _, leaderRuns := range []bool{true, false} {
		ctx.ParallelLeaderParticipation = leaderRuns
		ctx.GatherWorkerStats = nil
		parLines := runExplainOverChild(t, ctx, explain, &optimizer.Gather{Child: planBitmapForced(t, ctx, sql), WorkersPlanned: 2})
		out := strings.Join(parLines, "\n")
		leader, leaderLines := exactPages(parLines, false)
		workers, workerLines := exactPages(parLines, true)
		if leader+workers != serial {
			t.Errorf("leader participation %v: the leader line (%d) plus worker lines (%d) must equal the serial pages (%d):\n%s",
				leaderRuns, leader, workers, serial, out)
		}
		// Without the leader every page is a worker's: PG's leader stats
		// stay zero, so it prints no main line, only worker lines.
		if !leaderRuns && (leaderLines != 0 || workerLines == 0) {
			t.Errorf("leader participation off: want only `Worker N:  Heap Blocks:` lines, got %d main / %d worker:\n%s",
				leaderLines, workerLines, out)
		}
	}
}

// TestExplainUncorrelatedArraySublinkIsInitPlan pins M0122-0021 residual 9:
// an uncorrelated ARRAY(SELECT …) is PG's initplan (subselect.c make_subplan
// → SS_make_initplan_from_plan), not a per-row SubPlan:
//
//   - it prints `InitPlan 1` at its query level's top node, before the
//     children, and the reference deparses as `(InitPlan 1).col1` — the
//     plain EXPLAIN below is PG 18.3's text line for line. goopg printed
//     `ARRAY(SubPlan 1)` under the scan, because NodeSublinks' walker never
//     descended into the `= ANY (…)` operand, so the level never saw the
//     sublink;
//   - it runs once: under ANALYZE the subtree reports loops=1 over three
//     outer rows (goopg re-ran it per row, loops=3).
func TestExplainUncorrelatedArraySublinkIsInitPlan(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	got := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT count(*) FROM mo WHERE k = ANY(ARRAY(SELECT id FROM mi WHERE v > 500)) AND pad > 2")
	want := []string{
		"Aggregate",
		"  InitPlan 1",
		"    ->  Seq Scan on mi",
		"          Filter: (v > 500)",
		"  ->  Seq Scan on mo",
		"        Filter: ((pad > 2) AND (k = ANY ((InitPlan 1).col1)))",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("plain EXPLAIN:\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	out := strings.Join(runExplainRows(t, ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) SELECT k, ARRAY(SELECT v FROM mi WHERE v > 500) FROM mo WHERE pad < 3"), "\n")
	for _, w := range []string{"InitPlan 1", "->  Seq Scan on mi (actual rows=5.00 loops=1)"} {
		if !strings.Contains(out, w) {
			t.Errorf("ANALYZE: missing %q:\n%s", w, out)
		}
	}
}

// TestExplainAnalyzeBitmapIndexRecheckRemoved pins M0122-0021 residual 14: a
// lossy bitmap page is walked whole and every tuple rechecked against the
// original index qual; the ones the recheck rejects are PG's nfiltered2 on the
// Bitmap Heap Scan, printed as `Rows Removed by Index Recheck` directly under
// `Recheck Cond:` and before the Filter / Heap Blocks lines (explain.c
// T_BitmapHeapScan arm; nodeBitmapHeapscan.c InstrCountFiltered2). goopg
// rejected them with a bare return and printed nothing. A tiny work_mem forces
// every page lossy, so the scan rechecks whole pages and rejects every
// non-matching tuple on them.
func TestExplainAnalyzeBitmapIndexRecheckRemoved(t *testing.T) {
	ctx := spillFixture(t, 2000, 10, 400)
	if err := runDDL(t, ctx, "CREATE INDEX sp_probe_k ON sp_probe (k)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	ctx.WorkMem = 1 // smallest bitmap: every page lossy
	const sql = "SELECT pad FROM sp_probe WHERE k < 40"
	lines := runExplainOverChild(t, ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) "+sql, planBitmapForced(t, ctx, sql))
	out := strings.Join(lines, "\n")
	at := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "Recheck Cond:") {
			at = i
		}
	}
	if at < 0 || at+1 >= len(lines) {
		t.Fatalf("no Recheck Cond line:\n%s", out)
	}
	var removed int64
	if n, _ := fmt.Sscanf(strings.TrimSpace(lines[at+1]), "Rows Removed by Index Recheck: %d", &removed); n != 1 || removed <= 0 {
		t.Errorf("the line after Recheck Cond is %q, want a positive `Rows Removed by Index Recheck`:\n%s", strings.TrimSpace(lines[at+1]), out)
	}
	if !strings.Contains(out, "Heap Blocks:") || !strings.Contains(out, " lossy=") {
		t.Errorf("work_mem=1 should make the bitmap lossy:\n%s", out)
	}
	if !strings.Contains(out, "(actual rows=200.00 loops=1)") {
		t.Errorf("the recheck must still return exactly the 200 matching rows:\n%s", out)
	}
}
