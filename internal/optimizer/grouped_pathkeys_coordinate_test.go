package optimizer

import "testing"

// TestGroupedRelPathkeysShareOneCoordinate pins M0146-0189. Every path on the
// GROUP_AGG rel must name its ordering the same way — the group keys over the
// aggregate's input row — because add_path compares pathkeys by expression
// (PG compares canonical group pathkeys). The sorted Finalize split named its
// ordering by the transport row's positions (`wp_web_page_sk` at Index 0 where
// the serial arms had Index 62), so add_path called the two PATHKEYS_DIFFERENT
// and kept a fuzzily-tied, slow-start split beside PG's fast-start serial
// GroupAggregate; the split then won on total (TPC-DS Q77's ws).
func TestGroupedRelPathkeysShareOneCoordinate(t *testing.T) {
	key := &ColumnRef{Name: "wp_web_page_sk", SourceTableIdx: 3, Index: 62}
	agg := &Aggregate{GroupExprs: []Expr{key}}
	serialKeys := pathkeysForSortKeys(groupKeysSortKeys(agg))
	if got := comparePathkeysDim(finalizeGroupPathkeys(agg), serialKeys); got != dimEqual {
		t.Fatalf("Finalize pathkeys compare %v with the serial arms' group pathkeys, want equal", got)
	}

	// With one coordinate, add_path settles PG's way: totals within
	// STD_FUZZ_FACTOR, so the serial aggregate's early start evicts the split.
	grouped := newRelOptInfo(0, 60, 32)
	serial := &Path{Kind: PathAgg, AggStrategy: AggStrategySorted, Rel: grouped, Rows: 60,
		Cost: Cost{Startup: 3049.91, Total: 8774.11}, Pathkeys: serialKeys}
	split := &Path{Kind: PathFinalizeAgg, AggStrategy: AggStrategySorted, Rel: grouped, Rows: 60,
		Cost: Cost{Startup: 8697.61, Total: 8708.11}, Pathkeys: finalizeGroupPathkeys(agg)}
	addPath(grouped, serial, "upper.groupagg.sort")
	addPath(grouped, split, "upper.groupagg.sortinput")
	if len(grouped.Pathlist) != 1 || grouped.Pathlist[0] != serial {
		t.Fatalf("grouped rel kept %d paths, want only the fast-start serial GroupAggregate", len(grouped.Pathlist))
	}
}
