package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestSemiOnlyProblemReducesEquivalenceClauses pins M0146-0128: a SEMI join
// null-extends nothing, so a problem whose special joins are all SEMI keeps
// M0146-0022's one-clause-per-class rule. Above a unique-ified RHS r, PG
// joins s with one clause of the class {a.x, s.y, r.k} — TPC-DS Q14 printed
// a redundant `Join Filter: (store_sales.ss_item_sk = cross_items.ss_item_sk)`
// beside the probe's `ss_item_sk = item.i_item_sk`.
func TestSemiOnlyProblemReducesEquivalenceClauses(t *testing.T) {
	semi := &SpecialJoinInfo{Jointype: parser.JoinSemi}
	if !onlySemiJoins(nil) || !onlySemiJoins([]*SpecialJoinInfo{semi, semi}) {
		t.Fatal("no special joins / only SEMI joins: reduction must be on")
	}
	for _, jt := range []parser.JoinType{parser.JoinLeft, parser.JoinAnti, parser.JoinFull} {
		if onlySemiJoins([]*SpecialJoinInfo{semi, {Jointype: jt}}) {
			t.Errorf("a %v join can null-extend a class member: reduction must stay off", jt)
		}
	}

	int4 := catalog.Type{Name: "int4"}
	ax := &ColumnRef{Name: "x", Index: 0, Type: int4} // item
	rk := &ColumnRef{Name: "k", Index: 1, Type: int4} // cross_items (unique-ified)
	sy := &ColumnRef{Name: "y", Index: 2, Type: int4} // store_sales
	spans := []leafSpan{{0, 1}, {1, 2}, {2, 3}}
	where := &BinaryOp{Op: parser.OpEq, Left: sy, Right: ax}
	semiQual := &BinaryOp{Op: parser.OpEq, Left: sy, Right: rk}
	derived := &BinaryOp{Op: parser.OpEq, Left: ax, Right: rk}
	l := buildRestrictInfos([]Expr{where, semiQual, derived}, 0, spans)
	l.ecReduce = true
	got := l.buildJoinRelRestrictList(RelSet(1)|RelSet(2), RelSet(4), nil)
	if len(got) != 1 {
		t.Fatalf("{a r} | {s}: %d clauses, want one per class", len(got))
	}
}
