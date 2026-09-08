package optimizer

// The base-rel Gather crossover — why `GOOPG_GATHER_PATHS=all` could not make
// a plain base-relation Gather win, and what fixed it.
//
// C-19d DESIGN §5.1 stated the arithmetic as a general fact ("the charge is an
// order of magnitude larger … `add_path` correctly DOMINATES the base-rel
// Gather with the serial scan at ANY relation size"), and joinpathsparallel.go
// repeated it as a statement about PG ("PG escapes that arithmetic by putting
// the join below the Gather"). Both were wrong about PG and right about goopg,
// and the difference was never in `cost_gather`, `add_path` or
// `get_parallel_divisor` — all three are faithful. It was in the SCAN's inputs.
//
//	PG      cost_seqscan charges (cpu_tuple_cost + qual) × `baserel->tuples`
//	        — every tuple SCANNED — and divides that term by the parallel
//	        divisor; `cost_gather` then charges parallel_tuple_cost ×
//	        `baserel->rows` — only the survivors CROSSING the boundary. Two
//	        different numbers, so a selective scan has a crossover.
//
//	goopg   the base-rel scan (buildInitialRels' prebuilt path and
//	        addBaseRelPartialPaths' partial twin alike) was priced on the
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
//	        positive for every row count and every selectivity. There was no
//	        crossover to find, so no measurement of the `all` mode could find one.
//
// Ledger `c19-baserel-scan-priced-on-output-rows` unified both sites onto
// `baseSeqScanCostInputs` — `baserel->pages` / `baserel->tuples` for the scan,
// `rel.Rows` only for what crosses the Gather. The two tests below are the
// before and after of that one change: (7a) drives the PRODUCTION producers
// and asserts the crossover now EXISTS (it is the inversion of the pin that
// used to assert no selectivity could cross), and (7b) is the design statement
// it was landed against, showing the SAME constants crossing on PG's inputs.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// crossoverWidth/Tuples are a TPC-H `lineitem`-shaped relation: big enough to
// clear min_parallel_table_scan_size several times over, so the worker ladder
// is saturated and the only variable is selectivity.
const (
	crossoverWidth  = 117
	crossoverTuples = 6000000.0
)

// (7a) THE FIX, through the PRODUCTION producers. `buildInitialRels` files the
// serial prebuilt scan and `addBaseRelPartialPaths` its partial twin from ONE
// resolver (`baseSeqScanCostInputs`), and both now read `baserel->pages` /
// `baserel->tuples` while only what crosses the Gather is priced on
// `rel.Rows`. So a base-rel Gather has a real crossover: it loses on an
// unfiltered scan (everything crosses) and wins on a selective one (the CPU
// share the workers take dwarfs the transfer), which is what vanilla PG 18.3
// does on `select * from lineitem where l_extendedprice > 90000`.
//
// This is the INVERSION of the pin that stood here before the fix, which
// asserted `gather > serial` at every selectivity and matched the closed form
// `parallel_setup_cost + (parallel_tuple_cost − per_tuple_cpu×(1−1/d))×rows`.
// If it ever flips back, the two sites have drifted apart again.
//
// It is a statement about the PRODUCER, not about `makeGatherPath`:
// TestGatherPathWinsExactlyAtTheSetupCostCrossover already shows the
// comparator honours a crossover when a subpath offers one. What changed is
// that the producer can now offer one.
func TestBaseRelGatherCrossesOverOnASelectiveScan(t *testing.T) {
	withParallelOn(t, func() {
		for _, tc := range []struct {
			sel      float64
			wantWins bool
		}{
			{1.0, false},  // everything crosses: serial, as in PG
			{0.5, false},  //
			{0.01, true},  // selective: the CPU share dwarfs the transfer
			{0.001, true}, //
		} {
			serial, gather := crossoverArms(t, tc.sel)
			wins := gather.Cost.Total < serial.Cost.Total
			if wins != tc.wantWins {
				t.Errorf("sel=%v: gather %v vs serial %v: wins = %v, want %v — "+
					"the base-rel scan cost inputs moved; see ledger "+
					"c19-baserel-scan-priced-on-output-rows",
					tc.sel, gather.Cost.Total, serial.Cost.Total, wins, tc.wantWins)
			}
		}
	})
}

// crossoverArms runs the production producer chain over a single
// `lineitem`-shaped relation carrying a local filter of the given selectivity,
// and returns its serial prebuilt scan and the Gather over its partial twin.
// The relation is stamped with pg_class-shaped reltuples/relpages so
// `baseRelPages` reads the real figure, exactly as an ANALYZEd relation does.
func crossoverArms(t *testing.T, sel float64) (serial, gather *Path) {
	t.Helper()
	names := []string{"lineitem", "dim"}
	prob := rfjProblem(names, []int64{int64(crossoverTuples), 100}, []Expr{rfjEq(names, 0, 1)})
	realPages := estScanPages(crossoverTuples, crossoverWidth)
	prob.relInfos[0].table.Stats = &catalog.TableStats{
		RowCount: int64(crossoverTuples), Pages: int(realPages), Analyzed: true,
	}
	prob.relInfos[1].table.Stats = &catalog.TableStats{RowCount: 100, Pages: 1, Analyzed: true}
	// The local filter: one operator per tuple, and the selectivity that
	// separates `baserel->tuples` from `baserel->rows`.
	prob.relInfos[0].localFilter = &BinaryOp{
		Op:    parser.OpGt,
		Left:  &ColumnRef{Name: "lineitem1", Index: 1, SourceTableIdx: 0},
		Right: &IntegerConst{Value: 90000},
	}
	prob.relInfos[0].hasLocalFilter = true
	prob.relInfos[0].filteredRows = int64(crossoverTuples * sel)

	s := cpSearch(t, prob)
	rel := s.joinrels[1][0]
	for _, p := range rel.Pathlist {
		if p.Kind == PathPrebuilt {
			serial = p
		}
	}
	if serial == nil || len(rel.PartialPathlist) == 0 {
		t.Fatalf("sel=%v: producers filed serial=%v partial=%d", sel, serial, len(rel.PartialPathlist))
	}
	gather = makeGatherPath(rel, rel.PartialPathlist[0], s.cp)
	if gather == nil {
		t.Fatalf("sel=%v: makeGatherPath declined the production partial path", sel)
	}
	return serial, gather
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
