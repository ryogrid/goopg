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

// P1-14b var_eq_non_const slice: column-to-nonconst equality splits the
// non-null mass across the distinct count, capped at the top MCV
// frequency — not the flat 0.005 the fallthrough used for every shape.
func TestVarEqNonConstSplitsByNDistinct(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 100, NullFrac: 0.1},
		},
	}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	})
	ca := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	cb := &ColumnRef{Index: 1, Name: "b", Type: catalog.Type{Name: "int4"}}
	// (1 - 0.1) / 100 = 0.009, resolved against the LEFT column.
	got := clauseSelectivity(&BinaryOp{Op: parser.OpEq, Left: ca, Right: cb}, &SeqScan{Table: tbl})
	if math.Abs(got-0.009) > 1e-9 {
		t.Errorf("col=col selectivity = %v, want 0.009", got)
	}
	est := clauseSelectivityWithSource(&BinaryOp{Op: parser.OpEq, Left: ca, Right: cb}, &SeqScan{Table: tbl})
	if !est.reliable || math.Abs(est.value-0.009) > 1e-9 {
		t.Errorf("WithSource col=col = %+v, want {0.009 true}", est)
	}
}

// The MCV cross-check caps the answer at the commonest value: raw
// (1-0)/2 = 0.5 must not exceed MCV frequency 0.4.
func TestVarEqNonConstCappedByMCV(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 2, NullFrac: 0,
				MCV: []catalog.MCVEntry{{Value: "7", Frequency: 0.4}}},
		},
	}, []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0}})
	ca := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	cb := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	if got := clauseSelectivity(&BinaryOp{Op: parser.OpEq, Left: ca, Right: cb}, &SeqScan{Table: tbl}); math.Abs(got-0.4) > 1e-9 {
		t.Errorf("MCV-capped col=col = %v, want 0.4", got)
	}
}

// No statistics, or no column on either side: the pre-existing default,
// reported unreliable by the twin.
func TestVarEqNonConstDefaults(t *testing.T) {
	bare := makeStatsTable(nil, []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0}})
	ca := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	cb := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	expr := &BinaryOp{Op: parser.OpEq, Left: ca, Right: cb}
	if got := clauseSelectivity(expr, &SeqScan{Table: bare}); got != defaultEqSelectivity {
		t.Errorf("no-stats col=col = %v, want default %v", got, defaultEqSelectivity)
	}
	if est := clauseSelectivityWithSource(expr, &SeqScan{Table: bare}); est.reliable {
		t.Errorf("no-stats col=col marked reliable: %+v", est)
	}
	nocol := &BinaryOp{Op: parser.OpEq, Left: &IntegerConst{Value: 1}, Right: &IntegerConst{Value: 2}}
	if got := clauseSelectivity(nocol, &SeqScan{Table: bare}); got != defaultEqSelectivity {
		t.Errorf("const=const = %v, want default %v", got, defaultEqSelectivity)
	}
}

// P1-14b scalararraysel slice: per-element operator estimators merged
// OR (ANY) or AND (ALL), with PG's disjoint-sum rule for equality-ANY.
func saFixture() (*catalog.Table, *ColumnRef) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 10, NullFrac: 0,
				MCV: []catalog.MCVEntry{
					{Value: "a", Frequency: 0.2},
					{Value: "b", Frequency: 0.3},
				}},
		},
	}, []catalog.Column{{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 0}})
	return tbl, &ColumnRef{Index: 0, Name: "c", Type: catalog.Type{Name: "text"}}
}

func TestScalarArrayEqualityANYDisjoint(t *testing.T) {
	tbl, col := saFixture()
	// 0.2 + 0.3 = 0.5, in range: the disjoint sum (and the old plain sum).
	in := &InExpr{Operand: col, List: []Expr{&StringConst{Value: "a"}, &StringConst{Value: "b"}}}
	if got := clauseSelectivity(in, &SeqScan{Table: tbl}); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("IN (a,b) = %v, want disjoint 0.5", got)
	}
	// NOT IN negates it.
	nin := &InExpr{Operand: col, List: []Expr{&StringConst{Value: "a"}}, Negated: true}
	if got := clauseSelectivity(nin, &SeqScan{Table: tbl}); math.Abs(got-0.8) > 1e-9 {
		t.Errorf("NOT IN (a) = %v, want 0.8", got)
	}
}

func TestScalarArrayOutOfRangeFallsBackToORMerge(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 100000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 1000, NullFrac: 0,
				Histogram: []string{"1", "100", "200", "300", "400", "500"}},
		},
	}, []catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0}})
	// 1500 distinct non-MCV values at ~0.001 each: disjoint sum 1.5 is out
	// of range, so the OR merge (~0.777) must win over the old cap-at-1.0.
	list := make([]Expr, 0, 1500)
	for i := 0; i < 1500; i++ {
		list = append(list, &IntegerConst{Value: int64(1000 + i)})
	}
	col := &ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}}
	got := clauseSelectivity(&InExpr{Operand: col, List: list}, &SeqScan{Table: tbl})
	want := 1 - math.Pow(0.999, 1500)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("saturated IN = %v, want OR-merge %v (not the old cap 1.0)", got, want)
	}
}

func TestScalarArrayNonEqualityRoutesByOperator(t *testing.T) {
	tbl, col := saFixture()
	// `c < ANY('m','z')`: range estimates OR-merged. Each side is priced by
	// the range estimator, so the result must equal the manual OR merge —
	// and must NOT equal the equality-sum the old arm computed.
	lo := rangeOpSelectivity(parser.OpLt, col, &StringConst{Value: "m"}, &SeqScan{Table: tbl})
	hi := rangeOpSelectivity(parser.OpLt, col, &StringConst{Value: "z"}, &SeqScan{Table: tbl})
	want := lo + hi - lo*hi
	in := &InExpr{Operand: col, AnyOp: parser.OpLt,
		List: []Expr{&StringConst{Value: "m"}, &StringConst{Value: "z"}}}
	if got := clauseSelectivity(in, &SeqScan{Table: tbl}); math.Abs(got-want) > 1e-9 {
		t.Errorf("c < ANY = %v, want OR-merged ranges %v", got, want)
	}
	// `c = ALL('a','b')`: AND of equalities = product.
	all := &InExpr{Operand: col, AllOp: true,
		List: []Expr{&StringConst{Value: "a"}, &StringConst{Value: "b"}}}
	if got := clauseSelectivity(all, &SeqScan{Table: tbl}); math.Abs(got-0.06) > 1e-9 {
		t.Errorf("c = ALL(a,b) = %v, want product 0.06", got)
	}
	// `c != ANY('a','b')`: OR of inequalities.
	neq := &InExpr{Operand: col, NotEqualAny: true,
		List: []Expr{&StringConst{Value: "a"}, &StringConst{Value: "b"}}}
	if got := clauseSelectivity(neq, &SeqScan{Table: tbl}); math.Abs(got-0.94) > 1e-9 {
		t.Errorf("c != ANY(a,b) = %v, want 0.94", got)
	}
}

// P1-14b rowcomparesel slice: a row-constructor comparison estimates from
// its LEADING pair as an ordinary scalar comparison (selfuncs.c:2204).
func TestRowCompareSelectivityLeadingPair(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 100, NullFrac: 0,
				MCV:       []catalog.MCVEntry{{Value: "1", Frequency: 0.25}},
				Histogram: []string{"1", "100", "200", "300", "400", "500"}},
			{NDistinct: 10, NullFrac: 0},
		},
	}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	})
	ca := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	cb := &ColumnRef{Index: 1, Name: "b", Type: catalog.Type{Name: "int4"}}
	row := func(x, y Expr) *RowExpr { return &RowExpr{Elems: []Expr{x, y}} }
	one := &IntegerConst{Value: 1}
	two := &IntegerConst{Value: 2}

	// Equality on the leading pair: MCV hit 0.25; later pairs ignored.
	eq := &BinaryOp{Op: parser.OpEq, Left: row(ca, cb), Right: row(one, two)}
	if got := clauseSelectivity(eq, &SeqScan{Table: tbl}); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("(a,b)=(1,2) = %v, want leading-pair MCV 0.25", got)
	}
	// Inequality routes through the range estimator: pin structural
	// equivalence with the scalar call, not a golden.
	gt := &BinaryOp{Op: parser.OpGt, Left: row(ca, cb), Right: row(one, two)}
	want := rangeOpSelectivity(parser.OpGt, ca, one, &SeqScan{Table: tbl})
	if got := clauseSelectivity(gt, &SeqScan{Table: tbl}); got != want {
		t.Errorf("(a,b)>(1,2) = %v, want scalar range %v", got, want)
	}
	// Twin agrees and reports reliable with stats.
	est := clauseSelectivityWithSource(eq, &SeqScan{Table: tbl})
	if !est.reliable || math.Abs(est.value-0.25) > 1e-9 {
		t.Errorf("twin (a,b)=(1,2) = %+v, want {0.25 true}", est)
	}
}

// Non-row shapes decline to the pre-existing default: row-vs-scalar,
// empty rows, and non-comparison operators over rows.
func TestRowCompareSelectivityDeclines(t *testing.T) {
	tbl := makeStatsTable(&catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{{NDistinct: 100, NullFrac: 0}},
	}, []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0}})
	ca := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	row := func(x, y Expr) *RowExpr { return &RowExpr{Elems: []Expr{x, y}} }
	one := &IntegerConst{Value: 1}
	for _, tc := range []struct {
		name string
		expr Expr
		want float64
	}{
		{"row-vs-scalar", &BinaryOp{Op: parser.OpEq, Left: row(ca, one), Right: one}, defaultEqSelectivity},
		{"empty-row", &BinaryOp{Op: parser.OpEq, Left: &RowExpr{}, Right: row(ca, one)}, defaultEqSelectivity},
		{"non-comparison", &BinaryOp{Op: parser.OpLike, Left: row(ca, one), Right: row(ca, one)}, defaultMatchSelectivity},
	} {
		if got := clauseSelectivity(tc.expr, &SeqScan{Table: tbl}); got != tc.want {
			t.Errorf("%s = %v, want default %v", tc.name, got, tc.want)
		}
	}
}
