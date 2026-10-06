package executor

import (
	"strings"
	"testing"
)

// TestPlpgsqlVariablesBindInsideSublinksAndWith pins M0146-0072 against PG
// 18.3. A PL/pgSQL expression whose sublink reads a variable — a scalar
// subquery, EXISTS, a WITH clause or its CTE body — failed `column "i" does
// not exist` (the SQL fallback planned the tree with no variable binding),
// and a WITH-led `SELECT … INTO` failed `syntax error at or near "NULL"`
// (the INTO target was not recognised and got substituted). PG binds the
// variables as parameters. Every want is PG's output for the same functions.
func TestPlpgsqlVariablesBindInsideSublinksAndWith(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		`create or replace function m72a(i int) returns text language plpgsql as $$
declare v text;
begin
  v := (WITH x AS (SELECT g FROM generate_series(10,11) g) SELECT (sum(x.g) * i)::text FROM x);
  return v;
end $$`,
		`create or replace function m72b() returns text language plpgsql as $$
declare r record; acc text := '';
begin
  for r in select g from generate_series(1,2) g loop
    acc := acc || (WITH x AS (SELECT r.g * 10 AS k) SELECT k::text FROM x) || ',';
  end loop;
  return acc;
end $$`,
		`create or replace function m72c(i int) returns int language plpgsql as $$
declare n int;
begin
  WITH x AS (SELECT g FROM generate_series(1,5) g WHERE g <= i) SELECT count(*) INTO n FROM x;
  return n;
end $$`,
		`create or replace function m72d(i int) returns int language plpgsql as $$
declare n int;
begin
  SELECT count(*) INTO n FROM (WITH x AS (SELECT g FROM generate_series(1,5) g) SELECT g FROM x WHERE g > i) s;
  return n;
end $$`,
		`create or replace function m72e(i int) returns setof int language plpgsql as $$
begin
  RETURN QUERY WITH x AS (SELECT g FROM generate_series(1,4) g) SELECT g * i FROM x WHERE g % 2 = 0;
end $$`,
		`create or replace function m72f(i int) returns int language plpgsql as $$
declare n int := 0;
begin
  if (WITH x AS (SELECT i + 1 AS k) SELECT k FROM x) = 6 then n := 1; end if;
  return n;
end $$`,
		`create or replace function m72g(i int) returns text language plpgsql as $$
declare v text;
begin
  v := (SELECT (sum(g) * i)::text FROM generate_series(10,11) g);
  return v;
end $$`,
		`create or replace function m72h(i int) returns int language plpgsql as $$
declare n int;
begin
  SELECT count(*) INTO n FROM generate_series(1,5) g WHERE g <= i;
  return n;
end $$`,
		`create or replace function m72j(i int) returns bool language plpgsql as $$
begin
  return exists (select 1 from generate_series(1,3) g where g = i);
end $$`,
	} {
		runSQL(t, ctx, q)
	}
	const q = "SELECT m72a(2), m72b(), m72c(3), m72d(2), (SELECT string_agg(x::text, ',') FROM m72e(3) x), m72f(5), m72g(2), m72h(3), m72j(2), m72j(9)"
	rows, err := runQueryWithErr(ctx, q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if got, want := strings.Join(renderRows(rows), ";"), "42|10,20,|3|3|6,12|1|42|3|t|f"; got != want {
		t.Errorf("got  %s\nwant PG's %s", got, want)
	}
}
