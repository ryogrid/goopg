package optimizer

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

// P1-14b booltestsel slice: IS [NOT] TRUE/FALSE/UNKNOWN over a boolean
// column with MCV [{true @0.6}] and nullfrac 0.1 -> true 0.6, false 0.3.
func TestBoolTestSelectivityMCV(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 2, NullFrac: 0.1,
				MCV: []catalog.MCVEntry{{Value: "true", Frequency: 0.6}}},
		},
	}, []catalog.Column{{Name: "b", Type: catalog.Type{Name: "bool"}, Ordinal: 0}})
	col := func() *ColumnRef { return &ColumnRef{Index: 0, Name: "b", Type: catalog.Type{Name: "bool"}} }
	for _, tc := range []struct {
		name                 string
		expr                 *IsBoolExpr
		want                 float64
	}{
		{"IS TRUE", &IsBoolExpr{Operand: col(), TestTrue: true}, 0.6},
		{"IS FALSE", &IsBoolExpr{Operand: col(), TestFalse: true}, 0.3},
		{"IS NOT TRUE", &IsBoolExpr{Operand: col(), TestTrue: true, Negated: true}, 0.4},
		{"IS NOT FALSE", &IsBoolExpr{Operand: col(), TestFalse: true, Negated: true}, 0.7},
		{"IS UNKNOWN", &IsBoolExpr{Operand: col()}, 0.1},
		{"IS NOT UNKNOWN", &IsBoolExpr{Operand: col(), Negated: true}, 0.9},
	} {
		if got := clauseSelectivity(tc.expr, &SeqScan{Table: tbl}); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
	// MCV[0] false: TRUE mass = 1 - 0.5 - 0.1.
	tbl2 := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 2, NullFrac: 0.1,
				MCV: []catalog.MCVEntry{{Value: "false", Frequency: 0.5}}},
		},
	}, []catalog.Column{{Name: "b", Type: catalog.Type{Name: "bool"}, Ordinal: 0}})
	if got := clauseSelectivity(&IsBoolExpr{Operand: col(), TestTrue: true}, &SeqScan{Table: tbl2}); math.Abs(got-0.4) > 1e-9 {
		t.Errorf("MCV[0]=false IS TRUE = %v, want 0.4", got)
	}
}

// No MCV: nullfrac answers UNKNOWN, everything else splits non-null 50/50.
func TestBoolTestSelectivityNoMCV(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{{NDistinct: 2, NullFrac: 0.2}},
	}, []catalog.Column{{Name: "b", Type: catalog.Type{Name: "bool"}, Ordinal: 0}})
	col := &ColumnRef{Index: 0, Name: "b", Type: catalog.Type{Name: "bool"}}
	for _, tc := range []struct {
		name string
		expr *IsBoolExpr
		want float64
	}{
		{"IS TRUE", &IsBoolExpr{Operand: col, TestTrue: true}, 0.4},
		{"IS FALSE", &IsBoolExpr{Operand: col, TestFalse: true}, 0.4},
		{"IS NOT TRUE", &IsBoolExpr{Operand: col, TestTrue: true, Negated: true}, 0.6},
		{"IS UNKNOWN", &IsBoolExpr{Operand: col}, 0.2},
		{"IS NOT UNKNOWN", &IsBoolExpr{Operand: col, Negated: true}, 0.8},
	} {
		if got := clauseSelectivity(tc.expr, &SeqScan{Table: tbl}); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// No statistics, non-column operand: the pre-existing default, and the
// WithSource twin reports it unreliable — except the shape+stats arm,
// which is reliable.
func TestBoolTestSelectivityDefaults(t *testing.T) {
	bare := makeStatsTable(nil, []catalog.Column{{Name: "b", Type: catalog.Type{Name: "bool"}, Ordinal: 0}})
	expr := &IsBoolExpr{Operand: &ColumnRef{Index: 0, Name: "b"}, TestTrue: true}
	if got := clauseSelectivity(expr, &SeqScan{Table: bare}); got != defaultGenericSelectivity {
		t.Errorf("no-stats IS TRUE = %v, want default %v", got, defaultGenericSelectivity)
	}
	if est := clauseSelectivityWithSource(expr, &SeqScan{Table: bare}); est.reliable {
		t.Errorf("no-stats IS TRUE marked reliable: %+v", est)
	}
	noncol := &IsBoolExpr{Operand: &BooleanConst{Value: true}, TestTrue: true}
	if got := clauseSelectivity(noncol, &SeqScan{Table: bare}); got != defaultGenericSelectivity {
		t.Errorf("non-column IS TRUE = %v, want default %v", got, defaultGenericSelectivity)
	}
}
