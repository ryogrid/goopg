package planner

// M0127-P5.9-j — the plain nested loop's per-tuple CPU charge rides the number
// of pairs it PROCESSES, not the number it emits.
//
// PG writes the two as one line and comments the distinction in place
// (final_cost_nestloop, costsize.c): `ntuples = outer_path_rows *
// inner_path_rows` is annotated "Compute number of tuples processed (not number
// emitted!)", and `cpu_per_tuple = cpu_tuple_cost + restrict_qual_cost.per_tuple`
// is charged on it. goopg splits the sum — the qual half is the caller's
// `qualEvalCost` — and the `cpu_tuple_cost` half used to land on the join's
// OUTPUT rows.
//
// That error is only visible where it does the most damage. A nested loop is
// preferred exactly when its output is small, so an output-rows charge is
// smallest on the plans the term exists to deter: two large inputs whose join
// estimate has collapsed. Q47 is the observed case — three CTE self-scans, four
// stats-less equalities, so the outer joinrel sizes to 1 row — and the NL beat
// the hash by 0.02 on a total of ~968 before rescanning 7 193 rows per outer row
// at runtime (8 m 40 s against 11-13 s).

import "testing"

// nlCollapsedPair builds the Q47 shape at path level, with the numbers the
// search really produced for `{v1,v1_lag} ⋈ v1_lead` (SF0.5): the OUTER is the
// already-collapsed joinrel — one estimated row but a path total of 669.66,
// because building it was not free — and the inner is a 7 193-row CTE scan at
// 226.93. The joinrel collapses to one row again.
//
// Both arms are offered the way `addPathsToJoinrel` offers them, including BOTH
// hash orientations: the orientation that wins hashes the one-row side, and a
// fixture that only ever builds the large side would prove nothing about which
// method the search picks.
func nlCollapsedPair(t *testing.T, innerRows float64) *RelOptInfo {
	t.Helper()
	cp := defaultCostParams()
	outer := relWithScanCost(RelSet(0b01), 1, 669.66)
	inner := relWithScanCost(RelSet(0b10), innerRows, 226.93)
	// The collapsed estimate: four independent equalities on stats-less inputs.
	joinRel := newRelOptInfo(RelSet(0b11), 1, 80)
	keys := []*restrictInfo{{}, {}, {}, {}}
	generateHashJoinPaths(joinRel, outer, inner, cp, keys, nil)
	addNestLoopPath(joinRel, outer, inner, cp, keys)
	setCheapest(joinRel)
	return joinRel
}

// TestNestLoopChargesTuplesProcessedNotEmitted is the direct statement of the
// fix: with the join's output collapsed to one row, the loop must still be
// charged for every pair it walks, so the hash path wins.
//
// Asserting on the CHEAPEST path rather than on a cost number is deliberate —
// the plan is the thing that regressed, and a cost assertion would have to be
// re-pinned by every unrelated tweak to the shared constants. The margin this
// guards is real but thin: charged on output rows the loop came to 968.94
// against the hash's 968.96, so it won by 0.02 and then ran for 8 m 40 s.
func TestNestLoopChargesTuplesProcessedNotEmitted(t *testing.T) {
	joinRel := nlCollapsedPair(t, 7193)
	if joinRel.CheapestTotal == nil {
		t.Fatalf("no path survived")
	}
	if got := joinRel.CheapestTotal.Kind; got != PathHashJoin {
		t.Fatalf("cheapest path is %v; a nested loop rescanning 7193 inner rows must not "+
			"undercut a hash join just because the join estimate collapsed to 1 row", got)
	}
}

// TestNestLoopCollapsedPairMarginIsNotAFluke checks the same fixture the other
// way round: at a SMALL inner the loop is genuinely competitive, so the test
// above is measuring the tuple count and not a blanket preference for hashing.
func TestNestLoopCollapsedPairMarginIsNotAFluke(t *testing.T) {
	big := nlCollapsedPair(t, 7193).CheapestTotal
	small := nlCollapsedPair(t, 2).CheapestTotal
	if big.Cost.Total <= small.Cost.Total {
		t.Fatalf("a 7193-row inner (%v) must cost more than a 2-row one (%v)", big.Cost.Total, small.Cost.Total)
	}
}

// TestNestLoopTupleChargeScalesWithBothSides pins the shape of the term rather
// than one plan: the charge is a product, so doubling EITHER input's row count
// doubles it. Charged on output rows — which the fixture holds fixed at 1 — the
// cost would not move at all.
func TestNestLoopTupleChargeScalesWithBothSides(t *testing.T) {
	cp := defaultCostParams()
	// Rescan cost zero, so the only term that varies is the per-tuple charge.
	base := nestloopCost(cp, Cost{}, Cost{}, 100, 100, 0).Total
	wideOuter := nestloopCost(cp, Cost{}, Cost{}, 200, 100, 0).Total
	wideInner := nestloopCost(cp, Cost{}, Cost{}, 100, 200, 0).Total

	if want := 2 * base; wideOuter != want {
		t.Errorf("doubling the outer should double the tuple charge: got %v, want %v", wideOuter, want)
	}
	if want := 2 * base; wideInner != want {
		t.Errorf("doubling the inner should double the tuple charge: got %v, want %v", wideInner, want)
	}
	if want := cp.cpuTupleCost * 100 * 100; base != want {
		t.Errorf("tuple charge = cpu_tuple_cost * outerRows * innerRows: got %v, want %v", base, want)
	}
}

// TestNestLoopClampsZeroRowSides is PG's guard at the top of
// final_cost_nestloop: a zero path row count must not zero the whole per-tuple
// charge. It floors each side at one tuple, which is the difference between "a
// loop that processes one pair" and "a loop that is free".
func TestNestLoopClampsZeroRowSides(t *testing.T) {
	cp := defaultCostParams()
	one := cp.cpuTupleCost
	if got := nestloopCost(cp, Cost{}, Cost{}, 0, 0, 0).Total; got != one {
		t.Errorf("both sides zero should still cost one tuple: got %v, want %v", got, one)
	}
	if got := nestloopCost(cp, Cost{}, Cost{}, 0, 50, 0).Total; got != 50*cp.cpuTupleCost {
		t.Errorf("a zero outer must not zero the inner's tuples: got %v, want %v", got, 50*cp.cpuTupleCost)
	}
}

// TestNestLoopSurvivesForAClauselessPair is the counterweight, and it guards the
// invariant the whole arm exists for. A pair with no usable equality gets no
// hash path at all, so the nested loop is the ONLY path — and `joinSearch`
// treats an empty pathlist as a hard failure (joinsearchlevel.go). Making the
// loop dearer must never make it absent.
func TestNestLoopSurvivesForAClauselessPair(t *testing.T) {
	cp := defaultCostParams()
	outer := relWithScanCost(RelSet(0b01), 1000, 50)
	inner := relWithScanCost(RelSet(0b10), 1000, 50)
	joinRel := newRelOptInfo(RelSet(0b11), 1000000, 80)
	// A cartesian pair: no keys, so addPathsToJoinrel would skip the hash arm.
	addNestLoopPath(joinRel, outer, inner, cp, nil)
	setCheapest(joinRel)

	if joinRel.CheapestTotal == nil || joinRel.CheapestTotal.Kind != PathNestLoop {
		t.Fatalf("a clauseless pair must still get its nested loop; got %+v", joinRel.CheapestTotal)
	}
	// And it is charged for the whole cross product it walks, which is what
	// makes the search avoid the order rather than treat it as free.
	if want := cp.cpuTupleCost * 1000 * 1000; joinRel.CheapestTotal.Cost.Total <= want {
		t.Fatalf("cartesian loop total %v should exceed the %v tuple charge alone",
			joinRel.CheapestTotal.Cost.Total, want)
	}
}
