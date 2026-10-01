package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestBoolChainsPrintFlat pins M0146-0005cl against PG 18.3: AND / OR are
// N-ary BoolExprs and eval_const_expressions flattens nested same-kind
// arms, so a planned qual prints one flat list per operator. Each want line
// is PG's own output for the statement.
func TestBoolChainsPrintFlat(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE bb (a int, b int, c int, d int)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"a = 1 AND b = 2 AND c = 3", "Filter: ((a = 1) AND (b = 2) AND (c = 3))"},
		{"(a = 1 AND b = 2) AND (c = 3 AND d = 4)", "Filter: ((a = 1) AND (b = 2) AND (c = 3) AND (d = 4))"},
		{"a = 1 OR b = 2 OR (c = 3 OR d = 4)", "Filter: ((a = 1) OR (b = 2) OR (c = 3) OR (d = 4))"},
		{"(a = 1 AND b = 2) OR (c = 3 AND d = 4) OR a = 9", "Filter: (((a = 1) AND (b = 2)) OR ((c = 3) AND (d = 4)) OR (a = 9))"},
		{"a = 1 AND (b = 2 OR (c = 3 OR d = 4))", "Filter: ((a = 1) AND ((b = 2) OR (c = 3) OR (d = 4)))"},
	} {
		q := "SELECT * FROM bb WHERE " + c.where
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
