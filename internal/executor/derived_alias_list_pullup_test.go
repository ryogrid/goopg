package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestDerivedAliasListPullup pins M0146-0028g against PG 18.3. A FROM
// subquery with a column-alias list (`AS x(c, d)`) is still a simple
// subquery: pull_up_simple_subquery flattens it, the list renaming its
// leading outputs (addRangeTableEntryForSubquery). A list SHORTER than the
// output renames only the leading columns — goopg's analyzer required an
// exact count — and a longer one is PG's 42P10 error, raised with no
// position.
func TestDerivedAliasListPullup(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE al_t (a int, b int)",
		"CREATE TABLE al_u (k int PRIMARY KEY, v text)",
		"INSERT INTO al_t SELECT g % 50, g FROM generate_series(1, 2000) g",
		"INSERT INTO al_u SELECT g, 'v' || g FROM generate_series(0, 60) g",
		"ANALYZE al_t",
		"ANALYZE al_u",
	} {
		runSQL(t, ctx, q)
	}
	for _, c := range []struct{ query, rows string }{
		{"select x.c, d from (select a, b + 1 from al_t) x(c, d) where c > 45 order by d limit 3", "46|47;47|48;48|49"},
		{"select * from (select a, b from al_t where b < 4) x(c) order by 1, 2", "1|1;2|2;3|3"},
		{"select u.v, s.z from al_u u, (select a from al_t where b < 5) s(z) where u.k = s.z order by 2", "v1|1;v2|2;v3|3;v4|4"},
		{"select x from (select a, b from al_t where b = 7) x(c, d)", "(7,7)"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.query)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want PG's %q", c.query, got, c.rows)
		}
	}
	// Pulled up: the join reads al_t directly, no Subquery Scan.
	plan := strings.Join(renderRows(runSQL(t, ctx,
		"EXPLAIN (COSTS OFF) select u.v, s.z from al_u u, (select a from al_t where b < 5) s(z) where u.k = s.z")), "\n")
	if strings.Contains(plan, "Subquery Scan") || !strings.Contains(plan, "Seq Scan on al_t") {
		t.Errorf("alias-listed subquery not pulled up:\n%s", plan)
	}
	stmts, err := parser.Parse("select * from (select a, b from al_t) x(c, d, e)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := optimizer.Plan(stmts[0], ctx.Catalog); err == nil ||
		!strings.Contains(err.Error(), `table "x" has 2 columns available but 3 columns specified`) {
		t.Errorf("too many aliases: got %v, want PG's 42P10 error", err)
	}
}
