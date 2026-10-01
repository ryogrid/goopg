package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestIntegerOperandPromotedToNumeric pins M0146-0005cu against PG 18.3:
// there are no mixed integer/numeric operators, so make_op coerces the
// integer operand to numeric and get_oper_expr shows the coercion. Integer
// against integer keeps the cross-type operator. Each want line is PG's
// own output for the statement.
func TestIntegerOperandPromotedToNumeric(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE nn (a int, b int8, n numeric, m numeric)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"a * n > m", "Filter: (((a)::numeric * n) > m)"},
		{"n = a", "Filter: (n = (a)::numeric)"},
		{"b + n > 1.5", "Filter: (((b)::numeric + n) > 1.5)"},
		{"(a - b) < n", "Filter: (((a - b))::numeric < n)"},
		{"a < b", "Filter: (a < b)"},
	} {
		q := "SELECT * FROM nn WHERE " + c.where
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		found := false
		for _, l := range lines {
			if l == c.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s\nwant line %q in:\n%s", q, c.want, strings.Join(lines, "\n"))
		}
	}
}
