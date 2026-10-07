package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRangeRestrictionRowsOnEveryPath pins M0146-0009r on regress
// aggregates' agg_sort_order fixture (100 rows, c1 pkey, c2 unique). PG 18.3
// estimates `c2 < 100` at 99 rows and `c2 < 50` at 48 by the histogram, on a
// Seq Scan under default settings and on the c2 index with seq scans off.
//
// goopg's EstimateRows priced every index range bound at DEFAULT_INEQ_SEL:
// the default-settings plan read rows=33 for both bounds, and with seq scans
// off the 33 sized the grouped rel above the c2 index scan and elected an
// Incremental Sort PG does not. The default-settings plan is still an Index
// Scan where PG keeps the Seq Scan — the single-table rule-based index
// producer overrides the search's costed choice (M0146-0060) — so only its
// rows are pinned here.
func TestRangeRestrictionRowsOnEveryPath(t *testing.T) {
	// With seq scans off PG keeps the c2 Index Scan over its range bitmap.
	// goopg's plain index probe costs indexProbeCostMultiplier (2) times
	// PG's, so since M0146-0061 gave the search a range bitmap path the
	// bitmap wins there at the shipped multiplier (regress aggregates shows
	// the same hunk). Pinned under PG's costing; the multiplier is
	// M0146-0068's owner decision.
	defer optimizer.SetIndexProbeCostMultiplier("1")()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE agg_sort_order (c1 int PRIMARY KEY, c2 int)",
		"CREATE UNIQUE INDEX agg_sort_order_c2_idx ON agg_sort_order(c2)",
		"INSERT INTO agg_sort_order SELECT i, i FROM generate_series(1, 100) i",
		"ANALYZE agg_sort_order",
	} {
		runSQL(t, ctx, q)
	}
	explain := func(q string, ps optimizer.PlannerSettings) []string {
		return renderRows(runSQLWith(t, ctx, "EXPLAIN "+q, ps))
	}
	def := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, want string }{
		{"SELECT * FROM agg_sort_order WHERE c2 < 100", "rows=99 "},
		{"SELECT * FROM agg_sort_order WHERE c2 < 50", "rows=48 "},
	} {
		if plan := explain(c.q, def); countLinesContaining(plan, c.want) != 1 {
			t.Errorf("%s: want %q (PG 18.3):\n%s", c.q, c.want, strings.Join(plan, "\n"))
		}
	}
	noSeq := optimizer.DefaultPlannerSettings()
	noSeq.EnableSeqScan = false
	const agg = "SELECT array_agg(c1 ORDER BY c2), c2 FROM agg_sort_order WHERE c2 < 100 GROUP BY c1 ORDER BY 2"
	plan := explain(agg, noSeq)
	joined := strings.Join(plan, "\n")
	if countLinesContaining(plan, "Incremental Sort") != 0 ||
		countLinesContaining(plan, "Index Scan using agg_sort_order_c2_idx") != 1 ||
		countLinesContaining(plan, "GroupAggregate") != 1 {
		t.Errorf("want PG's Sort → GroupAggregate → Sort → Index Scan using agg_sort_order_c2_idx:\n%s", joined)
	}
	for _, l := range plan {
		if strings.Contains(l, "GroupAggregate") && !strings.Contains(l, "rows=99 ") {
			t.Errorf("GroupAggregate rows: want PG's 99:\n%s", joined)
		}
	}
}
