package optimizer

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestNameCastLiteralReadsStatistics pins M0149-0001j's selectivity fix: the
// CastExpr{StringConst → name} that the resolver wraps around a literal
// compared with a name column (and an explicit 'x'::name) is read as the name
// Const PG's eval_const_expressions folds it to — truncated to 63 bytes as
// namein does — so `c = 'hot'` finds the column's MCV (PG 18.3: rows=500 of
// 2500 for a 0.2-frequency value) instead of pricing column = expression.
func TestNameCastLiteralReadsStatistics(t *testing.T) {
	col := &ColumnRef{Index: 0, Name: "c", Type: catalog.Type{Name: "name"}}
	lit := func(v string, explicit bool) Expr {
		return &CastExpr{Operand: &StringConst{Value: v}, TargetType: "name", Explicit: explicit}
	}
	for _, tc := range []struct {
		l, r  Expr
		right bool
	}{
		{col, lit("hot", false), false},
		{lit("hot", true), col, true},
	} {
		cr, val, ok := normalizeColumnConst(tc.l, tc.r)
		if !ok || cr != col {
			t.Fatalf("normalizeColumnConst: ok=%v", ok)
		}
		if s, _ := formatExprConstant(val); s != "hot" {
			t.Errorf("const = %q, want hot", s)
		}
		if _, _, onRight, ok := normalizeColumnConstRange(tc.l, tc.r); !ok || onRight != tc.right {
			t.Errorf("normalizeColumnConstRange: ok=%v onRight=%v", ok, onRight)
		}
	}
	long := strings.Repeat("y", 70)
	if nc, ok := nameCastLiteral(lit(long, true)); !ok || nc.Value != long[:63] {
		t.Errorf("long literal not truncated to 63 bytes: %v", ok)
	}
	if _, ok := nameCastLiteral(&CastExpr{Operand: &StringConst{Value: "x"}, TargetType: "text"}); ok {
		t.Error("a non-name cast must not fold")
	}
	stats := &catalog.ColumnStats{NDistinct: 21, MCV: []catalog.MCVEntry{{Value: "hot", Frequency: 0.2}, {Value: "nm0", Frequency: 0.04}}}
	_, val, _ := normalizeColumnConst(col, lit("hot", false))
	if sel := eqSelectivityForColumn(stats, val, 2500, "name"); sel != 0.2 {
		t.Errorf("selectivity = %v, want the MCV frequency 0.2", sel)
	}
}
