package executor

import (
	"regexp"
	"strings"
	"testing"
)

// TestCTELeafColumnTakesBaseStatistics pins M0146-0005dq against PG 18.3's
// examine_simple_variable RTE_CTE recursion: a column of a pulled-up CTE leaf
// whose body is a plain join takes the statistics of the base column it is a
// Var of (csi.k), so eqjoinsel_semi has a real ndistinct on both sides. With
// nd(outer) <= nd(inner) every outer row is expected to match: the semi join's
// rows equal its outer scan's. Without the recursion the CTE side was a
// default 200 and the semi selectivity punted to 0.5 — half the outer rows.
// PG 18.3: Hash Semi Join rows=150 over Seq Scan on cso rows=150.
func TestCTELeafColumnTakesBaseStatistics(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE cso (k int, v int)",
		"CREATE TABLE csi (k int, w int)",
		"INSERT INTO cso SELECT g % 3000, g FROM generate_series(1,30000) g",
		"INSERT INTO csi SELECT g % 3000, g % 5 FROM generate_series(1,6000) g",
		"ANALYZE cso", "ANALYZE csi",
	} {
		runSQL(t, ctx, q)
	}
	lines := renderRows(runSQL(t, ctx, "EXPLAIN WITH c AS MATERIALIZED (SELECT a.k FROM csi a JOIN csi b ON a.k = b.k AND a.w <> b.w) "+
		"SELECT count(*) FROM cso WHERE cso.v % 10 = 0 AND cso.k IN (SELECT k FROM c)"))
	rowsRe := regexp.MustCompile(`rows=(\d+) `)
	semi, outer := "", ""
	for i, l := range lines {
		m := rowsRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		// The IN's join is the one keyed on cso.k (a Semi Join, or — once
		// the CTE leaf is unique-ified — a Hash Join over its HashAggregate;
		// both are sized as the semi joinrel).
		if semi == "" && strings.Contains(l, "Join") && i+1 < len(lines) && strings.Contains(lines[i+1], "cso.k") {
			semi = m[1]
		}
		if outer == "" && strings.Contains(l, "Scan on cso") {
			outer = m[1]
		}
	}
	if semi == "" || outer == "" {
		t.Fatalf("expected a semi join over the CTE with a cso scan:\n%s", strings.Join(lines, "\n"))
	}
	if semi != outer {
		t.Errorf("semi join rows=%s, want the outer scan's rows=%s (eqjoinsel_semi selectivity 1.0 from csi.k's statistics):\n%s",
			semi, outer, strings.Join(lines, "\n"))
	}
}
