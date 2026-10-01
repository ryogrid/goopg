package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCaseNullArmIsTyped pins M0146-0005ct against PG 18.3: a NULL CASE
// result is coerced to the CASE's type and get_const_expr labels the typed
// NULL Const; an omitted ELSE is the parser's NULL default, which ruleutils
// always prints. Each want line is PG's own output for the statement.
func TestCaseNullArmIsTyped(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ce (a int, n numeric, t text)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"CASE WHEN n > 0 THEN n / 2 ELSE NULL END > 1", "Filter: (CASE WHEN (n > '0'::numeric) THEN (n / '2'::numeric) ELSE NULL::numeric END > '1'::numeric)"},
		{"CASE WHEN a > 0 THEN a END > 1", "Filter: (CASE WHEN (a > 0) THEN a ELSE NULL::integer END > 1)"},
		{"CASE WHEN a > 0 THEN NULL ELSE t END = 'x'", "Filter: (CASE WHEN (a > 0) THEN NULL::text ELSE t END = 'x'::text)"},
		{"CASE WHEN a > 0 THEN 1 ELSE 2 END = 1", "Filter: (CASE WHEN (a > 0) THEN 1 ELSE 2 END = 1)"},
	} {
		q := "SELECT * FROM ce WHERE " + c.where
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
