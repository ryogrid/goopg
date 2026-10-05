package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestQualEvalOpsChargesSubPlanCost pins M0146-0005di: cost_qual_eval adds a
// planned sublink's cost_subplan charge to the qual (costsize.c), where goopg
// charged every sublink one cpu_operator_cost. Units are cpu_operator_cost
// (0.0025). The subplan is a 100-row plan costing 10..50.
func TestQualEvalOpsChargesSubPlanCost(t *testing.T) {
	tbl := bigTable(t, "spc_t")
	plan := func(correlated bool) Node {
		var pred Expr = &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: 0, Name: "a"}, Right: &IntegerConst{Value: 1}}
		if correlated {
			pred = &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0, Name: "a"}, Right: &OuterColumnRef{Level: 1, Index: 0, Name: "a"}}
		}
		f := &Filter{Child: seqScanOver(tbl), Predicate: pred}
		stampPlanCost(f, &Path{Cost: Cost{Startup: 10, Total: 50}, Rows: 100})
		return f
	}
	op := pgBootCPUOperatorCost
	gt := func(l Expr) Expr { return &BinaryOp{Op: parser.OpGt, Left: l, Right: &IntegerConst{Value: 0}} }
	cases := []struct {
		name             string
		qual             Expr
		startup, perTupl float64 // absolute cost
	}{
		// EXPR, correlated: all of the run cost plus the startup, per call;
		// plus the `> 0` comparison.
		{"correlated scalar", gt(&SubqueryExpr{Plan: plan(true)}), 0, 40 + 10 + op},
		// EXPR, uncorrelated: an InitPlan upstream — the qual reads a Param.
		{"uncorrelated scalar", gt(&SubqueryExpr{Plan: plan(false), IsNonCorrelated: true}), 0, op},
		// EXISTS, correlated: one tuple's share of the run cost plus startup.
		{"correlated exists", &ExistsExpr{Plan: plan(true)}, 0, 40.0/100 + 10},
		// IN, uncorrelated plain equality: a hashed SubPlan (build_subplan
		// sets useHashTable; AlternativeSubPlan is a correlated EXISTS's
		// only). cost_subplan loads the table once — the plan's total plus a
		// cpu_operator_cost per row — and each call pays the comparison.
		// M0146-0019a.
		{"hashable uncorrelated in", &InExpr{Operand: &ColumnRef{Index: 0, Name: "a"}, Plan: plan(false), IsNonCorrelated: true}, 50 + 100*op, op},
		// IN, correlated: half the run cost and half the rows' comparisons,
		// plus startup, per call; plus the test comparison.
		{"correlated in", &InExpr{Operand: &ColumnRef{Index: 0, Name: "a"}, Plan: plan(true)}, 0, 0.5*40 + 0.5*100*op + 10 + op},
		// Unplanned: the old one-operator charge.
		{"unplanned scalar", gt(&SubqueryExpr{}), 0, 2 * op},
	}
	for _, c := range cases {
		s, p := qualEvalOps(c.qual)
		if math.Abs(s*op-c.startup) > 1e-9 || math.Abs(p*op-c.perTupl) > 1e-9 {
			t.Errorf("%s: startup %.6f per-tuple %.6f, want %.6f / %.6f", c.name, s*op, p*op, c.startup, c.perTupl)
		}
	}
}
