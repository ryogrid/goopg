package optimizer

import (
	"math"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor/hashsize"
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

// TestRangePairingAddsBackNullFrac pins rowest A1 (PG clausesel.c:283):
// a paired band (lo+hi-1) double-excludes NULLs, so the column's null
// fraction is added back. Band 20<=d<30 on a NullFrac=0.2 column must
// estimate ~0.2 above the same band on a null-free column; without
// stats the term stays omitted (slight under-estimate, never the
// independent product).
func TestRangePairingAddsBackNullFrac(t *testing.T) {
	loC, hiC := rqBound(parser.OpGe, 20), rqBound(parser.OpLt, 30)
	withNulls := rqScan(t)
	withNulls.Table.Stats.Columns[0].NullFrac = 0.2
	// Per-bound selectivities under the same stats (each bound already
	// accounts for NULLs its own way); the pairing must add exactly the
	// column's null fraction back on top (PG clausesel.c:283).
	lo := clauseSelectivity(loC, withNulls)
	hi := clauseSelectivity(hiC, withNulls)
	got := conjunctionSelectivity([]Expr{loC, hiC}, withNulls)
	if math.Abs(got-(lo+hi-1.0+0.2)) > 0.03 {
		t.Errorf("paired band = %.4f, want lo+hi-1+NullFrac = %.4f+%.4f-1+0.2",
			got, lo, hi)
	}
	// Punt bounds (histogram too short to read) plus a null fraction
	// must NOT fabricate confidence: PG falls back to
	// DEFAULT_RANGE_INEQ_SEL when either bound punted, before any
	// null correction (0.33+0.33-1+0.5 = +0.16 trusted would be 33x
	// over PG's 0.005).
	short := rqScan(t)
	short.Table.Stats.Columns[0] = catalog.ColumnStats{NDistinct: 2, Histogram: []string{"0"}, NullFrac: 0.5}
	if got := conjunctionSelectivity([]Expr{loC, hiC}, short); math.Abs(got-defaultRangeIneqSel) > 1e-9 {
		t.Errorf("paired punt bounds with NullFrac=0.5 = %.4f, want default %.4f", got, defaultRangeIneqSel)
	}
	// No stats at all: correction unavailable — falls back to the
	// default inequality selectivity, never a fabricated correction.
	nostats := rqScan(t)
	nostats.Table.Stats = nil
	if got := conjunctionSelectivity([]Expr{loC, hiC}, nostats); math.Abs(got-defaultRangeIneqSel) > 1e-9 {
		t.Errorf("paired band without stats = %.4f, want default %.4f", got, defaultRangeIneqSel)
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

// TestConvertTimevalueToScalar pins take2 P1-11b. Before it, `numericValue`
// handled only the numeric family, so `bucketFraction` returned a flat 0.5 for
// every date and timestamp and every histogram interpolation landed
// mid-bucket.
//
// Measured on lineitem's l_shipdate at three cut points, the error fell from
// -0.19% / -0.99% / -3.22% to -0.06% / -0.07% / -0.04% — the worst case
// improving about eightyfold. Bounded to begin with because ISO-8601 strings
// sort in date order, so the right BUCKET was already found; this removes the
// residual half-bucket.
func TestConvertTimevalueToScalar(t *testing.T) {
	for _, tc := range []struct {
		typ, lo, hi, lit string
		want             float64
	}{
		// A literal one quarter through a four-day bucket.
		{"date", "2024-01-01", "2024-01-05", "2024-01-02", 0.25},
		{"date", "2024-01-01", "2024-01-05", "2024-01-03", 0.50},
		{"timestamp", "2024-01-01 00:00:00", "2024-01-01 04:00:00", "2024-01-01 01:00:00", 0.25},
	} {
		got := bucketFraction(tc.lo, tc.hi, tc.lit, tc.typ)
		if math.Abs(got-tc.want) > 0.01 {
			t.Errorf("%s %q in [%s, %s]: fraction = %.4f, want %.2f",
				tc.typ, tc.lit, tc.lo, tc.hi, got, tc.want)
		}
	}

	// B-08 (take2 P1-11b remainder): text now interpolates through
	// convert_string_to_scalar instead of the flat half-bucket default.
	// Bucket ["c","g"] widens to the full a-z run (base 26, no common
	// prefix), so "d" sits one quarter across: (3-2)/(6-2) = 0.25.
	if got := bucketFraction("c", "g", "d", "text"); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("text bucketFraction = %.6f, want the interpolated 0.25 "+
			"(convert_string_to_scalar is ported)", got)
	}
	// An unparseable date must also fall back rather than return 0.
	if got := bucketFraction("2024-01-01", "2024-01-05", "not-a-date", "date"); got != 0.5 {
		t.Errorf("unparseable date = %.4f, want the 0.5 fallback", got)
	}
}

// TestConvertStringToScalar pins B-08, the string half of convert_to_scalar
// (postgres/src/backend/utils/adt/selfuncs.c:4787-4906): the adaptive digit
// range with A-Z/a-z/0-9 widening, the <9-chars full-ASCII rule, the common
// prefix strip, and the 12-byte base-N fraction (empty scales to 0).
func TestConvertStringToScalar(t *testing.T) {
	// Common-prefix strip ("zoom in"): the shared "555" carries no
	// information, so "5553000" scales as "3" over the digit range
	// (base 10): 0.3.
	if got := convertStringToScalar("5553000", "5551000", "5559000"); math.Abs(got-0.3) > 1e-9 {
		t.Errorf("prefix-stripped scalar = %.6f, want 0.3", got)
	}
	// ... and the bucket interpolates on the stripped scale:
	// (0.3-0.1)/(0.9-0.1) = 0.25.
	if got := bucketFraction("5551000", "5559000", "5553000", "text"); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("stripped bucketFraction = %.6f, want 0.25", got)
	}
	// ASCII widening: bounds "A".."C" touch the upper-case run, so the
	// range widens to all of A-Z (base 26) and "M" is 12/26 — not the
	// 1.0 an unwidened 3-wide range would clamp it to.
	if got := convertStringToScalar("M", "A", "C"); math.Abs(got-12.0/26.0) > 1e-9 {
		t.Errorf("widened scalar = %.6f, want 12/26", got)
	}
	// Narrow range without a widening trigger falls back to full ASCII:
	// bounds "!".."#" span 2, so "!" is (33-32)/96 = 1/96, not 0.
	if got := convertStringToScalar("!", "!", "#"); math.Abs(got-1.0/96.0) > 1e-9 {
		t.Errorf("narrow-range scalar = %.6f, want 1/96", got)
	}
	// Empty string scales to 0.
	if got := convertStringToScalar("", "a", "z"); got != 0 {
		t.Errorf("empty scalar = %.6f, want 0", got)
	}
	// 12-byte cap: bytes past the twelfth do not move the value.
	long := "abcdefghijklmnop"
	if got, want := convertStringToScalar(long, "a", "z"), convertStringToScalar(long[:12], "a", "z"); got != want {
		t.Errorf("uncapped scalar = %.9f, truncated = %.9f, want them equal", got, want)
	}
}

// TestStringHistogramInterpolatesAcrossTenBounds is the take2 06 §12.3
// shape for text: 11 bounds (10 buckets) a..k, literal "du" inside bucket
// ["d","e"]. The three share no common byte (bounds differ at once), so
// nothing strips; over the widened a-z run lo is 78/676, hi 104/676 and
// the literal 98/676, giving a bucket fraction of 20/26 and a selectivity
// of 3/10 + (20/26)/10 ≈ 0.3769 — not the 0.35 the flat 0.5 gave.
func TestStringHistogramInterpolatesAcrossTenBounds(t *testing.T) {
	bounds := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	got := histogramOpSelectivity(parser.OpLt, bounds, "du", "text")
	want := 0.3 + (20.0/26.0)/10.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("10-bound text histogram selectivity = %.6f, want %.6f", got, want)
	}
}

// TestPathCarriesItsOwnWidth pins take2 P4-01's first slice: a path that emits
// FEWER COLUMNS than its relation must be costed on its own width.
//
// pathgen.go read column counts from the RELS, justified by a comment saying "a
// parameterised path returns fewer ROWS than its rel but the same columns".
// True of parameterisation, false of PROJECTION — an index-only path emits only
// the columns its index covers. The hash geometry was therefore solved for the
// relation's full width while the executor measured the narrowed node's schema
// at runtime (`len(o.left.Schema())`), so planner and executor disagreed about
// the size of the same hash table. That is exactly what the shared
// hashsize.Choose exists to prevent.
func TestPathCarriesItsOwnWidth(t *testing.T) {
	rel := &RelOptInfo{Relids: 1, NCols: 9, AvgVarBytes: 99}

	// A path that does not narrow inherits the rel's figures.
	wide := &Path{Kind: PathSeqScan, Rel: rel}
	if got := pathNCols(wide); got != 9 {
		t.Errorf("un-narrowed path NCols = %d, want the rel's 9", got)
	}
	if got := pathAvgVarBytes(wide); got != 99 {
		t.Errorf("un-narrowed path AvgVarBytes = %v, want the rel's 99", got)
	}

	// A projecting path carries its own.
	narrow := &Path{Kind: PathIndexScan, Rel: rel, NCols: 2, AvgVarBytes: 21}
	if got := pathNCols(narrow); got != 2 {
		t.Errorf("projecting path NCols = %d, want its own 2", got)
	}
	if got := pathAvgVarBytes(narrow); got != 21 {
		t.Errorf("projecting path AvgVarBytes = %v, want its own 21", got)
	}

	// And the difference must reach the hash geometry, which is the whole
	// point: 9 columns of a 200k-row build batches at 64MB, 2 does not.
	const rows, mem = 200000.0, 64 << 20
	wideSizing := hashsize.Choose(rows, pathNCols(wide), pathAvgVarBytes(wide), mem)
	narrowSizing := hashsize.Choose(rows, pathNCols(narrow), pathAvgVarBytes(narrow), mem)
	if wideSizing.NBatch <= narrowSizing.NBatch {
		t.Errorf("narrowing did not reduce batching: wide NBatch=%d, narrow NBatch=%d",
			wideSizing.NBatch, narrowSizing.NBatch)
	}
	if narrowSizing.NBatch != 1 {
		t.Errorf("the narrowed build should fit in one batch at 64MB, got NBatch=%d",
			narrowSizing.NBatch)
	}
}
