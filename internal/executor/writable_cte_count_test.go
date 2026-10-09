package executor

import (
	"strings"
	"testing"
)

// TestWritableCTEStatementCountsTopLevelRows pins M0146-0081 against PG 18.3.
//
// A statement led by data-modifying WITH queries is planned as a
// CTEDMLPrefix over the top-level statement. The prefix operator did not
// report RowsAffected, so the wire tag fell through to `SELECT 0` (PG: the
// top-level statement's tag, `INSERT 0 1`). PL/pgSQL's FOUND counted output
// rows, so every DML statement without RETURNING, plain or WITH-led, left
// FOUND false. PG sets FOUND from SPI_processed, the same es_processed count
// as the command tag, and only the top-level ModifyTable advances it.
func TestWritableCTEStatementCountsTopLevelRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE t81 (a int)")
	runSQL(t, ctx, "INSERT INTO t81 VALUES (1), (2), (3), (4), (5), (6), (7)")

	for _, c := range []struct {
		sql  string
		want int64
	}{
		// The CTE deletes one row and the INSERT writes one: 1, not 2.
		{"WITH d AS (DELETE FROM t81 WHERE a = 6 RETURNING a) INSERT INTO t81 SELECT a + 100 FROM d", 1},
		// The CTE deletes two rows; the UPDATE touches one.
		{"WITH d AS (DELETE FROM t81 WHERE a IN (4, 5) RETURNING a) UPDATE t81 SET a = a + 1000 WHERE a IN (SELECT a - 3 FROM d WHERE a = 5)", 1},
		// The CTE writes nothing; the DELETE removes one row.
		{"WITH d AS (DELETE FROM t81 WHERE a = -1 RETURNING a) DELETE FROM t81 WHERE a = 7", 1},
		// The CTE deletes one row; the UPDATE matches none.
		{"WITH d AS (DELETE FROM t81 WHERE a = 3 RETURNING a) UPDATE t81 SET a = a WHERE a = -1", 0},
	} {
		ctx.CommandCounterIncrement()
		ctx.CmdID = ctx.GetCurrentCommandId(true)
		op, err := buildStatementScoped(planOne(t, c.sql, ctx.Catalog))
		if err != nil {
			t.Fatalf("Build(%q): %v", c.sql, err)
		}
		if _, err := Run(op, ctx); err != nil {
			t.Fatalf("Run(%q): %v", c.sql, err)
		}
		rc, ok := op.(RowCounter)
		if !ok {
			t.Fatalf("%s: root operator %T reports no row count", c.sql, op)
		}
		if got := rc.RowsAffected(); got != c.want {
			t.Errorf("%s\nrows affected = %d, want PG's %d", c.sql, got, c.want)
		}
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT a FROM t81 ORDER BY a")), ";"); got != "1;106;1002" {
		t.Errorf("t81 after the statements = %s, want PG's 1;106;1002", got)
	}

	_ = ctx.TakeNotices()
	runSQL(t, ctx, `DO $$ BEGIN
		INSERT INTO t81 VALUES (50); RAISE NOTICE 'ins %', found;
		UPDATE t81 SET a = a WHERE a = -5; RAISE NOTICE 'upd0 %', found;
		DELETE FROM t81 WHERE a = 50; RAISE NOTICE 'del %', found;
		WITH d AS (DELETE FROM t81 WHERE a = 1 RETURNING a) INSERT INTO t81 SELECT a + 400 FROM d; RAISE NOTICE 'cte %', found;
		WITH d AS (DELETE FROM t81 WHERE a = 2 RETURNING a) UPDATE t81 SET a = a WHERE a = -5; RAISE NOTICE 'cte0 %', found;
		END $$`)
	if got, want := strings.Join(ctx.TakeNotices(), "|"), "ins t|upd0 f|del t|cte t|cte0 f"; got != want {
		t.Errorf("FOUND after DML:\ngot  %s\nwant PG's %s", got, want)
	}

	// GET DIAGNOSTICS was not a PL/pgSQL statement at all (`unsupported
	// statement (got get)`). ROW_COUNT is the same processed count as FOUND
	// and the tag; GET STACKED reads the handled error. Every want is PG's.
	runSQL(t, ctx, "CREATE TABLE g81 (a int PRIMARY KEY)")
	runSQL(t, ctx, "INSERT INTO g81 VALUES (1), (2), (3), (4), (5)")
	runSQL(t, ctx, `CREATE FUNCTION f81g() RETURNS text LANGUAGE plpgsql AS $$
	DECLARE n int; m bigint; t text; st text; msg text;
	BEGIN
		UPDATE g81 SET a = a WHERE a > 2; GET DIAGNOSTICS n = ROW_COUNT; t := 'upd=' || n;
		PERFORM * FROM g81; GET DIAGNOSTICS n := ROW_COUNT; t := t || ' perf=' || n;
		SELECT a INTO n FROM g81 WHERE a = 1; GET CURRENT DIAGNOSTICS m = ROW_COUNT;
		t := t || ' into=' || m || CASE WHEN found THEN 'Y' ELSE 'N' END;
		SELECT a INTO n FROM g81 WHERE a = -1; t := t || CASE WHEN found THEN 'Y' ELSE 'N' END;
		EXECUTE 'DELETE FROM g81 WHERE a > 3'; GET DIAGNOSTICS n = ROW_COUNT; t := t || ' exec=' || n;
		EXECUTE 'SELECT a FROM g81'; GET DIAGNOSTICS n = ROW_COUNT; t := t || ' execsel=' || n;
		WITH d AS (DELETE FROM g81 WHERE a = 3 RETURNING a) INSERT INTO g81 SELECT a + 10 FROM d;
		GET DIAGNOSTICS n = ROW_COUNT; t := t || ' cte=' || n;
		BEGIN
			INSERT INTO g81 VALUES (1);
		EXCEPTION WHEN unique_violation THEN
			GET STACKED DIAGNOSTICS st = RETURNED_SQLSTATE, msg = MESSAGE_TEXT;
			t := t || ' st=' || st || ' msg=' || msg;
		END;
		RETURN t;
	END $$`)
	if got, want := strings.Join(renderRows(runSQL(t, ctx, "SELECT f81g()")), ";"),
		`upd=3 perf=5 into=1YN exec=2 execsel=3 cte=1 st=23505 msg=duplicate key value violates unique constraint "g81_pkey"`; got != want {
		t.Errorf("GET DIAGNOSTICS:\ngot  %s\nwant PG's %s", got, want)
	}
	for _, c := range []struct{ sql, err string }{
		{`DO $$ DECLARE x text; BEGIN GET STACKED DIAGNOSTICS x = MESSAGE_TEXT; END $$`,
			"GET STACKED DIAGNOSTICS cannot be used outside an exception handler"},
		{`DO $$ DECLARE x int; BEGIN GET STACKED DIAGNOSTICS x = ROW_COUNT; END $$`,
			"diagnostics item ROW_COUNT is not allowed in GET STACKED DIAGNOSTICS"},
		{`DO $$ DECLARE x int; BEGIN GET DIAGNOSTICS x = MESSAGE_TEXT; END $$`,
			"diagnostics item MESSAGE_TEXT is not allowed in GET CURRENT DIAGNOSTICS"},
		{`DO $$ DECLARE x int; BEGIN GET DIAGNOSTICS x = bogus; END $$`,
			"unrecognized GET DIAGNOSTICS item"},
	} {
		if _, err := runSQLCtxErr(t, ctx, c.sql); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err = %v, want %q", c.sql, err, c.err)
		}
	}
}
