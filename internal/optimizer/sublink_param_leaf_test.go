package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestParamSubPlanConjunctIsALeafRestriction pins M0146-0203. TPC-H Q20's
// `ps_availqty > (SELECT 0.5 * sum(l_quantity) ... WHERE l_partkey =
// ps_partkey AND l_suppkey = ps_suppkey)` reaches the flattened search as a
// SubPlan already lowered to PARAM_EXEC form by the kept-sublink rebase: its
// correlation travels in Args (ps_partkey, ps_suppkey), not in outer
// references. PG distributes the clause by the Vars of testexpr and args
// (pull_varnos) — all partsupp's — so it is partsupp's base restriction.
// goopg declined any SubPlan with Args, held the clause above the search
// root, where partsupp's columns are padded under the semi join, and the
// seam fell back to the syntactic plan.
func TestParamSubPlanConjunctIsALeafRestriction(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	// Binding 0 is supplier (columns 0-1), binding 1 is partsupp (2-4).
	spans := []leafSpan{{0, 2}, {2, 5}}
	availqty := &ColumnRef{Name: "ps_availqty", Index: 4, Type: int4}
	sub := &SubqueryExpr{
		Plan:     &SeqScan{},
		Args:     []Expr{&ColumnRef{Name: "ps_partkey", Index: 2, Type: int4}, &ColumnRef{Name: "ps_suppkey", Index: 3, Type: int4}},
		ParParam: []int{-1, -2},
	}
	conj := &BinaryOp{Op: parser.OpGt, Left: availqty, Right: sub}
	if b := correlatedScalarSublinkLeaf(conj, spans); b != 1 {
		t.Fatalf("PARAM_EXEC SubPlan over partsupp's own columns: binding %d, want 1 (partsupp)", b)
	}

	// An Arg from another relation makes it a join clause.
	cross := &SubqueryExpr{
		Plan:     &SeqScan{},
		Args:     []Expr{&ColumnRef{Name: "s_suppkey", Index: 0, Type: int4}},
		ParParam: []int{-1},
	}
	if b := correlatedScalarSublinkLeaf(&BinaryOp{Op: parser.OpGt, Left: availqty, Right: cross}, spans); b != -1 {
		t.Fatalf("Args naming supplier: binding %d, want -1 (a join clause)", b)
	}

	// Args without matching parameter slots are malformed; decline.
	bad := &SubqueryExpr{Plan: &SeqScan{}, Args: sub.Args, ParParam: []int{-1}}
	if b := correlatedScalarSublinkLeaf(&BinaryOp{Op: parser.OpGt, Left: availqty, Right: bad}, spans); b != -1 {
		t.Fatalf("Args/ParParam mismatch: binding %d, want -1", b)
	}
}
