package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestNumericConstantFoldMatchesPG pins M0146-0041 against PG 18.3: the
// planner folds numeric literal arithmetic with the executor's numeric
// operators (numeric_add/sub/mul/div, select_div_scale), not float64. The
// float fold returned 0.1+0.2 as 0.30000000000000004, 1.0/3 as
// 0.3333333333333333 and 12345678901234567890.5 + 1 as
// 12345678901234567000, and compared 1.00000000000000000001 > 1 as false.
// Every want value is PG's own output.
func TestNumericConstantFoldMatchesPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	q := "SELECT 0.1+0.2, 1.1*1.1, 12345678901234567890.5 + 1, 1.50 + 1, 0.1 - 0.3, " +
		"1.0/3, 2.0/3.0*3, 7/2.0, 1e3/7, " +
		"1.00000000000000000001 > 1, 0.3 = 0.1+0.2, 2 < 2.0000000000000000001"
	want := []string{"0.3", "1.21", "12345678901234567891.5", "2.50", "-0.2",
		"0.33333333333333333333", "2.00000000000000000001", "3.5000000000000000", "142.8571428571428571",
		"t", "t", "t"}
	rows := drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps))
	if len(rows) != 1 || len(rows[0]) != len(want) {
		t.Fatalf("got %d rows / %v", len(rows), rows)
	}
	for i, w := range want {
		got := rows[0][i].Format()
		if got == "true" {
			got = "t"
		}
		if got != w {
			t.Errorf("column %d: got %q, want %q", i+1, got, w)
		}
	}

	// The folded constant is what EXPLAIN shows (PG prints the same Const).
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT 1 WHERE random()::numeric >= 2.0/3.0", ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), ">= 0.66666666666666666667)") {
		t.Errorf("EXPLAIN lacks the numeric-folded constant:\n%s", strings.Join(lines, "\n"))
	}
}
