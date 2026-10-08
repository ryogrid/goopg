package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0146-0116: a pulled-up subquery's WHERE is distributed before the parent's
// (deconstruct_recurse walks a FromExpr's items before its own quals). The
// order shows in a scan's Filter and in the equivalence class's member order,
// which picks the members a join clause equates
// (generate_join_implied_equalities_normal). Before, the parent's quals came
// first: `Filter: ((b < 2) AND (a > 1))` and `m116a.a = m116b.a` on top.
func TestPulledSubqueryQualsPrecedeParentQuals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE m116f (a int, b int)",
		"CREATE TABLE m116a (a int, x int)",
		"CREATE TABLE m116b (a int, y int)",
		"CREATE TABLE m116c (a int, z int)",
		"INSERT INTO m116a SELECT i, i FROM generate_series(1, 2000) i",
		"INSERT INTO m116b SELECT i, i FROM generate_series(1, 2000) i",
		"INSERT INTO m116c SELECT i % 50, i FROM generate_series(1, 100) i",
		"ANALYZE m116a", "ANALYZE m116b", "ANALYZE m116c",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"SELECT * FROM (SELECT * FROM m116f WHERE a > 1) s WHERE b < 2",
			[]string{"Seq Scan on m116f", "  Filter: ((a > 1) AND (b < 2))"}},
		{"SELECT * FROM (SELECT m116a.a, m116b.a AS b FROM m116a, m116b WHERE m116a.a = m116b.a) y, m116c WHERE y.b = m116c.a",
			[]string{
				"Hash Join",
				"  Hash Cond: (m116b.a = m116a.a)",
				"  ->  Seq Scan on m116b",
				"  ->  Hash",
				"        ->  Hash Join",
				"              Hash Cond: (m116a.a = m116c.a)",
				"              ->  Seq Scan on m116a",
				"              ->  Hash",
				"                    ->  Seq Scan on m116c",
			}},
	} {
		got := renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q, ps))
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\nwant:\n%s\ngot:\n%s", tc.q, strings.Join(tc.want, "\n"), strings.Join(got, "\n"))
		}
	}
}
