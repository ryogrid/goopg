package planner

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// makeStatsTable builds a *catalog.Table populated with the
// supplied stats so the selectivity pass can find them. Used by
// every test in this file — the planner doesn't need a real
// storage layer to score predicates.
func makeStatsTable(stats *catalog.TableStats, cols []catalog.Column) *catalog.Table {
	return &catalog.Table{
		Schema:  "public",
		Name:    "t",
		Columns: cols,
		Stats:   stats,
	}
}

// TestSelectivityEqualityHitsMCV: a predicate that probes a known
// MCV value should resolve to that value's frequency directly.
// 0006-0001's split rule guarantees the MCV list has the right
// shape; this test pins that the planner consumes it.
func TestSelectivityEqualityHitsMCV(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000,
		Columns: []catalog.ColumnStats{
			{NDistinct: 3, NullFrac: 0,
				MCV: []catalog.MCVEntry{
					{Value: "F", Frequency: 0.8},
					{Value: "O", Frequency: 0.15},
				}},
		},
	}, []catalog.Column{{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 0}})

	scan := &SeqScan{Table: tbl}
	pred := &BinaryOp{
		Op: parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "label", Type: catalog.Type{Name: "text"}},
		Right: &StringConst{Value: "F"},
	}
	got := clauseSelectivity(pred, scan)
	if math.Abs(got-0.8) > 1e-9 {
		t.Errorf("clauseSelectivity(label='F')=%v want 0.8", got)
	}
}

// TestSelectivityEqualityFallsThroughMCV: a literal not in the
// MCV list gets the non-MCV-mass / non-MCV-distinct fallback.
// MCV mass = 0.95, NDistinct = 3, MCV size = 2 → non-MCV mass
// 0.05 spread across 1 distinct value.
func TestSelectivityEqualityFallsThroughMCV(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000,
		Columns: []catalog.ColumnStats{
			{NDistinct: 3, NullFrac: 0,
				MCV: []catalog.MCVEntry{
					{Value: "F", Frequency: 0.8},
					{Value: "O", Frequency: 0.15},
				}},
		},
	}, []catalog.Column{{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 0}})

	scan := &SeqScan{Table: tbl}
	pred := &BinaryOp{
		Op: parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "label", Type: catalog.Type{Name: "text"}},
		Right: &StringConst{Value: "P"},
	}
	got := clauseSelectivity(pred, scan)
	want := 0.05 / 1.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("clauseSelectivity(label='P')=%v want %v", got, want)
	}
}

// TestSelectivityRangeUsesHistogram: a numeric column with
// boundaries [1, 100, 200, 300, 400, 500] (5 buckets) and `id <
// 200` should land at bucket 2 / 5 = 0.4. With no MCV, the whole
// non-MCV mass = 1 and the histogram drives the answer.
func TestSelectivityRangeUsesHistogram(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 500,
		Columns: []catalog.ColumnStats{
			{NDistinct: 500, NullFrac: 0,
				Histogram: []string{"1", "100", "200", "300", "400", "500"}},
		},
	}, []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}})

	scan := &SeqScan{Table: tbl}
	pred := &BinaryOp{
		Op: parser.OpLt,
		Left:  &ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: 200},
	}
	got := clauseSelectivity(pred, scan)
	if math.Abs(got-0.4) > 1e-9 {
		t.Errorf("clauseSelectivity(id<200)=%v want 0.4", got)
	}
}

// TestSelectivityAndProductRule: AND combines via independence
// assumption — sel(A AND B) = sel(A) * sel(B).
func TestSelectivityAndProductRule(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000,
		Columns: []catalog.ColumnStats{
			{NDistinct: 3, NullFrac: 0,
				MCV: []catalog.MCVEntry{{Value: "F", Frequency: 0.8}}},
			{NDistinct: 500, NullFrac: 0,
				Histogram: []string{"1", "100", "200", "300", "400", "500"}},
		},
	}, []catalog.Column{
		{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 0},
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	})

	scan := &SeqScan{Table: tbl}
	pred := &BinaryOp{
		Op: parser.OpAnd,
		Left: &BinaryOp{
			Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "label", Type: catalog.Type{Name: "text"}},
			Right: &StringConst{Value: "F"},
		},
		Right: &BinaryOp{
			Op: parser.OpLt,
			Left:  &ColumnRef{Index: 1, Name: "id", Type: catalog.Type{Name: "int4"}},
			Right: &IntegerConst{Value: 200},
		},
	}
	got := clauseSelectivity(pred, scan)
	want := 0.8 * 0.4
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("clauseSelectivity(F AND <200)=%v want %v", got, want)
	}
}

// TestSelectivityOrInclusionExclusion: OR uses inclusion-exclusion
// — sel(A OR B) = sel(A) + sel(B) - sel(A)*sel(B).
func TestSelectivityOrInclusionExclusion(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000,
		Columns: []catalog.ColumnStats{
			{NDistinct: 3, NullFrac: 0,
				MCV: []catalog.MCVEntry{
					{Value: "F", Frequency: 0.8},
					{Value: "O", Frequency: 0.15},
				}},
		},
	}, []catalog.Column{{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 0}})

	scan := &SeqScan{Table: tbl}
	pred := &BinaryOp{
		Op: parser.OpOr,
		Left: &BinaryOp{
			Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "label", Type: catalog.Type{Name: "text"}},
			Right: &StringConst{Value: "F"},
		},
		Right: &BinaryOp{
			Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "label", Type: catalog.Type{Name: "text"}},
			Right: &StringConst{Value: "O"},
		},
	}
	got := clauseSelectivity(pred, scan)
	want := 0.8 + 0.15 - 0.8*0.15
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("clauseSelectivity(F OR O)=%v want %v", got, want)
	}
}

// TestSelectivityFallsBackToOneThirdWhenNoStats: an unanalysed
// table reverts to the M0003 generic constant. This is the
// documented fallback contract from
// 0006-0003-clauselist-selectivity.md.
func TestSelectivityFallsBackToOneThirdWhenNoStats(t *testing.T) {
	tbl := &catalog.Table{
		Schema:  "public",
		Name:    "u",
		Columns: []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}},
	}
	scan := &SeqScan{Table: tbl}
	pred := &BinaryOp{
		Op: parser.OpLt,
		Left:  &ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: 200},
	}
	got := clauseSelectivity(pred, scan)
	if math.Abs(got-defaultIneqSelectivity) > 1e-9 {
		t.Errorf("clauseSelectivity(unanalysed range)=%v want %v", got, defaultIneqSelectivity)
	}
}

// TestSelectivityInExprSumsValues: IN (v1, v2) sums per-value
// equality selectivities, capped at 1.0.
func TestSelectivityInExprSumsValues(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000,
		Columns: []catalog.ColumnStats{
			{NDistinct: 4, NullFrac: 0,
				MCV: []catalog.MCVEntry{
					{Value: "F", Frequency: 0.5},
					{Value: "O", Frequency: 0.3},
				}},
		},
	}, []catalog.Column{{Name: "label", Type: catalog.Type{Name: "text"}, Ordinal: 0}})
	scan := &SeqScan{Table: tbl}
	in := &InExpr{
		Operand: &ColumnRef{Index: 0, Name: "label", Type: catalog.Type{Name: "text"}},
		List: []Expr{
			&StringConst{Value: "F"},
			&StringConst{Value: "O"},
		},
	}
	got := clauseSelectivity(in, scan)
	if math.Abs(got-0.8) > 1e-9 {
		t.Errorf("clauseSelectivity(label IN (F,O))=%v want 0.8", got)
	}
}
