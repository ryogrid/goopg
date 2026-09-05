package optimizer

import "testing"

// B-18 commits 2-4 — GUC-effect fixtures for the cost GUCs the B-12d/e/f
// propagation arms and TestCostGUCsReachTheCostingOnAHashJoin deliberately
// leave out, under the same honesty rule those tests state: a GUC can only
// move a cost whose shape the fixture produces.
//
// What was covered before this slice, and why it was not enough:
//   - random_page_cost: only the two-variable extreme-arms struct test
//     (TestPlannerSettingsReachTheJoinSearch varies RandomPageCost AND
//     SeqPageCost together), so no test attributes a move to
//     random_page_cost alone.
//   - cpu_index_tuple_cost, effective_cache_size: conversion-only
//     (TestCostGUCConversionIsTotal pins the field is carried, not that it
//     prices anything). The hash-join fixture cannot observe them: a
//     seq-scan/hash shape reads no index pages and consults no cache
//     budget.
//   - parallel_setup_cost, parallel_tuple_cost: conversion-only. The only
//     costing that reads them is gatherCost (cost_gather, costsize.c:446),
//     which prices the Gather shape.
//
// Each test below varies exactly ONE PlannerSettings field and requires the
// cost to move, so a move is attributable to that variable alone. Combined
// with TestCostGUCConversionIsTotal (every field is carried into
// costParams), these discharge P2-02d's acceptance row — "every cost GUC
// demonstrably changes at least one cost" — for the five remaining GUCs.
//
// design: take3 09 §5 P2 row.

// indexGUCEffectInput is the ONE index-shape fixture all three index-GUC
// tests share (B-18: one fixture may cover all three since each arm touches
// only one input). It is a selective scan of a table that fits in the
// default cache but overflows a shrunken one, so that:
//   - random_page_cost prices the fetched heap/index pages (max_IO_cost),
//   - cpu_index_tuple_cost prices the inspected index tuples,
//   - effective_cache_size prices the Mackert-Lohman page count — but ONLY
//     in the T > b regime, which the default 4GB cache never reaches for a
//     test-sized table. That arm therefore shrinks the cache instead of
//     growing it (a smaller cache fetches MORE pages), which is also PG's
//     own direction.
//
// Geometry: 10000-page / 100000-tuple heap, 2000-page index, selectivity
// 0.05 (5000 tuples fetched — above the shrunken cache's `lim`, so the
// linear tail answers, and below saturation, so the answer stays physical).
func indexGUCEffectInput() indexScanInputs {
	return indexScanInputs{
		relPages:    10000,
		relTuples:   100000,
		indexPages:  2000,
		indexTuples: 100000,
		treeHeight:  2,
		selectivity: 0.05,
		correlation: 0,
		// Heap pages plus index pages: total_table_pages pro-rates the
		// cache budget between every relation in the query, this one
		// included (index_pages_fetched, costsize.c:906).
		totalTablePages: 12000,
	}
}

// indexGUCEffectBase prices the shared input under the defaults; arms reprice
// it under one varied field.
func indexGUCEffectBase(t *testing.T) Cost {
	t.Helper()
	return costIndexScan(DefaultPlannerSettings().costParams(), indexGUCEffectInput())
}

// TestRandomPageCostReachesIndexCosting pins that random_page_cost alone
// reprices an index scan. The pre-existing struct test varies it together
// with seq_page_cost, so a regression that dropped just this field would
// keep passing there and fail here.
func TestRandomPageCostReachesIndexCosting(t *testing.T) {
	base := indexGUCEffectBase(t)
	ps := DefaultPlannerSettings()
	ps.RandomPageCost *= 1000
	got := costIndexScan(ps.costParams(), indexGUCEffectInput())
	if got.Total == base.Total && got.Startup == base.Startup {
		t.Errorf("random_page_cost x1000 left the index scan at (%.4f..%.4f) — "+
			"the GUC does not reach the index costing",
			got.Startup, got.Total)
	}
	if !(got.Total > base.Total) {
		t.Errorf("random_page_cost x1000 gave total %.4f, not above baseline %.4f — "+
			"dearer random pages must cost more", got.Total, base.Total)
	}
}

// TestCPUIndexTupleCostReachesIndexCosting pins that cpu_index_tuple_cost
// alone reprices an index scan (the per-index-tuple term of
// btreeIndexAMCost). The hash-join fixture cannot observe it: no index
// tuple is inspected there.
func TestCPUIndexTupleCostReachesIndexCosting(t *testing.T) {
	base := indexGUCEffectBase(t)
	ps := DefaultPlannerSettings()
	ps.CPUIndexTupleCost *= 1000
	got := costIndexScan(ps.costParams(), indexGUCEffectInput())
	if got.Total == base.Total && got.Startup == base.Startup {
		t.Errorf("cpu_index_tuple_cost x1000 left the index scan at (%.4f..%.4f) — "+
			"the GUC does not reach the index costing",
			got.Startup, got.Total)
	}
	if !(got.Total > base.Total) {
		t.Errorf("cpu_index_tuple_cost x1000 gave total %.4f, not above baseline %.4f",
			got.Total, base.Total)
	}
}

// TestEffectiveCacheSizeReachesIndexCosting pins that effective_cache_size
// alone reprices an index scan, via the Mackert-Lohman page count. The hot
// arm SHRINKS the cache to 100 pages: under the default 4GB cache the whole
// test table fits its pro-rated share (T <= b) and the page count is
// cache-independent, so growing the cache further would prove nothing —
// shrinking it crosses into the T > b regime where the budget binds, and a
// smaller cache must fetch more pages.
func TestEffectiveCacheSizeReachesIndexCosting(t *testing.T) {
	base := indexGUCEffectBase(t)
	ps := DefaultPlannerSettings()
	ps.EffectiveCacheSize = 100
	got := costIndexScan(ps.costParams(), indexGUCEffectInput())
	if got.Total == base.Total && got.Startup == base.Startup {
		t.Errorf("effective_cache_size shrunk to 100 pages left the index scan at "+
			"(%.4f..%.4f) — the GUC does not reach the index costing",
			got.Startup, got.Total)
	}
	if !(got.Total > base.Total) {
		t.Errorf("a 100-page cache gave total %.4f, not above baseline %.4f — "+
			"a smaller cache must fetch more pages", got.Total, base.Total)
	}
}

// gatherGUCEffectSub is the ONE Gather-shape fixture both parallel-cost
// tests share: a partial subpath and the row count emerging from the
// Gather, as gatherCost takes them.
func gatherGUCEffectSub() (Cost, float64) {
	return Cost{Startup: 10, Total: 100}, 5000
}

// TestParallelSetupCostReachesGatherCosting pins that parallel_setup_cost
// alone reprices a Gather, at startup AND total (cost_gather charges the
// setup before the first row emerges). Driven from PlannerSettings rather
// than a bare costParams literal, so the test pins the session value's
// whole route: GUC -> PlannerSettings -> costParams -> gatherCost.
//
// SCOPE, STATED PLAINLY: gatherCost has no search caller yet — the
// post-pass places Gathers by size rule, and real Gather/GatherMerge paths
// competing in add_path arrive with P5-04. This pins the knob reaches the
// Gather costing it will compete on, not that the search compares it today.
func TestParallelSetupCostReachesGatherCosting(t *testing.T) {
	sub, rows := gatherGUCEffectSub()
	base := gatherCost(DefaultPlannerSettings().costParams(), sub, rows)
	ps := DefaultPlannerSettings()
	ps.ParallelSetupCost *= 1000
	got := gatherCost(ps.costParams(), sub, rows)
	if got.Total == base.Total && got.Startup == base.Startup {
		t.Errorf("parallel_setup_cost x1000 left the Gather at (%.4f..%.4f) — "+
			"the GUC does not reach the Gather costing",
			got.Startup, got.Total)
	}
	if !(got.Startup > base.Startup && got.Total > base.Total) {
		t.Errorf("parallel_setup_cost x1000 gave (%.4f..%.4f), want both above "+
			"baseline (%.4f..%.4f) — the setup is paid before the first row",
			got.Startup, got.Total, base.Startup, base.Total)
	}
}

// TestParallelTupleCostReachesGatherCosting pins that parallel_tuple_cost
// alone reprices a Gather — total ONLY, startup unchanged. The startup pin
// is the one-variable discipline: it distinguishes the per-row transfer
// term from the flat setup term above, so the two tests cannot pass for
// each other's reason. Same P5-04 scope note as the setup test.
func TestParallelTupleCostReachesGatherCosting(t *testing.T) {
	sub, rows := gatherGUCEffectSub()
	base := gatherCost(DefaultPlannerSettings().costParams(), sub, rows)
	ps := DefaultPlannerSettings()
	ps.ParallelTupleCost *= 1000
	got := gatherCost(ps.costParams(), sub, rows)
	if got.Total == base.Total {
		t.Errorf("parallel_tuple_cost x1000 left the Gather total at %.4f — "+
			"the GUC does not reach the Gather costing", got.Total)
	}
	if got.Startup != base.Startup {
		t.Errorf("parallel_tuple_cost x1000 moved startup to %.4f from %.4f — "+
			"the per-row term must not charge before the first row",
			got.Startup, base.Startup)
	}
	if !(got.Total > base.Total) {
		t.Errorf("parallel_tuple_cost x1000 gave total %.4f, not above baseline %.4f",
			got.Total, base.Total)
	}
}
