package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplicitCastPrints pins M0146-0005cw against PG 18.3: a cast written
// in the query is a COERCE_EXPLICIT_CAST node that ruleutils always shows,
// `(arg)::type` with format_type_with_typemod's name; a cast of a literal is
// folded to a Const of the target type and printed by get_const_expr; a cast
// to the operand's own type is no node at all. Each want line is PG's own
// output for the statement.
func TestExplicitCastPrints(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE cx (a int, n numeric, d date, t text, v varchar(10))")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"cast(a as decimal(15,4)) / cast(n as decimal(15,4)) > 1", "Filter: (((a)::numeric(15,4) / (n)::numeric(15,4)) > '1'::numeric)"},
		{"(n / 50)::integer = 3", "Filter: (((n / '50'::numeric))::integer = 3)"},
		{"cast(d as date) = '2000-01-01'", "Filter: (d = '2000-01-01'::date)"},
		{"a::text = t", "Filter: ((a)::text = t)"},
		{"n > cast(5 as numeric(10,2))", "Filter: (n > 5.00::numeric(10,2))"},
		{"n > cast(2.555 as numeric(10,2))", "Filter: (n > 2.56::numeric(10,2))"},
		{"a > cast(5 as integer)", "Filter: (a > 5)"},
		{"n > cast(7 as bigint)", "Filter: (n > '7'::numeric)"},
		{"t = cast('x' as text)", "Filter: (t = 'x'::text)"},
	} {
		q := "SELECT * FROM cx WHERE " + c.where
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
