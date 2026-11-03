package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestGroupingSetsOrderedAggRunsSorted: PG never hashes a grouping with an
// ordered aggregate (numOrderedAggs), grouping sets included, so a ROLLUP
// with count(DISTINCT …) is a sorted GroupAggregate, not a MixedAggregate,
// and CUBE's second rollup re-sorts its input (M0148-0001j1). The values are
// pinned too: each set's DISTINCT runs over that set's own groups.
func TestGroupingSetsOrderedAggRunsSorted(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE gs (ten int, three int, s text)")
	runSQL(t, ctx, "INSERT INTO gs SELECT g % 10, g % 3, (ARRAY['A','H','O','V'])[g % 4 + 1] FROM generate_series(1, 200) g")
	explain := func(q string) string {
		return strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, optimizer.DefaultPlannerSettings())), "\n")
	}
	if got, want := explain("SELECT ten, count(DISTINCT s) FROM gs GROUP BY ROLLUP (ten)"), strings.Join([]string{
		"GroupAggregate",
		"  Group Key: ten",
		"  Group Key: ()",
		"  ->  Sort",
		"        Sort Key: ten",
		"        ->  Seq Scan on gs",
	}, "\n"); got != want {
		t.Errorf("rollup:\n got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
	if got := explain("SELECT ten, three, array_agg(s ORDER BY s) FROM gs GROUP BY CUBE (ten, three)"); strings.Contains(got, "Hash") || !strings.HasPrefix(got, "GroupAggregate") {
		t.Errorf("cube with an ORDER BY aggregate must run sorted, got:\n%s", got)
	}
	got := renderRows(runSQL(t, ctx, "SELECT ten, count(DISTINCT s) FROM gs WHERE ten < 2 GROUP BY ROLLUP (ten) ORDER BY 1"))
	if want := []string{"0|2", "1|2", "NULL|4"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want %v", got, want)
	}
}
