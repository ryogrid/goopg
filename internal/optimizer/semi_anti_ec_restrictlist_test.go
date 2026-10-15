package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestSemiJoinRestrictListTakesFirstMemberClause pins M0146-0200 on TPC-H
// Q21's shape: orders o, lineitem l1, the EXISTS RHS l2 and the NOT EXISTS
// RHS l3.
//
//   - The ANTI qual `l3.k = l1.k` is outerjoin-delayed in PG and never an
//     equivalence-class member, so it keeps no class id and a problem with
//     SEMI and ANTI joins still reduces to one clause per class.
//   - At the SEMI join {o l1} | {l2}, build_joinrel_restrictlist takes
//     generate_join_implied_equalities' clause for the class {o.k, l1.k, l2.k}:
//     the first outer member against the inner one, `o.k = l2.k` — the
//     semijoin-derived clause, not the written `l2.k = l1.k`. A probe of l2
//     keyed on l1 enforces the written one only, so PG prints the derived
//     clause as the Nested Loop Semi Join's Join Filter.
func TestSemiJoinRestrictListTakesFirstMemberClause(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	ok := &ColumnRef{Name: "o_orderkey", Index: 0, Type: int4}
	l1k := &ColumnRef{Name: "l_orderkey", Index: 1, Type: int4}
	l2k := &ColumnRef{Name: "l_orderkey", Index: 2, Type: int4}
	l3k := &ColumnRef{Name: "l_orderkey", Index: 3, Type: int4}
	spans := []leafSpan{{0, 1}, {1, 2}, {2, 3}, {3, 4}}
	o, l1, l2, l3 := RelSet(1), RelSet(2), RelSet(4), RelSet(8)

	where := &BinaryOp{Op: parser.OpEq, Left: ok, Right: l1k}
	semiQual := &BinaryOp{Op: parser.OpEq, Left: l2k, Right: l1k}
	antiQual := &BinaryOp{Op: parser.OpEq, Left: l3k, Right: l1k}
	derived := &BinaryOp{Op: parser.OpEq, Left: l2k, Right: ok}

	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinSemi, MinLefthand: l1, MinRighthand: l2, SynLefthand: o | l1, SynRighthand: l2},
		{Jointype: parser.JoinAnti, MinLefthand: l1, MinRighthand: l3, SynLefthand: o | l1 | l2, SynRighthand: l3},
	}
	if !onlySemiOrAntiJoins(sjis) {
		t.Fatal("SEMI and ANTI null-extend nothing visible above them: reduction must be on")
	}
	if onlySemiOrAntiJoins([]*SpecialJoinInfo{{Jointype: parser.JoinLeft}}) {
		t.Fatal("a LEFT join null-extends its RHS: reduction must stay off")
	}
	if got := antiRighthands(sjis); got != l3 {
		t.Fatalf("antiRighthands = %#x, want l3", uint32(got))
	}

	l := buildRestrictInfosExcluding([]Expr{where, semiQual, antiQual, derived}, 0, spans, antiRighthands(sjis))
	l.ecReduce = onlySemiOrAntiJoins(sjis)
	var derivedRI *restrictInfo
	for _, ri := range l.all {
		switch ri.clause {
		case antiQual:
			if ri.ecID != noEquivClass {
				t.Fatalf("the ANTI qual joined class %d; it must stay a plain join clause", ri.ecID)
			}
		case derived:
			ri.onlyBesideRel = l2
			derivedRI = ri
		}
	}
	if derivedRI == nil || derivedRI.ecID == noEquivClass {
		t.Fatal("fixture: the semijoin-derived clause must be a class member")
	}

	got := l.buildJoinRelRestrictList(o|l1, l2, sjis[0])
	if len(got) != 1 {
		t.Fatalf("{o l1} | {l2}: %d clauses, want the class's one", len(got))
	}
	bin, _ := got[0].clause.(*BinaryOp)
	if bin == nil || !((bin.Left == ok && bin.Right == l2k) || (bin.Left == l2k && bin.Right == ok)) {
		t.Fatalf("{o l1} | {l2} applies %#v, want o.k = l2.k (first outer member)", got[0].clause)
	}

	// The ANTI join keeps its own qual, unreduced.
	anti := l.buildJoinRelRestrictList(o|l1|l2, l3, sjis[1])
	if len(anti) != 1 || anti[0].clause != antiQual {
		t.Fatalf("{o l1 l2} | {l3}: got %d clauses, want the ANTI qual alone", len(anti))
	}
}
