package optimizer

import (
	"math"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// rqCol builds a resolved column reference on FROM-item 1, column 0, with a
// histogram spanning 0..100 so a bound's selectivity is its position.
func rqScan(t *testing.T) *SeqScan {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"},
		[]catalog.Column{{Name: "d", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	hist := make([]string, 0, 101)
	for i := 0; i <= 100; i++ {
		hist = append(hist, strconv.Itoa(i))
	}
	tbl.Stats = &catalog.TableStats{
		RowCount: 10000,
		Analyzed: true,
		Columns:  []catalog.ColumnStats{{NDistinct: 101, Histogram: hist}},
	}
	return &SeqScan{Table: tbl, EstRelRows: 10000}
}

func rqBound(op parser.OpCode, v int64) Expr {
	return &BinaryOp{
		Op:    op,
		Left:  &ColumnRef{Name: "d", Index: 0, SourceTableIdx: 1, Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: v},
	}
}

// TestRangeQueryClausePairsBoundsOnOneVariable is the case the item exists for.
// A band covering a tenth of the column must estimate near 0.1, not near the
// product of the two tail fractions (~0.18 for this band), which is what the
// independence assumption gives.
func TestRangeQueryClausePairsBoundsOnOneVariable(t *testing.T) {
	scan := rqScan(t)
	// 20 <= d < 30 on a 0..100 column: one tenth.
	and := &BinaryOp{
		Op:    parser.OpAnd,
		Left:  rqBound(parser.OpGe, 20),
		Right: rqBound(parser.OpLt, 30),
	}
	got := clauseSelectivity(and, scan)

	lo := clauseSelectivity(rqBound(parser.OpGe, 20), scan)
	hi := clauseSelectivity(rqBound(parser.OpLt, 30), scan)
	independent := lo * hi

	if math.Abs(got-0.10) > 0.03 {
		t.Errorf("paired selectivity = %.4f, want ~0.10 (lo=%.4f hi=%.4f)", got, lo, hi)
	}
	if math.Abs(got-independent) < 0.02 {
		t.Errorf("paired selectivity %.4f is indistinguishable from the independent "+
			"product %.4f — the pairing did not fire", got, independent)
	}
}

// TestRangeQueryClauseKeepsIndependenceAcrossVariables guards the other side:
// bounds on DIFFERENT columns must not be paired, or an unrelated pair of
// inequalities would be collapsed into one band.
func TestRangeQueryClauseKeepsIndependenceAcrossVariables(t *testing.T) {
	scan := rqScan(t)
	other := &BinaryOp{
		Op:    parser.OpLt,
		Left:  &ColumnRef{Name: "e", Index: 1, SourceTableIdx: 2, Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: 30},
	}
	and := &BinaryOp{Op: parser.OpAnd, Left: rqBound(parser.OpGe, 20), Right: other}
	got := clauseSelectivity(and, scan)
	want := clauseSelectivity(rqBound(parser.OpGe, 20), scan) * clauseSelectivity(other, scan)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cross-variable AND = %.6f, want the independent product %.6f", got, want)
	}
}

// TestRangeQueryClauseKeepsMoreRestrictiveDuplicate mirrors clausesel.c:456-471:
// `x > y AND x >= z` keeps only the more restrictive bound.
func TestRangeQueryClauseKeepsMoreRestrictiveDuplicate(t *testing.T) {
	scan := rqScan(t)
	conj := []Expr{rqBound(parser.OpGe, 20), rqBound(parser.OpGe, 40), rqBound(parser.OpLt, 60)}
	got := conjunctionSelectivity(conj, scan)
	// The 40 bound is the restrictive one, so the band is 40..60 = 0.2, not
	// 20..60 = 0.4.
	if math.Abs(got-0.20) > 0.05 {
		t.Errorf("duplicate lower bounds: selectivity = %.4f, want ~0.20", got)
	}
}

// TestNullTestSelectivityReadsNullFrac pins take2 P1-14. ANALYZE has always
// collected NullFrac and persisted it as stanullfrac, and `IS NULL` is the one
// clause it exists to answer — but there was no arm for IsNullExpr at all, so
// the predicate fell to a generic default and the statistic was never read.
func TestNullTestSelectivityReadsNullFrac(t *testing.T) {
	scan := rqScan(t)
	scan.Table.Stats.Columns[0].NullFrac = 0.25
	col := &ColumnRef{Name: "d", Index: 0, SourceTableIdx: 1, Type: catalog.Type{Name: "int4"}}

	if got := clauseSelectivity(&IsNullExpr{Operand: col}, scan); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("IS NULL selectivity = %.4f, want 0.25 (the column's NullFrac)", got)
	}
	if got := clauseSelectivity(&IsNullExpr{Operand: col, Negated: true}, scan); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("IS NOT NULL selectivity = %.4f, want 0.75", got)
	}
}

// TestNullTestSelectivityFallsBackWithoutStats mirrors nulltestsel's
// no-statistics arm: DEFAULT_UNK_SEL / DEFAULT_NOT_UNK_SEL.
func TestNullTestSelectivityFallsBackWithoutStats(t *testing.T) {
	scan := rqScan(t)
	scan.Table.Stats = nil
	col := &ColumnRef{Name: "d", Index: 0, SourceTableIdx: 1, Type: catalog.Type{Name: "int4"}}

	if got := clauseSelectivity(&IsNullExpr{Operand: col}, scan); math.Abs(got-defaultUnkSel) > 1e-9 {
		t.Errorf("IS NULL with no stats = %.5f, want DEFAULT_UNK_SEL %.5f", got, defaultUnkSel)
	}
	if got := clauseSelectivity(&IsNullExpr{Operand: col, Negated: true}, scan); math.Abs(got-defaultNotUnkSel) > 1e-9 {
		t.Errorf("IS NOT NULL with no stats = %.5f, want DEFAULT_NOT_UNK_SEL %.5f", got, defaultNotUnkSel)
	}
}

// TestDistinctIsSizedNotPassedThrough pins take2 P1-25. `SELECT DISTINCT` is a
// grouping over every output column, and upstream sizes it with
// estimate_num_groups (create_distinct_paths). goopg passed the child's row
// count straight through, so a DISTINCT that collapses a million rows to a
// hundred was costed — and every node above it sized — as if it collapsed
// nothing.
func TestDistinctIsSizedNotPassedThrough(t *testing.T) {
	scan := rqScan(t) // 10000 rows, one column, 101 distinct
	d := &Distinct{Child: scan}
	d.schema = Schema{SchemaColumn{Name: "d", Type: catalog.Type{Name: "int4"}}}

	in := EstimateRows(scan)
	out := EstimateRows(d)
	if out >= in {
		t.Errorf("DISTINCT over %d rows with ~101 distinct values estimated %d rows — "+
			"it was passed through rather than sized", in, out)
	}
	if out < 50 || out > 200 {
		t.Errorf("DISTINCT rows = %d, want roughly the column's distinct count (~101)", out)
	}
}

// TestEqjoinselInnerMCVBeatsFlatNDistinct pins take2 P1-15. Without the MCV
// branch every inner equi-join was priced at 1/max(nd1, nd2) — upstream's
// NO-STATISTICS fallback — even when the statistics needed to do better were
// present. The gap is widest exactly where being wrong is most expensive: two
// skewed columns, where a few values carry most of the rows.
func TestEqjoinselInnerMCVBeatsFlatNDistinct(t *testing.T) {
	cat := catalog.NewInMemory()
	mk := func(name string, rows int64, mcv []catalog.MCVEntry, nd int64) *catalog.Table {
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name},
			[]catalog.Column{{Name: "k", Type: catalog.Type{Name: "int4"}}})
		if err != nil {
			t.Fatal(err)
		}
		tbl.Stats = &catalog.TableStats{
			RowCount: rows, Analyzed: true,
			Columns: []catalog.ColumnStats{{NDistinct: nd, MCV: mcv}},
		}
		return tbl
	}
	// Both sides dominated by the SAME value: a real join between them
	// produces far more rows than 1/max(nd) predicts.
	skew := []catalog.MCVEntry{{Value: "7", Frequency: 0.8}}
	l := mk("jl", 10000, skew, 100)
	r := mk("jr", 10000, skew, 100)

	j := &Join{
		Left:  &SeqScan{Table: l, EstRelRows: 10000},
		Right: &SeqScan{Table: r, EstRelRows: 10000},
		LeftKey: &ColumnRef{Name: "k", Index: 0, SourceTableIdx: 1,
			Type: catalog.Type{Name: "int4"}},
		RightKey: &ColumnRef{Name: "k", Index: 0, SourceTableIdx: 2,
			Type: catalog.Type{Name: "int4"}},
	}
	pair := JoinKeyPair{Left: j.LeftKey, Right: j.RightKey}

	sel, ok := eqjoinselInnerMCV(j, pair)
	if !ok {
		t.Fatal("both sides carry MCV lists; the MCV branch must fire")
	}
	flat := 1.0 / float64(pairNDistinct(j, pair))
	// matchprodfreq alone is 0.8*0.8 = 0.64, far above 1/100.
	if sel <= flat {
		t.Errorf("MCV selectivity %.4f is not above the flat 1/max(nd) = %.4f; "+
			"two columns sharing an 80%% value must join far more than uniformly", sel, flat)
	}
	if sel < 0.6 {
		t.Errorf("MCV selectivity %.4f: matchprodfreq alone is 0.64, so the "+
			"estimate should be at least that", sel)
	}
}

// TestEqjoinselInnerMCVDeclinesWithoutBothLists keeps the caller on the
// 1/max(nd) path when either side lacks an MCV list — which is what upstream
// does, and what the no-statistics fallback exists for.
func TestEqjoinselInnerMCVDeclinesWithoutBothLists(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "one"},
		[]catalog.Column{{Name: "k", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 100, Analyzed: true,
		Columns: []catalog.ColumnStats{{NDistinct: 10}}}
	j := &Join{
		Left:     &SeqScan{Table: tbl, EstRelRows: 100},
		Right:    &SeqScan{Table: tbl, EstRelRows: 100},
		LeftKey:  &ColumnRef{Name: "k", Index: 0, SourceTableIdx: 1, Type: catalog.Type{Name: "int4"}},
		RightKey: &ColumnRef{Name: "k", Index: 0, SourceTableIdx: 2, Type: catalog.Type{Name: "int4"}},
	}
	if _, ok := eqjoinselInnerMCV(j, JoinKeyPair{Left: j.LeftKey, Right: j.RightKey}); ok {
		t.Error("with no MCV list on either side the MCV branch must decline")
	}
}

// TestUniqueSingleColumnKeyOverridesSampledNDistinct pins take2 P1-19:
// `get_variable_numdistinct`'s isunique branch (selfuncs.c:6332) — "assume it
// is unique no matter what pg_statistic says".
//
// goopg's per-column statistics come from a CAPPED RESERVOIR even though the
// row count is an exact full-scan figure, so a unique column whose sample
// understates its distinct count would otherwise have every equality against it
// over-estimated and every join on it under-divided.
func TestUniqueSingleColumnKeyOverridesSampledNDistinct(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "u"},
		[]catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	// A deliberately UNDERSTATED sample: 10 000 rows, sample says 50 distinct.
	tbl.Stats = &catalog.TableStats{
		RowCount: 10000, Analyzed: true,
		Columns: []catalog.ColumnStats{{NDistinct: 50}},
	}

	// Without uniqueness evidence the sampled figure stands.
	plain := &SeqScan{Table: tbl, EstRelRows: 10000}
	if got := columnNDistinctForChild(0, plain); got != 50 {
		t.Errorf("no unique key: ndistinct = %d, want the sampled 50", got)
	}

	// With a SINGLE-column unique key, the relation's tuple count wins.
	uniq := &SeqScan{Table: tbl, EstRelRows: 10000, UniqueKeys: [][]string{{"id"}}}
	if got := columnNDistinctForChild(0, uniq); got != 10000 {
		t.Errorf("single-column unique key: ndistinct = %d, want the row count 10000", got)
	}

	// A MULTI-column unique key says nothing about any one column —
	// plancat.c:2244 requires nkeycolumns == 1. Q9's two-column partsupp PK is
	// the standing example.
	multi := &SeqScan{Table: tbl, EstRelRows: 10000, UniqueKeys: [][]string{{"id", "other"}}}
	if got := columnNDistinctForChild(0, multi); got != 50 {
		t.Errorf("multi-column unique key: ndistinct = %d, want the sampled 50 "+
			"(a composite key does not make its members unique)", got)
	}
}

// TestColumnStatsResolverIsOneArmList pins take2 P1-26. There used to be two
// full walkers over the plan tree resolving a column to its statistics, and
// they had DRIFTED: the selectivity-side one had no *IndexScan arm, so a column
// reached through an index-probed leaf resolved to no statistics and every
// clause over it fell to a default selectivity — while the ndistinct-side
// walker resolved the same column fine.
//
// The two now share one arm list. This test pins the specific consequence, so a
// future split would fail rather than silently re-open the gap.
func TestColumnStatsResolverIsOneArmList(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "ix"},
		[]catalog.Column{{Name: "k", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{
		RowCount: 1000, Analyzed: true,
		Columns: []catalog.ColumnStats{{
			NDistinct: 10,
			MCV:       []catalog.MCVEntry{{Value: "1", Frequency: 0.5}},
		}},
	}

	// A SeqScan leaf always resolved.
	if got := columnStatsForChild(0, &SeqScan{Table: tbl}); got == nil || len(got.MCV) == 0 {
		t.Fatal("seq-scan leaf must resolve to the column's statistics")
	}
	// An INDEX-probed leaf is the case that used to return nil.
	ix := &IndexScan{Table: tbl}
	stats := columnStatsForChild(0, ix)
	if stats == nil {
		t.Fatal("index-probed leaf resolved to no statistics — the two resolvers " +
			"have drifted apart again")
	}
	if len(stats.MCV) == 0 {
		t.Error("index-probed leaf resolved without its MCV list")
	}
	// And the two resolvers must agree, which is the property that makes them
	// one arm list rather than two that happen to match today.
	if columnStatsForChild(0, ix) != columnStatsForChildBase(0, ix) {
		t.Error("the selectivity and cardinality resolvers returned different stats")
	}
}

// TestEquivClassPropagatesConstants pins take2 P1-20. An equivalence class is
// exactly the structure that lets `a = b AND a = 42` imply `b = 42`, and
// upstream's equivclass.c generates that restriction for every member. goopg's
// closure synthesised only column-to-column equalities, so the constant stayed
// on the relation it was written against.
//
// Measured on the bench clusters before the fix, for
// `customer, orders WHERE c_custkey = o_custkey AND c_custkey = 42`:
// goopg scanned all 1 500 000 orders at cost 32249.25 where PG used an index
// condition on 16 rows at cost 13.30.
func TestEquivClassPropagatesConstants(t *testing.T) {
	a := &ColumnRef{Name: "a", Index: 0, SourceTableIdx: 1, Type: catalog.Type{Name: "int4"}}
	b := &ColumnRef{Name: "b", Index: 1, SourceTableIdx: 2, Type: catalog.Type{Name: "int4"}}
	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpEq, Left: a, Right: b},
		&BinaryOp{Op: parser.OpEq, Left: a, Right: &IntegerConst{Value: 42}},
	}
	got := inferTransitiveEqualities(conjuncts)

	foundBEq42 := false
	for _, e := range got {
		bo, ok := e.(*BinaryOp)
		if !ok || bo.Op != parser.OpEq {
			continue
		}
		cr, isCol := bo.Left.(*ColumnRef)
		k, isConst := bo.Right.(*IntegerConst)
		if isCol && isConst && cr.SourceTableIdx == 2 && k.Value == 42 {
			foundBEq42 = true
		}
	}
	if !foundBEq42 {
		t.Errorf("`a = b AND a = 42` must imply `b = 42`; synthesised %d clause(s), "+
			"none of them the constant for b", len(got))
	}

	// The member the constant was already stated for must NOT get a duplicate.
	dupes := 0
	for _, e := range got {
		if bo, ok := e.(*BinaryOp); ok {
			if cr, isCol := bo.Left.(*ColumnRef); isCol && cr.SourceTableIdx == 1 {
				if _, isConst := bo.Right.(*IntegerConst); isConst {
					dupes++
				}
			}
		}
	}
	if dupes > 0 {
		t.Errorf("re-stated the constant for the member that already had it (%d times)", dupes)
	}
}

// TestEquivClassDoesNotPropagateNonLiterals guards the narrowness: only a
// literal may be restated against another relation. A volatile or
// parameterised expression duplicated onto a second relation would be
// evaluated twice and might not agree with itself.
func TestEquivClassDoesNotPropagateNonLiterals(t *testing.T) {
	a := &ColumnRef{Name: "a", Index: 0, SourceTableIdx: 1, Type: catalog.Type{Name: "int4"}}
	b := &ColumnRef{Name: "b", Index: 1, SourceTableIdx: 2, Type: catalog.Type{Name: "int4"}}
	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpEq, Left: a, Right: b},
		&BinaryOp{Op: parser.OpEq, Left: a, Right: &FuncCall{Name: "random"}},
	}
	for _, e := range inferTransitiveEqualities(conjuncts) {
		if bo, ok := e.(*BinaryOp); ok {
			if _, isFn := bo.Right.(*FuncCall); isFn {
				t.Error("a function call was propagated across the equivalence class")
			}
		}
	}
}
