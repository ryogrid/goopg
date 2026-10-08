package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0146-0115: PG 18's remove_useless_self_joins (regress join's "test that
// semi- or inner self-joins on a unique column are removed"). An inner
// self-join on every column of a unique index keeps the later reference; the
// join equality becomes `expr IS NOT NULL`.
func TestSelfJoinOnUniqueKeyIsRemoved(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE m115sj (a int UNIQUE, b int, c int UNIQUE)",
		"INSERT INTO m115sj VALUES (1, NULL, 2), (NULL, 2, NULL), (2, 1, 1)",
		"CREATE TABLE m115z (q1 int, q2 int)",
		"INSERT INTO m115z VALUES (1, 10), (2, 20), (5, 50)",
		"ANALYZE m115sj", "ANALYZE m115z",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin, ps.EnableMergeJoin = false, false
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"SELECT p.* FROM m115sj p, m115sj q WHERE q.a = p.a AND q.b = q.a - 1",
			[]string{"Seq Scan on m115sj q", "  Filter: ((a IS NOT NULL) AND (b = (a - 1)))"}},
		{"SELECT x.b, y.c FROM m115sj x, m115sj y WHERE x.c = y.c AND x.b IS NULL",
			[]string{"Seq Scan on m115sj y", "  Filter: ((b IS NULL) AND (c IS NOT NULL))"}},
		{"SELECT * FROM m115sj t1 JOIN m115sj t2 ON t1.a = t2.a AND t1.b = t2.b JOIN m115sj t3 ON t2.a = t3.a AND t2.b + 1 = t3.b + 1",
			[]string{"Seq Scan on m115sj t3", "  Filter: ((a IS NOT NULL) AND (b IS NOT NULL) AND ((b + 1) IS NOT NULL))"}},
	} {
		got := renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q, ps))
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\nwant:\n%s\ngot:\n%s", tc.q, strings.Join(tc.want, "\n"), strings.Join(got, "\n"))
		}
	}
	// Not removable: two different unique columns, a non-unique column.
	for _, q := range []string{
		"SELECT * FROM m115sj t1, m115sj t2 WHERE t1.a = t2.c",
		"SELECT * FROM m115sj t1, m115sj t2 WHERE t1.b = t2.b",
	} {
		if plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n"); !strings.Contains(plan, "Nested Loop") {
			t.Errorf("%s: the join must stay:\n%s", q, plan)
		}
	}
	// Results: every output column of both references, star-expanded.
	for _, tc := range []struct{ q, want string }{
		{"SELECT * FROM m115sj x, m115sj y WHERE x.a = y.a ORDER BY 1", "1|NULL|2|1|NULL|2;2|1|1|2|1|1"},
		{"SELECT * FROM m115sj x JOIN m115sj y ON x.a = y.a LEFT JOIN m115z z ON x.a = z.q1 ORDER BY 1",
			"1|NULL|2|1|NULL|2|1|10;2|1|1|2|1|1|2|20"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, tc.q)), ";"); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.q, got, tc.want)
		}
	}
}
