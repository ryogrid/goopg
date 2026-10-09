package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestSubPlanClauseSelectivity pins M0146-0002h: a clause that stayed a
// SubPlan — [NOT] IN (subquery), [NOT] EXISTS — takes PG's boolvarsel
// default 0.5 (clause_selectivity_ext has no SubPlan arm), in both twin
// estimators, negated or not. The value-list IN keeps its own estimator.
func TestSubPlanClauseSelectivity(t *testing.T) {
	scan := &SeqScan{Table: &catalog.Table{Name: "t"}, schema: Schema{ijCol("a")}}
	sub := &SeqScan{Table: &catalog.Table{Name: "u"}, schema: Schema{ijCol("b")}}
	col := &ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}
	cases := map[string]Expr{
		"IN (subquery)":     &InExpr{Operand: col, Plan: sub},
		"NOT IN (subquery)": &InExpr{Operand: col, Plan: sub, Negated: true},
		"EXISTS":            &ExistsExpr{Plan: sub},
		"NOT EXISTS":        &ExistsExpr{Plan: sub, Negated: true},
	}
	for name, e := range cases {
		if got := clauseSelectivity(e, scan); got != 0.5 {
			t.Errorf("%s: clauseSelectivity = %v, want 0.5", name, got)
		}
		if got := clauseSelectivityWithSource(e, scan); got.value != 0.5 || got.reliable {
			t.Errorf("%s: clauseSelectivityWithSource = %+v, want unreliable 0.5", name, got)
		}
	}
}
