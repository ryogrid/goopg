package planner

// M0127-P5.2 tests. Three properties are separated deliberately, because they
// are three different PG functions that the design doc's one-line summary of
// §3 had fused together:
//
//  1. what a clause IS (relids over the whole qual; the two-sided key split
//     when it is an equality) — PG's RestrictInfo fields;
//  2. when two rels are worth joining (`have_relevant_joinclause`, an OVERLAP
//     test) versus which quals a join may evaluate (`build_joinrel_restrictlist`,
//     a SUBSET test) — a three-rel qual separates them, and a test that only
//     used two-rel quals would never notice they are different rules;
//  3. how many times one equivalence class may be charged (04 §5).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// riTestCols builds n single-column FROM items: cumOffsets over one column
// each, and a ColumnRef per FROM position. Position i owns schema index i.
func riTestCols(n int) ([]int, []*ColumnRef) {
	cum := make([]int, n+1)
	refs := make([]*ColumnRef, n)
	for i := 0; i < n; i++ {
		cum[i+1] = cum[i] + 1
		refs[i] = &ColumnRef{
			Name:           "id",
			Index:          i,
			Type:           catalog.Type{Name: "int4"},
			SourceTableIdx: int16(i + 1),
		}
	}
	return cum, refs
}

func riEq(l, r Expr) Expr  { return &BinaryOp{Op: parser.OpEq, Left: l, Right: r} }
func riLt(l, r Expr) Expr  { return &BinaryOp{Op: parser.OpLt, Left: l, Right: r} }
func riAdd(l, r Expr) Expr { return &BinaryOp{Op: parser.OpAdd, Left: l, Right: r} }

// TestRestrictInfoRelidsAndKeySplit pins property (1): a clause's relids cover
// every relation it mentions, and the key split exists exactly when the clause
// is an equality whose two operands live on disjoint, non-empty sides.
func TestRestrictInfoRelidsAndKeySplit(t *testing.T) {
	cum, c := riTestCols(3)
	conjuncts := []Expr{
		riEq(c[0], c[1]),                     // two-rel equijoin
		riLt(c[1], c[2]),                     // two-rel, NOT an equijoin
		riEq(c[0], riAdd(c[1], c[2])),        // three-rel equijoin: {0} vs {1,2}
		riEq(c[0], riAdd(c[0], c[2])),        // equality, operands overlap -> not an equijoin
		riEq(c[2], &IntegerConst{Value: 42}), // single-rel: must not be a join clause
	}
	l := buildRestrictInfos(conjuncts, 0, cum)
	if len(l.all) != 4 {
		t.Fatalf("expected 4 join clauses (the single-rel qual excluded), got %d", len(l.all))
	}

	want := []struct {
		relids     RelSet
		isEquijoin bool
		left, righ RelSet
	}{
		{0b011, true, 0b001, 0b010},
		{0b110, false, 0, 0},
		{0b111, true, 0b001, 0b110},
		{0b101, false, 0, 0},
	}
	for i, w := range want {
		got := l.all[i]
		if got.relids != w.relids {
			t.Errorf("clause %d: relids %#b, want %#b", i, got.relids, w.relids)
		}
		if got.isEquijoin != w.isEquijoin {
			t.Errorf("clause %d: isEquijoin %v, want %v", i, got.isEquijoin, w.isEquijoin)
		}
		if got.leftRelids != w.left || got.rightRelids != w.righ {
			t.Errorf("clause %d: key split (%#b,%#b), want (%#b,%#b)",
				i, got.leftRelids, got.rightRelids, w.left, w.righ)
		}
		if w.isEquijoin && (got.leftKey == nil || got.rightKey == nil) {
			t.Errorf("clause %d: equijoin with a nil key operand", i)
		}
	}
}

// TestRestrictInfoExcludesSingleRelQuals pins that a baserel restriction never
// reaches the list. It is not a style rule: P5.1 already pushed local quals
// inside the leaf node and folded their selectivity into the initial rel's row
// count (joinsearch.go:220-240), so a single-rel qual here would be charged a
// second time by P5.6.
func TestRestrictInfoExcludesSingleRelQuals(t *testing.T) {
	cum, c := riTestCols(2)
	l := buildRestrictInfos([]Expr{
		riEq(c[0], &IntegerConst{Value: 1}),
		riLt(c[1], &IntegerConst{Value: 9}),
	}, 0, cum)
	if len(l.all) != 0 {
		t.Fatalf("expected no join clauses from two local quals, got %d", len(l.all))
	}
}

// TestHasRelevantJoinClauseIsOverlapNotCoverage pins the distinction PG draws
// and the design doc's §3 one-liner elided: `have_relevant_joinclause`
// (joininfo.c:39) is two overlap tests, NOT a coverage test. A three-rel qual
// therefore makes rel0 ⋈ rel1 relevant even though the qual cannot be
// evaluated until rel2 arrives. Requiring coverage here would refuse to form
// that pair at all, and the only route to it would be the cartesian/last-ditch
// path — a different enumeration than PG's.
func TestHasRelevantJoinClauseIsOverlapNotCoverage(t *testing.T) {
	cum, c := riTestCols(4)
	// The ONLY clause connecting rels 0 and 1 spans rel 2 as well.
	l := buildRestrictInfos([]Expr{
		riEq(c[0], riAdd(c[1], c[2])),
	}, 0, cum)

	r0 := newRelOptInfo(0b0001, 10, 4)
	r1 := newRelOptInfo(0b0010, 10, 4)
	r3 := newRelOptInfo(0b1000, 10, 4)

	if !l.hasRelevantJoinClause(r0, r1) {
		t.Error("rel0 ⋈ rel1 must be relevant: PG's test is bms_overlap on both sides, not coverage")
	}
	if l.hasRelevantJoinClause(r0, r3) {
		t.Error("rel3 is named by no clause; rel0 ⋈ rel3 must not be relevant")
	}
	// The same clause is NOT applicable at rel0 ⋈ rel1 — that is the coverage
	// rule, and it lives in clausesFor.
	if got := l.clausesFor(r0.Relids, r1.Relids); len(got) != 0 {
		t.Errorf("clausesFor(rel0, rel1) = %d clauses, want 0: the qual needs rel2", len(got))
	}
	if got := l.clausesFor(0b0011, 0b0100); len(got) != 1 {
		t.Errorf("clausesFor({0,1}, {2}) = %d clauses, want 1: rel2 completes the qual", len(got))
	}
}

// TestHasNoJoinClauseAtAll pins the gate on phase 1's clauseless else-branch
// (joinrels.c:120): a rel named by no clause at all is crossed in eagerly at
// every level, so a disconnected 1-row dimension can join at level 2 instead
// of waiting for the last-ditch pass.
func TestHasNoJoinClauseAtAll(t *testing.T) {
	cum, c := riTestCols(3)
	l := buildRestrictInfos([]Expr{riEq(c[0], c[1])}, 0, cum)

	if l.hasNoJoinClauseAtAll(newRelOptInfo(0b001, 1, 4)) {
		t.Error("rel0 is named by the clause; it is not clauseless")
	}
	if !l.hasNoJoinClauseAtAll(newRelOptInfo(0b100, 1, 4)) {
		t.Error("rel2 is named by no clause; it must read as clauseless")
	}
	// A join rel that contains a connected member is connected.
	if l.hasNoJoinClauseAtAll(newRelOptInfo(0b101, 1, 4)) {
		t.Error("{rel0,rel2} contains rel0; it is not clauseless")
	}
}

// TestEquivClassChargedOncePerJoin is 04 §5, the rule this task exists for.
// Three relations in one equivalence class (a=b explicit, b=c explicit, a=c
// synthesised) put TWO clauses across the {a,b} ⋈ {c} boundary. PG's
// `generate_join_implied_equalities_normal` emits exactly one — "we can equate
// any one outer member to any one inner member" — because the members within
// each side are already equal. Charging both would square one restriction's
// selectivity, which is the cardinality error the old ×2.0 `inferredEdgePenalty`
// was compensating for in the cost dimension.
func TestEquivClassChargedOncePerJoin(t *testing.T) {
	cum, c := riTestCols(3)
	conjuncts := []Expr{
		riEq(c[0], c[1]), // a = b   explicit
		riEq(c[1], c[2]), // b = c   explicit
		riEq(c[0], c[2]), // a = c   synthesised (inferredCount=1)
	}
	l := buildRestrictInfos(conjuncts, 1, cum)
	if len(l.all) != 3 {
		t.Fatalf("expected 3 join clauses, got %d", len(l.all))
	}
	if l.nclasses != 1 {
		t.Fatalf("a=b, b=c, a=c is ONE equivalence class, got %d", l.nclasses)
	}
	for i, ri := range l.all {
		if ri.ecID != 0 {
			t.Errorf("clause %d: ecID %d, want 0 (all three are one class)", i, ri.ecID)
		}
	}
	if !l.all[2].inferred || l.all[0].inferred || l.all[1].inferred {
		t.Fatalf("inferredCount=1 must tag only the trailing clause: %v %v %v",
			l.all[0].inferred, l.all[1].inferred, l.all[2].inferred)
	}

	// Two clauses cross the {a,b} ⋈ {c} boundary...
	applicable := l.clausesFor(0b011, 0b100)
	if len(applicable) != 2 {
		t.Fatalf("clausesFor({a,b},{c}) = %d, want 2 (b=c and a=c)", len(applicable))
	}
	// ...but only one may be charged.
	sel := l.selectivityClauses(0b011, 0b100)
	if len(sel) != 1 {
		t.Fatalf("selectivityClauses({a,b},{c}) = %d, want 1 (one class, one clause)", len(sel))
	}
	if sel[0].inferred {
		t.Error("the surviving member must be the written clause, not the synthesised one")
	}
}

// TestSelectivityClausesKeepsDistinctRestrictions is the other half: the EC
// rule must not collapse restrictions that are genuinely independent. Two
// different equivalence classes each contribute one clause, and a clause in no
// class (here a range qual) always contributes.
func TestSelectivityClausesKeepsDistinctRestrictions(t *testing.T) {
	// Four FROM items, two columns each, so a relation can carry two
	// independent join keys.
	cum := []int{0, 2, 4}
	col := func(idx int, name string) *ColumnRef {
		return &ColumnRef{Name: name, Index: idx, Type: catalog.Type{Name: "int4"},
			SourceTableIdx: int16(idx/2 + 1)}
	}
	aX, aY := col(0, "x"), col(1, "y")
	bX, bY := col(2, "x"), col(3, "y")

	l := buildRestrictInfos([]Expr{
		riEq(aX, bX), // class 1
		riEq(aY, bY), // class 2 — independent restriction
		riLt(aX, bY), // no class at all
	}, 0, cum)
	if l.nclasses != 2 {
		t.Fatalf("x and y are two independent classes, got %d", l.nclasses)
	}
	if l.all[2].ecID != noEquivClass {
		t.Errorf("a range qual belongs to no equivalence class, got ecID %d", l.all[2].ecID)
	}
	sel := l.selectivityClauses(0b01, 0b10)
	if len(sel) != 3 {
		t.Fatalf("selectivityClauses = %d, want 3 (two classes + one classless qual)", len(sel))
	}
}

// TestSelectivityClausesPrefersExplicitMember pins the tie-break directly on
// the list, independent of how it was built. `buildRestrictInfos` appends the
// synthesised conjuncts last (buildJoinGraph's convention), so under today's
// builder the explicit member is also the earlier one and first-wins would
// suffice; the rule is stated as a property of the list so a later builder
// (P5.4 generating clauses from classes) cannot silently pick the synthesised
// member, which is the one with no statistics behind it.
func TestSelectivityClausesPrefersExplicitMember(t *testing.T) {
	inferredFirst := &restrictInfoList{
		all: []*restrictInfo{
			{clause: &IntegerConst{Value: 1}, relids: 0b11, inferred: true, ecID: 0},
			{clause: &IntegerConst{Value: 2}, relids: 0b11, inferred: false, ecID: 0},
		},
		nclasses: 1,
	}
	sel := inferredFirst.selectivityClauses(0b01, 0b10)
	if len(sel) != 1 {
		t.Fatalf("one class must yield one clause, got %d", len(sel))
	}
	if sel[0].inferred {
		t.Error("explicit member must win over the synthesised one regardless of list order")
	}
}

// TestEquivClassIDsAreDeterministic guards the one property that would make
// plans move between identical runs: the class id decides which member carries
// the selectivity, so an id derived from Go's randomised map order would make
// the answer depend on the run. Same input, repeated, must give the same ids.
func TestEquivClassIDsAreDeterministic(t *testing.T) {
	cum := []int{0, 2, 4, 6}
	col := func(idx int, name string) *ColumnRef {
		return &ColumnRef{Name: name, Index: idx, Type: catalog.Type{Name: "int4"},
			SourceTableIdx: int16(idx/2 + 1)}
	}
	build := func() []int {
		l := buildRestrictInfos([]Expr{
			riEq(col(1, "y"), col(3, "y")),
			riEq(col(0, "x"), col(2, "x")),
			riEq(col(2, "x"), col(4, "x")),
			riEq(col(3, "y"), col(5, "y")),
		}, 0, cum)
		ids := make([]int, len(l.all))
		for i, ri := range l.all {
			ids[i] = ri.ecID
		}
		return ids
	}
	first := build()
	if len(first) != 4 {
		t.Fatalf("expected 4 clauses, got %d", len(first))
	}
	// x-clauses share a class, y-clauses share a class, and the two differ.
	if first[1] != first[2] || first[0] != first[3] || first[0] == first[1] {
		t.Fatalf("class grouping wrong: %v", first)
	}
	for i := 0; i < 20; i++ {
		if got := build(); !equalInts(got, first) {
			t.Fatalf("run %d gave ecIDs %v, first run gave %v", i, got, first)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRelidsOfExprRejectsForeignCoordinates pins that an expression whose
// columns fall outside the offsets is refused rather than guessed at. A clause
// admitted with a wrong relset would be applied at the wrong join level, which
// is silent: the plan still runs, it just filters somewhere else.
func TestRelidsOfExprRejectsForeignCoordinates(t *testing.T) {
	cum, _ := riTestCols(2)
	stray := &ColumnRef{Name: "id", Index: 99, Type: catalog.Type{Name: "int4"}}
	if _, ok := relidsOfExpr(stray, cum); ok {
		t.Error("a column past the last relation's slice must not resolve to a relset")
	}
	if _, ok := relidsOfExpr(nil, cum); ok {
		t.Error("a nil expression has no relset")
	}
	// A clause carrying such a column never enters the list.
	l := buildRestrictInfos([]Expr{riEq(&ColumnRef{Name: "id", Index: 0,
		Type: catalog.Type{Name: "int4"}}, stray)}, 0, cum)
	if len(l.all) != 0 {
		t.Errorf("expected the unattributable clause to be dropped, got %d clauses", len(l.all))
	}
}
