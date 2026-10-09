package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCorrelatedSubPlanQualIsParallelRestricted pins M0146-0130's gate: a
// qual holding a correlated SubPlan (PARAM_EXEC args, so
// subplan->parallel_safe is false) makes its rel parallel-restricted
// (max_parallel_hazard_walker / set_rel_consider_parallel), so a producer
// running the whole subtree in workers (partial aggregate / distinct) must
// refuse it. An uncorrelated sublink (an InitPlan) stays safe, and the
// node-kind gate subtreeHasUnsafeNode does not change: a Gather placed
// BELOW the qual stays allowed (TPC-DS Q32's Join Filter).
func TestCorrelatedSubPlanQualIsParallelRestricted(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	inner := func(pred Expr) Node {
		return &Filter{Child: &SeqScan{Table: &catalog.Table{Name: "t2"}, schema: Schema{{Name: "c", Type: int4}}}, Predicate: pred}
	}
	correlated := &SubqueryExpr{Plan: inner(&BinaryOp{Op: parser.OpEq,
		Left: &ColumnRef{Name: "c", Index: 0, Type: int4}, Right: &OuterColumnRef{Level: 1, Name: "a", Index: 0, Type: int4}})}
	uncorrelated := &SubqueryExpr{IsNonCorrelated: true, Plan: inner(&BinaryOp{Op: parser.OpEq,
		Left: &ColumnRef{Name: "c", Index: 0, Type: int4}, Right: &IntegerConst{Value: 1}})}
	host := func(sub Expr) Node {
		return &Filter{Child: &SeqScan{Table: &catalog.Table{Name: "t1"}, schema: Schema{{Name: "a", Type: int4}}},
			Predicate: &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Name: "a", Index: 0, Type: int4}, Right: sub}}
	}
	if !subtreeHasParallelRestrictedQual(host(correlated)) {
		t.Error("a correlated SubPlan qual must make the subtree parallel-restricted")
	}
	if subtreeHasParallelRestrictedQual(host(uncorrelated)) {
		t.Error("an uncorrelated SubPlan (InitPlan) qual must stay parallel-safe")
	}
	j := &Join{Type: JoinTypeInner, Left: host(uncorrelated), Right: &SeqScan{Table: &catalog.Table{Name: "t3"}}, Predicate: &BinaryOp{Op: parser.OpGt,
		Left: &ColumnRef{Name: "a", Index: 0, Type: int4}, Right: correlated}}
	if !subtreeHasParallelRestrictedQual(j) {
		t.Error("a correlated SubPlan in a join qual must make the subtree parallel-restricted")
	}
	if subtreeHasUnsafeNode(j) {
		t.Error("subtreeHasUnsafeNode must stay node-kind only: a Gather below the qual is allowed")
	}
}
