package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestEstimateBaseRowsUsesLocalFilterSelectivity pins the
// design 02 §8.1 contract: when a binding has a stats-driven
// local filter (e.g., `r_name = 'ASIA'` with MCV/NDistinct on
// the column), `baseRelInfo.filteredRows` reflects the scaled
// post-filter row count rather than the raw `tableRows`.
// (M0077-0002 / Slice B.)
func TestEstimateBaseRowsUsesLocalFilterSelectivity(t *testing.T) {
	tbl := &catalog.Table{
		Name: "region",
		Columns: []catalog.Column{
			{Name: "r_regionkey", Type: catalog.Type{Name: "numeric"}},
			{Name: "r_name", Type: catalog.Type{Name: "char"}},
			{Name: "r_comment", Type: catalog.Type{Name: "varchar"}},
		},
		Stats: &catalog.TableStats{
			RowCount: 5,
			Columns: []catalog.ColumnStats{
				{NDistinct: 5},
				{NDistinct: 5, MCV: []catalog.MCVEntry{{Value: "ASIA", Frequency: 0.2}}},
				{NDistinct: 5},
			},
		},
	}
	scan := &SeqScan{Table: tbl, schema: tableSchema(tbl)}
	binding := rangeBinding{table: tbl, offset: 0}
	// Local predicate: `r_name = 'ASIA'`. Column index 1 in
	// region's leaf-local schema (r_regionkey=0, r_name=1).
	pred := &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: 1, Name: "r_name", Type: catalog.Type{Name: "char"}},
		Right: &StringConst{Value: "ASIA"},
	}
	info := estimateBaseRelInfo(binding, scan, pred)
	if info.baseRows != 5 {
		t.Errorf("baseRows = %d; want 5", info.baseRows)
	}
	if !info.hasLocalFilter {
		t.Error("hasLocalFilter must be true when local != nil")
	}
	// Selectivity 0.2 × 5 = 1.0 → filtered floor = 1.
	if info.filteredRows != 1 {
		t.Errorf("filteredRows = %d; want 1 (5 × MCV(ASIA)=0.2 with 1-row floor)", info.filteredRows)
	}
}

// TestEstimateBaseRowsKeepsBaseRowsWhenSelectivityFallback
// pins design 02 §2 rule (4): when the selectivity estimate
// falls back to the generic default (no stats, unrecognised
// shape, missing histogram), `filteredRows` keeps `baseRows`
// rather than over-trusting an arbitrary 0.005 / 0.333.
// (M0077-0002 / Slice B.)
func TestEstimateBaseRowsKeepsBaseRowsWhenSelectivityFallback(t *testing.T) {
	tbl := &catalog.Table{
		Name: "orders",
		Columns: []catalog.Column{
			{Name: "o_orderdate", Type: catalog.Type{Name: "timestamp"}},
		},
		Stats: &catalog.TableStats{
			RowCount: 1500000,
			// Column stats present but no histogram → range
			// path falls back to defaultIneqSelectivity.
			Columns: []catalog.ColumnStats{
				{NDistinct: 2406},
			},
		},
	}
	scan := &SeqScan{Table: tbl, schema: tableSchema(tbl)}
	binding := rangeBinding{table: tbl, offset: 0}
	// `o_orderdate >= '1994-01-01'` — range op without histogram.
	pred := &BinaryOp{
		Op:    parser.OpGe,
		Left:  &ColumnRef{Index: 0, Name: "o_orderdate", Type: catalog.Type{Name: "timestamp"}},
		Right: &StringConst{Value: "1994-01-01"},
	}
	info := estimateBaseRelInfo(binding, scan, pred)
	if info.baseRows != 1500000 {
		t.Errorf("baseRows = %d; want 1500000", info.baseRows)
	}
	if info.filteredRows != info.baseRows {
		t.Errorf("filteredRows = %d; want baseRows=%d (selectivity unreliable → keep base)", info.filteredRows, info.baseRows)
	}
}

// TestEstimateBaseRowsMarksSmallDimension pins the
// SmallDimension propagation: catalog flag flows into
// `baseRelInfo` so Slice C / D can read it without a separate
// catalog lookup. (M0077-0002 / Slice B.)
func TestEstimateBaseRowsMarksSmallDimension(t *testing.T) {
	region := &catalog.Table{Name: "region", SmallDimension: true}
	lineitem := &catalog.Table{Name: "lineitem"}
	cases := []struct {
		name string
		tbl  *catalog.Table
		want bool
	}{
		{"region", region, true},
		{"lineitem", lineitem, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := estimateBaseRelInfo(rangeBinding{table: tc.tbl}, nil, nil)
			if info.isSmallDimension != tc.want {
				t.Errorf("isSmallDimension = %v; want %v", info.isSmallDimension, tc.want)
			}
		})
	}
}

// TestSelectivityEstimateReliableSignal pins the boundary
// between stat-driven and fallback estimates. AND/OR composes
// reliability conjunctively. (M0077-0002 / Slice B per design
// 02 §2.)
func TestSelectivityEstimateReliableSignal(t *testing.T) {
	statsTbl := &catalog.Table{
		Columns: []catalog.Column{{Name: "a", Type: catalog.Type{Name: "char"}}},
		Stats: &catalog.TableStats{
			RowCount: 100,
			Columns: []catalog.ColumnStats{
				{NDistinct: 10, MCV: []catalog.MCVEntry{{Value: "X", Frequency: 0.1}}},
			},
		},
	}
	statsScan := &SeqScan{Table: statsTbl, schema: tableSchema(statsTbl)}

	noStatsTbl := &catalog.Table{
		Columns: []catalog.Column{{Name: "a", Type: catalog.Type{Name: "char"}}},
	}
	noStatsScan := &SeqScan{Table: noStatsTbl, schema: tableSchema(noStatsTbl)}

	eq := &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "char"}},
		Right: &StringConst{Value: "X"},
	}

	// Stats present → reliable.
	if got := clauseSelectivityWithSource(eq, statsScan); !got.reliable {
		t.Error("eq with stats must be reliable")
	}
	// Stats missing → reliable=false (default fallback).
	if got := clauseSelectivityWithSource(eq, noStatsScan); got.reliable {
		t.Error("eq without stats must be reliable=false")
	}
	// AND: reliable iff both sides reliable.
	andMixed := &BinaryOp{
		Op:    parser.OpAnd,
		Left:  eq,
		Right: &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 99, Name: "ghost"}, Right: &StringConst{Value: "x"}},
	}
	got := clauseSelectivityWithSource(andMixed, statsScan)
	if got.reliable {
		t.Error("AND of reliable + unreliable must be reliable=false")
	}
	// BooleanConst: reliable=true.
	if got := clauseSelectivityWithSource(&BooleanConst{Value: true}, statsScan); !got.reliable {
		t.Error("BooleanConst(true) must be reliable=true")
	}
}

// TestSelectivityEstimateRangeWithoutHistogramIsUnreliable
// pins range-path reliability: histogram-less stats fall back
// to the generic ineq default and must report reliable=false
// so `estimateBaseRelInfo` keeps `baseRows`. (M0077-0002 /
// Slice B.)
func TestSelectivityEstimateRangeWithoutHistogramIsUnreliable(t *testing.T) {
	tbl := &catalog.Table{
		Columns: []catalog.Column{{Name: "d", Type: catalog.Type{Name: "timestamp"}}},
		Stats: &catalog.TableStats{
			RowCount: 100,
			Columns:  []catalog.ColumnStats{{NDistinct: 50}},
		},
	}
	scan := &SeqScan{Table: tbl, schema: tableSchema(tbl)}
	rng := &BinaryOp{
		Op:    parser.OpGe,
		Left:  &ColumnRef{Index: 0, Name: "d", Type: catalog.Type{Name: "timestamp"}},
		Right: &StringConst{Value: "1994-01-01"},
	}
	got := clauseSelectivityWithSource(rng, scan)
	if got.reliable {
		t.Error("range op without histogram must be reliable=false")
	}
}
