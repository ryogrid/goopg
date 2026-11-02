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
