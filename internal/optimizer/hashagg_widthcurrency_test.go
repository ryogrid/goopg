package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/executor/hashsize"
)

// R120 pins for the HashAggregate spill arm's byte currency. See
// docs/design/not_ralph/plan_parity_fix_take2/r120-hashagg-width-currency/SCOPE.md.

// widthCurrencyCostParams gives the arm a real budget to reason about. The
// 128 MiB figure is the bench budget the round pins: work_mem 64MB x
// hash_mem_multiplier 2 (SCOPE §3).
func widthCurrencyCostParams() costParams {
	cp := defaultCostParams()
	cp.workMem = 128 * 1024 * 1024
	return cp
}

// A grouping wide enough that the corrected currency changes the verdict.
// These are TPC-H Q10's MEASURED arm inputs (R120 diagnostic dump, since
// removed), not estimates: 37 columns is its join input (lineitem 16 +
// orders 9 + customer 8 + nation 4) and avgVar is the concatenated variable
// payload. SCOPE §4a had back-solved 334 for avgVar and was wrong by 6.2x —
// the real figure is what tips Q10 from 93% to 171% of the hash budget.
const (
	wcNCols  = 37
	wcAvgVar = 2080.0
)

func TestHashAggTupleWidthIsEntryBytesMinusRowSlice(t *testing.T) {
	// Arm B's mapping: PG hands hash_agg_entry_size a BARE width and that
	// function adds its own header, so goopg must not also pass its 24-byte
	// RowSliceBytes or the header is double-counted. The identity below is
	// what keeps Arm A and Arm B mutually consistent.
	got := hashAggTupleWidth(wcNCols, wcAvgVar)
	want := hashsize.EntryBytes(wcNCols, wcAvgVar) - hashsize.RowSliceBytes
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("hashAggTupleWidth = %v, want EntryBytes-RowSliceBytes = %v", got, want)
	}
	// And it is the plain 48*ncols + avgVar the SCOPE names.
	if plain := float64(wcNCols)*hashsize.DatumBytes + wcAvgVar; math.Abs(got-plain) > 1e-9 {
		t.Fatalf("hashAggTupleWidth = %v, want 48*ncols+avgVar = %v", got, plain)
	}
}

func TestHashAggTupleWidthClampsNegatives(t *testing.T) {
	if got := hashAggTupleWidth(-3, -7); got != 0 {
		t.Fatalf("negative inputs must clamp to 0, got %v", got)
	}
}

func TestHashAggWidthCurrencyFlagIsStrictAndDefaultOff(t *testing.T) {
	// Mirrors R113's GOOPG_PG_SORT_RELATION_BYTES_COST: only "1" elects.
	for _, v := range []string{"", "0", "true", "TRUE", "yes", "on", " 1", "1 ", "01"} {
		if hashAggWidthCurrencyFromEnv(v) {
			t.Errorf("value %q must NOT enable the corrected currency", v)
		}
	}
	if !hashAggWidthCurrencyFromEnv("1") {
		t.Error(`"1" must enable the corrected currency`)
	}
}

// TestHashAggWidthCurrencyOffIsBitIdentical is the P0 pin: with the flag off,
// costAgg must reproduce the pre-R120 arithmetic exactly — the old currency
// was bare inAvgVarBytes at both sites.
func TestHashAggWidthCurrencyOffIsBitIdentical(t *testing.T) {
	defer setHashAggWidthCurrencyForTest(false)()
	cp := widthCurrencyCostParams()

	// Large enough that the arm would fire under the corrected currency.
	const rows, groups = 2_000_000.0, 400_000.0
	got := costAgg(cp, AggStrategyHashed, rows, 0, 1000, 3, groups, 1, wcNCols, wcAvgVar)

	// Recompute the legacy arithmetic independently, in the old currency.
	entry := hashAggEntrySize(1, wcAvgVar)
	memLimit, ngroupsLimit, numPartitions := hashAggSetLimits(cp, entry, groups)
	nbatches := math.Max(math.Ceil(math.Max(groups*entry/memLimit, groups/ngroupsLimit)), 1)
	if numPartitions < 2 {
		numPartitions = 2
	}
	depth := math.Ceil(math.Log(nbatches) / math.Log(float64(numPartitions)))
	want := costAggNoSpillBaseline(cp, rows, 1000, 3, groups)
	if depth > 0 {
		pages := rows * wcAvgVar / float64(blockSizeBytes)
		written := pages * depth * 2.0
		read := pages * depth * 2.0
		spillCPU := depth * rows * 2.0 * cp.cpuTupleCost
		want.Startup += written*cp.randomPageCost + spillCPU
		want.Total += written*cp.randomPageCost + read*cp.seqPageCost + spillCPU
	}
	// Guard against this pin degenerating into a tautology: if the arm stops
	// firing (a changed DatumBytes, hashAggEntrySize, or wcAvgVar), `want`
	// collapses to the arm-free baseline and the comparison proves nothing.
	if depth <= 0 {
		t.Fatalf("spill arm did not fire (depth=%v) — this pin no longer tests the OFF currency; "+
			"raise groups/rows or lower the budget", depth)
	}
	if base := costAggNoSpillBaseline(cp, rows, 1000, 3, groups); math.Abs(want.Total-base.Total) < 1e-6 {
		t.Fatal("legacy expectation equals the arm-free baseline — the arm contributed nothing")
	}
	if math.Abs(got.Total-want.Total) > 1e-6 || math.Abs(got.Startup-want.Startup) > 1e-6 {
		t.Fatalf("OFF arm drifted from legacy arithmetic:\n got  %+v\n want %+v", got, want)
	}
}

// costAggNoSpillBaseline is the arm-free portion, obtained by asking costAgg
// itself for a grouping the spill arm cannot reach (avgVar 0 keeps the legacy
// gate shut, and ncols 0 keeps the corrected gate shut too).
func costAggNoSpillBaseline(cp costParams, rows, inputTotal float64, nGroupCols int, groups float64) Cost {
	return costAgg(cp, AggStrategyHashed, rows, 0, inputTotal, nGroupCols, groups, 1, 0, 0)
}

// TestHashAggWidthCurrencyOnRaisesWideGroupingCost is the core behavioural
// pin: on a WIDE input the corrected currency must charge strictly more,
// because 48*ncols dominates the variable payload.
func TestHashAggWidthCurrencyOnRaisesWideGroupingCost(t *testing.T) {
	cp := widthCurrencyCostParams()
	const rows, groups = 2_000_000.0, 400_000.0

	restore := setHashAggWidthCurrencyForTest(false)
	off := costAgg(cp, AggStrategyHashed, rows, 0, 1000, 3, groups, 1, wcNCols, wcAvgVar)
	restore()

	defer setHashAggWidthCurrencyForTest(true)()
	on := costAgg(cp, AggStrategyHashed, rows, 0, 1000, 3, groups, 1, wcNCols, wcAvgVar)

	if !(on.Total > off.Total) {
		t.Fatalf("corrected currency must cost more on a wide grouping: off=%v on=%v", off.Total, on.Total)
	}
}

// TestHashAggWidthCurrencyGateOpensOnFixedWidthInput is Arm C: an input with
// no variable payload still has a real 48*ncols+24 footprint. Under the legacy
// gate the arm was skipped outright, pricing hash as if it always fits.
func TestHashAggWidthCurrencyGateOpensOnFixedWidthInput(t *testing.T) {
	cp := widthCurrencyCostParams()
	// Enough groups of a fixed-width row to exceed the budget once the real
	// footprint is counted.
	const rows, groups = 4_000_000.0, 2_000_000.0

	restore := setHashAggWidthCurrencyForTest(false)
	off := costAgg(cp, AggStrategyHashed, rows, 0, 1000, 2, groups, 1, wcNCols, 0)
	restore()

	defer setHashAggWidthCurrencyForTest(true)()
	on := costAgg(cp, AggStrategyHashed, rows, 0, 1000, 2, groups, 1, wcNCols, 0)

	if on.Total <= off.Total {
		t.Fatalf("fixed-width input must reach the arm under Arm C: off=%v on=%v", off.Total, on.Total)
	}
}

// TestHashAggWidthCurrencyPlainArmStaysInert is SCOPE §6 / note 4: the PLAIN
// (ungrouped) aggregate is priced through AggStrategyHashed with numGroups=1
// (groupingpaths.go:367-370). Arm C opens the gate for it too, so pin that a
// single group still cannot spill — otherwise Q6/Q14 would move.
func TestHashAggWidthCurrencyPlainArmStaysInert(t *testing.T) {
	cp := widthCurrencyCostParams()
	const rows = 6_000_000.0

	restore := setHashAggWidthCurrencyForTest(false)
	off := costAgg(cp, AggStrategyHashed, rows, 0, 1000, 0, 1, 1, wcNCols, wcAvgVar)
	restore()

	defer setHashAggWidthCurrencyForTest(true)()
	on := costAgg(cp, AggStrategyHashed, rows, 0, 1000, 0, 1, 1, wcNCols, wcAvgVar)

	if math.Abs(on.Total-off.Total) > 1e-9 || math.Abs(on.Startup-off.Startup) > 1e-9 {
		t.Fatalf("plain (1-group) aggregate must stay inert: off=%+v on=%+v", off, on)
	}
}

// TestHashAggWidthCurrencySortedRivalUntouched pins that the cut is confined
// to the hashed arm: the sorted strategy's price must not move.
func TestHashAggWidthCurrencySortedRivalUntouched(t *testing.T) {
	cp := widthCurrencyCostParams()
	const rows, groups = 2_000_000.0, 400_000.0

	restore := setHashAggWidthCurrencyForTest(false)
	off := costAgg(cp, AggStrategySorted, rows, 0, 1000, 3, groups, 1, wcNCols, wcAvgVar)
	restore()

	defer setHashAggWidthCurrencyForTest(true)()
	on := costAgg(cp, AggStrategySorted, rows, 0, 1000, 3, groups, 1, wcNCols, wcAvgVar)

	if math.Abs(on.Total-off.Total) > 1e-9 {
		t.Fatalf("sorted rival must be untouched: off=%v on=%v", off.Total, on.Total)
	}
}
