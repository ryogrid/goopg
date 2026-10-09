package executor

import (
	"crypto/md5"
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestSortedGroupingSetsRollupsMatchPG pins M0146-0020b against PG 18.3.
// preprocess_grouping_sets splits the grouping sets into rollups
// (extract_rollup_sets' chain cover, reorder_grouping_sets' prefix order), and
// AGG_SORTED computes them one pass each: the first over the input sorted on
// its order, every later one over its own sort, printed as `Sort Key:` with
// its sets' `Group Key:` lines indented under it. Rows come rollup by rollup.
// With hashing allowed the sets are hashed in rollup order, and when they do
// not all fit hash_mem the knapsack-chosen rollups are hashed beside the
// sorted ones (AGG_MIXED).
func TestSortedGroupingSetsRollupsMatchPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE gs_t (unique1 int, ten int, two int, v int)",
		"INSERT INTO gs_t SELECT g, g % 10, g % 2, g % 7 FROM generate_series(1, 10000) g",
		"ANALYZE gs_t",
		"CREATE TABLE ks_t (unique1 int, twothousand int, thousand int, hundred int, ten int, four int, two int)",
		"INSERT INTO ks_t SELECT g, g % 2000, g % 1000, g % 100, g % 10, g % 4, g % 2 FROM generate_series(0, 9999) g",
		"ANALYZE ks_t",
	} {
		runSQL(t, ctx, q)
	}
	noHash := optimizer.DefaultPlannerSettings()
	noHash.EnableHashAgg = false
	// groupingsets.sql "test the knapsack": hash_mem 64kB.
	smallHashMem := optimizer.DefaultPlannerSettings()
	smallHashMem.WorkMem = 64 << 10
	smallHashMem.HashMemMultiplier = 1.0
	cases := []struct {
		ps    optimizer.PlannerSettings
		query string
		plan  []string
		rows  string
	}{
		{
			noHash,
			"select two, ten, sum(unique1) from gs_t group by grouping sets ((ten), (two))",
			[]string{
				"GroupAggregate",
				"  Group Key: ten",
				"  Sort Key: two",
				"    Group Key: two",
				"  ->  Sort",
				"        Sort Key: ten",
				"        ->  Seq Scan on gs_t",
			},
			"NULL|0|5005000;NULL|1|4996000;NULL|2|4997000;NULL|3|4998000;NULL|4|4999000;" +
				"NULL|5|5000000;NULL|6|5001000;NULL|7|5002000;NULL|8|5003000;NULL|9|5004000;" +
				"0|NULL|25005000;1|NULL|25000000",
		},
		{
			noHash,
			"select ten, two, v, count(*) from gs_t group by cube (ten, two, v)",
			[]string{
				"GroupAggregate",
				"  Group Key: ten, two, v",
				"  Group Key: ten, two",
				"  Group Key: ten",
				"  Group Key: ()",
				"  Sort Key: two, v",
				"    Group Key: two, v",
				"    Group Key: two",
				"  Sort Key: v, ten",
				"    Group Key: v, ten",
				"    Group Key: v",
				"  ->  Sort",
				"        Sort Key: ten, two, v",
				"        ->  Seq Scan on gs_t",
			},
			// md5 of PG's 184 rows joined with ";" (psql -A, null shown
			// as NULL): rollup by rollup, each along its own order.
			"md5:16a77fd4443c489e12d7bd6524d37c1d",
		},
		{
			// AGG_MIXED: hash_mem as a knapsack over the rollups after the
			// first; the four small tables are hashed (listed in reverse,
			// as PG lcons them), the rest sorted, and the all-hashed path
			// is not made because it does not fit.
			smallHashMem,
			"select unique1, count(two), count(four), count(ten), count(hundred), count(thousand), count(twothousand), count(*) " +
				"from ks_t group by grouping sets (unique1,twothousand,thousand,hundred,ten,four,two)",
			[]string{
				"MixedAggregate",
				"  Hash Key: two",
				"  Hash Key: four",
				"  Hash Key: ten",
				"  Hash Key: hundred",
				"  Group Key: unique1",
				"  Sort Key: twothousand",
				"    Group Key: twothousand",
				"  Sort Key: thousand",
				"    Group Key: thousand",
				"  ->  Sort",
				"        Sort Key: unique1",
				"        ->  Seq Scan on ks_t",
			},
			"",
		},
		{
			smallHashMem,
			"select sum(c) from (select count(*) c from ks_t group by grouping sets (unique1,twothousand,thousand,hundred,ten,four,two)) s",
			nil,
			"70000",
		},
		{
			optimizer.DefaultPlannerSettings(),
			"select ten, two, count(*) from gs_t group by grouping sets ((ten), (two))",
			[]string{
				"HashAggregate",
				"  Hash Key: ten",
				"  Hash Key: two",
				"  ->  Seq Scan on gs_t",
			},
			"",
		},
	}
	for _, c := range cases {
		got := explainLines(t, ctx, c.ps, "EXPLAIN (COSTS OFF) "+c.query)
		if c.plan != nil && strings.Join(got, "\n") != strings.Join(c.plan, "\n") {
			t.Errorf("%s:\ngot\n%s\nwant PG's\n%s", c.query, strings.Join(got, "\n"), strings.Join(c.plan, "\n"))
		}
		if c.rows == "" {
			continue
		}
		rows := strings.Join(renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, c.query, c.ps))), ";")
		if strings.HasPrefix(c.rows, "md5:") {
			rows = "md5:" + fmt.Sprintf("%x", md5.Sum([]byte(rows)))
		}
		if rows != c.rows {
			t.Errorf("%s: rows\n%s\nwant PG's\n%s", c.query, rows, c.rows)
		}
	}
}
