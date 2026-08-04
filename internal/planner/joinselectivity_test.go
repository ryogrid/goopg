package planner

// M0127-P5.6-a — the join-selectivity substrate (joinselectivity.go).
//
// These tests are the whole falsifiable surface of the slice: nothing calls it
// yet (`sizeJoinRel` has no production implementation until P5.6-b, and
// `GOOPG_PGSHAPED_DP` is OFF), so no plan, cost or row count in the repository
// moves if any of it is wrong. What they pin, in order: each branch of PG's
// `get_variable_numdistinct` ladder; that `eqjoinsel` divides by the LARGER of
// the two ndistincts and applies both null fractions; that an operand resolves
// to its statistics by column NAME rather than by the clause-space `Index`;
// and that the clause dispatcher sends an equality to eqjoinsel even when it is
// not a two-sided equijoin.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// jsTable builds a one-table catalog entry with positional statistics over its
// columns. `rowCount` is the RAW row count ANALYZE measured.
func jsTable(t *testing.T, c catalog.Catalog, name string, cols []catalog.Column, rowCount int64, stats ...catalog.ColumnStats) *catalog.Table {
	t.Helper()
	tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
	if err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: rowCount, Columns: stats, Analyzed: true}
	return tbl
}

// jsCtx is a two-relation search whose rel 0 is `orders` and rel 1 is
// `lineitem`, both analysed. Only `relInfos` matters to this slice — the level
// lists and pathlists belong to the enumerator, which is not under test here.
func jsCtx(t *testing.T) *searchCtx {
	t.Helper()
	c := catalog.NewInMemory()
	orders := jsTable(t, c, "orders", []catalog.Column{
		{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}},
		{Name: "o_custkey", Type: catalog.Type{Name: "int4"}},
	}, 1500000,
		catalog.ColumnStats{NDistinctFrac: 1.0},
		catalog.ColumnStats{NDistinctFrac: 0.1},
	)
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}},
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000,
		catalog.ColumnStats{NDistinctFrac: 0.25},
		catalog.ColumnStats{NDistinct: 20000},
	)
	s, err := newSearchCtx(2, defaultCostParams())
	if err != nil {
		t.Fatal(err)
	}
	s.relInfos = []baseRelInfo{
		{table: orders, baseRows: 1500000},
		{table: lineitem, baseRows: 6000000},
	}
	return s
}

// jsCol is a clause operand as the search carries it: a `*ColumnRef` in the
// pre-search concatenation's coordinate space, so its `Index` is a GLOBAL
// offset that has nothing to do with the base table's column order.
func jsCol(index int, name string) *ColumnRef {
	return &ColumnRef{Index: index, Name: name, Type: catalog.Type{Name: "int4"}}
}

// TestGetVariableNumDistinctAbsoluteWins: a positive `stadistinct` is returned
// as-is and never scaled — PG returns before it ever looks at the relation's
// size (selfuncs.c). goopg reaches the positive case through `NDistinct` with
// no `NDistinctFrac` beside it.
func TestGetVariableNumDistinctAbsoluteWins(t *testing.T) {
	v := joinVarStats{stats: &catalog.ColumnStats{NDistinct: 42}, tuples: 6000000}
	nd, isDefault := getVariableNumDistinct(v)
	if nd != 42 || isDefault {
		t.Fatalf("nd=%v isDefault=%v; want 42, false (the absolute statistic, unscaled)", nd, isDefault)
	}
}

// TestGetVariableNumDistinctRelativeScales: a negative `stadistinct` is a
// FRACTION of the relation's raw rows, so it scales with the table. This is the
// branch that matters at TPC-H scale — a sampled absolute count saturates
// around 30,000 while the true distinct count of `l_orderkey` is in the
// millions (bushy.go:1031's finding, reached here through PG's own ladder).
func TestGetVariableNumDistinctRelativeScales(t *testing.T) {
	v := joinVarStats{stats: &catalog.ColumnStats{NDistinctFrac: 0.25}, tuples: 6000000}
	nd, isDefault := getVariableNumDistinct(v)
	if nd != 1500000 || isDefault {
		t.Fatalf("nd=%v isDefault=%v; want 1.5e6, false", nd, isDefault)
	}
}

// TestGetVariableNumDistinctFractionBeatsCount: when goopg has stored both
// halves of what upstream packs into one signed float, the FRACTION is the one
// that speaks. The assertion is against `ColumnStats.StaDistinct` rather than
// against a literal because the whole point of that method is that the
// estimator and the two catalog paths publishing `stadistinct` to the user
// agree; a test that hard-coded the answer here would still pass if one of them
// changed.
func TestGetVariableNumDistinctFractionBeatsCount(t *testing.T) {
	cs := catalog.ColumnStats{NDistinct: 30000, NDistinctFrac: 0.25}
	if sd := cs.StaDistinct(); sd != -0.25 {
		t.Fatalf("StaDistinct()=%v; want -0.25 (the fraction, in PG's negative convention)", sd)
	}
	nd, _ := getVariableNumDistinct(joinVarStats{stats: &cs, tuples: 6000000})
	if nd != 1500000 {
		t.Fatalf("nd=%v; want 1.5e6 — the saturated sample count 30000 must not win", nd)
	}
}

// TestGetVariableNumDistinctNoStatsLadder: the three statistic-free branches.
// A small relation is assumed all-distinct (an estimate that cannot be off by
// more than the relation's own size); a large one falls to the constant; an
// unsized one falls to the constant without consulting anything.
func TestGetVariableNumDistinctNoStatsLadder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tuples    float64
		wantND    float64
		wantIsDef bool
	}{
		{"small relation: every row distinct", 50, 50, false},
		{"large relation: DEFAULT_NUM_DISTINCT", 6000000, defaultNumDistinct, true},
		{"unknown size: DEFAULT_NUM_DISTINCT", 0, defaultNumDistinct, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nd, isDefault := getVariableNumDistinct(joinVarStats{tuples: tc.tuples})
			if nd != tc.wantND || isDefault != tc.wantIsDef {
				t.Fatalf("nd=%v isDefault=%v; want %v, %v", nd, isDefault, tc.wantND, tc.wantIsDef)
			}
		})
	}
}

// TestGetVariableNumDistinctBoolean: PG's BOOLOID arm. A boolean column has two
// values whether or not it has ever been analysed, so it must not be estimated
// at 200 — which would under-estimate a join on it by 100×.
func TestGetVariableNumDistinctBoolean(t *testing.T) {
	nd, isDefault := getVariableNumDistinct(joinVarStats{tuples: 6000000, isBool: true})
	if nd != 2 || isDefault {
		t.Fatalf("nd=%v isDefault=%v; want 2, false", nd, isDefault)
	}
}

// TestEqJoinSelectivityDividesByLargerND is the central property. The estimate
// is the MINIMUM of two upper bounds — (1-nf)(1-nf)/nd1 and .../nd2 — so the
// LARGER nd goes in the denominator. Getting this backwards would not fail
// loudly; it would over-estimate every joinrel by the ratio of the two
// ndistincts, which at TPC-H scale is four orders of magnitude.
func TestEqJoinSelectivityDividesByLargerND(t *testing.T) {
	small := joinVarStats{stats: &catalog.ColumnStats{NDistinct: 10}, tuples: 1000}
	large := joinVarStats{stats: &catalog.ColumnStats{NDistinct: 1000}, tuples: 1000}
	want := 1.0 / 1000.0
	if got := eqJoinSelectivity(small, large); math.Abs(got-want) > 1e-12 {
		t.Fatalf("selectivity=%v; want %v (1/max(10,1000))", got, want)
	}
	// Symmetric: the operand order of a clause is a syntactic accident and
	// must not change the joinrel's size.
	if got := eqJoinSelectivity(large, small); math.Abs(got-want) > 1e-12 {
		t.Fatalf("reversed selectivity=%v; want %v — eqjoinsel must be symmetric", got, want)
	}
}

// TestEqJoinSelectivityAppliesBothNullFractions: NULLs never match under an
// equality, so each side's non-null fraction multiplies the estimate.
func TestEqJoinSelectivityAppliesBothNullFractions(t *testing.T) {
	l := joinVarStats{stats: &catalog.ColumnStats{NDistinct: 100, NullFrac: 0.5}, tuples: 1000}
	r := joinVarStats{stats: &catalog.ColumnStats{NDistinct: 100, NullFrac: 0.2}, tuples: 1000}
	want := 0.5 * 0.8 / 100.0
	if got := eqJoinSelectivity(l, r); math.Abs(got-want) > 1e-12 {
		t.Fatalf("selectivity=%v; want %v ((1-0.5)(1-0.2)/100)", got, want)
	}
}

// TestEqJoinSelectivityUnknownVarsGiveDefaultEqSel: two operands PG cannot
// describe land on 1/DEFAULT_NUM_DISTINCT, which IS upstream's DEFAULT_EQ_SEL.
// The assertion is written against goopg's own `defaultEqSelectivity` constant
// to pin that the two agree: an unanalysed join and an unanalysable one must
// not be charged two different "we don't know" numbers.
func TestEqJoinSelectivityUnknownVarsGiveDefaultEqSel(t *testing.T) {
	got := eqJoinSelectivity(joinVarStats{}, joinVarStats{})
	if math.Abs(got-defaultEqSelectivity) > 1e-12 {
		t.Fatalf("selectivity=%v; want DEFAULT_EQ_SEL %v", got, defaultEqSelectivity)
	}
}

// TestExamineJoinVarResolvesByNameNotIndex is the coordinate-space guard. The
// search's clauses are written in the pre-search CONCATENATION's space, so a
// `ColumnRef.Index` of 3 is the fourth column of `orders ++ lineitem`, not the
// fourth column of `lineitem`. Resolving positionally would read the wrong
// column's statistics — or, as here, none at all — and the join estimate would
// change merely because the relation moved in the FROM list.
func TestExamineJoinVarResolvesByNameNotIndex(t *testing.T) {
	s := jsCtx(t)
	// `l_partkey` is lineitem's column 1, but column 3 of the concatenation.
	v := s.examineJoinVar(jsCol(3, "l_partkey"), relsetOf(1))
	if v.stats == nil {
		t.Fatal("l_partkey did not resolve; a positional read of Index=3 would look like this")
	}
	if v.stats.NDistinct != 20000 {
		t.Fatalf("resolved NDistinct=%d; want 20000 (l_partkey's, not l_orderkey's)", v.stats.NDistinct)
	}
	if v.tuples != 6000000 {
		t.Fatalf("tuples=%v; want lineitem's RAW row count 6e6", v.tuples)
	}
}

// TestExamineJoinVarRejectsMultiRelOperand: `b.y + c.z` has no single relation
// whose statistics could describe it, which is exactly the state PG leaves
// `vardata->rel` NULL for. The unresolved answer is correct, not a failure —
// but it must be UNRESOLVED rather than attributed to whichever relation
// happens to be lowest in the relset.
func TestExamineJoinVarRejectsMultiRelOperand(t *testing.T) {
	s := jsCtx(t)
	v := s.examineJoinVar(jsCol(0, "o_orderkey"), relsetOf(0)|relsetOf(1))
	if v.stats != nil || v.tuples != 0 {
		t.Fatalf("a two-rel operand resolved to %+v; want the unresolved zero value", v)
	}
}

// TestExamineJoinVarSubqueryLeafIsUnresolved: `buildInitialRels` admits every
// FROM item, so a search relation need not have a catalog table behind it. Such
// a rel has no per-column statistics to read, and the estimator must fall to
// the default rather than dereference a nil table.
func TestExamineJoinVarSubqueryLeafIsUnresolved(t *testing.T) {
	s := jsCtx(t)
	s.relInfos[1] = baseRelInfo{baseRows: 900}
	v := s.examineJoinVar(jsCol(3, "sub_col"), relsetOf(1))
	if v.stats != nil || v.tuples != 0 {
		t.Fatalf("a table-less rel resolved to %+v; want the unresolved zero value", v)
	}
	if nd, isDefault := getVariableNumDistinct(v); nd != defaultNumDistinct || !isDefault {
		t.Fatalf("nd=%v isDefault=%v; want the default", nd, isDefault)
	}
}

// jsEqui is `orders.o_custkey = lineitem.l_orderkey` in the search's canonical
// two-sided form, with the operands NAMED — an unnamed `col(i)` operand would
// make every name-resolution assertion below pass vacuously.
func jsEqui(op parser.OpCode) *restrictInfo {
	l, r := jsCol(1, "o_custkey"), jsCol(2, "l_orderkey")
	return &restrictInfo{
		clause:      &BinaryOp{Op: op, Left: l, Right: r},
		relids:      relsetOf(0) | relsetOf(1),
		leftKey:     l,
		rightKey:    r,
		leftRelids:  relsetOf(0),
		rightRelids: relsetOf(1),
		isEquijoin:  true,
		ecID:        noEquivClass,
	}
}

// TestJoinClauseSelectivityEquijoin: the canonical case end to end.
// o_custkey has 0.1 × 1.5e6 = 150,000 distinct; l_orderkey 0.25 × 6e6 =
// 1,500,000; the larger one is the divisor.
func TestJoinClauseSelectivityEquijoin(t *testing.T) {
	s := jsCtx(t)
	want := 1.0 / 1500000.0
	if got := s.joinClauseSelectivity(jsEqui(parser.OpEq)); math.Abs(got-want) > 1e-15 {
		t.Fatalf("selectivity=%v; want %v", got, want)
	}
}

// TestJoinClauseSelectivityNotEqual is `neqjoinsel`'s inner-join arm: one minus
// the negator's selectivity. A `<>` join qual is very nearly unrestrictive, and
// charging it the equality's selectivity instead would under-estimate the
// joinrel by six orders of magnitude at this scale.
func TestJoinClauseSelectivityNotEqual(t *testing.T) {
	s := jsCtx(t)
	want := 1.0 - 1.0/1500000.0
	if got := s.joinClauseSelectivity(jsEqui(parser.OpNe)); math.Abs(got-want) > 1e-15 {
		t.Fatalf("selectivity=%v; want %v", got, want)
	}
}

// TestJoinClauseSelectivityInequality: PG has no join-selectivity model for an
// ordering comparison — `scalarltjoinsel` is a one-line return of
// DEFAULT_INEQ_SEL — so neither has goopg, and it must not silently reuse the
// equality path just because the operands happen to be two columns.
func TestJoinClauseSelectivityInequality(t *testing.T) {
	s := jsCtx(t)
	for _, op := range []parser.OpCode{parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe} {
		if got := s.joinClauseSelectivity(jsEqui(op)); got != defaultIneqJoinSel {
			t.Fatalf("op %v: selectivity=%v; want DEFAULT_INEQ_SEL %v", op, got, defaultIneqJoinSel)
		}
	}
}

// TestJoinClauseSelectivityNonEquijoinEqualityStillUsesEqjoinsel is the
// dispatch distinction spelled out. `a.x = b.y + c.z` is an equality with NO
// two-sided operand split, so `isEquijoin` is false and it can key no hash
// join — but it is still an equality, and PG still prices it with eqjoinsel.
// Treating it as an unhandled clause would charge 0.5 where upstream charges
// 0.005: a 100× over-estimate of every joinrel above it.
func TestJoinClauseSelectivityNonEquijoinEqualityStillUsesEqjoinsel(t *testing.T) {
	s := jsCtx(t)
	ri := &restrictInfo{
		clause: &BinaryOp{
			Op:    parser.OpEq,
			Left:  jsCol(1, "o_custkey"),
			Right: &BinaryOp{Op: parser.OpAdd, Left: jsCol(2, "l_orderkey"), Right: jsCol(3, "l_partkey")},
		},
		relids: relsetOf(0) | relsetOf(1),
		ecID:   noEquivClass,
	}
	got := s.joinClauseSelectivity(ri)
	if math.Abs(got-defaultEqSelectivity) > 1e-12 {
		t.Fatalf("selectivity=%v; want DEFAULT_EQ_SEL %v, not the unhandled-clause %v",
			got, defaultEqSelectivity, defaultUnhandledClauseSel)
	}
}

// TestJoinClauseSelectivityUnhandledShapes: a clause that is not a binary
// comparison at all, and the nil guards. PG's `clause_selectivity_ext`
// initialises its answer to 0.5 for exactly this case.
func TestJoinClauseSelectivityUnhandledShapes(t *testing.T) {
	s := jsCtx(t)
	for _, tc := range []struct {
		name string
		ri   *restrictInfo
	}{
		{"nil restrictInfo", nil},
		{"no clause expression", &restrictInfo{relids: relsetOf(0) | relsetOf(1), ecID: noEquivClass}},
		{"non-comparison operator", &restrictInfo{
			clause: &BinaryOp{Op: parser.OpAdd, Left: jsCol(1, "o_custkey"), Right: jsCol(2, "l_orderkey")},
			relids: relsetOf(0) | relsetOf(1), ecID: noEquivClass,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.joinClauseSelectivity(tc.ri); got != defaultUnhandledClauseSel {
				t.Fatalf("selectivity=%v; want %v", got, defaultUnhandledClauseSel)
			}
		})
	}
}

// TestClampSelectivity: the range clamp, and the NaN arm. A NaN selectivity
// would propagate into every joinrel above it and compare false against every
// cost, which disables `add_path`'s pruning silently instead of producing a
// visibly wrong plan — so it is mapped to the same "no idea" constant an
// unhandled clause gets.
func TestClampSelectivity(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{-0.5, 0},
		{0, 0},
		{0.25, 0.25},
		{1, 1},
		{1.5, 1},
		{math.NaN(), defaultUnhandledClauseSel},
	} {
		if got := clampSelectivity(tc.in); got != tc.want {
			t.Fatalf("clampSelectivity(%v)=%v; want %v", tc.in, got, tc.want)
		}
	}
}
