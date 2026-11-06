package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplainSRFCastResult pins M0148-0001i3 (PG 18.3 output): a cast over
// an unnest is evaluated in a Result above the ProjectSet
// (split_pathtarget_at_srfs), and a sort key on that column deparses as the
// Result's entry, `(((unnest(st.a)))::integer)`. A bare unnest has no Result.
func TestExplainSRFCastResult(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE st (y int, a text[])")
	explain := func(q string) string {
		return strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, optimizer.DefaultPlannerSettings())), "\n")
	}
	if got, want := explain("SELECT * FROM (SELECT y, unnest(a)::int AS v FROM st) s ORDER BY y, v"), strings.Join([]string{
		"Sort",
		"  Sort Key: st.y, (((unnest(st.a)))::integer)",
		"  ->  Result",
		"        ->  ProjectSet",
		"              ->  Seq Scan on st",
	}, "\n"); got != want {
		t.Errorf("cast over unnest:\n got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
	if got, want := explain("SELECT y, unnest(a) FROM st"), strings.Join([]string{
		"ProjectSet",
		"  ->  Seq Scan on st",
	}, "\n"); got != want {
		t.Errorf("bare unnest:\n got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
}
