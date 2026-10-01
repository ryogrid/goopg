package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestTextConcatAndLiteralKeysPrintTyped pins M0146-0005dc against PG 18.3:
//   - `||` over character operands is textcat / textanycat, so a literal
//     prints as its text Const and a char(n) / varchar / integer operand
//     shows its `(x)::text` cast; the result is text, so a literal compared
//     with it is `'q'::text`;
//   - a UNION ALL arm's literal is a target-list entry resolved to text and
//     reaches the keys above the Append as an OUTER_VAR, printed
//     parenthesized: `Group Key: ('store'::text), …` (TPC-DS Q80's shape).
//
// Every want line is PG's output for the statement.
func TestTextConcatAndLiteralKeysPrintTyped(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE cc (c char(5), d text, f varchar(9), a int)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	union := "SELECT ch, id, sum(a) FROM (SELECT 'store' AS ch, 'x' || c AS id, a FROM cc UNION ALL SELECT 'web' AS ch, 'y' || c, a FROM cc) s GROUP BY ch, id ORDER BY ch, id"
	for _, c := range []struct{ q, want string }{
		{"SELECT * FROM cc WHERE 'x' || c = 'q'", "Filter: (('x'::text || (c)::text) = 'q'::text)"},
		{"SELECT * FROM cc WHERE d || f = 'q'", "Filter: ((d || (f)::text) = 'q'::text)"},
		{"SELECT * FROM cc WHERE d || a = 'q'", "Filter: ((d || (a)::text) = 'q'::text)"},
		{"SELECT * FROM cc WHERE 'a' || d || 'b' = 'q'", "Filter: ((('a'::text || d) || 'b'::text) = 'q'::text)"},
		{union, "Sort Key: ('store'::text), (('x'::text || (cc.c)::text))"},
		{union, "Group Key: ('store'::text), (('x'::text || (cc.c)::text))"},
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
