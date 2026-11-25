package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestMoneyStatsComparison pins M0149-0001k5's money-aware statistics: a
// literal cast to money folds to the cash_out text the statistics are
// stamped in (so `m = '42'` finds the "$42.00" MCV), and histogram /
// equality comparison runs on the Cash count, as convert_numeric_to_scalar
// does for CASHOID ("$1,000.00" sorts above "$123.75", not below it as text).
func TestMoneyStatsComparison(t *testing.T) {
	col := &ColumnRef{Index: 0, Name: "m", Type: catalog.Type{Name: "money"}}
	_, val, ok := normalizeColumnConst(col, &CastExpr{Operand: &StringConst{Value: "42"}, TargetType: "money"})
	if !ok {
		t.Fatal("money literal not recognised as a constant")
	}
	if s, _ := formatExprConstant(val); s != "$42.00" {
		t.Errorf("folded literal = %q, want $42.00", s)
	}
	if histCmp("$1,000.00", "$123.75", "money") <= 0 {
		t.Error("money histogram compare must be numeric")
	}
	if !statLiteralEqual("$42.00", "$42.00", "money") || statLiteralEqual("$42.00", "$42.01", "money") {
		t.Error("money equality")
	}
	stats := &catalog.ColumnStats{NDistinct: 1001, MCV: []catalog.MCVEntry{{Value: "$42.00", Frequency: 0.167}}}
	if sel := eqSelectivityForColumn(stats, val, 3600, "money"); sel != 0.167 {
		t.Errorf("selectivity = %v, want the MCV frequency", sel)
	}
}
