package executor

import (
	"strings"
	"testing"
)

// TestCTEMaterialisationIsStatementScoped pins M0146-0059 against PG 18.3.
// CTE materialisations are keyed by declaration, and a routine body's
// statements run on one Context, so a second statement declaring `x` at
// the same position replayed the first statement's rows (a 500-value list
// where PG returns ten counts of 100). PG starts every statement with fresh
// CTE tuplestores. The FOR loop keeps its query's CTE open while body
// statements declare their own `x`. Each want is PG's answer.
func TestCTEMaterialisationIsStatementScoped(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE t59 (a int, b int)",
		"INSERT INTO t59 SELECT g, g % 10 FROM generate_series(1, 1000) g",
		`CREATE FUNCTION f59() RETURNS text LANGUAGE plpgsql AS $$
DECLARE r1 text; r2 text;
BEGIN
  r1 := (WITH x AS (SELECT a, b FROM t59 WHERE b < 5) SELECT count(*)::text FROM x, x x2 WHERE x.a = x2.a);
  r2 := (WITH x AS (SELECT b, count(*) c FROM t59 GROUP BY b) SELECT string_agg(x2.c::text, ',' ORDER BY x.b) FROM x, x x2 WHERE x.b = x2.b);
  RETURN r1 || '/' || r2;
END $$`,
		`CREATE FUNCTION f59b() RETURNS text LANGUAGE plpgsql AS $$
DECLARE i int; s text := ''; v text;
BEGIN
  FOR i IN WITH x AS (SELECT g FROM generate_series(1,3) g) SELECT x.g FROM x, x x2 WHERE x.g = x2.g ORDER BY 1 LOOP
    v := (WITH x AS (SELECT g FROM generate_series(10,11) g) SELECT sum(x.g)::text FROM x, x x2 WHERE x.g = x2.g);
    s := s || i || ':' || v || ';';
  END LOOP;
  RETURN s;
END $$`,
	} {
		runSQL(t, ctx, q)
	}
	for _, c := range []struct{ query, want string }{
		{"SELECT f59()", "500/100,100,100,100,100,100,100,100,100,100"},
		{"SELECT f59b()", "1:21;2:21;3:21;"},
		// Two statements on one Context, as a routine body runs them.
		{"WITH x AS (SELECT a, b FROM t59 WHERE b < 5) SELECT count(*)::text FROM x, x x2 WHERE x.a = x2.a", "500"},
		{"WITH x AS (SELECT b, count(*) c FROM t59 GROUP BY b) SELECT string_agg(x2.c::text, ',' ORDER BY x.b) FROM x, x x2 WHERE x.b = x2.b",
			"100,100,100,100,100,100,100,100,100,100"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.query)), ";"); got != c.want {
			t.Errorf("%s: got %q, want PG's %q", c.query, got, c.want)
		}
	}
}
