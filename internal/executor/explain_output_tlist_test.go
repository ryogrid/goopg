package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplainVerboseScanOutputTlist pins show_plan_tlist for the shapes
// M0148-0001f1 covers (PG 18.3 output): a scan prints the statement's
// projection (plus a resjunk sort key), an Append prints no Output, and an
// appendrel member prints only the columns the Append's consumer reads —
// not the restriction evaluated on the member.
func TestExplainVerboseScanOutputTlist(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ot (a int, b int, c text)")
	explain := func(q string) string {
		return strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (VERBOSE, COSTS OFF) "+q, optimizer.DefaultPlannerSettings())), "\n")
	}
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"SELECT a FROM ot", []string{
			"Seq Scan on public.ot",
			"  Output: a",
		}},
		{"SELECT a FROM ot ORDER BY b LIMIT 2", []string{
			"Limit",
			"  Output: a, b",
			"  ->  Sort",
			"        Output: a, b",
			"        Sort Key: ot.b",
			"        ->  Seq Scan on public.ot",
			"              Output: a, b",
		}},
		{"SELECT sum(a) FROM (SELECT * FROM ot UNION ALL SELECT * FROM ot) u", []string{
			"Aggregate",
			"  Output: sum(ot.a)",
			"  ->  Append",
			"        ->  Seq Scan on public.ot",
			"              Output: ot.a",
			"        ->  Seq Scan on public.ot ot_1",
			"              Output: ot_1.a",
		}},
		{"SELECT a + 1, c FROM ot UNION ALL SELECT b, c FROM ot WHERE a > 5", []string{
			"Append",
			"  ->  Seq Scan on public.ot",
			"        Output: (ot.a + 1), ot.c",
			"  ->  Seq Scan on public.ot ot_1",
			"        Output: ot_1.b, ot_1.c",
			"        Filter: (ot_1.a > 5)",
		}},
	} {
		if got, want := explain(tc.q), strings.Join(tc.want, "\n"); got != want {
			t.Errorf("%s:\n got:\n%s\nwant (PG 18.3):\n%s", tc.q, got, want)
		}
	}
}
