package executor

import (
	"strings"
	"testing"
)

// TestNestedSimpleSubqueriesPullUp pins M0146-0007i: pull_up_simple_subquery
// flattens a subquery's own simple subqueries (pull_up_subqueries recursing)
// before splicing it into the parent, so every level's relations join the
// parent's search and every level's WHERE reaches the scans. goopg pulled up
// one level: a derived table or inlinable CTE inside a pulled body stayed a
// subquery, and the outer qual stayed above the join. Expected values and
// shapes are PG 18.3's.
func TestNestedSimpleSubqueriesPullUp(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE n_t (a int, b int)",
		"INSERT INTO n_t SELECT g, g % 10 FROM generate_series(1, 1000) g",
		"CREATE TABLE n_u (a int, b int)",
		"INSERT INTO n_u SELECT g, g % 7 FROM generate_series(1, 500) g",
		"ANALYZE n_t",
		"ANALYZE n_u",
	} {
		runSQL(t, ctx, q)
	}
	cases := []struct {
		name, q, rows string
		want          []string // plan lines PG prints
		absent        []string
	}{
		{
			name: "derived table inside a pulled derived table",
			q: `SELECT count(*), sum(q.a) FROM (SELECT s.a, s.b, n_u.b AS ub
			      FROM (SELECT a, b FROM n_t WHERE b < 5) s, n_u WHERE s.a = n_u.a) q
			    WHERE q.b = 1`,
			rows:   "50|12300",
			want:   []string{"Filter: ((b < 5) AND (b = 1))"},
			absent: []string{"Subquery Scan", "Filter: (n_t.b = 1)"},
		},
		{
			name: "three levels",
			q: `SELECT count(*) FROM (SELECT a FROM (SELECT a FROM
			      (SELECT a FROM n_t WHERE b = 1) s1 WHERE a > 100) s2 WHERE a < 900) s3`,
			rows: "80",
			want: []string{"Filter: ((a > 100) AND (a < 900) AND (b = 1))"},
		},
		{
			name: "NOT MATERIALIZED CTE read inside another one's body",
			q: `WITH y AS NOT MATERIALIZED (SELECT a, b FROM n_u),
			         x AS NOT MATERIALIZED (SELECT y.a, y.b FROM y, n_t WHERE y.a = n_t.a)
			    SELECT count(*) FROM x, x x2, y WHERE x.a = x2.a AND y.a = x.a AND x2.b = 3`,
			rows:   "72",
			absent: []string{"Subquery Scan", "CTE "},
		},
	}
	for _, c := range cases {
		plan := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+c.q))
		joined := strings.Join(plan, "\n")
		for _, w := range c.want {
			if countLinesContaining(plan, w) != 1 {
				t.Errorf("%s: want %q:\n%s", c.name, w, joined)
			}
		}
		for _, a := range c.absent {
			if countLinesContaining(plan, a) != 0 {
				t.Errorf("%s: want no %q:\n%s", c.name, a, joined)
			}
		}
		if got := strings.Join(renderRows(runSQL(t, ctx, c.q)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want PG's %q\nplan:\n%s", c.name, got, c.rows, joined)
		}
	}
}
