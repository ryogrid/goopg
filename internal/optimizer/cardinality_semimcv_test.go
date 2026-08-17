package optimizer

// M0127-P5.6-g — `eqjoinsel_semi`'s MCV arm and the `(1 - nullfrac1)` factor
// (design leftdeep-joins/09 §5.7).
//
// The two audit violations that survived P5.6-f are both SEMI/ANTI joinrels
// (Q18's final SEMI 42 837× OVER, Q21's final ANTI 4 003× UNDER), and both come
// from the same property of the no-MCV heuristic: it can only return 1.0,
// `nd2/nd1` or 0.5. Once P5.6-e-iii made ndistinct truthful, `nd1 <= nd2` holds
// on every PK-FK-shaped semi-join, so the fraction is 1.0 — which makes SEMI
// the outer verbatim and ANTI floor at one row. Upstream never gets there on a
// skewed column, because it prices the MATCHED MCV mass exactly first and runs
// the heuristic only on the uncertain remainder.
//
// These tests pin the arm selection and the arithmetic of each branch; the
// end-to-end movement is the estimate-audit run recorded in 09 §5.7.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// mcvTable is statsTable with per-column MCV lists and null fractions — the
// two pg_statistic slots the plain helper flattens away.
func mcvTable(name string, rows int64, cols ...catalog.ColumnStats) *catalog.Table {
	stats := make([]catalog.ColumnStats, len(cols))
	columns := make([]catalog.Column, len(cols))
	copy(stats, cols)
	for i := range cols {
		columns[i] = catalog.Column{Name: "c", Type: catalog.Type{Name: "int4"}}
	}
	return &catalog.Table{
		Name:    name,
		Columns: columns,
		Stats:   &catalog.TableStats{RowCount: rows, Columns: stats},
	}
}

func mcvScan(name string, rows int64, cols ...catalog.ColumnStats) *SeqScan {
	tbl := mcvTable(name, rows, cols...)
	return &SeqScan{Table: tbl, schema: tableSchema(tbl)}
}

func mcvList(pairs ...any) []catalog.MCVEntry {
	out := make([]catalog.MCVEntry, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, catalog.MCVEntry{
			Value:     pairs[i].(string),
			Frequency: pairs[i+1].(float64),
		})
	}
	return out
}

func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// --- (1) the MCV arm is taken when both sides have a list ------------------

func TestSemiMatchFractionUsesMatchedMCVMass(t *testing.T) {
	// Outer: 4 MCVs, 0.4 of the relation, of which "a" and "b" (0.30) also
	// appear in the inner's list. nd1 = 100, nd2 = 20, so the heuristic alone
	// would have returned nd2/nd1 = 0.2 for the WHOLE relation.
	//
	// Upstream instead: matchfreq1 = 0.30 is exact, the remainder
	// uncertain = 1 - 0.30 - 0 = 0.70 is priced with the discounted counts
	// (nd1-2 = 98, nd2-2 = 18 → 18/98), and
	// selec = 0.30 + 0.70 · 18/98 = 0.428571…
	outer := mcvScan("o", 1000, catalog.ColumnStats{
		NDistinct: 100,
		MCV:       mcvList("a", 0.20, "b", 0.10, "y", 0.06, "z", 0.04),
	})
	inner := mcvScan("i", 500, catalog.ColumnStats{
		NDistinct: 20,
		MCV:       mcvList("a", 0.30, "b", 0.25, "q", 0.05),
	})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	want := 0.30 + 0.70*(18.0/98.0)
	if got := semiJoinMatchFraction(j, 500); !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v (matchfreq1 + uncertainfrac·uncertain)", got, want)
	}
	// And the estimate is the outer scaled by it — the property P5.6-e-ii
	// established, now with a fraction that is not on the 1.0/nd2-nd1 rails.
	if got, want := EstimateRows(j), int64(float64(1000)*want); got != want {
		t.Fatalf("semi join estimate = %d, want %d", got, want)
	}
}

func TestSemiMCVArmLiftsAntiOffItsFloor(t *testing.T) {
	// The Q21 shape: with a truthful ndistinct nd1 <= nd2 holds, the no-MCV
	// heuristic returns 1.0, and ANTI = outer · (1 - 1.0) floors at 1 row.
	// The matched MCV mass is the only measured evidence that can say
	// "0.55 of the outer matches", leaving 45 % as the anti-join's output.
	outerStats := catalog.ColumnStats{
		NDistinct: 50,
		MCV:       mcvList("a", 0.30, "b", 0.25, "c", 0.20),
	}
	innerStats := catalog.ColumnStats{
		NDistinct: 80,
		MCV:       mcvList("a", 0.50, "b", 0.50),
	}

	// Control: same ndistinct, no MCV lists → the old behaviour.
	plainOuter := scanWithStats("o", 1000, 50)
	plainInner := scanWithStats("i", 900, 80)
	plain := keyed(mergedJoin(JoinTypeAnti, plainOuter, plainInner), 0, 0)
	if got := EstimateRows(plain); got != 1 {
		t.Fatalf("control anti estimate = %d, want the 1-row floor the no-MCV arm produces", got)
	}

	outer := mcvScan("o", 1000, outerStats)
	inner := mcvScan("i", 900, innerStats)
	j := keyed(mergedJoin(JoinTypeAnti, outer, inner), 0, 0)

	// matchfreq1 = 0.55, uncertain = 0.45, and the discounted counts leave
	// rem1 = 48 <= rem2 = 78 → uncertainfrac 1.0, so selec = 1.0 and ANTI
	// would still floor. Verified explicitly: the arm has to price the
	// UNCERTAIN part, not the matched part, for ANTI to move.
	if got := semiJoinMatchFraction(j, 900); !closeTo(got, 1.0) {
		t.Fatalf("match fraction = %v, want 1.0 (uncertain remainder all assumed matched)", got)
	}

	// Now make the inner genuinely narrower than the outer, which is what an
	// anti-join's estimate turns on: nd2 = 10 < nd1 = 50.
	inner.Table.Stats.Columns[0].NDistinct = 10
	// rem1 = 48, rem2 = 8 → uncertainfrac = 8/48; selec = 0.55 + 0.45·(1/6).
	want := 0.55 + 0.45*(8.0/48.0)
	if got := semiJoinMatchFraction(j, 900); !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v", got, want)
	}
	if got, want := EstimateRows(j), int64(float64(1000)*(1-want)); got != want {
		t.Fatalf("anti estimate = %d, want %d (outer · (1 - selec))", got, want)
	}
}

func TestSemiMCVArmClampsInnerListToClampedNDistinct(t *testing.T) {
	// "The clamping above could have resulted in nd2 being less than
	// sslot2->nvalues; in which case, we assume that precisely the nd2 most
	// common values in the relation will appear in the join input."
	//
	// The inner has only 2 rows, so nd2 clamps to 2 and only the first TWO
	// entries of its 3-entry MCV list are eligible to match. "c" is therefore
	// unmatchable even though it is present in both lists.
	outer := mcvScan("o", 1000, catalog.ColumnStats{
		NDistinct: 100,
		MCV:       mcvList("c", 0.50),
	})
	inner := mcvScan("i", 2, catalog.ColumnStats{
		NDistinct: 3,
		MCV:       mcvList("a", 0.40, "b", 0.35, "c", 0.25),
	})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	// nmatches = 0, matchfreq1 = 0; nd2 clamped to 2 (and thus non-default),
	// rem1 = 100 > rem2 = 2 → 2/100; selec = 0 + 0.02·1.0.
	if got, want := semiJoinMatchFraction(j, 2), 0.02; !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v (inner MCV list truncated to nd2)", got, want)
	}
}

func TestSemiMCVArmMatchesEachInnerValueAtMostOnce(t *testing.T) {
	// Upstream: "we assume that each MCV will match at most one member of the
	// other MCV list". Two identical outer entries must not both consume the
	// single inner "a" and double-count its frequency.
	outer := mcvScan("o", 1000, catalog.ColumnStats{
		NDistinct: 100,
		MCV:       mcvList("a", 0.20, "a", 0.20),
	})
	inner := mcvScan("i", 500, catalog.ColumnStats{
		NDistinct: 100,
		MCV:       mcvList("a", 0.90),
	})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	// matchfreq1 = 0.20 (not 0.40); nmatches = 1; rem1 = 99 <= rem2 = 99 →
	// uncertainfrac 1.0; selec = 0.20 + 0.80 = 1.0. The value under test is
	// nmatches/matchfreq1, so assert through a case where they are visible:
	// give the outer a much larger nd so the remainder is not 1.0.
	outer.Table.Stats.Columns[0].NDistinct = 1000
	// rem1 = 999, rem2 = 99 → 99/999; selec = 0.20 + 0.80·(99/999).
	want := 0.20 + 0.80*(99.0/999.0)
	if got := semiJoinMatchFraction(j, 500); !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v (inner MCV consumed once)", got, want)
	}
}

func TestSemiMCVArmPuntsUncertainRemainderWithoutReliableNDistinct(t *testing.T) {
	// Both MCV lists present but the outer's ndistinct is unknown: upstream
	// still prices the matched mass exactly and punts to 0.5 on the rest.
	outer := mcvScan("o", 1000, catalog.ColumnStats{
		NDistinct: 0,
		MCV:       mcvList("a", 0.40),
	})
	inner := mcvScan("i", 5000, catalog.ColumnStats{
		NDistinct: 0,
		MCV:       mcvList("a", 0.10),
	})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	// matchfreq1 = 0.40, uncertain = 0.60, uncertainfrac = 0.5.
	if got, want := semiJoinMatchFraction(j, 5000), 0.40+0.30; !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v (exact matched mass + half the rest)", got, want)
	}
}

// --- (2) the (1 - nullfrac1) factor ----------------------------------------

func TestSemiMatchFractionDiscountsOuterNulls(t *testing.T) {
	// A NULL outer key matches nothing, so upstream multiplies every branch by
	// (1 - nullfrac1). nd1 = 100 <= nd2 = 400 is the "all rows match" branch,
	// which without the factor returned 1.0 for a column that is 25 % NULL.
	outer := mcvScan("o", 1000, catalog.ColumnStats{NDistinct: 100, NullFrac: 0.25})
	inner := mcvScan("i", 500, catalog.ColumnStats{NDistinct: 400})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	if got, want := semiJoinMatchFraction(j, 500), 0.75; !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v (1 - nullfrac1)", got, want)
	}
	if got, want := EstimateRows(j), int64(750); got != want {
		t.Fatalf("semi estimate = %d, want %d", got, want)
	}
}

func TestSemiMatchFractionDiscountsNullsInTheNdRatioBranch(t *testing.T) {
	// nd1 = 1000 > nd2 = 100 → (nd2/nd1)·(1 - nullfrac1) = 0.1 · 0.9.
	outer := mcvScan("o", 1000, catalog.ColumnStats{NDistinct: 1000, NullFrac: 0.10})
	inner := mcvScan("i", 500, catalog.ColumnStats{NDistinct: 100})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	if got, want := semiJoinMatchFraction(j, 500), 0.09; !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v", got, want)
	}
}

func TestSemiMatchFractionDiscountsNullsInThePunt(t *testing.T) {
	// Upstream applies the factor to the 0.5 punt too: 0.5 · (1 - 0.2).
	outer := mcvScan("o", 1000, catalog.ColumnStats{NDistinct: 1000, NullFrac: 0.20})
	inner := mcvScan("i", 5000, catalog.ColumnStats{NDistinct: 0})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	if got, want := semiJoinMatchFraction(j, 5000), 0.40; !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v (0.5 punt is null-discounted too)", got, want)
	}
}

func TestSemiMCVArmSubtractsNullsFromTheUncertainMass(t *testing.T) {
	// In the MCV arm the factor enters as `uncertain = 1 - matchfreq1 -
	// nullfrac1` rather than as a multiplier: the matched MCV mass is a
	// measured frequency and is NOT discounted a second time.
	outer := mcvScan("o", 1000, catalog.ColumnStats{
		NDistinct: 1000,
		NullFrac:  0.20,
		MCV:       mcvList("a", 0.30),
	})
	inner := mcvScan("i", 500, catalog.ColumnStats{
		NDistinct: 100,
		MCV:       mcvList("a", 0.60),
	})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	// matchfreq1 = 0.30, uncertain = 1 - 0.30 - 0.20 = 0.50,
	// rem1 = 999 > rem2 = 99 → 99/999.
	want := 0.30 + 0.50*(99.0/999.0)
	if got := semiJoinMatchFraction(j, 500); !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v", got, want)
	}
}

// --- (3) the resolver the arm reads through --------------------------------

func TestSemiMCVArmResolvesStatsThroughAnIndexScan(t *testing.T) {
	// `columnStatsForChild` — the MCV resolver every other caller uses — has
	// no *IndexScan arm, so reading MCVs through it would make a semi-join's
	// match fraction depend on which scan the planner picked. This arm goes
	// through `resolveBaseColumn`, which resolves both.
	inner := &IndexScan{Table: mcvTable("i", 500, catalog.ColumnStats{
		NDistinct: 20,
		MCV:       mcvList("a", 0.30, "b", 0.25),
	})}
	inner.schema = tableSchema(inner.Table)
	outer := mcvScan("o", 1000, catalog.ColumnStats{
		NDistinct: 100,
		MCV:       mcvList("a", 0.20, "b", 0.10),
	})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	if st := rightExprStats(j, j.RightKey); st == nil || len(st.MCV) != 2 {
		t.Fatalf("rightExprStats did not resolve the inner *IndexScan's MCV list: %+v", st)
	}
	// matchfreq1 = 0.30, uncertain = 0.70, rem1 = 98 > rem2 = 18.
	want := 0.30 + 0.70*(18.0/98.0)
	if got := semiJoinMatchFraction(j, 500); !closeTo(got, want) {
		t.Fatalf("match fraction = %v, want %v", got, want)
	}
}

func TestSemiMCVArmRightStatsUseTheMergedCoordinateShift(t *testing.T) {
	// The right operand's Index counts from the start of the merged left‖right
	// schema. Reading nd from one column and the MCV list of another is the
	// P5.6-e-ii/-e-iii defect class; this pins that both lookups land on the
	// SAME inner column.
	outer := mcvScan("o", 1000,
		catalog.ColumnStats{NDistinct: 100},
		catalog.ColumnStats{NDistinct: 7},
	)
	inner := mcvScan("i", 500,
		catalog.ColumnStats{NDistinct: 11, MCV: mcvList("x", 0.10)},
		catalog.ColumnStats{NDistinct: 22, MCV: mcvList("y", 0.20)},
	)
	// Right operand = inner column 1 → merged index 2 + 1 = 3.
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 1)

	st := rightExprStats(j, j.RightKey)
	if st == nil || st.NDistinct != 22 {
		t.Fatalf("rightExprStats resolved the wrong inner column: %+v", st)
	}
	if int64(rightExprNDistinct(j, j.RightKey)) != st.NDistinct {
		t.Fatalf("nd lookup (%d) and stats lookup (%d) disagree about the column",
			rightExprNDistinct(j, j.RightKey), st.NDistinct)
	}
	if len(st.MCV) != 1 || st.MCV[0].Value != "y" {
		t.Fatalf("rightExprStats returned the wrong MCV list: %+v", st.MCV)
	}
}

// --- (4) clampProbability ---------------------------------------------------

func TestClampProbabilityPinsOutOfRangeInputs(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{-0.5, 0}, {0, 0}, {0.25, 0.25}, {1, 1}, {1.5, 1},
		{math.NaN(), 0},
	}
	for _, c := range cases {
		if got := clampProbability(c.in); !closeTo(got, c.want) {
			t.Fatalf("clampProbability(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSemiMCVArmClampsAnOverfullOuterMCVMass(t *testing.T) {
	// Stale statistics can leave the MCV frequencies summing past 1. Without
	// CLAMP_PROBABILITY, `uncertain` goes negative and the match fraction
	// comes out ABOVE the matched mass it was supposed to bound.
	outer := mcvScan("o", 1000, catalog.ColumnStats{
		NDistinct: 100,
		MCV:       mcvList("a", 0.80, "b", 0.80),
	})
	inner := mcvScan("i", 500, catalog.ColumnStats{
		NDistinct: 20,
		MCV:       mcvList("a", 0.50, "b", 0.50),
	})
	j := keyed(mergedJoin(JoinTypeSemi, outer, inner), 0, 0)

	got := semiJoinMatchFraction(j, 500)
	if !closeTo(got, 1.0) {
		t.Fatalf("match fraction = %v, want 1.0 (matchfreq1 clamped, uncertain clamped to 0)", got)
	}
	if got, want := EstimateRows(j), int64(1000); got != want {
		t.Fatalf("semi estimate = %d, want %d (never above the outer)", got, want)
	}
}
