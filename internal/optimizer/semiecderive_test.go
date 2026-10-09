package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestSemiJoinEqualityJoinsEquivalenceClass pins M0146-0127: a SEMI join's
// equality is an equivalence-class member (PG distribute_qual_to_rels), so
// with `a.x = b.y` in WHERE and `a.x IN (SELECT k FROM r)` the class derives
// `b.y = r.k` — TPC-DS Q14's `item.i_item_sk = cross_items.ss_item_sk`. The
// derived clause reads r's columns, which goopg's semi/anti joins do not
// emit, so it applies only where r is a whole join input, once per class,
// and a probe keyed on it only under an outer path that kept r.
func TestSemiJoinEqualityJoinsEquivalenceClass(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	ax := &ColumnRef{Name: "x", Index: 0, Type: int4}
	by := &ColumnRef{Name: "y", Index: 1, Type: int4}
	rk := &ColumnRef{Name: "k", Index: 2, Type: int4}
	spans := []leafSpan{{0, 1}, {1, 2}, {2, 3}}
	const relA, relB, relR = RelSet(1), RelSet(2), RelSet(4)
	semi := []*SpecialJoinInfo{{Jointype: parser.JoinSemi, SynLefthand: relA, SynRighthand: relR}}
	where := &BinaryOp{Op: parser.OpEq, Left: ax, Right: by}
	semiQual := &BinaryOp{Op: parser.OpEq, Left: ax, Right: rk}

	list, rhs := semiRHSImpliedEqualities([]Expr{where}, []Expr{semiQual}, spans, semi, 0)
	if len(list) != 1 {
		t.Fatalf("derived %d clauses, want b.y = r.k only", len(list))
	}
	d := list[0].(*BinaryOp)
	if rs, _ := relidsOfExpr(d, spans); rs != relB|relR {
		t.Fatalf("derived clause spans %b, want b and r", rs)
	}
	if rs, _ := relidsOfExpr(rhs[d], spans); rs != relR {
		t.Fatalf("RHS operand spans %b, want r", rs)
	}
	// b on an outer join's nullable side, or an ANTI join: nothing derived.
	if l, _ := semiRHSImpliedEqualities([]Expr{where}, []Expr{semiQual}, spans, semi, relB); len(l) != 0 {
		t.Errorf("derived %v through a nullable member", l)
	}
	anti := []*SpecialJoinInfo{{Jointype: parser.JoinAnti, SynLefthand: relA, SynRighthand: relR}}
	if l, _ := semiRHSImpliedEqualities([]Expr{where}, []Expr{semiQual}, spans, anti, 0); len(l) != 0 {
		t.Errorf("derived %v through an ANTI join", l)
	}

	// clausesFor: the derived clause joins b to r alone, not to a rel that
	// semi-joined r away; at the semijoin itself it yields to the semijoin's
	// own qual of the same class.
	l := buildRestrictInfos([]Expr{where, semiQual, d}, 0, spans)
	for _, ri := range l.all {
		if ri.clause == Expr(d) {
			ri.onlyBesideRel = relR
		}
	}
	has := func(ris []*restrictInfo) bool {
		for _, ri := range ris {
			if ri.clause == Expr(d) {
				return true
			}
		}
		return false
	}
	if !has(l.clausesFor(relB, relR)) {
		t.Error("{b} | {r}: the derived clause is the join clause")
	}
	if has(l.clausesFor(relA|relR, relB)) {
		t.Error("{a semi r} | {b}: the derived clause reads r above the semijoin")
	}
	if has(l.clausesFor(relA|relB, relR)) {
		t.Error("{a b} | {r}: the semijoin qual already equates r to the class")
	}

	// pathKeepsRels: a semijoin over r loses r; a unique-ified r keeps it.
	scan := func(r RelSet) *Path { return &Path{Kind: PathSeqScan, Rel: &RelOptInfo{Relids: r}} }
	semiPath := &Path{Kind: PathHashJoin, Jointype: parser.JoinSemi, Children: []*Path{scan(relA), scan(relR)}}
	if pathKeepsRels(semiPath, relR) {
		t.Error("a semijoin over r keeps r's columns")
	}
	if !pathKeepsRels(semiPath, relA) {
		t.Error("a semijoin loses its preserved side")
	}
	rightSemi := &Path{Kind: PathHashJoin, Jointype: parser.JoinRightSemi, Children: []*Path{scan(relR), scan(relA)}}
	if pathKeepsRels(rightSemi, relR) {
		t.Error("a right semijoin over r keeps r's columns")
	}
	if !pathKeepsRels(&Path{Kind: PathUnique, Children: []*Path{scan(relR)}}, relR) {
		t.Error("a unique-ified r loses its columns")
	}
}
