package executor

import (
	"strings"
	"testing"
)

// M0146-0119: a pulled-up body whose target list is `*` (or `alias.*`) is
// pulled up like one with an explicit list — parse analysis has expanded the
// star before pull_up_simple_subquery sees it. Regress subselect's
// NOT MATERIALIZED pair, `x as not materialized (select * from (select f1,
// now() as n …) ss)`, plans as a gating Result over the nested loop because
// `x.n = x2.n` becomes the pseudoconstant `now() = now()`; goopg kept each
// reference a subquery and merge-joined on `(now())`.
func TestPulledStarBodyIsPulledUp(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE m119t (f1 int, f2 int, f3 float8)",
		"INSERT INTO m119t VALUES (1, 2, 3), (4, 5, 6)",
	} {
		runSQL(t, ctx, q)
	}
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"WITH x AS NOT MATERIALIZED (SELECT * FROM (SELECT f1, now() AS n FROM m119t) ss) SELECT * FROM x, x x2 WHERE x.n = x2.n",
			[]string{
				"Result",
				"  One-Time Filter: (now() = now())",
				"  ->  Nested Loop",
				"        ->  Seq Scan on m119t",
				"        ->  Materialize",
				"              ->  Seq Scan on m119t m119t_1",
			}},
		{"SELECT * FROM (SELECT * FROM (SELECT f1, f1 + 1 AS n FROM m119t) ss) a WHERE a.n = 3",
			[]string{"Seq Scan on m119t", "  Filter: ((f1 + 1) = 3)"}},
	} {
		got := renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q))
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\nwant:\n%s\ngot:\n%s", tc.q, strings.Join(tc.want, "\n"), strings.Join(got, "\n"))
		}
	}
	for _, tc := range []struct{ q, want string }{
		{"SELECT * FROM (SELECT * FROM (SELECT f1, f1 + 1 AS n FROM m119t) ss) a WHERE a.n = 2", "1|2"},
		{"SELECT * FROM (SELECT s.*, t.f1 AS g FROM m119t s, m119t t WHERE s.f1 = t.f1) q ORDER BY 1", "1|2|3|1;4|5|6|4"},
		{"SELECT a.* FROM (SELECT * FROM m119t) a(x, y) ORDER BY 1", "1|2|3;4|5|6"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, tc.q)), ";"); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.q, got, tc.want)
		}
	}
}
