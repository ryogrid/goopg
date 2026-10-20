package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0146-0214: initial_cost_mergejoin + final_cost_mergejoin charge an
// input's skipped prefix (the rows below the other side's minimum) at the
// join's startup — its share of the input's run and the merge quals evaluated
// on it. PG 18.3 on TPC-DS SF1:
//
//	Merge Join (cost=389569.93..)  customer_demographics ⋈ store_sales
//	  -> Index Only Scan customer_demographics_pkey (cost=0.43..49916.43 rows=1920800)
//	  -> Sort (cost=389568.13..396768.01 rows=2879953)
//
// with store_sales' start fraction 9.545e-5 (scalarineqsel(ss_cdemo_sk <
// 254), clamped to the histogram cutoff, times 1 - nullfrac): 275 rows skipped,
// 0.69 of the Sort's run and 0.69 of merge-qual CPU at startup.
func TestMergeJoinChargesSkippedPrefixAtStartup(t *testing.T) {
	cp := defaultCostParams()
	outer := Cost{Startup: 0.43, Total: 49916.43}
	inner := Cost{Startup: 389568.13, Total: 396768.01}
	sel := mergeScanSel{outerStart: 0, outerEnd: 1, innerStart: 1e-4 * (1 - 0.045533333), innerEnd: 1}
	mi := mergeInner{qualOps: 1, sorted: true, markRestore: true, skipMarkRestore: true}
	got, _ := mergeJoinCost(cp, outer, inner, 1920800, 2879953, 2748819, sel, mi)
	if math.Abs(got.Startup-389569.93) > 0.01 {
		t.Fatalf("merge join startup %.4f, want PG's 389569.93", got.Startup)
	}
	// The full-scan default charges no prefix: outer + inner startup only.
	full, _ := mergeJoinCost(cp, outer, inner, 1920800, 2879953, 2748819, fullMergeScan, mi)
	if math.Abs(full.Startup-(0.43+389568.13)) > 1e-6 {
		t.Fatalf("full-scan startup %.4f, want %.4f", full.Startup, 0.43+389568.13)
	}
	// Skipping moves cost from the run to the startup; it never adds any.
	if got.Total > full.Total+1e-6 {
		t.Fatalf("skipped prefix raised the total: %.4f > %.4f", got.Total, full.Total)
	}
	init := mergeJoinInitialCost(outer, inner, 1920800, 2879953, sel)
	if init.Startup > got.Startup || init.Total > got.Total {
		t.Fatalf("initial cost %+v is not a lower bound of the final %+v", init, got)
	}
}

// get_variable_range widens the histogram's endpoints by the MCV extremes,
// and trusts MCV-only statistics only when they cover the whole table.
func TestVariableRangeWidensByMCVExtremes(t *testing.T) {
	cs := &catalog.ColumnStats{
		Histogram: []string{"10", "20", "30"},
		MCV:       []catalog.MCVEntry{{Value: "5", Frequency: 0.1}, {Value: "40", Frequency: 0.1}},
	}
	min, max, ok := variableRange(cs, "int4")
	if !ok || min != "5" || max != "40" {
		t.Fatalf("variableRange = (%q, %q, %v), want (5, 40, true)", min, max, ok)
	}
	partial := &catalog.ColumnStats{MCV: []catalog.MCVEntry{{Value: "7", Frequency: 0.5}}}
	if _, _, ok := variableRange(partial, "int4"); ok {
		t.Fatalf("variableRange trusted MCVs covering half the table")
	}
	whole := &catalog.ColumnStats{NullFrac: 0.2, MCV: []catalog.MCVEntry{{Value: "7", Frequency: 0.5}, {Value: "3", Frequency: 0.3}}}
	if min, max, ok := variableRange(whole, "int4"); !ok || min != "3" || max != "7" {
		t.Fatalf("variableRange over table-covering MCVs = (%q, %q, %v), want (3, 7, true)", min, max, ok)
	}
}
