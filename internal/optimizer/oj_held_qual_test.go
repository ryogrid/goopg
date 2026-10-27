package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0147-0053: an outer join's preserved-side-only ON conjunct is held at the
// join (PG's distribute_qual_to_rels: not is_pushed_down, required_relids =
// the join's scope) instead of declining the statement out of the search.

func ojHeldGt(idx int) Expr {
	return &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: idx, Name: "q"}, Right: &IntegerConst{Value: 0}}
}

// TestOuterOnQualsOKAdmitsPreservedSideConjunct: `t LEFT JOIN p ON t.k = p.k
// AND t.x > 0` used to decline (`outer-on-qual`); the preserved-side conjunct
// is now admitted, a constant conjunct still declines, and so does a scope
// with a zero-width leaf (its scope columns could not name every leaf).
func TestOuterOnQualsOKAdmitsPreservedSideConjunct(t *testing.T) {
	spans := []leafSpan{{0, 2}, {2, 4}}
	eq := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0, Name: "k"}, Right: &ColumnRef{Index: 2, Name: "k"}}
	link := func(pred Expr) []outerChainLink {
		return []outerChainLink{{jointype: parser.JoinLeft, preserved: relsetOf(0), nullable: relsetOf(1), pred: pred}}
	}
	if !outerOnQualsOK(link(&BinaryOp{Op: parser.OpAnd, Left: eq, Right: ojHeldGt(1)}), spans) {
		t.Fatal("a preserved-side ON conjunct must be admitted (held at the join), not declined")
	}
	constant := &BinaryOp{Op: parser.OpGt, Left: &IntegerConst{Value: 1}, Right: &IntegerConst{Value: 0}}
	if outerOnQualsOK(link(&BinaryOp{Op: parser.OpAnd, Left: eq, Right: constant}), spans) {
		t.Fatal("a constant ON conjunct has no relids to scope and must still decline")
	}
	zeroWidth := []leafSpan{{0, 2}, {2, 2}}
	if outerOnQualsOK(link(ojHeldGt(1)), zeroWidth) {
		t.Fatal("a scope with a zero-width leaf cannot be expressed per leaf and must decline")
	}
}

// TestAppendOJHeldOwnsOnlyTheSplittingProblem: the held qual becomes a
// restrict info — with the link's scope as its required relids, flagged
// ojHeld — only in a problem whose items separate the join's two sides. A
// problem whose single item holds the whole scope, or one that cannot resolve
// a scope column, leaves it to another problem.
func TestAppendOJHeldOwnsOnlyTheSplittingProblem(t *testing.T) {
	held := []ojHeldQual{{clause: ojHeldGt(1), scopeCols: []int{0, 2}}}

	split := &restrictInfoList{}
	split.appendOJHeld(held, []leafSpan{{0, 2}, {2, 4}})
	if len(split.all) != 1 {
		t.Fatalf("splitting problem: got %d restrict infos, want 1", len(split.all))
	}
	ri := split.all[0]
	if !ri.ojHeld || ri.relids != relsetOf(0, 1) || ri.ecID != noEquivClass {
		t.Fatalf("held restrict info = {ojHeld:%v relids:%#x ec:%d}, want {true 0x3 none}", ri.ojHeld, uint32(ri.relids), ri.ecID)
	}

	whole := &restrictInfoList{}
	whole.appendOJHeld(held, []leafSpan{{0, 4}})
	if len(whole.all) != 0 {
		t.Fatal("a problem whose one item holds the whole scope must not take the qual")
	}
	elsewhere := &restrictInfoList{}
	elsewhere.appendOJHeld(held, []leafSpan{{10, 12}, {12, 14}})
	if len(elsewhere.all) != 0 {
		t.Fatal("a problem that cannot resolve the scope must not take the qual")
	}
}
