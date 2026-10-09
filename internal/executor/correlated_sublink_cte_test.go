package executor

import (
	"strings"
	"testing"
)

// TestCorrelatedSublinkCTERematerialises pins M0146-0050 against PG 18.3: a
// CTE declared inside a correlated sublink and reading the outer row is
// materialised afresh for every outer row, as ExecReScanCteScan clears the
// tuplestore when the subplan's parameters change. goopg keyed the
// materialisation by declaration only and replayed the first execution's
// rows (2, 2, 2 for g = 1, 2, 3). Every sublink kind is covered, plus a CTE
// of the enclosing scope read from the sublink. The ARRAY case's body is a
// set-returning function of the outer row, which the correlation check did
// not see (the sublink ran as an InitPlan). Each want is PG's answer.
func TestCorrelatedSublinkCTERematerialises(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, c := range []struct{ query, want string }{
		{"SELECT string_agg(g || ':' || (SELECT k FROM (WITH c AS MATERIALIZED (SELECT g*2 AS k) SELECT k FROM c) z), ',' ORDER BY g) FROM generate_series(1,3) g",
			"1:2,2:4,3:6"},
		{"SELECT string_agg(g::text, ',' ORDER BY g) FROM generate_series(1,6) g WHERE g IN (WITH c AS MATERIALIZED (SELECT g*2 AS k) SELECT k - g FROM c WHERE k > 4)",
			"3,4,5,6"},
		{"SELECT string_agg(g::text, ',' ORDER BY g) FROM generate_series(1,4) g WHERE EXISTS (WITH c AS MATERIALIZED (SELECT g AS k) SELECT 1 FROM c WHERE k % 2 = 0)",
			"2,4"},
		{"SELECT string_agg(g || ':' || array_to_string(ARRAY(WITH c AS MATERIALIZED (SELECT generate_series(1, g) AS k) SELECT k FROM c), '-'), ',' ORDER BY g) FROM generate_series(1,3) g",
			"1:1,2:1-2,3:1-2-3"},
		{"WITH o AS MATERIALIZED (SELECT 10 AS t) SELECT string_agg(g || ':' || (SELECT t + g FROM o), ',' ORDER BY g) FROM generate_series(1,3) g",
			"1:11,2:12,3:13"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.query)), ";"); got != c.want {
			t.Errorf("%s: got %q, want PG's %q", c.query, got, c.want)
		}
	}
}
