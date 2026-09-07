package optimizer

// The base-rel Gather crossover — why `GOOPG_GATHER_PATHS=all` cannot make a
// plain base-relation Gather win, and what would.
//
// C-19d DESIGN §5.1 states the arithmetic as a general fact ("the charge is an
// order of magnitude larger … `add_path` correctly DOMINATES the base-rel
// Gather with the serial scan at ANY relation size"), and joinpathsparallel.go
// repeats it as a statement about PG ("PG escapes that arithmetic by putting
// the join below the Gather"). Both are wrong about PG and right about goopg,
// and the difference is not in `cost_gather`, `add_path` or
// `get_parallel_divisor` — all three are faithful. It is in the SCAN's inputs.
//
//	PG      cost_seqscan charges (cpu_tuple_cost + qual) × `baserel->tuples`
//	        — every tuple SCANNED — and divides that term by the parallel
//	        divisor; `cost_gather` then charges parallel_tuple_cost ×
//	        `baserel->rows` — only the survivors CROSSING the boundary. Two
//	        different numbers, so a selective scan has a crossover.
//
//	goopg   the base-rel scan (joinsearch.go:440's prebuilt path and
//	        considerparallel.go's partial twin alike) is priced on the
//	        POST-restriction row count for BOTH terms, and on a page count
//	        derived from that same number. Scanned == crossed, so
//
//	            gather − serial = parallel_setup_cost
//	                            + (parallel_tuple_cost
//	                               − per_tuple_cpu × (1 − 1/divisor)) × rows
//
//	        which is 1000 + 0.0906×rows for a one-operator qual at the shipped
//	        constants (per_tuple_cpu = cpu_tuple_cost + cpu_operator_cost ×
//	        numQualOps, divisor 4): strictly
//	        positive for every row count and every selectivity. There is no
//	        crossover to find, so no measurement of the `all` mode can find one.
//
// The two tests below pin exactly that pair of statements, so that whoever
// unifies the base-rel scan onto `rel->pages` / `rel->tuples` (ledger
// `c19-baserel-scan-priced-on-output-rows`) sees the first one flip and knows
// which item it belongs to.

import (
	"testing"
)

// crossoverWidth/Tuples are a TPC-H `lineitem`-shaped relation: big enough to
// clear min_parallel_table_scan_size several times over, so the worker ladder
// is saturated and the only variable is selectivity.
const (
	crossoverWidth  = 117
	crossoverTuples = 6000000.0
)

// (7a) THE DEFECT. Through the PRODUCTION producers — `generateScanPaths`,
// which files the serial path and `addPartialSeqScanPath`'s partial twin from
// one set of inputs, exactly as `addBaseRelPartialPaths` does — a Gather over
// the partial base scan loses at EVERY selectivity, and the margin is the
// closed form above rather than a magic total.
//
// This is not a statement about `makeGatherPath`: TestGatherPathWins
// ExactlyAtTheSetupCostCrossover already shows the comparator honours a
// crossover when a subpath offers one. It is a statement about the PRODUCER —
// the saving it can offer is structurally bounded below the charge. Both
// candidates ARE generated here; the loss is in the pricing, which is the
// opposite of the Q8 lesson and worth pinning as its own shape.
func TestBaseRelGatherCannotWinAtAnySelectivity(t *testing.T) {
	cp := defaultCostParams()
	realPages := estScanPages(crossoverTuples, crossoverWidth)
	workers := computeParallelWorkerForRel(cp, realPages, 0)
	if workers <= 0 {
		t.Fatalf("fixture does not clear the parallel ladder: %d pages", realPages)
	}
	for _, sel := range []float64{1.0, 0.5, 0.1, 0.01, 0.001} {
		rows := crossoverTuples * sel
		rel := newRelOptInfo(RelSet(1), rows, crossoverWidth)
		rel.ConsiderParallel = true
		// Production's split, verbatim: the COST reads pages derived from the
		// post-restriction row count (considerparallel.go's `estScanPages(
		// rel.Rows, rel.Width)`), while the WORKER COUNT reads the real one.
		generateScanPaths(rel, cp, estScanPages(rows, crossoverWidth), 1, workers, true)

		var serial *Path
		for _, p := range rel.Pathlist {
			if p.Kind == PathSeqScan {
				serial = p
			}
		}
		if serial == nil || len(rel.PartialPathlist) == 0 {
			t.Fatalf("sel=%v: producer filed serial=%v partial=%d", sel, serial, len(rel.PartialPathlist))
		}
		g := makeGatherPath(rel, rel.PartialPathlist[0], cp)
		if g == nil {
			t.Fatalf("sel=%v: makeGatherPath declined the production partial path", sel)
		}
		if g.Cost.Total <= serial.Cost.Total {
			t.Fatalf("sel=%v: gather %v beat serial %v — the base-rel scan cost model changed; "+
				"see ledger c19-baserel-scan-priced-on-output-rows and re-run C-19d §5.1",
				sel, g.Cost.Total, serial.Cost.Total)
		}
		// …and by exactly the closed form, in the currency of the constants.
		d := getParallelDivisor(workers, cp.parallelLeaderParticipation)
		// The scan's per-tuple CPU is (cpu_tuple_cost + cpu_operator_cost ×
		// numQualOps) — one qual op in this fixture — and that whole term is
		// what the divisor divides.
		perTuple := cp.cpuTupleCost + cp.cpuOperatorCost
		want := cp.parallelSetupCost + (cp.parallelTupleCost-perTuple*(1-1/d))*rows
		if got := g.Cost.Total - serial.Cost.Total; !gpClose(got, want) {
			t.Errorf("sel=%v: gather−serial = %v, want %v (setup + (parallel_tuple_cost − per_tuple_cpu×(1−1/d))×rows)",
				sel, got, want)
		}
	}
}

// (7b) THE FIX, stated as arithmetic before anyone pays for it. Priced on PG's
// inputs — `rel->pages` and `rel->tuples` for the scan, `rel->rows` only for
// what crosses the Gather — the SAME constants produce a crossover, and it
// falls where PG's does: a selective scan of a large relation parallelises, a
// full one does not.
//
// Nothing in production calls this arrangement yet. The test is the design
// statement's witness, so the claim "the constants are not the problem" is
// checkable rather than asserted.
func TestPGShapedScanInputsRestoreTheGatherCrossover(t *testing.T) {
	cp := defaultCostParams()
	realPages := estScanPages(crossoverTuples, crossoverWidth)
	workers := computeParallelWorkerForRel(cp, realPages, 0)
	serial := costSeqscan(cp, realPages, crossoverTuples, 1)

	gatherTotalAt := func(sel float64) float64 {
		pcost, prows := costParallelSeqscan(cp, realPages, crossoverTuples, crossoverTuples*sel, 1, workers)
		return gatherCost(cp, pcost, computeGatherRows(&Path{
			Rows: prows, ParallelWorkers: workers, ParallelSafe: true,
		}, cp)).Total
	}

	for _, tc := range []struct {
		sel      float64
		wantWins bool
	}{
		{1.0, false},  // everything crosses: serial, as in PG
		{0.5, false},  //
		{0.01, true},  // selective: the CPU share dwarfs the transfer
		{0.001, true}, //
	} {
		wins := gatherTotalAt(tc.sel) < serial.Total
		if wins != tc.wantWins {
			t.Errorf("PG-shaped inputs, sel=%v: gather %v vs serial %v: wins = %v, want %v",
				tc.sel, gatherTotalAt(tc.sel), serial.Total, wins, tc.wantWins)
		}
	}
	// The crossover is REAL — the two arms above are on opposite sides of one —
	// which is the whole difference from (7a), where no selectivity crosses.
	if !(gatherTotalAt(0.5) > serial.Total && gatherTotalAt(0.01) < serial.Total) {
		t.Fatal("no crossover under PG-shaped inputs; the fix statement in the header is stale")
	}
}
