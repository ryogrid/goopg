package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestInListPrintsFoldedArrayConst pins M0146-0005ch against PG 18.3: an
// all-literal IN list is transformAExprIn's ScalarArrayOpExpr, whose
// ArrayExpr eval_const_expressions folds to one array Const, so EXPLAIN
// prints `(a = ANY ('{1,2}'::integer[]))` — element type from
// select_common_type, elements quoted by array_out. NOT IN, and a NOT
// pushed in by negate_clause, print the `<> ALL` form. Every want line is
// PG's own output for the statement.
func TestInListPrintsFoldedArrayConst(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ii (a int, b int8, c char(5), d text, e numeric, f varchar(9), g date, h int2)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"a IN (1,2)", "Filter: (a = ANY ('{1,2}'::integer[]))"},
		{"b IN (1,2)", "Filter: (b = ANY ('{1,2}'::bigint[]))"},
		{"c IN ('x','y z')", `Filter: (c = ANY ('{x,"y z"}'::bpchar[]))`},
		{`d IN ('a,b','"q"','', 'NULL')`, `Filter: (d = ANY ('{"a,b","\"q\"","","NULL"}'::text[]))`},
		{"d IN ('it''s','x')", "Filter: (d = ANY ('{it''s,x}'::text[]))"},
		{"e IN (1, 2.5)", "Filter: (e = ANY ('{1,2.5}'::numeric[]))"},
		{"h IN (1,2)", "Filter: (h = ANY ('{1,2}'::integer[]))"},
		{"a IN (1, 2.5)", "Filter: ((a)::numeric = ANY ('{1,2.5}'::numeric[]))"},
		{"f IN ('a','b')", "Filter: ((f)::text = ANY ('{a,b}'::text[]))"},
		{"g IN ('2001-01-01','2001-02-01')", "Filter: (g = ANY ('{2001-01-01,2001-02-01}'::date[]))"},
		{"a NOT IN (1,2)", "Filter: (a <> ALL ('{1,2}'::integer[]))"},
		{"NOT (b IN (3,4))", "Filter: (b <> ALL ('{3,4}'::bigint[]))"},
		{"a <> ANY (ARRAY[1,2])", "Filter: (a <> ANY ('{1,2}'::integer[]))"},
	} {
		q := "SELECT * FROM ii WHERE " + c.where
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
