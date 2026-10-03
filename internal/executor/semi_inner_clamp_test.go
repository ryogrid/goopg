package executor

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestSemiJoinSizeClampedByInnerJoin pins M0146-0009g against PG 18.3's
// eqjoinsel SEMI/ANTI arm (selfuncs.c:2417): a semijoin never yields more
// rows than the inner join of the same inputs, so its selectivity is clamped
// to N2 * Sinner. With a grouped CTE inner whose column has no statistics,
// eqjoinsel_semi punts to 0.5; the clamp turns that into inner rows / nd1.
// The CTE body is a grouped JOIN, TPC-DS Q23's frequent_ss_items shape, so
// its grouping column has no statistics on either engine (PG's
// examine_simple_variable stops at a multi-key GROUP BY).
func TestSemiJoinSizeClampedByInnerJoin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zs (item int, d int)")
	runSQL(t, ctx, "CREATE TABLE zt (k int, label int)")
	runSQL(t, ctx, "CREATE TABLE zc (item int, v int)")
	runSQL(t, ctx, "INSERT INTO zs SELECT g % 300, g % 4 FROM generate_series(1,6000) g")
	runSQL(t, ctx, "INSERT INTO zt SELECT g, g FROM generate_series(0,3) g")
	runSQL(t, ctx, "INSERT INTO zc SELECT (g*13) % 8000, g FROM generate_series(1,100000) g")
	runSQL(t, ctx, "ANALYZE zs")
	runSQL(t, ctx, "ANALYZE zt")
	runSQL(t, ctx, "ANALYZE zc")

	rowsRe := regexp.MustCompile(`^\s*(?:->\s+)?(\S.*?)\s+\(cost=\S+ rows=(\d+) `)
	lines := renderRows(runSQL(t, ctx,
		"EXPLAIN WITH f AS MATERIALIZED (SELECT zs.item + 0 AS item, zt.label, count(*) c FROM zs, zt WHERE zs.d = zt.k GROUP BY zs.item + 0, zt.label) SELECT * FROM zc WHERE item IN (SELECT item FROM f)"))
	semi, cte := -1.0, -1.0
	for _, l := range lines {
		m := rowsRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		r, _ := strconv.ParseFloat(m[2], 64)
		switch {
		case strings.Contains(m[1], "Semi Join") && semi < 0:
			semi = r
		case strings.HasPrefix(m[1], "HashAggregate") && cte < 0:
			cte = r // the CTE body's group estimate
		}
	}
	if semi < 0 || cte < 0 {
		t.Fatalf("expected a semi join over the CTE:\n%s", strings.Join(lines, "\n"))
	}
	// Ssemi = min(0.5 punt, N2 * 1/max(nd1, nd2)) with nd1 = 8000 measured.
	want := 100000 * math.Min(0.5, cte/8000)
	if semi > want*1.02+1 || semi < want*0.98-1 {
		t.Errorf("semi join rows=%v, want %.0f (100000 x min(0.5, %v/8000)):\n%s", semi, want, cte, strings.Join(lines, "\n"))
	}
}
