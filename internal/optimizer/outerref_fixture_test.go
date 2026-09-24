package optimizer

import "github.com/goopg/goopg/internal/parser"

// innerPlanWithOuterRef builds a subplan whose predicate is an
// OuterColumnRef at Level 1 (the immediate parent scope) AND a plain inner
// ColumnRef, for the scope-opening arms of the expression walkers: the first
// is the parent's column, the second the subplan's own.
//
// It came from remap_arms_test.go, whose remapByPosMap pins were deleted with
// the legacy pinned-spine splice (M0145-0008); the walker arm tests still
// share the fixture.
func innerPlanWithOuterRef(outerIdx, innerIdx int) (Node, *OuterColumnRef, *ColumnRef) {
	outer := &OuterColumnRef{Level: 1, Index: outerIdx, Name: "o"}
	inner := &ColumnRef{Index: innerIdx, Name: "i"}
	n := &Filter{
		Child:     &SeqScan{},
		Predicate: &BinaryOp{Op: parser.OpEq, Left: outer, Right: inner},
	}
	return n, outer, inner
}
