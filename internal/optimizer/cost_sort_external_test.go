package optimizer

// M0127-P5.9-k — `cost_tuplesort`'s external-merge arm (costsize.c:2144).
//
// The defect these tests pin is not "the sort was slightly cheap". It is an
// ASYMMETRY between two rivals in one `addPath` comparison: a hash join that
// spills has been charged its batch I/O since P5.7-a, while the merge join's
// sort — which spills the same bytes through the same executor work_mem budget
// (`mergeSortedSource.flushRun`) — was charged nothing at all. The last test in
// this file is therefore the one that matters most: it asserts the two charges
// exist in the same currency, not merely that each exists.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/executor/hashsize"
)

// sortRowsFillingBudget returns a row count whose footprint at `ncols` columns
// is `factor` times the given memory budget. Expressed as a helper because
// every test below is about which side of the budget the input lands on, and
// hard-coded row counts would silently stop testing that if EntryBytes changed.
func sortRowsFillingBudget(workMem int64, ncols int, factor float64) float64 {
	return math.Ceil(factor * float64(workMem) / hashsize.EntryBytes(ncols, 0))
}

// TestCostSortRun_InMemorySortChargesNoDiskIO — the `output_bytes <=
// sort_mem_bytes` arm. A sort that fits is plain quicksort: comparisons at
// startup, an operator-cost emit at run, and NOT ONE page. This is the
// behaviour that existed before the external arm was added, and it must be
// preserved exactly, because most sorts in a plan really do fit.
func TestCostSortRun_InMemorySortChargesNoDiskIO(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 8
	rows := sortRowsFillingBudget(cp.workMem, ncols, 0.5)

	got := costSortRun(cp, rows, ncols, 0)
	wantStartup := 2 * cp.cpuOperatorCost * rows * math.Log2(rows)
	if !approx(got.Startup, wantStartup) {
		t.Fatalf("startup = %v, want the pure comparison cost %v (a fitting sort must not be charged I/O)", got.Startup, wantStartup)
	}
	if !approx(got.Total-got.Startup, cp.cpuOperatorCost*rows) {
		t.Fatalf("run = %v, want cpu_operator_cost per emitted row %v", got.Total-got.Startup, cp.cpuOperatorCost*rows)
	}
}

// TestCostSortRun_SpillingSortChargesTheMergePasses — the disk arm, reproduced
// term for term against upstream's arithmetic rather than against a
// hand-copied number, so a future change to the byte model or to the page
// constants moves both sides together.
//
// `nruns` here is 2, far under the 500-run MAXORDER fan-in the default budget
// buys, so this is the SINGLE-pass case: `log_runs = 1`.
func TestCostSortRun_SpillingSortChargesTheMergePasses(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 16
	rows := sortRowsFillingBudget(cp.workMem, ncols, 2.0)

	got := costSortRun(cp, rows, ncols, 0)

	inputBytes := rows * hashsize.EntryBytes(ncols, 0)
	npages := math.Ceil(inputBytes / blockSizeBytes)
	wantDisk := 2.0 * npages * 1.0 * (cp.seqPageCost*0.75 + cp.randomPageCost*0.25)
	wantStartup := 2*cp.cpuOperatorCost*rows*math.Log2(rows) + wantDisk

	if !approx(got.Startup, wantStartup) {
		t.Fatalf("startup = %v, want comparisons + one merge pass over %v pages = %v", got.Startup, npages, wantStartup)
	}
	// The whole point: the I/O term is not a rounding correction.
	if !(wantDisk > 2*(wantStartup-wantDisk)) {
		t.Fatalf("the disk term %v is not dominant over the comparison term %v — this test no longer exercises the arm it names",
			wantDisk, wantStartup-wantDisk)
	}
}

// TestCostSortRun_MultiPassMergeCostsMoreThanOnePass — `log_runs = ceil(log(r)
// / log(M))`. Reaching a second merge pass needs more than MAXORDER=500 runs,
// which at the default 512 MB budget means a 256 GB input; the reachable way to
// exercise it is a SMALL work_mem, which is also the configuration a real
// session can land in. Asserted as a strict inequality against the same input
// sorted with a budget one pass wide, because the point is that the model
// distinguishes them at all.
func TestCostSortRun_MultiPassMergeCostsMoreThanOnePass(t *testing.T) {
	const ncols = 4
	rowBytes := hashsize.EntryBytes(ncols, 0)

	// A budget of 512 kB buys mergeorder = max(512K/278528, 6) = 6 tapes.
	small := defaultCostParams()
	small.workMem = 512 << 10
	if order := tuplesortMergeOrder(small.workMem); order != 6 {
		t.Fatalf("mergeorder at 512 kB = %v, want the MINORDER floor of 6", order)
	}
	// 50 runs against a fan-in of 6 needs ceil(log(50)/log(6)) = 3 passes.
	rows := math.Ceil(50 * float64(small.workMem) / rowBytes)
	multi := costSortRun(small, rows, ncols, 0)

	// The same input under a budget wide enough that one pass suffices. The
	// comparison term is identical, so any difference is the pass count.
	wide := small
	wide.workMem = int64(math.Ceil(rows*rowBytes)) / 2 // exactly 2 runs
	single := costSortRun(wide, rows, ncols, 0)

	if !(multi.Startup > single.Startup) {
		t.Fatalf("a 3-pass merge (%v) must cost more than a 1-pass merge (%v) of the same rows", multi.Startup, single.Startup)
	}
	npages := math.Ceil(rows * rowBytes / blockSizeBytes)
	perPass := 2.0 * npages * (small.seqPageCost*0.75 + small.randomPageCost*0.25)
	if !approx(multi.Startup-single.Startup, 2*perPass) {
		t.Fatalf("the extra cost %v is not exactly two more merge passes (%v)", multi.Startup-single.Startup, 2*perPass)
	}
}

// TestCostSortRun_UnknownWidthChargesNoDiskIO — a zero `ncols` means the caller
// could not determine the row width, and an unknown width must not be allowed
// to invent an I/O charge. This is the SAME reading `hashJoinCost` gives a zero
// `innerCols` ("assume no spill"), and the two must agree: a rel whose width is
// unknown would otherwise have its merge candidate penalised and its hash
// candidate excused, which is the asymmetry this slice exists to remove.
func TestCostSortRun_UnknownWidthChargesNoDiskIO(t *testing.T) {
	cp := defaultCostParams()
	rows := sortRowsFillingBudget(cp.workMem, 16, 4.0)

	unknown := costSortRun(cp, rows, 0, 0)
	wantStartup := 2 * cp.cpuOperatorCost * rows * math.Log2(rows)
	if !approx(unknown.Startup, wantStartup) {
		t.Fatalf("startup = %v, want the pure comparison cost %v", unknown.Startup, wantStartup)
	}
	if known := costSortRun(cp, rows, 16, 0); !(known.Startup > unknown.Startup) {
		t.Fatalf("a KNOWN width over the same budget must reach the disk arm: %v vs %v", known.Startup, unknown.Startup)
	}
}

// TestCostSortRun_TinyInputIsClampedNotFree — `if (tuples < 2.0) tuples = 2.0`.
// PG clamps because of log(0), but the consequence goopg needs is the one
// P5.9-j closed on the nested-loop side: an estimate that has collapsed to 1
// must not make the operator above it free.
func TestCostSortRun_TinyInputIsClampedNotFree(t *testing.T) {
	cp := defaultCostParams()
	two := costSortRun(cp, 2, 4, 0)
	for _, rows := range []float64{0, 0.5, 1} {
		got := costSortRun(cp, rows, 4, 0)
		if got != two {
			t.Fatalf("costSortRun(%v) = %+v, want the 2-tuple floor %+v", rows, got, two)
		}
	}
	if !(two.Total > 0) {
		t.Fatalf("the 2-tuple floor must cost something, got %+v", two)
	}
}

// TestSpillingSortAndSpillingHashAreChargedInOneCurrency is the regression this
// slice exists for, stated as the invariant rather than as a plan.
//
// Take one relation big enough that BOTH rivals spill it. The hash join writes
// it to batch files; the merge join writes it to sort runs. Before P5.9-k the
// first was charged ~1.3 M cost units and the second exactly 0, so the search
// chose Merge Join over a full ordered index scan of `orders` for five TPC-H
// queries at 2-4x the runtime. The assertion is deliberately loose on the RATIO
// — the two access patterns genuinely differ — and strict on the ORDER OF
// MAGNITUDE, which is what a comparison inside `addPath` can survive.
func TestSpillingSortAndSpillingHashAreChargedInOneCurrency(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 16
	rows := sortRowsFillingBudget(cp.workMem, ncols, 8.0)

	sortIO := costSortRun(cp, rows, ncols, 0).Startup - 2*cp.cpuOperatorCost*rows*math.Log2(rows)
	// The hash rival's charge for spilling the SAME rows: `hashJoinCost`'s
	// batch term, inner side (written once at build, read back at probe).
	hashIO := cp.seqPageCost * 2 * spillPages(rows, ncols, 0)

	if !(sortIO > 0) {
		t.Fatalf("the sort of %v rows over a %v-byte budget must be charged for spilling", rows, cp.workMem)
	}
	if ratio := sortIO / hashIO; ratio < 0.1 || ratio > 10 {
		t.Fatalf("sort I/O %v and hash I/O %v differ by %.1fx — they are no longer in one currency and addPath cannot rank them",
			sortIO, hashIO, ratio)
	}
}

// TestCostSortRun_VarBytesWidenTheSpillCharge — spill-calibration Cut 1
// (`docs/design/planner-spill-cost-calibration/DESIGN.md` §3.3). The
// variable-width payload enters the SAME `EntryBytes` the hash rival is sized
// with, so (a) a fitting sort stays untouched by it — the in-memory arm never
// reads bytes — and (b) once the input spills, the I/O term grows exactly as
// the modelled page count grows, and by nothing else.
func TestCostSortRun_VarBytesWidenTheSpillCharge(t *testing.T) {
	cp := defaultCostParams()
	const ncols = 8
	const varBytes = 200.0

	fitting := sortRowsFillingBudget(cp.workMem, ncols, 0.25)
	if a, b := costSortRun(cp, fitting, ncols, 0), costSortRun(cp, fitting, ncols, varBytes); a != b {
		t.Fatalf("a sort that fits at both payloads must price identically: %+v vs %+v", a, b)
	}

	rows := sortRowsFillingBudget(cp.workMem, ncols, 2.0)
	lean := costSortRun(cp, rows, ncols, 0)
	wide := costSortRun(cp, rows, ncols, varBytes)
	if !(wide.Startup > lean.Startup) {
		t.Fatalf("payload bytes must raise a spilling sort's I/O: lean %v, wide %v", lean.Startup, wide.Startup)
	}
	pages := func(avg float64) float64 {
		return math.Ceil(rows * hashsize.EntryBytes(ncols, avg) / blockSizeBytes)
	}
	perPage := 2.0 * (cp.seqPageCost*0.75 + cp.randomPageCost*0.25)
	if want := (pages(varBytes) - pages(0)) * perPage; !approx(wide.Startup-lean.Startup, want) {
		t.Fatalf("delta = %v, want the extra pages alone %v (the comparison term must not move)", wide.Startup-lean.Startup, want)
	}
}
