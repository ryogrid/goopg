package optimizer

// B-04 (take3 P1-21): `max(outer,inner)` fallback cap — verify-and-keep.
//
// The cap lives in `estimateJoin`'s unmeasurable fallback
// (cardinality.go:655-666). These tests pin its exact guard conditions and
// prove the P1-21 precondition claim: P1-15's MCV arm (eqjoinselInnerMCV)
// cannot reach the fallback, so deleting the cap would move big unmeasurable
// joins from `min(l*r*0.005, max(l,r))` to `l*r*0.005` with no evidence the
// backstop is unneeded. Verdict: KEEP.
//
// Guard, read off estimateJoin (cardinality.go:579-670):
//   - Cross / Semi / Anti return before the fallback (lines 585-602).
//   - Hash / Merge with ANY measured pair returns via the measured arm
//     (lines 603-653): superkey prover fired, or an MCV hit, or an nd > 0.
//   - Everything else falls through: non-hash/merge algos unconditionally,
//     hash/merge only when NO pair measured anything.
//   - The cap itself fires iff l*r*0.005 > max(l,r), i.e. min(l,r) > 200,
//     with a floor at 1 below.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// capScan builds a stats-bearing SeqScan like scanWithStats; nd <= 0 means
// "ndistinct unavailable" (ResolvedNDistinct resolves NDistinct:-1 to 0, the
// same unknown the unanalysed state produces).
func capScan(name string, rows, nd int64) *SeqScan {
	tbl := statsTable(name, rows, nd)
	return &SeqScan{Table: tbl, schema: tableSchema(tbl)}
}

// capMCVScan is capScan with an MCV list on the single column.
func capMCVScan(name string, rows, nd int64, mcv []catalog.MCVEntry) *SeqScan {
	s := capScan(name, rows, nd)
	s.Table.Stats.Columns[0].MCV = mcv
	return s
}

// uncappedFallback is the fallback product before the cap/floor.
func uncappedFallback(l, r int64) float64 {
	return float64(l) * float64(r) * defaultEqSelectivity
}

// TestFallbackCapFiresOnLargeUnmeasurableJoin pins the cap's firing case: a
// hash inner join with no usable ndistinct on either side, no MCV lists, and
// no proven key estimates min(l*r*0.005, max(l,r)).
func TestFallbackCapFiresOnLargeUnmeasurableJoin(t *testing.T) {
	l := capScan("l", 6000000, -1)
	r := capScan("r", 800000, -1)
	j := keyed(mergedJoin(JoinTypeInner, l, r), 0, 0)

	// "No key was proven" half of the guard, asserted directly.
	if sk := superkeyJoinEstimate(j, joinEquiPairs(j)); sk.fired {
		t.Fatal("superkey prover fired on keyless scans; the fallback precondition is not what this test assumes")
	}
	// "P1-15 cannot reach it" half: the MCV arm declines with no lists.
	if _, ok := eqjoinselInnerMCV(j, joinEquiPairs(j)[0]); ok {
		t.Fatal("MCV arm fired with no MCV lists on either side")
	}
	if nd := pairNDistinct(j, joinEquiPairs(j)[0]); nd > 0 {
		t.Fatalf("pairNDistinct = %d, want <= 0 (no usable ndistinct)", nd)
	}

	if uncapped := uncappedFallback(6000000, 800000); uncapped <= 6000000 {
		t.Fatalf("test setup wrong: uncapped %v does not exceed max(l,r); the cap would be a no-op", uncapped)
	}
	if got, want := EstimateRows(j), int64(6000000); got != want {
		t.Fatalf("EstimateRows = %d, want %d (min(l*r*0.005, max(l,r)))", got, want)
	}
}

// TestFallbackCapShapePinsThreshold pins the whole min/product/floor shape,
// including the strict `est > mx` edge: the cap fires iff min(l,r) > 200.
func TestFallbackCapShapePinsThreshold(t *testing.T) {
	cases := []struct {
		name string
		l, r int64
		want int64
	}{
		// l*r*0.005 below max: no cap.
		{"small inputs untruncated", 100, 100, 50},
		// min(l,r) = 200: product equals max; the cap is a no-op either way.
		{"boundary min=200", 200, 100000, 100000},
		// min(l,r) = 201: product exceeds max by 500; the cap truncates.
		{"just over min=200", 201, 100000, 100000},
		// Large: product dwarfs max.
		{"large inputs capped", 6000000, 800000, 6000000},
		// Product below one row: floor, not zero.
		{"floor at one", 10, 10, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := keyed(mergedJoin(JoinTypeInner, capScan("l", tc.l, -1), capScan("r", tc.r, -1)), 0, 0)
			if got := EstimateRows(j); got != tc.want {
				t.Errorf("EstimateRows(%d, %d) = %d, want %d", tc.l, tc.r, got, tc.want)
			}
		})
	}
	// The "just over" row must actually exercise the cap: its uncapped
	// product (100500) differs from the capped answer (100000).
	if got := uncappedFallback(201, 100000); got <= 100000 {
		t.Fatalf("uncapped(201, 100000) = %v, want > max so the test proves truncation", got)
	}
}

// TestP1_15MCVTakesMeasuredPathNotFallback is the precondition proof, firing
// direction: the same big-input shape that the fallback would cap takes the
// measured arm instead as soon as P1-15's MCV branch has both lists, so the
// cap never sees it.
func TestP1_15MCVTakesMeasuredPathNotFallback(t *testing.T) {
	skew := []catalog.MCVEntry{{Value: "7", Frequency: 0.8}}
	l := capMCVScan("l", 10000, 100, skew)
	r := capMCVScan("r", 10000, 100, skew)
	j := keyed(mergedJoin(JoinTypeInner, l, r), 0, 0)

	sel, ok := eqjoinselInnerMCV(j, joinEquiPairs(j)[0])
	if !ok {
		t.Fatal("both sides carry MCV lists with usable nd; the MCV branch must fire")
	}
	got := EstimateRows(j)
	// The fallback would answer max(l,r) = 10000. The measured arm answers
	// l*r*sel with sel >= matchprodfreq 0.64 — orders of magnitude above the
	// cap, proving this shape never reaches the fallback.
	if got <= 10000 {
		t.Fatalf("EstimateRows = %d, want >> max(l,r)=10000 (measured MCV path, not the capped fallback)", got)
	}
	if want := int64(float64(10000) * float64(10000) * sel); got != want {
		t.Fatalf("EstimateRows = %d, want %d (l*r*mcvSel, residual-free inner join)", got, want)
	}
}

// TestP1_15UnreachableFromFallback is the precondition proof, declining
// direction: MCV lists ALONE do not move the fallback. The MCV arm also
// requires nd > 0 on both sides (cardinality.go:1493-1497), so lists without
// nd still decline and the cap still fires — the fallback output is invariant
// to MCV presence whenever P1-15 declines.
func TestP1_15UnreachableFromFallback(t *testing.T) {
	skew := []catalog.MCVEntry{{Value: "7", Frequency: 0.8}}
	l := capMCVScan("l", 6000000, -1, skew)
	r := capMCVScan("r", 800000, -1, skew)
	j := keyed(mergedJoin(JoinTypeInner, l, r), 0, 0)

	if _, ok := eqjoinselInnerMCV(j, joinEquiPairs(j)[0]); ok {
		t.Fatal("MCV arm fired without usable ndistinct; it must require nd > 0 on both sides")
	}
	if got, want := EstimateRows(j), int64(6000000); got != want {
		t.Fatalf("EstimateRows = %d, want %d (MCV lists without nd still take the capped fallback)", got, want)
	}
}

// TestFallbackCapFiresForNonHashAlgoDespiteStats pins the algo half of the
// guard: a nested-loop inner join skips the measured arm entirely
// (cardinality.go:603), so even fully measured ndistincts fall back and cap.
// The identical hash join prices l*r/max(nd) with no cap (existing
// TestEstimateJoinCapFallback case 4 shape).
func TestFallbackCapFiresForNonHashAlgoDespiteStats(t *testing.T) {
	mk := func() *Join {
		j := keyed(mergedJoin(JoinTypeInner, capScan("l", 10000, 100), capScan("r", 50000, 500)), 0, 0)
		return j
	}
	hash := mk()
	if got, want := EstimateRows(hash), int64(1000000); got != want {
		t.Fatalf("hash join = %d, want %d (l*r/max(nd), cap must NOT fire)", got, want)
	}
	nl := mk()
	nl.Algo = JoinAlgoNestedLoop
	// Fallback: 10000*50000*0.005 = 2.5M, capped at max = 50000.
	if got, want := EstimateRows(nl), int64(50000); got != want {
		t.Fatalf("nested-loop join = %d, want %d (unmeasured algo: capped fallback despite stats)", got, want)
	}
}

// TestFallbackNeedsEveryPairUnmeasured pins the quantifier in the guard: ONE
// measured pair pulls the whole join onto the measured arm, where the
// remaining unmeasurable pair multiplies defaultEqSelectivity into the
// selectivity (cardinality.go:629-634) instead of triggering the cap.
func TestFallbackNeedsEveryPairUnmeasured(t *testing.T) {
	mkSide := func(name string) *SeqScan {
		tbl := statsTable(name, 1000000, 1000, -1)
		return &SeqScan{Table: tbl, schema: tableSchema(tbl)}
	}
	j := keyedPairs(mergedJoin(JoinTypeInner, mkSide("l"), mkSide("r")), [2]int{0, 0}, [2]int{1, 1})

	pairs := joinEquiPairs(j)
	if nd := pairNDistinct(j, pairs[0]); nd != 1000 {
		t.Fatalf("pair0 nd = %d, want 1000 (measured)", nd)
	}
	if nd := pairNDistinct(j, pairs[1]); nd > 0 {
		t.Fatalf("pair1 nd = %d, want <= 0 (unmeasurable)", nd)
	}
	// Measured: 1e12 * (1/1000 * 0.005) = 5M. The cap would answer max = 1M.
	if got, want := EstimateRows(j), int64(5000000); got != want {
		t.Fatalf("EstimateRows = %d, want %d (one measured pair defeats the fallback; no cap)", got, want)
	}
}
