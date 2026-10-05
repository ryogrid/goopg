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

// TestDerivedLateralPullup pins M0146-0028h against PG 18.3: a LATERAL simple
// subquery of the FROM list is pulled up (no outer join above it), its
// references to the items on its left becoming join quals of the parent,
// including references to an earlier pulled-up subquery's alias. A LATERAL
// body that is not simple (an aggregate) keeps the unpulled path.
func TestDerivedLateralPullup(t *testing.T) {
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
		{"select u.v, s.b from al_u u, lateral (select a, b from al_t where al_t.a = u.k and b < 200) s order by 2 limit 5",
			"v1|1;v2|2;v3|3;v4|4;v5|5"},
		{"select u.k, s.x from al_u u, lateral (select u.k * 10 + a as x from al_t where b < 3) s order by 1, 2 limit 4",
			"0|1;0|2;1|11;1|12"},
		// The body's own column wins over the left item's `a`.
		{"select count(*) from al_t t, lateral (select a from al_t where a = 3 and b < 100) s where s.a = t.a", "80"},
		{"select d.z, s.b from (select k as z from al_u where k < 3) d, lateral (select b from al_t where a = d.z and b < 120) s order by 1, 2",
			"0|50;0|100;1|1;1|51;1|101;2|2;2|52;2|102"},
		{"select u.k, s.c from al_u u, lateral (select count(*) c from al_t where a = u.k) s where u.k < 3 order by 1",
			"0|40;1|40;2|40"},
		{"select count(*) from al_u u where exists (select 1 from al_t t, lateral (select b from al_t t2 where t2.a = t.a and t2.a = u.k and t2.b < 60) s)",
			"50"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.query)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want PG's %q", c.query, got, c.rows)
		}
	}
}
