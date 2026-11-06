package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0148-0001g: pg_get_viewdef prints the view as ruleutils.c deparses its
// query (PRETTYFLAG_INDENT, wrap column 0): upper-case keywords, one target
// per line after the first, columns qualified when the range table holds
// more than one entry, VALUES constants labelled with their column's
// resolved type, a function RTE's alias and column-alias list, and a VALUES
// view whose columns were renamed printed as the SELECT over "*VALUES*".
// The expected strings follow aggregates.out / rules.out byte for byte.
func TestPgGetViewdefDeparse(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create view vd_values as values(1,2)",
		"create view vd_renamed(x) as values(1,2)",
		"create view vd_agg as select count(distinct a), max(b) from (values (1,3,'foo'),(0,null,null)) v(a,b,c), generate_series(1,3) i",
		"create view vd_single as select max(a order by b + 1) from (values (1,3),(2,4)) v(a,b)",
		"create table vd_t (ten int, hundred int)",
		"create view vd_where as select ten, count(*) from vd_t where hundred > 5 group by ten order by ten",
		"create table vd_os (ten int, thousand int, hundred int, string4 name)",
		"create view vd_ordset as select ten, percentile_disc(0.5) within group (order by thousand) as p50, " +
			"percentile_disc(0.5) within group (order by thousand) filter (where hundred=1) as px, " +
			"rank(5,'AZZZZ',50) within group (order by hundred, string4 desc, hundred) from vd_os group by ten order by ten",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	get := func(q string) string { return strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n") }
	for _, tc := range []struct{ view, want string }{
		{"vd_values", " VALUES (1,2);"},
		{"vd_renamed", " SELECT column1 AS x,\n    column2\n   FROM (VALUES (1,2)) \"*VALUES*\";"},
		{"vd_agg", " SELECT count(DISTINCT v.a) AS count,\n    max(v.b) AS max\n   FROM ( VALUES (1,3,'foo'::text), (0,NULL::integer,NULL::text)) v(a, b, c),\n    generate_series(1, 3) i(i);"},
		{"vd_single", " SELECT max(a ORDER BY (b + 1)) AS max\n   FROM ( VALUES (1,3), (2,4)) v(a, b);"},
		{"vd_where", " SELECT ten,\n    count(*) AS count\n   FROM vd_t\n  WHERE (hundred > 5)\n  GROUP BY ten\n  ORDER BY ten;"},
		// aggregates.out's aggordview1: ordered-set direct arguments print as
		// coerced (float8 for percentile_disc, the ORDER BY column's type for
		// the hypothetical-set rank).
		{"vd_ordset", " SELECT ten,\n    percentile_disc((0.5)::double precision) WITHIN GROUP (ORDER BY thousand) AS p50,\n" +
			"    percentile_disc((0.5)::double precision) WITHIN GROUP (ORDER BY thousand) FILTER (WHERE (hundred = 1)) AS px,\n" +
			"    rank(5, 'AZZZZ'::name, 50) WITHIN GROUP (ORDER BY hundred, string4 DESC, hundred) AS rank\n" +
			"   FROM vd_os\n  GROUP BY ten\n  ORDER BY ten;"},
	} {
		if got := get("select pg_get_viewdef('" + tc.view + "')"); got != tc.want {
			t.Errorf("%s:\n got: %q\nwant: %q (PG 18.3)", tc.view, got, tc.want)
		}
	}
	// pretty mode (psql's \d+) elides operator parentheses by precedence,
	// which the deparser does not model yet: it falls back to the stored
	// text rather than print the non-pretty parentheses.
	if got := get("select pg_get_viewdef('vd_where', true)"); strings.Contains(got, "(hundred > 5)") {
		t.Errorf("pretty mode must not print non-pretty parentheses: %q", got)
	}
}

// generate_relation_name qualifies a relation RelationIsVisible rejects:
// outside the search path (pg_dump empties search_path), or shadowed
// by a same-named relation in an earlier path schema. Expected text from a
// PG 18.3 scratch cluster.
func TestPgGetViewdefQualifiesInvisibleRelations(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table vq_foo (id int, name text, qty int)",
		"create view vq_view as select id, name from vq_foo where qty > 0",
		"create schema vq_s",
		"create table vq_s.t (a int)",
		"create view vq_vs as select a from vq_s.t",
		"create table vq_s.vq_foo (z int)",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	get := func(q string) string { return strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n") }
	check := func(view, want string) {
		t.Helper()
		if got := get("select pg_get_viewdef('" + view + "')"); got != want {
			t.Errorf("%s:\n got: %q\nwant: %q (PG 18.3)", view, got, want)
		}
	}
	check("vq_view", " SELECT id,\n    name\n   FROM vq_foo\n  WHERE (qty > 0);")
	check("vq_vs", " SELECT a\n   FROM vq_s.t;")
	searchPath := func(v string) {
		ctx.GetSetting = func(name string) (string, bool) {
			if name == "search_path" {
				return v, true
			}
			return "", false
		}
	}
	searchPath("vq_s, public")
	check("public.vq_view", " SELECT id,\n    name\n   FROM public.vq_foo\n  WHERE (qty > 0);")
	check("vq_vs", " SELECT a\n   FROM t;")
	searchPath("''")
	check("public.vq_view", " SELECT id,\n    name\n   FROM public.vq_foo\n  WHERE (qty > 0);")
}
