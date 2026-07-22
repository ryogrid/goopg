package planner

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestEstimateJoinCostNoInt64Overflow pins the scale-overflow fix: for
// deep composed subsets at SF scale, leftRows*rightRows exceeds int64
// and, before the fix, wrapped NEGATIVE then clamped to 1 — a garbage
// cardinality that poisoned the cost and the cost-driven pgCost. The
// saturating float64 path must return a large POSITIVE estimate instead.
func TestEstimateJoinCostNoInt64Overflow(t *testing.T) {
	// satRowsMulDiv: 1e14 * 1e6 / 1 overflows int64 (max ~9.2e18) only at
	// 1e20; use operands whose product exceeds int64 to exercise saturation.
	if got := satRowsMulDiv(5_000_000_000, 5_000_000_000, 1); got <= 0 {
		t.Errorf("satRowsMulDiv(5e9,5e9,1) = %d, want a large POSITIVE value (int64 product 2.5e19 overflows)", got)
	}
	if got := satRowsMulDiv(math.MaxInt64/2, 4, 1); got != math.MaxInt64 {
		t.Errorf("satRowsMulDiv saturation = %d, want MaxInt64", got)
	}
	if got := satCost(math.MaxInt64/2, math.MaxInt64/2, math.MaxInt64/2); got != math.MaxInt64 {
		t.Errorf("satCost saturation = %d, want MaxInt64", got)
	}

	// End-to-end: estimateJoinCost over two ~1e13-row sides (deep-subset
	// scale) must NOT collapse to the 1-row clamp that a wrapped product hits.
	tbl := &catalog.Table{Name: "t", Stats: &catalog.TableStats{RowCount: 1, Columns: []catalog.ColumnStats{{NDistinct: 1}}}}
	g := &joinGraph{nodes: 2, tables: []*catalog.Table{tbl, tbl}}
	edge := &joinEdge{leftTable: 0, rightTable: 1}
	out, cost := estimateJoinCost(20_000_000_000, 20_000_000_000, edge, g, nil)
	if out <= 1 {
		t.Errorf("estimateJoinCost outputRows = %d, want >1 (2e10*2e10 overflows int64 and must not clamp to 1)", out)
	}
	if cost <= 1 {
		t.Errorf("estimateJoinCost cost = %d, want >1 (must not collapse from overflow)", cost)
	}
}
