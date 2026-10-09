package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestStringOperandsShowTextCast pins M0146-0005cn against PG 18.3:
// varchar has no operators of its own and substr/upper take text only, so
// parse analysis coerces the operand to text, and EXPLAIN shows the
// coercion because operator and function arguments deparse with
// showimplicit. char(n) against char(n) keeps bpchar's operators. Each want
// line is PG's own output for the statement.
func TestStringOperandsShowTextCast(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE v1 (a varchar(20), b char(10), c text, n int)")
	runSQL(t, ctx, "CREATE TABLE v2 (a varchar(20), b char(10), c text, n int)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashJoin = false
	ps.EnableMergeJoin = false
	for _, c := range []struct{ q, want string }{
		{"SELECT * FROM v1 WHERE a <> upper(a)", "Filter: ((a)::text <> upper((a)::text))"},
		{"SELECT * FROM v1 WHERE a = c", "Filter: ((a)::text = c)"},
		{"SELECT * FROM v1 WHERE substr(b, 1, 2) = 'ab'", "Filter: (substr((b)::text, 1, 2) = 'ab'::text)"},
		{"SELECT * FROM v1 WHERE substr(a, 1, 2) = substr(c, 1, 2)", "Filter: (substr((a)::text, 1, 2) = substr(c, 1, 2))"},
		{"SELECT * FROM v1, v2 WHERE v1.a = v2.a", "Join Filter: ((v1.a)::text = (v2.a)::text)"},
		{"SELECT * FROM v1, v2 WHERE v1.b = v2.b", "Join Filter: (v1.b = v2.b)"},
	} {
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)) {
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
			t.Errorf("%s\nwant line %q in:\n%s", c.q, c.want, strings.Join(lines, "\n"))
		}
	}
}
