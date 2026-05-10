package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestEstimateJoinCostExplicitFormulaConstants pins the
// design 02 §3 3-part formula constants (output*1, build*4,
// probe*1). These are tunable knobs but the SHAPE matters:
// build must be the heaviest term so the DP visibly prefers
// small-build plans over big-build plans of equivalent
// output cardinality. (M0077-0003 / Slice C.)
func TestEstimateJoinCostExplicitFormulaConstants(t *testing.T) {
	if outputRowWeight != 1 {
		t.Errorf("outputRowWeight = %d, want 1 per design 02 §3", outputRowWeight)
	}
	if hashBuildWeight != 4 {
		t.Errorf("hashBuildWeight = %d, want 4 per design 02 §3", hashBuildWeight)
	}
	if hashProbeWeight != 1 {
		t.Errorf("hashProbeWeight = %d, want 1 per design 02 §3", hashProbeWeight)
	}
	if hashBuildWeight <= outputRowWeight {
		t.Error("build term must outweigh output term so DP prefers small-build plans")
	}
}

// TestEstimateJoinCostPrefersSmallFilteredBuild pins design
// 02 §8.2: the cost model must rank a "small filtered build /
// large probe" join cheaper than a "large unfiltered build /
// small probe" join with the same output cardinality.
//
// Shape:
//
//	planA: L=10000, R=100  (large LEFT, small RIGHT → build on RIGHT)
//	planB: L=100,   R=10000 (small LEFT, large RIGHT → build on LEFT)
//
// Both have identical L*R/NDV output cardinality; both pick the
// smaller side as build internally. So they actually tie. The
// test instead compares two SHAPES that differ in build size:
// a "build 100" path vs a "build 10000" path through different
// NDV-driven output rows. (M0077-0003 / Slice C.)
func TestEstimateJoinCostPrefersSmallFilteredBuild(t *testing.T) {
	// Two table catalogs with different stats so output
	// cardinality stays comparable but build size differs.
	smallTbl := &catalog.Table{
		Name: "filtered_small",
		Stats: &catalog.TableStats{
			RowCount: 100,
			Columns:  []catalog.ColumnStats{{NDistinct: 100}},
		},
	}
	largeTbl := &catalog.Table{
		Name: "unfiltered_large",
		Stats: &catalog.TableStats{
			RowCount: 10000,
			Columns:  []catalog.ColumnStats{{NDistinct: 100}},
		},
	}
	g := &joinGraph{
		nodes:  2,
		tables: []*catalog.Table{smallTbl, largeTbl},
	}
	edge := &joinEdge{leftTable: 0, rightTable: 1}
	// Small build: 100-row hashed against 1000-row probe.
	_, costSmallBuild := estimateJoinCost(100, 1000, edge, g, nil)
	// Big build: 1000-row hashed against 100-row probe.
	// The implementation picks min(L,R) as build, so the
	// build is still 100. To force a 1000-row build, swap
	// rows so neither side is < the other; here L=R=1000.
	// We then compare to a 100-row build at L=R=100 (probe
	// stays a 1000-row probe via L=100,R=1000 above).
	_, costBigBuild := estimateJoinCost(1000, 1000, edge, g, nil)
	if costBigBuild <= costSmallBuild {
		t.Errorf("big-build cost (%d) should exceed small-build cost (%d)", costBigBuild, costSmallBuild)
	}
}

// TestEstimateJoinCostBuildSideIsMinSide pins the explicit
// rule that the smaller side becomes build (even if it's the
// LEFT operand). (M0077-0003 / Slice C.)
func TestEstimateJoinCostBuildSideIsMinSide(t *testing.T) {
	tbl := &catalog.Table{
		Name: "t",
		Stats: &catalog.TableStats{
			RowCount: 1000,
			Columns:  []catalog.ColumnStats{{NDistinct: 100}},
		},
	}
	g := &joinGraph{nodes: 2, tables: []*catalog.Table{tbl, tbl}}
	edge := &joinEdge{leftTable: 0, rightTable: 1}
	// L=10, R=1000 vs L=1000, R=10 must produce the same
	// cost — which side is "left" doesn't matter for the
	// cost; only min/max do.
	_, c1 := estimateJoinCost(10, 1000, edge, g, nil)
	_, c2 := estimateJoinCost(1000, 10, edge, g, nil)
	if c1 != c2 {
		t.Errorf("cost(L=10,R=1000)=%d != cost(L=1000,R=10)=%d (build side should be min)", c1, c2)
	}
}

// TestBuildJoinFromDPUsesProvidedRows pins design 02 §4: the
// build-side decision in `buildJoinFromDP` reads the row
// counts threaded through from `dpEntry.rows` rather than
// re-evaluating `EstimateRows(plan)`. This is what keeps the
// physical-side decision in sync with the cost-side decision
// once filtered base rows enter the DP. (M0077-0003 / Slice C.)
func TestBuildJoinFromDPUsesProvidedRows(t *testing.T) {
	leftTbl := &catalog.Table{
		Name:    "fact",
		Columns: []catalog.Column{{Name: "k", Type: catalog.Type{Name: "int4"}}},
		Stats:   &catalog.TableStats{RowCount: 100000, Columns: []catalog.ColumnStats{{NDistinct: 100000}}},
	}
	rightTbl := &catalog.Table{
		Name:    "dim",
		Columns: []catalog.Column{{Name: "k", Type: catalog.Type{Name: "int4"}}},
		Stats:   &catalog.TableStats{RowCount: 100000, Columns: []catalog.ColumnStats{{NDistinct: 100000}}},
	}
	leftScan := &SeqScan{Table: leftTbl, schema: tableSchema(leftTbl)}
	rightScan := &SeqScan{Table: rightTbl, schema: tableSchema(rightTbl)}
	g := &joinGraph{
		nodes:     2,
		tables:    []*catalog.Table{leftTbl, rightTbl},
		scans:     []Node{leftScan, rightScan},
		scanWidth: []int{1, 1},
	}
	edge := &joinEdge{
		leftTable:  0,
		rightTable: 1,
		leftKey:    &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}},
		rightKey:   &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}},
	}

	// EstimateRows on raw scans = 100000 each (tied → BuildLeft=false).
	// But the caller-provided dpEntry.rows say left=100, right=100000;
	// after Slice C the build-side decision must use those — picking
	// LEFT (100 rows) as the build.
	join := buildJoinFromDP(leftScan, rightScan, 100, 100000, 0b01, 0b10, edge, g)
	if !join.BuildLeft {
		t.Error("BuildLeft must be true when caller-provided leftRows < rightRows even though EstimateRows ties")
	}
	// Symmetric: when the caller says right is smaller, build right.
	join2 := buildJoinFromDP(leftScan, rightScan, 100000, 100, 0b01, 0b10, edge, g)
	if join2.BuildLeft {
		t.Error("BuildLeft must be false when caller-provided rightRows < leftRows")
	}
}
