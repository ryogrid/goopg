package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplainVerboseOutputThroughSubquery pins M0148-0001f2: a column of a
// subquery whose Subquery Scan PG removed deparses, above a join, through the
// subquery's targetlist — the grouping column as its Var, the aggregate as
// `(sum(...))` — in the aggregate's own scope (aggregates.sql, PG 18.3).
func TestExplainVerboseOutputThroughSubquery(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	q := `EXPLAIN (VERBOSE, COSTS OFF)
select s1, s2, sm
from generate_series(1, 3) s1,
     lateral (select s2, sum(s1 + s2) sm
              from generate_series(1, 3) s2 group by s2) ss
order by 1, 2`
	got := strings.Join(renderRows(runSQLWith(t, ctx, q, optimizer.DefaultPlannerSettings())), "\n")
	want := strings.Join([]string{
		"Sort",
		"  Output: s1.s1, s2.s2, (sum((s1.s1 + s2.s2)))",
		"  Sort Key: s1.s1, s2.s2",
		"  ->  Nested Loop",
		"        Output: s1.s1, s2.s2, (sum((s1.s1 + s2.s2)))",
		"        ->  Function Scan on pg_catalog.generate_series s1",
		"              Output: s1.s1",
		"              Function Call: generate_series(1, 3)",
		"        ->  HashAggregate",
		"              Output: s2.s2, sum((s1.s1 + s2.s2))",
		"              Group Key: s2.s2",
		"              ->  Function Scan on pg_catalog.generate_series s2",
		"                    Output: s2.s2",
		"                    Function Call: generate_series(1, 3)",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
}
