package executor

// EX0-03c — minimal sortOp method/space counters + per-worker lines.
//
// Design: docs/design/executor-ex0-03c-sort/DESIGN.md §§1–4. No timing
// claim anywhere; no assertion on per-worker row distribution
// (schedule-dependent) — only presence of the main line and the
// launched worker-line count, mirroring EX0-03b's shape discipline.
//
// Harness reuse (same package, no edits to EX0-03b's file): spillFixture
// for table fixtures, planGatheredJoin/runExplainOverChild for forced
// parallel plans, TIMING OFF throughout.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// sortWorkerMethodRe matches only the sort per-worker lines (not EX0-03b's
// `Worker N:  actual time=` lines, and not `Workers Planned/Launched:`).
var sortWorkerMethodRe = regexp.MustCompile(`Worker \d+:  Sort Method:`)

// splitSortMethodLines partitions rendered EXPLAIN lines into the Sort
// main line(s) and the sort per-worker lines.
func splitSortMethodLines(lines []string) (main, workers []string) {
	for _, l := range lines {
		if !strings.Contains(l, "Sort Method:") {
			continue
		}
		if strings.Contains(l, "Worker") {
			workers = append(workers, l)
		} else {
			main = append(main, l)
		}
	}
	return main, workers
}

// TestExplainAnalyzeSerialSortReportsMethod is the serial golden (§4): a
// serial sort under EXPLAIN (ANALYZE, TIMING OFF) renders exactly one main
// `Sort Method:` line and no worker lines.
func TestExplainAnalyzeSerialSortReportsMethod(t *testing.T) {
	ctx := spillFixture(t, 200, 200, 40)

	const sortSQL = `SELECT k, pad FROM sp_probe ORDER BY k`
	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, TIMING OFF) "+sortSQL)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Sort Key:") {
		t.Fatalf("precondition: no Sort node in ANALYZE output:\n%s", joined)
	}
	main, workers := splitSortMethodLines(lines)
	if len(main) != 1 {
		t.Errorf("want exactly one Sort main line, got %d:\n%s", len(main), joined)
	}
	if len(workers) != 0 {
		t.Errorf("serial plan must render no sort worker lines, got %d:\n%s", len(workers), joined)
	}
	if len(main) == 1 {
		m := regexp.MustCompile(`Sort Method: (quicksort|external merge)  (Memory|Disk): (\d+)kB`).FindStringSubmatch(main[0])
		if m == nil {
			t.Errorf("main line %q is not PG's `Sort Method: %%s  %%s: %%dkB` form", main[0])
		} else if m[1] != "quicksort" || m[2] != "Memory" {
			t.Errorf("unbounded serial sort reports %q %q, want quicksort Memory:\n%s", m[1], m[2], joined)
		}
	}
	if got := len(workerLineRe.FindAllString(joined, -1)); got != 0 {
		t.Errorf("serial plan must render no `Worker N:` lines at all, got %d:\n%s", got, joined)
	}
}

// TestExplainAnalyzeParallelSortWorkerLines is the parallel golden (§4): a
// forced small parallel sort (Sort under Gather, so per-worker by
// construction) renders one main line plus exactly launched worker lines.
// Leader participation is off, so the main line is worker 0's promotion.
// Deliberately NOT asserted: exact per-worker SpaceKB (schedule-dependent).
func TestExplainAnalyzeParallelSortWorkerLines(t *testing.T) {
	ctx := spillFixture(t, 200, 200, 40)
	ctx.WorkMem = 0
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = false

	const sortSQL = `SELECT k, pad FROM sp_probe ORDER BY k`
	gathered := planGatheredJoin(t, ctx, sortSQL, 2)
	_, planned, ok := findGatherNode(gathered)
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

	lines := runExplainOverChild(t, ctx, "EXPLAIN (ANALYZE, TIMING OFF) "+sortSQL, gathered)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Sort Key:") {
		t.Fatalf("precondition: no Sort node in ANALYZE output:\n%s", joined)
	}
	main, workers := splitSortMethodLines(lines)
	if len(main) != 1 {
		t.Fatalf("want exactly one Sort main line (worker-0 promotion), got %d:\n%s", len(main), joined)
	}
	if len(workers) != launched {
		t.Errorf("want %d sort worker lines, got %d:\n%s", launched, len(workers), joined)
	}
	for w := 0; w < launched; w++ {
		if want := fmt.Sprintf("Worker %d:  Sort Method:", w); !strings.Contains(joined, want) {
			t.Errorf("missing %q in ANALYZE output:\n%s", want, joined)
		}
	}
	// The promoted main line carries worker 0's stats verbatim.
	mainStat := strings.TrimSpace(main[0])
	var w0 string
	for _, l := range workers {
		if strings.Contains(l, "Worker 0:") {
			w0 = l
		}
	}
	if w0 == "" {
		t.Fatalf("no Worker 0 sort line:\n%s", joined)
	}
	if !strings.HasSuffix(strings.TrimSpace(w0), mainStat) {
		t.Errorf("promoted main line %q != Worker 0 stats %q", mainStat, strings.TrimSpace(w0))
	}
}

// TestSortPeakBytesRescanReset is the rescan-reset unit half (§4): Open
// publishes a fresh peak, and a second Open discards an arbitrarily stale
// peak rather than max-merging with it.
func TestSortPeakBytesRescanReset(t *testing.T) {
	ctx, cleanup := resetFixture(t)
	defer cleanup()

	op := buildFor(t, ctx, "SELECT a FROM reset_t ORDER BY a DESC")
	srt, _ := findOp(op, func(o Operator) bool { _, ok := o.(*sortOp); return ok }).(*sortOp)
	if srt == nil {
		t.Fatalf("no sortOp found in the built tree")
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = drainAll(t, ctx, op)
	first := srt.peakBytes
	if first <= 0 {
		t.Fatalf("precondition: peakBytes=%d after draining 5 rows", first)
	}
	st, ok := ctx.SortStats[srt.plan]
	if !ok {
		t.Fatalf("Open published no SortStats entry for its plan node")
	}
	if st.Method != "quicksort" || st.SpaceType != "Memory" {
		t.Errorf("in-memory publish = %+v, want quicksort/Memory", st)
	}
	if want := (first + 1023) / 1024; st.SpaceKB != want {
		t.Errorf("published SpaceKB=%d, want peak-derived %d", st.SpaceKB, want)
	}

	// Simulate a stale max surviving from a previous Open, then rescan
	// without Close (the Stage-9 bare re-Open shape): the reset at Open
	// start must discard it.
	srt.peakBytes = 1 << 60
	if err := op.Open(ctx); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if srt.peakBytes != first {
		t.Errorf("re-Open peakBytes=%d, want fresh-run peak %d (stale max leaked)", srt.peakBytes, first)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSortFailedOpenPublishesNothing pins §1's failure half: an Open that
// never reaches the publish point leaves the ctx map untouched.
func TestSortFailedOpenPublishesNothing(t *testing.T) {
	plan := &optimizer.Sort{}
	op := newSortOp(plan, &errChild{})
	ctx := NewContext()
	if err := op.Open(ctx); err == nil {
		t.Fatalf("precondition: erroring child did not fail Open")
	}
	if len(ctx.SortStats) != 0 {
		t.Errorf("failed Open published %d SortStats entries, want none", len(ctx.SortStats))
	}
}

// errChild is an Operator stub whose Open always fails.
type errChild struct{}

func (o *errChild) Schema() optimizer.Schema { return nil }
func (o *errChild) Open(*Context) error      { return fmt.Errorf("boom") }
func (o *errChild) Next() (TupleSlot, error) { return nil, EOF }
func (o *errChild) Close() error             { return nil }

// TestSortSpillPublishesExternalMerge pins the spill branch of §1: a sort
// forced through flushChunk publishes `external merge`/`Disk` with a
// positive on-disk KiB sum (best-effort os.Stat over the spill files).
func TestSortSpillPublishesExternalMerge(t *testing.T) {
	rows := make([]Row, 0, 64)
	for i := int64(63); i >= 0; i-- {
		rows = append(rows, Row{NewIntDatum(i)})
	}
	plan := &optimizer.Sort{Keys: []optimizer.SortKey{{Expr: &optimizer.ColumnRef{Index: 0}}}}
	s := &sortOp{plan: plan, child: &fakeBorrowSource{rows: rows}, keys: plan.Keys, chunkLimitBytes: 1024}
	ctx := NewContext()
	if err := s.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if len(s.spillFiles) == 0 {
		t.Fatalf("precondition: no spill with chunkLimitBytes=1024 over 64 rows")
	}
	st, ok := ctx.SortStats[plan]
	if !ok {
		t.Fatalf("spilling Open published no SortStats entry")
	}
	if st.Method != "external merge" || st.SpaceType != "Disk" {
		t.Errorf("spill publish = %+v, want external merge/Disk", st)
	}
	if st.SpaceKB <= 0 {
		t.Errorf("spill SpaceKB=%d, want the positive on-disk sum", st.SpaceKB)
	}
}

// TestSortWorkerZeroPromotion is the promotion unit half (§4): leader
// absent + workers present renders worker 0's stats as the main line,
// plus one `Worker i:` line per carrier entry in slot order (the input is
// deliberately scrambled to prove the render-side sort).
func TestSortWorkerZeroPromotion(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	sortNode := &optimizer.Sort{Child: &optimizer.SeqScan{Table: tbl}}
	reg := &subPlanReg{rel: newExplainNames(sortNode), cte: collectCTEHoist(sortNode)}
	reg.sortWorkers = map[*optimizer.Sort][]SortStat{
		sortNode: {
			{Method: "quicksort", SpaceType: "Memory", SpaceKB: 42, Worker: 1},
			{Method: "quicksort", SpaceType: "Memory", SpaceKB: 43, Worker: 0},
		},
	}
	var rows []Row
	walkPlanAnalyzeFiltered(sortNode, 0, &rows, parser.ExplainOptions{Analyze: true},
		nil, nil, nil, nil, nil, nil, nil, nil, 0, reg)
	joined := joinRowText(rows)

	// Main line: worker 0's stats, with no Worker prefix.
	var main string
	for _, l := range strings.Split(joined, "\n") {
		if strings.Contains(l, "Sort Method:") && !strings.Contains(l, "Worker") {
			main = strings.TrimSpace(l)
		}
	}
	if want := "Sort Method: quicksort  Memory: 43kB"; main != want {
		t.Errorf("promoted main line = %q, want %q:\n%s", main, want, joined)
	}
	w0, w1 := "Worker 0:  Sort Method: quicksort  Memory: 43kB", "Worker 1:  Sort Method: quicksort  Memory: 42kB"
	if !strings.Contains(joined, w0) || !strings.Contains(joined, w1) {
		t.Errorf("missing per-worker lines %q / %q:\n%s", w0, w1, joined)
	}
	if strings.Index(joined, w0) > strings.Index(joined, w1) {
		t.Errorf("worker lines out of slot order:\n%s", joined)
	}
}

// TestExplainAnalyzeEmptySortCarrierByteIdentical is the item-class pin for
// EX0-03c (§4): the Sort arm is unreachable when both sort carriers are
// empty, so a render with nil carriers and one with empty (but non-nil)
// carriers — beside a populated stats table — must be byte-identical. The
// plain (non-ANALYZE) walker must never emit the ANALYZE-only line.
func TestExplainAnalyzeEmptySortCarrierByteIdentical(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	sortNode := &optimizer.Sort{Child: &optimizer.SeqScan{Table: tbl}}
	stats := nodeStatsTable{sortNode: &nodeStats{rowsOut: 5, loops: 1}}
	render := func(ss map[*optimizer.Sort]SortStat, sw map[*optimizer.Sort][]SortStat) string {
		var rows []Row
		reg := &subPlanReg{rel: newExplainNames(sortNode), cte: collectCTEHoist(sortNode)}
		reg.sortStats, reg.sortWorkers = ss, sw
		walkPlanAnalyzeFiltered(sortNode, 0, &rows, parser.ExplainOptions{Analyze: true},
			stats, nil, nil, nil, nil, nil, nil, nil, 0, reg)
		return joinRowText(rows)
	}
	nilOut := render(nil, nil)
	emptyOut := render(map[*optimizer.Sort]SortStat{}, map[*optimizer.Sort][]SortStat{})
	if nilOut != emptyOut {
		t.Errorf("empty-carrier render differs:\n--- nil ---\n%s\n--- empty ---\n%s", nilOut, emptyOut)
	}
	if strings.Contains(nilOut, "Sort Method:") {
		t.Errorf("carrier-less ANALYZE render must show no Sort Method line:\n%s", nilOut)
	}

	var plain []Row
	walkPlanFiltered(sortNode, 0, &plain, parser.ExplainOptions{}, nil, nil,
		&subPlanReg{rel: newExplainNames(sortNode), cte: collectCTEHoist(sortNode)})
	if out := joinRowText(plain); strings.Contains(out, "Sort Method:") {
		t.Errorf("plain EXPLAIN must never render the ANALYZE-only Sort Method line:\n%s", out)
	}
}
