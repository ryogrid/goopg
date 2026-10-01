package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestLiteralPrintsAsCoercedConst pins M0146-0005ci against PG 18.3: parse
// analysis coerces a literal to the other operand's type (make_op), and
// EXPLAIN prints the resulting Const through get_const_expr — a numeric
// integer or any negative as `'-6'::numeric`, a decimal bare, a string with
// its type label; a varchar operand compares as text and an integer one
// against a decimal is itself cast. Every want line is PG's own output.
func TestLiteralPrintsAsCoercedConst(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ll (a int, b int8, c char(5), d text, e numeric, f varchar(9), h int2, ts timestamp)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"e = -6", "Filter: (e = '-6'::numeric)"},
		{"e > 0", "Filter: (e > '0'::numeric)"},
		{"e > 2.5", "Filter: (e > 2.5)"},
		{"e / 50 > 1", "Filter: ((e / '50'::numeric) > '1'::numeric)"},
		{"5 < e", "Filter: ('5'::numeric < e)"},
		{"a = -6", "Filter: (a = '-6'::integer)"},
		{"a > 2.5", "Filter: ((a)::numeric > 2.5)"},
		{"b = 5000000000", "Filter: (b = '5000000000'::bigint)"},
		{"h = 3", "Filter: (h = 3)"},
		{"c = 'TN'", "Filter: (c = 'TN'::bpchar)"},
		{"d = 'x'", "Filter: (d = 'x'::text)"},
		{"f = 'x'", "Filter: ((f)::text = 'x'::text)"},
		{"ts <= '2001-07-15'::timestamp", "Filter: (ts <= '2001-07-15 00:00:00'::timestamp without time zone)"},
	} {
		q := "SELECT * FROM ll WHERE " + c.where
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
