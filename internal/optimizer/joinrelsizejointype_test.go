package optimizer

// C-05 (P1-18) — `calc_joinrel_size_estimate`'s jointype switch
// (costsize.c:5595-5633) inside the join search, the jointype-aware clause
// selectivity it depends on (`eqjoinsel_semi`, `neqjoinsel`'s semi arm), and
// the left-only rel-level publication for SEMI/ANTI.
//
// Every arm is FORCED by hand. No SEMI/ANTI/RIGHT/FULL joinrel is searchable
// today (SEMI/ANTI sit above the search, RIGHT is C-04b, FULL is declined at
// path generation), and the LEFT arm is numerically the C-04a floor it
// replaces — so these tests are the switch's whole falsifiable surface until
// the admitting slices land. "An unwinnable path is an untested path."
//
// Expectations are derived from the fixture's named constants, never written
// as row-count literals: the fixture is `jrsCatalog` (lineitem 6M raw, partsupp
// 800k raw, nd 200000 on both key columns), and each want below is the PG
// formula over those figures.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

const (
	// The fixture's figures (jrsCatalog).
	c05LineitemRaw = 6000000.0
	c05PartsuppRaw = 800000.0
	c05KeyND       = 200000.0
)

// c05SJI is a SpecialJoinInfo whose LHS is rel 0 and RHS rel 1, in the
// post-swap orientation `makeJoinRel` hands the sizer (outer covers the LHS).
func c05SJI(jt parser.JoinType) *SpecialJoinInfo {
	return &SpecialJoinInfo{
		Jointype:     jt,
		MinLefthand:  relsetOf(0),
		MinRighthand: relsetOf(1),
		SynLefthand:  relsetOf(0),
		SynRighthand: relsetOf(1),
	}
}

// TestCalcJoinrelSizeJointypeSwitch pins every arm of the switch on ONE
// clause with measured statistics (`jselec = 1/max(nd) = 1/200000`, not a
// default, so the fallback cap stays out of the way) and input sizes chosen so
// that each arm's distinguishing term is the one that decides:
//
//   - the product `o·i·jselec` is BELOW the preserved side for the outer arms,
//     so LEFT/RIGHT/FULL are decided by their floors, not by the product;
//   - the inner is small enough that `eqjoinsel_semi`'s `inner_rel->rows`
//     clamp on nd2 bites, so SEMI/ANTI are decided by `nd2/nd1`, not by 1.0.
func TestCalcJoinrelSizeJointypeSwitch(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	s := jrsCtx(t, lineitem, partsupp)
	clause := []*restrictInfo{jrsEq("l_partkey", "ps_partkey", noEquivClass)}

	const (
		o = c05LineitemRaw // outer (rel 0, lineitem) rows
		i = 100.0          // inner (rel 1, partsupp) rows: filtered hard
	)
	product := o * i / c05KeyND // 3000: below o, above i
	// eqjoinsel_semi: nd2 = min(200000, inner rows) = 100 < nd1 = 200000,
	// so the match fraction is nd2/nd1 (nullfrac1 = 0 in the fixture).
	semiSel := i / c05KeyND

	cases := []struct {
		name      string
		sj        *SpecialJoinInfo
		wantRows  float64
		wantWidth int
	}{
		{"nil sjinfo is INNER", nil, product, 40 + 24},
		{"INNER", c05SJI(parser.JoinInner), product, 40 + 24},
		{"LEFT floors at the LHS", c05SJI(parser.JoinLeft), math.Max(product, o), 40 + 24},
		{"RIGHT floors at the RHS (goopg keeps RIGHT un-commuted)", c05SJI(parser.JoinRight), math.Max(product, i), 40 + 24},
		{"FULL floors at both", c05SJI(parser.JoinFull), math.Max(math.Max(product, o), i), 40 + 24},
		{"SEMI scales the LHS by the match fraction", c05SJI(parser.JoinSemi), o * semiSel, 40},
		{"ANTI scales the LHS by its complement", c05SJI(parser.JoinAnti), o * (1 - semiSel), 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outer, inner := jrsRels(o, i)
			rows, width := s.calcJoinrelSize(c, outer, inner, clause, tc.sj)
			wantRows(t, rows, clampRowEst(tc.wantRows), tc.name)
			if width != tc.wantWidth {
				t.Fatalf("width=%d, want %d", width, tc.wantWidth)
			}
		})
	}

	// The RIGHT floor must bite on the RHS when THAT is the large side — the
	// mirror of the LEFT case, so a swapped arm cannot pass both.
	t.Run("RIGHT floors at a large RHS", func(t *testing.T) {
		outer, inner := jrsRels(i, o)
		rows, _ := s.calcJoinrelSize(c, outer, inner, clause, c05SJI(parser.JoinRight))
		wantRows(t, rows, clampRowEst(o), "RIGHT with the RHS large")
		rowsLeft, _ := s.calcJoinrelSize(c, outer, inner, clause, c05SJI(parser.JoinLeft))
		wantRows(t, rowsLeft, clampRowEst(product), "LEFT with the LHS small: the product already exceeds it")
	})
}

// TestCalcJoinrelSizeLeftArmIsTheC04aFloor: the production shape today. The
// LEFT arm is `max(clampedProduct, outer)` and the C-04a floor was
// `max(clampRowEst(clampedProduct), outer)`; for an integral outer the two are
// the same number, which is what makes C-05 a zero-drift change on every LEFT
// joinrel the search builds. Pinned on both sides of the floor.
func TestCalcJoinrelSizeLeftArmIsTheC04aFloor(t *testing.T) {
	c, partsupp, lineitem := jrsCatalog(t)
	s := jrsCtx(t, lineitem, partsupp)
	clause := []*restrictInfo{jrsEq("l_partkey", "ps_partkey", noEquivClass)}
	for _, io := range [][2]float64{{c05LineitemRaw, 100}, {c05LineitemRaw, c05PartsuppRaw}} {
		outer, inner := jrsRels(io[0], io[1])
		innerRows, _ := s.calcJoinrelSize(c, outer, inner, clause, nil)
		leftRows, _ := s.calcJoinrelSize(c, outer, inner, clause, c05SJI(parser.JoinLeft))
		wantRows(t, leftRows, math.Max(innerRows, io[0]), "LEFT vs floored INNER")
	}
}

// TestCalcJoinrelSizeSemiFKArm: `get_foreign_key_join_selectivity`'s SEMI/ANTI
// arm (costsize.c:5694-5697, :5811-5827). A DECLARED FK whose referenced table
// is exactly the singleton inner replaces its clause with `ref_rows/ref_tuples`
// — the fraction of parent rows that survive the parent's own restrictions —
// so a semijoin against a half-filtered parent matches half the children. The
// three punts are pinned beside it: a unique index alone proves nothing about
// the match FRACTION (C-05 DESIGN §4.4), a two-relation inner cannot be
// reasoned about, and the referenced rel on the OUTSIDE does not help.
func TestCalcJoinrelSizeSemiFKArm(t *testing.T) {
	const (
		ordersRaw   = 1500000.0
		lineitemRaw = 6000000.0
		keyND       = 100000.0
		ordersRows  = ordersRaw / 2 // the inner survives a 50% restriction
	)
	build := func(t *testing.T, declareFK, uniqueIndex bool) (catalog.Catalog, *searchCtx, *restrictInfo) {
		t.Helper()
		c := catalog.NewInMemory()
		orders := jsTable(t, c, "orders", []catalog.Column{
			{Name: "o_orderkey", Type: catalog.Type{Name: "int4"}},
		}, int64(ordersRaw), catalog.ColumnStats{NDistinct: keyND})
		lineitem := jsTable(t, c, "lineitem", []catalog.Column{
			{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}},
		}, int64(lineitemRaw), catalog.ColumnStats{NDistinct: keyND})
		if declareFK {
			lineitem.ForeignKeys = []catalog.ForeignKey{{
				Name: "lineitem_orderkey_fkey", Columns: []string{"l_orderkey"},
				RefTable: "orders", RefColumns: []string{"o_orderkey"},
			}}
		}
		if uniqueIndex {
			if _, err := c.CreateIndex(parser.ObjectName{Name: "orders_pkey"}, orders,
				[]string{"o_orderkey"}, true, "btree", true); err != nil {
				t.Fatal(err)
			}
		}
		s, err := newSearchCtx(3, defaultCostParams(), nil)
		if err != nil {
			t.Fatal(err)
		}
		s.relInfos = []baseRelInfo{
			{table: lineitem, baseRows: int64(lineitemRaw)},
			{table: orders, baseRows: int64(ordersRaw)},
			{table: orders, baseRows: int64(ordersRaw)},
		}
		return c, s, jrsEq("l_orderkey", "o_orderkey", noEquivClass)
	}
	// Without the FK the clause is priced by eqjoinsel_semi: nd1 = nd2 =
	// keyND ≤ ordersRows, so every non-null LHS row matches.
	semiSelNoFK := 1.0

	t.Run("declared FK, singleton inner: ref_rows/ref_tuples", func(t *testing.T) {
		c, s, ri := build(t, true, false)
		outer, inner := jrsRels(lineitemRaw, ordersRows)
		rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{ri}, c05SJI(parser.JoinSemi))
		wantRows(t, rows, clampRowEst(lineitemRaw*ordersRows/ordersRaw), "SEMI over a declared FK")
		anti, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{ri}, c05SJI(parser.JoinAnti))
		wantRows(t, anti, clampRowEst(lineitemRaw*(1-ordersRows/ordersRaw)), "ANTI over a declared FK")
	})
	t.Run("unique index alone is not FK evidence for SEMI", func(t *testing.T) {
		c, s, ri := build(t, false, true)
		outer, inner := jrsRels(lineitemRaw, ordersRows)
		rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{ri}, c05SJI(parser.JoinSemi))
		wantRows(t, rows, clampRowEst(lineitemRaw*semiSelNoFK), "SEMI with a unique inner and no FK")
		// ...while the same evidence DOES fire for INNER (the P5.6-b rule).
		innerRows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{ri}, nil)
		wantRows(t, innerRows, clampRowEst(lineitemRaw*ordersRows/ordersRaw), "INNER with a unique inner")
	})
	t.Run("declared FK, two-relation inner: punt", func(t *testing.T) {
		c, s, ri := build(t, true, false)
		outer := newRelOptInfo(relsetOf(0), lineitemRaw, 40)
		inner := newRelOptInfo(relsetOf(1)|relsetOf(2), ordersRows, 48)
		sj := &SpecialJoinInfo{Jointype: parser.JoinSemi, MinLefthand: relsetOf(0), MinRighthand: relsetOf(1) | relsetOf(2),
			SynLefthand: relsetOf(0), SynRighthand: relsetOf(1) | relsetOf(2)}
		rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{ri}, sj)
		wantRows(t, rows, clampRowEst(lineitemRaw*semiSelNoFK), "SEMI whose inner is a join")
	})
	t.Run("declared FK, referenced rel on the outside: punt", func(t *testing.T) {
		c, s, _ := build(t, true, false)
		// orders SEMI lineitem: the parent is the OUTER side.
		l, r := jsCol(0, "o_orderkey"), jsCol(9, "l_orderkey")
		ri := &restrictInfo{clause: &BinaryOp{Op: parser.OpEq, Left: l, Right: r},
			relids: relsetOf(0) | relsetOf(1), leftKey: l, rightKey: r,
			leftRelids: relsetOf(1), rightRelids: relsetOf(0), isEquijoin: true, ecID: noEquivClass}
		outer := newRelOptInfo(relsetOf(1), ordersRows, 24)
		inner := newRelOptInfo(relsetOf(0), lineitemRaw, 40)
		sj := &SpecialJoinInfo{Jointype: parser.JoinSemi, MinLefthand: relsetOf(1), MinRighthand: relsetOf(0),
			SynLefthand: relsetOf(1), SynRighthand: relsetOf(0)}
		rows, _ := s.calcJoinrelSize(c, outer, inner, []*restrictInfo{ri}, sj)
		// eqjoinsel_semi with the orders column as v1 (oriented by the
		// sizer, NOT by the clause's canonical split): nd1 = nd2 → 1.0.
		wantRows(t, rows, clampRowEst(ordersRows*semiSelNoFK), "SEMI with the parent outside")
	})
}

// TestEqJoinSelectivitySemi pins `eqjoinsel_semi`'s nd arms and BOTH nd2
// clamps (selfuncs.c:2668-2681), each against the formula.
func TestEqJoinSelectivitySemi(t *testing.T) {
	const (
		nd1      = 100.0
		nd2Stats = 1000.0
		nullfrac = 0.1
	)
	v1 := joinVarStats{stats: &catalog.ColumnStats{NDistinct: nd1, NullFrac: nullfrac}, tuples: 1e6}
	v2 := joinVarStats{stats: &catalog.ColumnStats{NDistinct: nd2Stats}, tuples: 1e6}

	sel, isdefault := eqJoinSelectivitySemi(v1, v2, 0)
	if isdefault {
		t.Fatal("two measured nds reported as a default")
	}
	wantRows(t, sel, 1-nullfrac, "nd1 <= nd2: every non-null LHS row matches")

	// inner_rel->rows clamp: nd2 = 50 < nd1 → nd2/nd1.
	sel, _ = eqJoinSelectivitySemi(v1, v2, 50)
	wantRows(t, sel, (50/nd1)*(1-nullfrac), "inner rows clamp nd2")

	// vardata2->rel->rows clamp (the base rel's post-filter rows), the
	// tighter of the two here.
	v2rows := v2
	v2rows.rows = 20
	sel, _ = eqJoinSelectivitySemi(v1, v2rows, 50)
	wantRows(t, sel, (20/nd1)*(1-nullfrac), "rel rows clamp nd2")

	// A default nd on either side is the 0.5 punt — unless a clamp made it
	// a measurement. v2 has no statistics: nd2 defaults to 200 (tuples ≥
	// DEFAULT_NUM_DISTINCT), which is a guess...
	v2none := joinVarStats{tuples: 1e6}
	sel, isdefault = eqJoinSelectivitySemi(v1, v2none, 0)
	if !isdefault {
		t.Fatal("an unanalysed inner must report a default")
	}
	wantRows(t, sel, 0.5*(1-nullfrac), "default nd2: the 0.5 punt")
	// ...and the inner's row count turns it into one.
	sel, isdefault = eqJoinSelectivitySemi(v1, v2none, 40)
	if isdefault {
		t.Fatal("a clamped nd2 is a measurement of the relation's size")
	}
	wantRows(t, sel, (40/nd1)*(1-nullfrac), "clamped default nd2")
}

// TestEqjoinselSemiCoreMCVArm: the MCV arm shared by the search sizer and the
// plan-node estimator (`semiPairMatchFraction`). Two MCV lists with one common
// value: the matched frequency mass is a measured floor on the match fraction
// and the remainder is priced by the discounted nd heuristic.
func TestEqjoinselSemiCoreMCVArm(t *testing.T) {
	const (
		nd1, nd2  = 10.0, 10.0
		nullfrac1 = 0.1
		freqA     = 0.3
	)
	st1 := &catalog.ColumnStats{NDistinct: nd1, NullFrac: nullfrac1,
		MCV: []catalog.MCVEntry{{Value: "a", Frequency: freqA}, {Value: "b", Frequency: 0.2}}}
	st2 := &catalog.ColumnStats{NDistinct: nd2,
		MCV: []catalog.MCVEntry{{Value: "a", Frequency: 0.5}, {Value: "c", Frequency: 0.5}}}
	got := eqjoinselSemiCore(st1, st2, nd1, nd2, true, true, nullfrac1)
	// nmatches = 1; nd1-1 <= nd2-1 → uncertainfrac = 1.0;
	// uncertain = 1 - matchfreq1 - nullfrac1.
	want := freqA + 1.0*(1-freqA-nullfrac1)
	wantRows(t, got, want, "MCV arm")

	// The search-side wrapper and the plan-node estimator go through the
	// same core: feeding the wrapper the same statistics must land on the
	// same number, and it must NOT be reported as a default (the matched
	// mass is measured).
	sel, isdefault := eqJoinSelectivitySemi(joinVarStats{stats: st1, tuples: 100}, joinVarStats{stats: st2, tuples: 100}, 0)
	wantRows(t, sel, want, "search-side wrapper over the same MCVs")
	if isdefault {
		t.Fatal("an MCV-arm estimate is a measurement")
	}
}

// TestJoinClauseSelectivityForJoinSemiArms: `<>` is neqjoinsel's semi arm
// (`1 - nullfrac` of the OUTER var), the orientation is decided by the sizer
// and not by the clause's canonical split, and non-semi jointypes fall
// through to the inner arms unchanged.
func TestJoinClauseSelectivityForJoinSemiArms(t *testing.T) {
	const nullfracL = 0.25
	c := catalog.NewInMemory()
	partsupp := jsTable(t, c, "partsupp", []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}},
	}, 800000, catalog.ColumnStats{NDistinct: c05KeyND})
	lineitem := jsTable(t, c, "lineitem", []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}},
	}, 6000000, catalog.ColumnStats{NDistinct: c05KeyND, NullFrac: nullfracL})
	s := jrsCtx(t, lineitem, partsupp)
	outer, inner := jrsRels(c05LineitemRaw, c05PartsuppRaw)

	ne := jrsEq("l_partkey", "ps_partkey", noEquivClass)
	ne.clause.(*BinaryOp).Op = parser.OpNe
	sel, isdefault := s.joinClauseSelectivityForJoin(ne, parser.JoinSemi, outer, inner)
	wantRows(t, sel, 1-nullfracL, "<> under SEMI: 1 - nullfrac(outer)")
	if isdefault {
		t.Fatal("a resolved outer null fraction is a measurement")
	}
	// Reversed orientation: partsupp is the outer. Its null fraction is 0.
	sel, _ = s.joinClauseSelectivityForJoin(ne, parser.JoinSemi, inner, outer)
	wantRows(t, sel, 1.0, "<> under SEMI with the other side outer")

	// Inner-join arms are untouched for every non-semi jointype.
	eq := jrsEq("l_partkey", "ps_partkey", noEquivClass)
	innerSel, _ := s.joinClauseSelectivityExt(eq)
	for _, jt := range []parser.JoinType{parser.JoinInner, parser.JoinLeft, parser.JoinRight, parser.JoinFull} {
		got, _ := s.joinClauseSelectivityForJoin(eq, jt, outer, inner)
		wantRows(t, got, innerSel, "non-semi jointype delegates to eqjoinsel_inner")
	}
}

// TestMakeJoinRelPublishesLeftOnlyForSemiAnti: the rel-level publication
// through the REAL builder. For SEMI and ANTI the joinrel carries the LHS's
// Width/NCols/AvgVarBytes/ColVarBytes alone; for LEFT and INNER the
// concatenation. Then the parent of a SEMI child sums the child's NARROWED
// figures with its own inner — which is the whole point: the level above is
// sized on what the child emits (C-05 DESIGN §4.6).
func TestMakeJoinRelPublishesLeftOnlyForSemiAnti(t *testing.T) {
	type figures struct {
		width, ncols int
		avg          float64
		cols         int
	}
	figuresOf := func(r *RelOptInfo) figures {
		return figures{r.Width, r.NCols, r.AvgVarBytes, len(r.ColVarBytes)}
	}
	build := func(t *testing.T, jt parser.JoinType) (*searchCtx, *RelOptInfo, *RelOptInfo, *RelOptInfo) {
		t.Helper()
		var sjis []*SpecialJoinInfo
		if jt != parser.JoinInner {
			sjis = []*SpecialJoinInfo{c05SJI(jt)}
		}
		s, err := newSearchCtx(3, defaultCostParams(), sjis)
		if err != nil {
			t.Fatal(err)
		}
		s.relInfos = make([]baseRelInfo, 3)
		specs := []struct {
			rows  float64
			width int
			ncols int
			avg   float64
			col   string
		}{{1000, 40, 4, 12.5, "a"}, {500, 24, 2, 3.5, "b"}, {200, 16, 1, 1.0, "c"}}
		for i, sp := range specs {
			rel := newRelOptInfo(relsetOf(i), sp.rows, sp.width)
			rel.NCols, rel.AvgVarBytes = sp.ncols, sp.avg
			rel.ColVarBytes = map[string]float64{sp.col: sp.avg}
			if err := s.addRel(rel); err != nil {
				t.Fatal(err)
			}
			addPath(rel, &Path{Kind: PathPrebuilt, Rel: rel, Rows: sp.rows, Cost: Cost{Total: sp.rows}}, "test")
			setCheapest(rel)
		}
		s.clauses = &restrictInfoList{all: []*restrictInfo{
			jrsEq("x", "y", noEquivClass),
			{clause: &BinaryOp{Op: parser.OpEq, Left: jsCol(0, "x"), Right: jsCol(20, "z")},
				relids: relsetOf(0) | relsetOf(2), leftKey: jsCol(0, "x"), rightKey: jsCol(20, "z"),
				leftRelids: relsetOf(0), rightRelids: relsetOf(2), isEquijoin: true, ecID: noEquivClass},
		}}
		s.builder = newJoinRelBuilder(s, nil)
		return s, s.findRel(relsetOf(0)), s.findRel(relsetOf(1)), s.findRel(relsetOf(2))
	}
	concat := func(a, b *RelOptInfo) figures {
		return figures{a.Width + b.Width, a.NCols + b.NCols, a.AvgVarBytes + b.AvgVarBytes, len(a.ColVarBytes) + len(b.ColVarBytes)}
	}

	for _, jt := range []parser.JoinType{parser.JoinSemi, parser.JoinAnti} {
		t.Run(joinTypeName(jt), func(t *testing.T) {
			s, a, b, c := build(t, jt)
			ab, err := s.makeJoinRel(a, b)
			if err != nil || ab == nil {
				t.Fatalf("makeJoinRel(a, b): %v", err)
			}
			if got, want := figuresOf(ab), figuresOf(a); got != want {
				t.Fatalf("%s joinrel publishes %+v, want the LHS alone %+v", joinTypeName(jt), got, want)
			}
			if ab.Rows > a.Rows {
				t.Fatalf("%s joinrel rows=%v exceed the LHS's %v", joinTypeName(jt), ab.Rows, a.Rows)
			}
			// The parent: (a ⋈semi b) ⋈ c publishes cols(a) + cols(c). The
			// level pass would have run setCheapest before offering `ab`
			// upward; done by hand here.
			setCheapest(ab)
			abc, err := s.makeJoinRel(ab, c)
			if err != nil || abc == nil {
				t.Fatalf("makeJoinRel(ab, c): %v", err)
			}
			if got, want := figuresOf(abc), concat(a, c); got != want {
				t.Fatalf("parent of a %s child publishes %+v, want cols(a)+cols(c) %+v", joinTypeName(jt), got, want)
			}
			// ...and the route through the other pairing agrees (§4.5's
			// relset invariant): (a ⋈ c) ⋈semi b is the same relset.
			s2, a2, b2, c2 := build(t, jt)
			ac, err := s2.makeJoinRel(a2, c2)
			if err != nil || ac == nil {
				t.Fatalf("makeJoinRel(a, c): %v", err)
			}
			setCheapest(ac)
			acb, err := s2.makeJoinRel(ac, b2)
			if err != nil || acb == nil {
				t.Fatalf("makeJoinRel(ac, b): %v", err)
			}
			if got, want := figuresOf(acb), figuresOf(abc); got != want {
				t.Fatalf("route (a⋈c)⋈b publishes %+v, route (a⋈b)⋈c %+v — the relset invariant is broken", got, want)
			}
		})
	}
	for _, jt := range []parser.JoinType{parser.JoinInner, parser.JoinLeft} {
		t.Run(joinTypeName(jt), func(t *testing.T) {
			s, a, b, _ := build(t, jt)
			ab, err := s.makeJoinRel(a, b)
			if err != nil || ab == nil {
				t.Fatalf("makeJoinRel(a, b): %v", err)
			}
			if got, want := figuresOf(ab), concat(a, b); got != want {
				t.Fatalf("%s joinrel publishes %+v, want the concatenation %+v", joinTypeName(jt), got, want)
			}
		})
	}
}

// TestJoinPublishesInner is the single meeting point's contract.
func TestJoinPublishesInner(t *testing.T) {
	if !joinPublishesInner(nil) {
		t.Fatal("a plain inner join concatenates")
	}
	for _, jt := range []parser.JoinType{parser.JoinInner, parser.JoinLeft, parser.JoinRight, parser.JoinFull} {
		if !joinPublishesInner(c05SJI(jt)) {
			t.Fatalf("%s must publish both inputs", joinTypeName(jt))
		}
	}
	for _, jt := range []parser.JoinType{parser.JoinSemi, parser.JoinAnti} {
		if joinPublishesInner(c05SJI(jt)) {
			t.Fatalf("%s must publish the LHS alone", joinTypeName(jt))
		}
	}
}
