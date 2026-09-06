package optimizer

// C-19g (P5-07) — the partial-aggregation path tournament.
//
// The acceptance argument is DESIGN §6.1. Two rules shape every test here:
//
//   - the crossover is asserted THROUGH the named `costParams` fields, never a
//     literal. A literal once put a crossover test inside `add_path`'s 1% fuzz
//     band, and separately pinned a stale calibration worth 27% of the suite.
//   - BOTH candidates are shown to exist before any cost is compared. Five
//     hypotheses were burned on Q8 because a producer emitted nothing at that
//     parameterisation and the comparison was vacuous.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// sizedAggFixture builds an aggregate over a scan with ABSOLUTE statistics —
// a row count and a per-column distinct count — because the priced model needs
// absolute quantities where the retired ratio model did not. `ndistinct` is the
// distinct count of EACH group column.
func sizedAggFixture(t *testing.T, rows int64, ndistinct float64, nAggs, nGroupCols int) *Aggregate {
	t.Helper()
	cat := catalog.NewInMemory()
	cols := []catalog.Column{
		{Name: "g0", Type: catalog.Type{Name: "int4"}},
		{Name: "g1", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "int4"}},
	}
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "agg_sized_t"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	colStats := make([]catalog.ColumnStats, len(cols))
	for i := range colStats {
		colStats[i].NDistinct = int64(ndistinct)
		colStats[i].NDistinctFrac = ndistinct / float64(rows)
	}
	tbl.Stats = &catalog.TableStats{RowCount: rows, Columns: colStats}

	scan := &SeqScan{Table: tbl, schema: Schema{{Name: "g0"}, {Name: "g1"}, {Name: "v"}}}
	agg := &Aggregate{Child: scan}
	for i := 0; i < nGroupCols; i++ {
		agg.GroupExprs = append(agg.GroupExprs, &ColumnRef{Index: i, Name: cols[i].Name})
	}
	for i := 0; i < nAggs; i++ {
		agg.Aggs = append(agg.Aggs, AggregateCall{Name: "sum"})
	}
	return agg
}

func c19gParams() costParams { return DefaultPlannerSettings().costParams() }

// TestPartialAggTournamentGeneratesBothCandidates is the "verify BOTH candidates
// were generated before comparing costs" pin. Without it every other test in
// this file could be passing vacuously against a producer that emitted one path.
func TestPartialAggTournamentGeneratesBothCandidates(t *testing.T) {
	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2) // Q1's shape
	tour := createPartialGroupingPaths(agg, 4, true, c19gParams())
	if tour == nil {
		t.Fatal("producer returned no tournament for a decomposable aggregate")
	}
	if tour.split == nil {
		t.Error("no Finalize->Gather->Partial candidate was generated")
	}
	if tour.noSplit == nil {
		t.Error("no Agg->Gather->input candidate was generated")
	}
	if tour.split.Cost.Total <= 0 || tour.noSplit.Cost.Total <= 0 {
		t.Fatalf("a candidate was generated with a non-positive price: split=%v nosplit=%v",
			tour.split.Cost, tour.noSplit.Cost)
	}
	if tour.split.Cost.Total == tour.noSplit.Cost.Total {
		t.Fatal("both candidates priced identically — the comparison is vacuous")
	}
	if tour.grouped.CheapestTotal == nil {
		t.Fatal("setCheapest left the GROUP_AGG rel with no cheapest path")
	}
	// The winner must have survived add_path, and the group-states that cross
	// the boundary must be fewer than the rows the no-split arm ships.
	if tour.splitWins() && !tour.splitFiled {
		t.Error("the split won CheapestTotal but is not on the pathlist")
	}
	if tour.crossedRows >= tour.inputRows {
		t.Errorf("crossedRows=%g is not below inputRows=%g — the partial "+
			"aggregate is not reducing what crosses the Gather",
			tour.crossedRows, tour.inputRows)
	}
}

// TestPartialAggSplitWinsForLowCardinalityGrouping is TPC-H Q1: ~5.9M rows into
// four groups. Without the split those 5.9M rows funnel through the Gather into
// ONE leader-side aggregate, which measurement pinned the query at ~7.1s.
func TestPartialAggSplitWinsForLowCardinalityGrouping(t *testing.T) {
	for _, workers := range []int{2, 4, 8} {
		agg := sizedAggFixture(t, 5_900_000, 2, 8, 2)
		tour := createPartialGroupingPaths(agg, workers, true, c19gParams())
		if tour == nil {
			t.Fatalf("workers=%d: no tournament", workers)
		}
		if !tour.splitWins() {
			t.Errorf("workers=%d: split lost for a low-cardinality grouping "+
				"(split=%v nosplit=%v, crossed=%g of %g rows)",
				workers, tour.split.Cost, tour.noSplit.Cost,
				tour.crossedRows, tour.inputRows)
		}
	}
}

// TestPartialAggSplitLosesWhenGroupingReducesNothing is the case the gate exists
// for: TPC-H Q18's inner `group by l_orderkey`, where every input row becomes
// its own partial state and the split reduces nothing.
func TestPartialAggSplitLosesWhenGroupingReducesNothing(t *testing.T) {
	const rows = 6_000_000
	for _, workers := range []int{1, 2, 4} {
		agg := sizedAggFixture(t, rows, rows, 1, 1) // every row its own group
		tour := createPartialGroupingPaths(agg, workers, true, c19gParams())
		if tour == nil {
			t.Fatalf("workers=%d: no tournament", workers)
		}
		if tour.splitWins() {
			t.Errorf("workers=%d: split accepted for a near-unique group key "+
				"(split=%v nosplit=%v, crossed=%g of %g rows)",
				workers, tour.split.Cost, tour.noSplit.Cost,
				tour.crossedRows, tour.inputRows)
		}
	}
}

// splitWinsClosedForm is DESIGN §3.4's inequality, written out of the SAME
// named costParams fields the producer reads. It is the independent statement
// of what the composed cost functions are supposed to compute; agreeing with it
// is what says the composition has no missing or doubled term.
//
//	ptc*(R - Gp) + coc*(A+K)*R*(1 - 1/d)  >  coc*A*Gw + ctc*Gw + coc*(A+K)*Gp
func splitWinsClosedForm(cp costParams, r, gp, gw, d float64, nAggs, nGroupCols int) float64 {
	a := float64(nAggs)
	k := float64(nGroupCols)
	saved := cp.parallelTupleCost*(r-gp) + cp.cpuOperatorCost*(a+k)*r*(1-1/d)
	paid := cp.cpuOperatorCost*a*gw + cp.cpuTupleCost*gw + cp.cpuOperatorCost*(a+k)*gp
	return saved - paid
}

// TestPartialAggCrossoverMatchesTheClosedForm sweeps the reduction ratio and
// requires the producer's verdict to agree with the closed form at every point
// EXCEPT inside add_path's fuzz band, where a disagreement is the comparator's
// tolerance rather than a term error.
//
// Sweeping rather than pinning one point is deliberate: what is being pinned is
// the CROSSOVER, and a single-point test cannot tell a moved crossover from a
// moved constant.
func TestPartialAggCrossoverMatchesTheClosedForm(t *testing.T) {
	cp := c19gParams()
	const rows = 4_000_000
	for _, ndistinct := range []float64{2, 100, 1e4, 1e5, 4e5, 1e6, 2e6, 3e6, 4e6} {
		for _, workers := range []int{1, 2, 4, 8} {
			agg := sizedAggFixture(t, rows, ndistinct, 4, 1)
			tour := createPartialGroupingPaths(agg, workers, true, cp)
			if tour == nil {
				t.Fatalf("ndistinct=%g workers=%d: no tournament", ndistinct, workers)
			}
			margin := splitWinsClosedForm(cp, tour.inputRows, tour.crossedRows,
				tour.partialGroups, tour.divisor, len(agg.Aggs), len(agg.GroupExprs))
			// add_path's fuzz is a RELATIVE band on the totals, so scale the
			// tolerance by the cheaper total rather than by an absolute number.
			cheaper := tour.split.Cost.Total
			if tour.noSplit.Cost.Total < cheaper {
				cheaper = tour.noSplit.Cost.Total
			}
			if margin > 0.02*cheaper && !tour.splitWins() {
				t.Errorf("ndistinct=%g workers=%d: closed form says split wins by %g "+
					"but the tournament chose no-split", ndistinct, workers, margin)
			}
			if margin < -0.02*cheaper && tour.splitWins() {
				t.Errorf("ndistinct=%g workers=%d: closed form says split loses by %g "+
					"but the tournament chose split", ndistinct, workers, -margin)
			}
		}
	}
}

// TestPartialAggRefusesWhenWorkerDuplicationSaturatesTheBoundary pins the
// property that decides this slice's economics, and that the retired constant
// model reached only by accident.
//
// Each worker produces its OWN copy of every group it sees, so the states that
// cross the boundary are `Gw * d`, and `Gw = min(ndistinct, R/d)`. Raising the
// worker count therefore does NOT shrink what crosses — past the point where
// each worker sees the full spread of groups it GROWS it, up to the saturation
// `Gp == R`, where the split ships exactly as many states as there were input
// rows and the transfer saving is zero.
//
// Measured on this fixture: 2M rows / 500k groups crosses at 850k states with
// one worker (split wins) and at 2M — full saturation — with four (split
// loses). That is the correct answer in both directions, and it is why the
// verdict cannot be read off the group count alone.
func TestPartialAggRefusesWhenWorkerDuplicationSaturatesTheBoundary(t *testing.T) {
	cp := c19gParams()
	const rows = 2_000_000
	saturatedSeen, reducingSeen := false, false
	for _, nd := range []float64{2, 1e3, 1e5, 5e5, 1e6, 2e6} {
		for _, workers := range []int{1, 2, 4, 8} {
			tour := createPartialGroupingPaths(sizedAggFixture(t, rows, nd, 2, 1), workers, true, cp)
			if tour == nil {
				t.Fatalf("ndistinct=%g workers=%d: no tournament", nd, workers)
			}
			if tour.crossedRows > tour.inputRows {
				t.Errorf("ndistinct=%g workers=%d: %g states cross for %g input rows — "+
					"the per-worker group count is not clamped to the rows a worker reads",
					nd, workers, tour.crossedRows, tour.inputRows)
			}
			if tour.crossedRows >= tour.inputRows {
				saturatedSeen = true
				if tour.splitWins() {
					t.Errorf("ndistinct=%g workers=%d: split won at full boundary "+
						"saturation (%g states for %g rows), where it saves nothing",
						nd, workers, tour.crossedRows, tour.inputRows)
				}
				continue
			}
			if tour.crossedRows*10 < tour.inputRows {
				reducingSeen = true
				if !tour.splitWins() {
					t.Errorf("ndistinct=%g workers=%d: split lost while reducing the "+
						"boundary tenfold (%g states for %g rows)",
						nd, workers, tour.crossedRows, tour.inputRows)
				}
			}
		}
	}
	// Both regimes must actually be exercised, or the sweep proves nothing.
	if !saturatedSeen || !reducingSeen {
		t.Fatalf("the sweep never reached both regimes (saturated=%v reducing=%v)",
			saturatedSeen, reducingSeen)
	}
}

// TestPartialAggRefusesNonDecomposableBeforePricing is the fail-CLOSED pin.
// `considerparallel.go` is fail-closed by hard-won design (a review found four
// fail-open holes), and an aggregate that cannot be split must never be priced,
// let alone split. Refusal is a nil tournament, not a losing candidate.
func TestPartialAggRefusesNonDecomposableBeforePricing(t *testing.T) {
	cases := map[string]func(*Aggregate){
		"array_agg is not decomposable": func(a *Aggregate) { a.Aggs[0].Name = "array_agg" },
		"DISTINCT is per-worker":        func(a *Aggregate) { a.Aggs[0].Distinct = true },
		"internal ORDER BY is ordered":  func(a *Aggregate) { a.Aggs[0].OrderBy = []SortKey{{}} },
		"a user aggregate has no combine": func(a *Aggregate) {
			a.Aggs[0].UserAgg = &catalog.UserAggregate{}
		},
		"non-Simple mode is already split":         func(a *Aggregate) { a.Mode = AggModeFinal },
		"a group-only node has nothing to combine": func(a *Aggregate) { a.Aggs = nil },
	}
	for name, mutate := range cases {
		agg := sizedAggFixture(t, 5_900_000, 2, 1, 1)
		mutate(agg)
		if tour := createPartialGroupingPaths(agg, 4, true, c19gParams()); tour != nil {
			t.Errorf("%s: a tournament was built anyway (split=%v)", name, tour.split.Cost)
		}
	}
	// And a zero/negative worker count, which is upstream's single_copy shape
	// and has never run here.
	agg := sizedAggFixture(t, 5_900_000, 2, 1, 1)
	if tour := createPartialGroupingPaths(agg, 0, true, c19gParams()); tour != nil {
		t.Error("a tournament was built for zero workers")
	}
}

// TestPartialAggModeOffIsTheSerialControlArm: with the knob off the verdict must
// be the retired size rule's, bit for bit. This is what makes the A/B in
// DESIGN §6.3 an A/B rather than two different experiments.
func TestPartialAggModeOffIsTheSerialControlArm(t *testing.T) {
	defer setPartialAggPathsModeForTest(partialAggPathsOff)()
	for _, frac := range []float64{1e-6, 1e-4, 1e-2, 0.1, 0.5, 1.0} {
		for _, workers := range []int{1, 2, 4, 8} {
			agg := aggFixture(t, frac, 2, 1)
			want := splitAggregateIsProfitable(agg, workers, true)
			if got := partialAggSplitPays(agg, workers, true); got != want {
				t.Errorf("frac=%g workers=%d: mode off gave %v, size rule gives %v",
					frac, workers, got, want)
			}
		}
	}
}

// TestPartialAggModeOnPricesWhatTheSizeRuleRefused is the behavioural delta this
// slice exists to create. `aggColumnStats` refuses to descend through a Project,
// so the retired rule returns "cannot estimate" and declines outright — dropping
// the Gather below the aggregate and funnelling the whole input into the leader.
// The priced model has no such hole: `estimateNumGroups` never refuses.
func TestPartialAggModeOnPricesWhatTheSizeRuleRefused(t *testing.T) {
	agg := sizedAggFixture(t, 5_900_000, 2, 8, 1)
	// Interpose a Project, which aggColumnStats explicitly declines to descend
	// through (a live index-remapping bug it must not inherit).
	scan := agg.Child
	agg.Child = &Project{Child: scan, Targets: []Expr{
		&ColumnRef{Index: 0, Name: "g0"},
		&ColumnRef{Index: 2, Name: "v"},
	}, schema: Schema{{Name: "g0"}, {Name: "v"}}}

	if _, ok := groupsToRowsRatio(agg, 4); ok {
		t.Skip("aggColumnStats learned to descend through Project; " +
			"this test's premise is gone and the delta must be re-stated")
	}
	if splitAggregateIsProfitable(agg, 4, true) {
		t.Fatal("premise broken: the size rule was expected to refuse")
	}
	tour := createPartialGroupingPaths(agg, 4, true, c19gParams())
	if tour == nil {
		t.Fatal("the priced model refused too — it has inherited the same hole")
	}
	if !tour.splitWins() {
		t.Errorf("the priced model declined a 5.9M-row/2-group aggregate "+
			"(split=%v nosplit=%v)", tour.split.Cost, tour.noSplit.Cost)
	}
}

// TestPartialAggModeLabelRoundTrips is flaglabels.go's contract: the token
// inside `unset(…)` re-exported verbatim must reproduce the arm.
func TestPartialAggModeLabelRoundTrips(t *testing.T) {
	for _, label := range []string{"off", "on"} {
		if got := partialAggModeLabel(partialAggModeFromEnv(label)); got != label {
			t.Errorf("%q round-tripped to %q", label, got)
		}
	}
	// Fail-closed: a typo must not enable the mode.
	for _, bogus := range []string{"", "ON!", "yes please", "true-ish"} {
		if partialAggModeFromEnv(bogus) != partialAggPathsOff {
			t.Errorf("%q enabled the mode; the switch is not fail-closed", bogus)
		}
	}
}

// TestPartialAggVerdictIsScaleFree pins the homogeneity that licenses
// `partialAggNotionalRows`.
//
// The difference between the two candidates' totals carries exactly one factor
// of the input row count, so scaling rows and distinct counts together must
// leave the verdict alone. If it does not, some term has lost its factor of R
// — and the blind arm, which substitutes a normalisation for a row count goopg
// often cannot see (`TableStats.RowCount` is not restored at startup, ledger
// pq-P6), would then be deciding on an invented number.
func TestPartialAggVerdictIsScaleFree(t *testing.T) {
	cp := c19gParams()
	for _, ratio := range []float64{1e-6, 1e-4, 1e-2, 0.2, 0.5, 0.9, 1.0} {
		for _, workers := range []int{1, 2, 4, 8} {
			var want bool
			for i, rows := range []int64{100_000, 1_000_000, 10_000_000, 100_000_000} {
				nd := ratio * float64(rows)
				if nd < 1 {
					nd = 1
				}
				tour := createPartialGroupingPaths(
					sizedAggFixture(t, rows, nd, 3, 1), workers, true, cp)
				if tour == nil {
					t.Fatalf("ratio=%g workers=%d rows=%d: no tournament", ratio, workers, rows)
				}
				got := tour.splitWins()
				if i == 0 {
					want = got
					continue
				}
				if got != want {
					t.Errorf("ratio=%g workers=%d: verdict changed with SCALE alone "+
						"(rows=%d gave %v, the 100k baseline gave %v) — a cost term "+
						"has lost its factor of R", ratio, workers, rows, got, want)
				}
			}
		}
	}
}

// TestPartialAggBlindArmMatchesTheSightedOne is the other half of the same
// property, at the seam where it is actually used: with no row count at all the
// producer normalises and takes rho from `NDistinctFrac`, and must reach the
// same verdict a sighted run reaches on the same shape.
func TestPartialAggBlindArmMatchesTheSightedOne(t *testing.T) {
	cp := c19gParams()
	const rows = 4_000_000
	for _, ratio := range []float64{1e-5, 1e-3, 0.05, 0.3, 0.8, 1.0} {
		for _, workers := range []int{1, 2, 4} {
			nd := ratio * rows
			sighted := createPartialGroupingPaths(sizedAggFixture(t, rows, nd, 3, 1), workers, true, cp)

			// The blind fixture keeps the FRACTION (which ANALYZE restores) and
			// drops the row count (which startup does not).
			blindAgg := sizedAggFixture(t, rows, nd, 3, 1)
			blindAgg.Child.(*SeqScan).Table.Stats.RowCount = 0
			blind := createPartialGroupingPaths(blindAgg, workers, true, cp)

			if sighted == nil || blind == nil {
				t.Fatalf("ratio=%g workers=%d: sighted=%v blind=%v", ratio, workers, sighted != nil, blind != nil)
			}
			if blind.inputRows != partialAggNotionalRows {
				t.Fatalf("ratio=%g: the blind fixture was not blind (R=%g)", ratio, blind.inputRows)
			}
			if got, want := blind.splitWins(), sighted.splitWins(); got != want {
				t.Errorf("ratio=%g workers=%d: blind verdict %v, sighted %v — the two "+
					"rho sources disagree about the same shape", ratio, workers, got, want)
			}
		}
	}
}
