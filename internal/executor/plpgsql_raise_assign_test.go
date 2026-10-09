package executor

import (
	"strings"
	"testing"
)

// TestPlpgsqlRaiseArgsAndAssignmentCoercion pins M0146-0080 against PG 18.3.
//
// RAISE: each parameter was lowered and evaluated by the interpreter, so a
// parameter holding a sublink could not be lowered, and any evaluation
// error, printed as an empty string (`b= c= d=`). Parameters now evaluate
// like any PL/pgSQL expression, and an error aborts the RAISE.
//
// Assignment: a DECLARE default, `:=` or RETURN rejected a value of another
// type (`v text := (SELECT count(*) …)` failed "expects type text but got
// integer"). PG's exec_cast_value falls back to I/O conversion, and a string
// goes through the target type's input function.
//
// The DO executor's DECLARE coerced nothing (`b bool := 'true'` printed
// true), and numeric -> int truncated instead of rounding. pg_trigger_depth()
// was unimplemented, and `new.*` was rejected as an expression; both were
// hidden by the swallowed RAISE errors. Every want is PG's output.
func TestPlpgsqlRaiseArgsAndAssignmentCoercion(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE r80 (a int)")
	runSQL(t, ctx, "INSERT INTO r80 VALUES (1), (2)")
	runSQL(t, ctx, `CREATE FUNCTION f80() RETURNS text LANGUAGE plpgsql AS $$ BEGIN RETURN (SELECT max(a) FROM r80); END $$`)
	// pg_trigger_depth() and `new.*` as RAISE arguments: once RAISE stopped
	// swallowing errors, both had to work (PG prints depth 1 / 2 and the row).
	for _, q := range []string{
		"CREATE TABLE depth_a (id int)",
		"CREATE TABLE depth_b (id int)",
		`CREATE FUNCTION depth_a_tf() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			RAISE NOTICE '%: depth = %', tg_name, pg_trigger_depth();
			INSERT INTO depth_b VALUES (new.id); RETURN new; END $$`,
		"CREATE TRIGGER depth_a_tr BEFORE INSERT ON depth_a FOR EACH ROW EXECUTE PROCEDURE depth_a_tf()",
		`CREATE FUNCTION depth_b_tf() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			RAISE NOTICE '%: depth = % old=% new=%', tg_name, pg_trigger_depth(), old.*::text, new.*::text;
			RETURN new; END $$`,
		"CREATE TRIGGER depth_b_tr BEFORE INSERT ON depth_b FOR EACH ROW EXECUTE PROCEDURE depth_b_tf()",
	} {
		runSQL(t, ctx, q)
	}
	_ = ctx.TakeNotices()
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT pg_trigger_depth()")), ";"); got != "0" {
		t.Errorf("pg_trigger_depth() outside a trigger = %s, want 0", got)
	}
	runSQL(t, ctx, "INSERT INTO depth_a VALUES (1)")
	if got, want := strings.Join(ctx.TakeNotices(), "|"),
		"depth_a_tr: depth = 1|depth_b_tr: depth = 2 old=<NULL> new=(1)"; got != want {
		t.Errorf("trigger depth notices:\ngot  %s\nwant PG's %s", got, want)
	}
	for _, c := range []struct{ sql, want string }{
		{`DO $$ BEGIN RAISE NOTICE 'a=% b=% c=% d=%', CASE WHEN false THEN 'x' END, (SELECT count(*) FROM r80),
			(SELECT 5), 1 + (SELECT max(a) FROM r80); END $$`, "a=<NULL> b=2 c=5 d=3"},
		{`DO $$ DECLARE v text; BEGIN v := (SELECT count(*) FROM r80); RAISE NOTICE 'v=%', v; END $$`, "v=2"},
		{`DO $$ DECLARE n int := '42'; d date := '2020-01-02'; b bool := 'true'; BEGIN
			RAISE NOTICE 'n=% d=% b=%', n + 1, d, b; END $$`, "n=43 d=2020-01-02 b=t"},
		{`DO $$ DECLARE n int; BEGIN n := 2.5; RAISE NOTICE 'n=%', n; END $$`, "n=3"},
		{`DO $$ DECLARE t text := 7; BEGIN RAISE NOTICE 't=% e=%', t || 'x', EXISTS (SELECT 1 FROM r80 WHERE a = 2); END $$`, "t=7x e=t"},
		{`DO $$ DECLARE iv interval := '1 day'; nu numeric := '2.50'; BEGIN RAISE NOTICE '% %', iv, nu; END $$`, "1 day 2.50"},
		// `int[]` keeps its array-ness through the DECLARE parser and the
		// frame, so `||` appends: PG {1,2,3}, goopg printed {1,2}3.
		{`DO $$ DECLARE a int[] := array[1,2]; BEGIN a := a || 3; RAISE NOTICE 'a = % b = %', a, a || 4; END $$`, "a = {1,2,3} b = {1,2,3,4}"},
	} {
		runSQL(t, ctx, c.sql)
		if got := strings.Join(ctx.TakeNotices(), "|"); got != c.want {
			t.Errorf("%s\ngot  %q\nwant PG's %q", c.sql, got, c.want)
		}
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT f80()")), ";"); got != "2" {
		t.Errorf("f80() = %s, want 2", got)
	}
	// row(old.*) is ROW(old.f1, old.f2, old.f3), compared element-wise: a
	// NULL field makes `=` NULL, so the trigger takes the ELSE branch (PG
	// prints "changed" for both rows when f3 stays NULL).
	for _, q := range []string{
		"CREATE TABLE trg80 (f1 int, f2 text, f3 text)",
		"INSERT INTO trg80 VALUES (1, 'foo', NULL), (2, 'bar', 'baz')",
		`CREATE FUNCTION trg80_tf() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF row(old.*) = row(new.*) THEN RAISE NOTICE 'row % not changed', new.f1;
			ELSE RAISE NOTICE 'row % changed', new.f1; END IF;
			RETURN new; END $$`,
		"CREATE TRIGGER trg80_tr BEFORE UPDATE ON trg80 FOR EACH ROW EXECUTE PROCEDURE trg80_tf()",
	} {
		runSQL(t, ctx, q)
	}
	_ = ctx.TakeNotices()
	for _, c := range []struct{ sql, want string }{
		{"UPDATE trg80 SET f3 = 'baz' WHERE f1 = 2", "row 2 not changed"},
		{"UPDATE trg80 SET f3 = NULL", "row 1 changed|row 2 changed"},
		{"UPDATE trg80 SET f3 = NULL", "row 1 changed|row 2 changed"},
	} {
		runSQL(t, ctx, c.sql)
		if got := strings.Join(ctx.TakeNotices(), "|"); got != c.want {
			t.Errorf("%s\ngot  %q\nwant PG's %q", c.sql, got, c.want)
		}
	}
	// The same rule in plain SQL: ROW(...) calls used to compare as text.
	if got := strings.Join(renderRows(runSQL(t, ctx, `SELECT row(1, null::int) = row(1, null::int),
		row(1, null::int) = row(2, null::int), row(1, null::int) <> row(2, null::int), row(1, 2) <= row(1, 2)`)), ";"); got != "NULL|f|t|t" {
		t.Errorf("ROW() comparisons = %s, want PG's NULL|f|t|t", got)
	}
	for _, c := range []struct{ sql, err string }{
		{`DO $$ DECLARE n int; BEGIN n := 'abc'; END $$`, `invalid input syntax for type integer: "abc"`},
		{`DO $$ BEGIN RAISE NOTICE 'x=%', 1/0; END $$`, "division by zero"},
		{`DO $$ BEGIN RAISE EXCEPTION 'cnt %', (SELECT count(*) FROM r80); END $$`, "cnt 2"},
	} {
		if _, err := runSQLCtxErr(t, ctx, c.sql); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err = %v, want %q", c.sql, err, c.err)
		}
	}
}
