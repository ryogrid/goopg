package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestAggScanReadSetMatchesFullDecode pins M0122-0012 C57: a seq scan whose
// rows reach only a plain or partial Aggregate (directly, through an absorbed
// Filter, a Sort, or the left side of a join) decodes only the columns the
// aggregate's arms and its own qual read. The table interleaves the read
// columns with unread dates, numerics and text (HammerDB's lineitem order is
// the shape), carries NULLs, and the queries cover group keys, FILTER,
// ORDER BY and DISTINCT inside the aggregate, a functionally dependent
// passthrough column, HAVING, count(*), a qual on an otherwise unread column,
// and hashed and sorted strategies. Every result must equal the full decode.
func TestAggScanReadSetMatchesFullDecode(t *testing.T) {
	t.Cleanup(func() { aggScanReadSetDisabled = false })
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE ars (d date, k numeric, a numeric(12,2), b numeric(12,2), c numeric,
		q numeric(10,2), f char(1), s text, i int PRIMARY KEY, n text)`)
	runSQL(t, ctx, `INSERT INTO ars SELECT
		DATE '2020-01-01' + (x % 400),
		CASE WHEN x % 53 = 0 THEN NULL ELSE (x / 4)::numeric END,
		(x % 97) * 1.25, (x % 89) * 0.5, x % 13,
		CASE WHEN x % 41 = 0 THEN NULL ELSE (x % 50)::numeric END,
		chr(65 + x % 3), 'row ' || x, x, CASE WHEN x % 7 = 0 THEN NULL ELSE 'n' || (x % 9) END
		FROM generate_series(1, 4000) x`)
	runSQL(t, ctx, "CREATE TABLE arj (k numeric, w text)")
	runSQL(t, ctx, "INSERT INTO arj SELECT x, 'w' || x FROM generate_series(0, 1100) x")
	runSQL(t, ctx, "ANALYZE ars")
	runSQL(t, ctx, "ANALYZE arj")
	hashed := optimizer.DefaultPlannerSettings()
	hashed.MaxParallelWorkersPerGather = 0
	sorted := hashed
	sorted.EnableHashAgg = false
	queries := []string{
		"SELECT k, sum(q) FROM ars GROUP BY k HAVING sum(q) > 60 ORDER BY k",
		"SELECT k FROM ars GROUP BY k HAVING sum(q) > 60 ORDER BY k",
		"SELECT f, count(*), sum(a) FILTER (WHERE c > 5), max(d) FROM ars WHERE b > 10 GROUP BY f ORDER BY f",
		"SELECT f, string_agg(s, ',' ORDER BY i DESC) FROM ars WHERE i < 60 GROUP BY f ORDER BY f",
		"SELECT f, count(DISTINCT n), avg(c) FROM ars GROUP BY f ORDER BY f",
		"SELECT i, s, sum(q) FROM ars WHERE i % 100 = 0 GROUP BY i ORDER BY i",
		"SELECT count(*) FROM ars WHERE s LIKE 'row 1%'",
		"SELECT sum(q), min(a), max(n) FROM ars WHERE d > DATE '2020-06-01'",
		"SELECT ars.f, count(*), sum(ars.q) FROM ars JOIN arj ON ars.k = arj.k WHERE arj.w <> 'w7' GROUP BY ars.f ORDER BY ars.f",
	}
	for _, ps := range []optimizer.PlannerSettings{hashed, sorted} {
		for _, q := range queries {
			got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
			aggScanReadSetDisabled = true
			want := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
			aggScanReadSetDisabled = false
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("hashagg=%v %s: narrowed scan differs from the full decode\nnarrowed %v\nfull     %v", ps.EnableHashAgg, q, got, want)
			}
			if len(got) == 0 {
				t.Fatalf("%s: no rows", q)
			}
		}
	}
}
