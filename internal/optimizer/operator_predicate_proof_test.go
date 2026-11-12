package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestOperatorPredicateProof pins operator_predicate_proof's implication
// table on integer constants (M0148-0001c3a4 (a)): what it proves, the
// boundary cases it must not, and the text column it must not touch at all.
func TestOperatorPredicateProof(t *testing.T) {
	x := &ColumnRef{Index: 0, Name: "x", Type: catalog.Type{Name: "int4"}}
	s := &ColumnRef{Index: 1, Name: "s", Type: catalog.Type{Name: "text"}}
	cmp := func(c *ColumnRef, op parser.OpCode, v int64) Expr {
		return &BinaryOp{Op: op, Left: c, Right: &IntegerConst{Value: v}}
	}
	lt, le, gt, ge, eq := parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe, parser.OpEq
	for _, c := range []struct {
		clause, pred Expr
		want         bool
	}{
		{cmp(x, lt, 1), cmp(x, lt, 3), true},
		{cmp(x, lt, 3), cmp(x, lt, 3), true},
		{cmp(x, lt, 4), cmp(x, lt, 3), false},
		{cmp(x, le, 3), cmp(x, lt, 3), false},
		{cmp(x, le, 2), cmp(x, lt, 3), true},
		{cmp(x, eq, 2), cmp(x, lt, 3), true},
		{cmp(x, eq, 3), cmp(x, lt, 3), false},
		{cmp(x, lt, 3), cmp(x, le, 3), true},
		{cmp(x, gt, 5), cmp(x, gt, 4), true},
		{cmp(x, ge, 5), cmp(x, gt, 5), false},
		{cmp(x, ge, 5), cmp(x, gt, 4), true},
		{cmp(x, gt, 4), cmp(x, ge, 4), true},
		{cmp(x, eq, 4), cmp(x, eq, 4), true},
		{cmp(x, lt, 4), cmp(x, eq, 4), false},
		{cmp(x, gt, 1), cmp(x, lt, 9), false},
		// The constant on the left is commuted: `3 > x` is `x < 3`.
		{&BinaryOp{Op: gt, Left: &IntegerConst{Value: 3}, Right: x}, cmp(x, lt, 5), true},
		// A text column orders '10' before '9': nothing is inferred.
		{cmp(s, lt, 10), cmp(s, lt, 9), false},
		{cmp(s, lt, 9), cmp(s, lt, 10), false},
	} {
		if got := operatorPredicateProof(c.clause, c.pred); got != c.want {
			t.Errorf("%v implies %v: got %v, want %v", c.clause, c.pred, got, c.want)
		}
	}
	// The OR-clause rule over it: (x < 1) OR (x < 3) implies x < 3 item by
	// item, and is what drops the restriction OR under a SAOP recheck.
	or := &BinaryOp{Op: parser.OpOr, Left: cmp(x, lt, 1), Right: cmp(x, lt, 3), Nested: true}
	if !predicateImpliedBy(or, cmp(x, lt, 3)) {
		t.Error("(x < 1) OR (x < 3) must imply x < 3")
	}
	if predicateImpliedBy(or, cmp(x, lt, 2)) {
		t.Error("(x < 1) OR (x < 3) must not imply x < 2")
	}
}
