package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestJoinQualPricesCorrelatedSubPlan pins M0146-0012a slice B: a join
// clause holding a correlated SubPlan pays cost_subplan's per-call cost on
// every evaluation (cost_qual_eval_walker's SubPlan arm), on top of goopg's
// flat cpu_operator_cost per conjunct. TPC-H Q17's correlated EXPR_SUBLINK
// re-pays its startup each call: run + startup. A pre-lowered sublink
// (ParParam set, its plan reading ExecParamRefs) is just as correlated; an
// uncorrelated one is an InitPlan and costs nothing per tuple.
func TestJoinQualPricesCorrelatedSubPlan(t *testing.T) {
	cp := defaultCostParams()
	body := func() *Aggregate {
		a := &Aggregate{}
		a.PlanCost = PlanCost{StartupCost: 123.58, TotalCost: 123.60, PlanRows: 1, CostSet: true}
		return a
	}
	clause := func(sub *SubqueryExpr) []*restrictInfo {
		return []*restrictInfo{{clause: &BinaryOp{Op: parser.OpLt, Left: &ColumnRef{Index: 0, Name: "l_quantity"}, Right: sub}}}
	}
	flat := cp.cpuOperatorCost
	lowered := &SubqueryExpr{Plan: body(), ParParam: []int{0}, Args: []Expr{&ColumnRef{Index: 3, Name: "p_partkey"}}}
	if got, want := joinQualPerTuple(cp, clause(lowered)), flat+123.60; math.Abs(got-want) > 1e-6 {
		t.Errorf("pre-lowered correlated SubPlan: per tuple %v, want flat + run + startup = %v", got, want)
	}
	uncorrelated := &SubqueryExpr{Plan: body()}
	if got := joinQualPerTuple(cp, clause(uncorrelated)); math.Abs(got-flat) > 1e-9 {
		t.Errorf("uncorrelated (InitPlan) SubPlan: per tuple %v, want only the flat %v", got, flat)
	}
	if got, want := joinQualEvalCost(cp, clause(lowered), 10), 10*(flat+123.60); math.Abs(got-want) > 1e-6 {
		t.Errorf("10 evaluations: %v, want %v", got, want)
	}
}
