package executor

import (
	"strings"
	"testing"
)

// TestCorrelatedExistsOrIsLeafRestriction (M0146-0005dx1): an OR of
// correlated EXISTS whose correlation reads only the first FROM relation is
// that relation's restriction, as PG's distribute_qual_to_rels makes it
// (TPC-DS Q10/Q35 filter the customer probe with `(ANY (c_customer_sk =
// (hashed SubPlan 2).col1)) OR (ANY …)`). The qual must sit on the c scan,
// still be converted to hashed ANY there, and return the rows the query
// returns with the qual held above the join. A RIGHT JOIN whose nullable
// side is c must not sink it: null-extended rows fail both EXISTS and are
// filtered after the join.
func TestCorrelatedExistsOrIsLeafRestriction(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE xo_c (ck int PRIMARY KEY, a int)",
		"CREATE TABLE xo_d (dk int PRIMARY KEY, ck int, b int)",
		"CREATE TABLE xo_s1 (ck int, v int)",
		"CREATE TABLE xo_s2 (ck int, v int)",
		"INSERT INTO xo_c SELECT g, g % 13 FROM generate_series(1, 4000) g",
		"INSERT INTO xo_d SELECT g, (g * 7) % 5000 + 1, g % 5 FROM generate_series(1, 6000) g",
		"INSERT INTO xo_s1 SELECT (g * 11) % 4500 + 1, g % 10 FROM generate_series(1, 3000) g",
		"INSERT INTO xo_s2 SELECT (g * 17) % 4500 + 1, g % 10 FROM generate_series(1, 2000) g",
		"ANALYZE xo_c", "ANALYZE xo_d", "ANALYZE xo_s1", "ANALYZE xo_s2",
	} {
		runSQL(t, ctx, q)
	}
	or := "(EXISTS (SELECT 1 FROM xo_s1 s WHERE s.ck = c.ck AND s.v > 6) OR EXISTS (SELECT 1 FROM xo_s2 s WHERE s.ck = c.ck AND s.v < 2))"
	inForm := "(c.ck IN (SELECT ck FROM xo_s1 WHERE v > 6) OR c.ck IN (SELECT ck FROM xo_s2 WHERE v < 2))"
	cases := []struct {
		name, sql, ref string
		leaf           bool
	}{
		{
			"inner",
			"SELECT count(*), sum(c.a), sum(d.b) FROM xo_c c, xo_d d WHERE c.ck = d.ck AND " + or,
			"SELECT count(*), sum(a), sum(b) FROM (SELECT c.ck, c.a, d.b FROM xo_c c JOIN xo_d d ON c.ck = d.ck OFFSET 0) c WHERE " + inForm,
			true,
		},
		{
			"right join, c nullable",
			"SELECT count(*), sum(d.b) FROM xo_c c RIGHT JOIN xo_d d ON c.ck = d.ck WHERE " + or,
			"SELECT count(*), sum(b) FROM (SELECT c.ck, d.b FROM xo_c c RIGHT JOIN xo_d d ON c.ck = d.ck OFFSET 0) c WHERE " + inForm,
			false,
		},
	}
	for _, c := range cases {
		want := strings.Join(renderRows(runSQL(t, ctx, c.ref)), ";")
		plan := explainText(t, ctx, c.sql)
		if got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";"); got != want {
			t.Fatalf("%s: got %s, want %s\nplan:\n%s", c.name, got, want, plan)
		}
		if !c.leaf {
			continue
		}
		// The hashed ANY filter's host is the nearest plan node above it.
		lines := strings.Split(plan, "\n")
		host := ""
		for i, l := range lines {
			if strings.Contains(l, "Filter:") && strings.Contains(l, "hashed SubPlan") {
				for j := i - 1; j >= 0; j-- {
					if strings.Contains(lines[j], "->") {
						host = lines[j]
						break
					}
				}
				break
			}
		}
		if !strings.Contains(host, "Scan") || !strings.Contains(host, "on xo_c c") {
			t.Fatalf("%s: want the hashed ANY OR on the c scan, host %q\nplan:\n%s", c.name, host, plan)
		}
	}
}
