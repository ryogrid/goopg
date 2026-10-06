package executor

import (
	"sort"
	"strings"
	"testing"
)

// TestDMLTriggersFireInPGOrder pins M0146-0076 against PG 18.3. Before the fix:
//   - INSERT never fired AFTER STATEMENT triggers;
//   - UPDATE, DELETE and MERGE fired no statement-level triggers at all;
//   - ON CONFLICT fired none for either of its commands;
//   - MERGE fired no AFTER ROW triggers;
//   - every AFTER ROW trigger ran inline, right after its own row was
//     written.
//
// PG instead:
//   - fires BEFORE STATEMENT once per query level for each relation and
//     command;
//   - queues the AFTER ROW and AFTER STATEMENT events, and fires them in
//     queue order once the query ends (AfterTriggerEndQuery);
//   - fires statement triggers even when no row is touched.
//
// A trigger body's own statement opens a nested level. Here zlog's INSERT
// into tt_log fires tt_log's triggers before the next tt event. A statement
// that errors fires none of its queued events.
//
// Every want is PG's NOTICE sequence for the same script. In the
// writable-CTE statement goopg fires the same events in a different order:
// its CTE runs to completion before the outer INSERT starts, as recorded in
// the deferral ledger. That statement is therefore compared as a multiset.
func TestDMLTriggersFireInPGOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE tt (a int PRIMARY KEY, b text)",
		"CREATE TABLE tt_log (ev text)",
		`CREATE FUNCTION tt_trg() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE n int;
		BEGIN
			SELECT count(*) INTO n FROM tt;
			IF tg_level = 'ROW' THEN
				IF tg_op = 'DELETE' THEN
					RAISE NOTICE '% % % old=% n=%', tg_name, tg_when, tg_op, old.a, n;
				ELSIF tg_op = 'UPDATE' THEN
					RAISE NOTICE '% % % old=% new=% n=%', tg_name, tg_when, tg_op, old.b, new.b, n;
				ELSE
					RAISE NOTICE '% % % new=% n=%', tg_name, tg_when, tg_op, new.a, n;
					IF new.a = 99 THEN RAISE EXCEPTION 'boom'; END IF;
				END IF;
			ELSE
				RAISE NOTICE '% % % n=%', tg_name, tg_when, tg_op, n;
			END IF;
			IF tg_when = 'BEFORE' AND tg_level = 'ROW' THEN
				IF tg_op = 'DELETE' THEN RETURN old; END IF;
				RETURN new;
			END IF;
			RETURN NULL;
		END $$`,
		`CREATE FUNCTION tt_log_ins() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO tt_log VALUES (tg_op || ' ' || new.a); RETURN NULL; END $$`,
		`CREATE FUNCTION tt_log_trg() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE NOTICE 'log % %', tg_level, tg_op; RETURN NULL; END $$`,
		"CREATE TRIGGER bs BEFORE INSERT OR UPDATE OR DELETE ON tt FOR EACH STATEMENT EXECUTE FUNCTION tt_trg()",
		"CREATE TRIGGER as_ AFTER INSERT OR UPDATE OR DELETE ON tt FOR EACH STATEMENT EXECUTE FUNCTION tt_trg()",
		"CREATE TRIGGER br BEFORE INSERT OR UPDATE OR DELETE ON tt FOR EACH ROW EXECUTE FUNCTION tt_trg()",
		"CREATE TRIGGER ar AFTER INSERT OR UPDATE OR DELETE ON tt FOR EACH ROW EXECUTE FUNCTION tt_trg()",
		"CREATE TRIGGER zlog AFTER INSERT ON tt FOR EACH ROW EXECUTE FUNCTION tt_log_ins()",
		"CREATE TRIGGER lr AFTER INSERT ON tt_log FOR EACH ROW EXECUTE FUNCTION tt_log_trg()",
		"CREATE TRIGGER ls AFTER INSERT ON tt_log FOR EACH STATEMENT EXECUTE FUNCTION tt_log_trg()",
	} {
		runSQL(t, ctx, q)
	}
	_ = ctx.TakeNotices()

	const logged = "log ROW INSERT|log STATEMENT INSERT"
	cases := []struct {
		sql      string
		want     string
		multiset bool
	}{
		{"INSERT INTO tt VALUES (1,'x'),(2,'y')",
			"bs BEFORE INSERT n=0|br BEFORE INSERT new=1 n=0|br BEFORE INSERT new=2 n=1|ar AFTER INSERT new=1 n=2|" + logged +
				"|ar AFTER INSERT new=2 n=2|" + logged + "|as_ AFTER INSERT n=2", false},
		{"INSERT INTO tt SELECT g, 'g' FROM generate_series(3,4) g WHERE false",
			"bs BEFORE INSERT n=2|as_ AFTER INSERT n=2", false},
		{"UPDATE tt SET b = b || '!' WHERE a <= 2",
			"bs BEFORE UPDATE n=2|br BEFORE UPDATE old=x new=x! n=2|br BEFORE UPDATE old=y new=y! n=2|" +
				"ar AFTER UPDATE old=x new=x! n=2|ar AFTER UPDATE old=y new=y! n=2|as_ AFTER UPDATE n=2", false},
		{"UPDATE tt SET b = 'q' WHERE a = 99",
			"bs BEFORE UPDATE n=2|as_ AFTER UPDATE n=2", false},
		{"DELETE FROM tt WHERE a = 2",
			"bs BEFORE DELETE n=2|br BEFORE DELETE old=2 n=2|ar AFTER DELETE old=2 n=1|as_ AFTER DELETE n=1", false},
		{"DELETE FROM tt WHERE a = 99",
			"bs BEFORE DELETE n=1|as_ AFTER DELETE n=1", false},
		{"INSERT INTO tt VALUES (5,'r'),(1,'u') ON CONFLICT (a) DO UPDATE SET b = excluded.b",
			"bs BEFORE INSERT n=1|bs BEFORE UPDATE n=1|br BEFORE INSERT new=5 n=1|br BEFORE INSERT new=1 n=2|" +
				"br BEFORE UPDATE old=x! new=u n=2|ar AFTER INSERT new=5 n=2|" + logged +
				"|ar AFTER UPDATE old=x! new=u n=2|as_ AFTER UPDATE n=2|as_ AFTER INSERT n=2", false},
		{"INSERT INTO tt VALUES (6,'v') ON CONFLICT DO NOTHING",
			"bs BEFORE INSERT n=2|br BEFORE INSERT new=6 n=2|ar AFTER INSERT new=6 n=3|" + logged + "|as_ AFTER INSERT n=3", false},
		{"WITH d AS (DELETE FROM tt WHERE a = 6 RETURNING a) INSERT INTO tt SELECT a + 100, 'c' FROM d",
			"bs BEFORE INSERT n=3|bs BEFORE DELETE n=3|br BEFORE DELETE old=6 n=3|br BEFORE INSERT new=106 n=2|" +
				"ar AFTER DELETE old=6 n=3|ar AFTER INSERT new=106 n=3|" + logged + "|as_ AFTER DELETE n=3|as_ AFTER INSERT n=3", true},
		{"MERGE INTO tt t USING (VALUES (1,'m'),(7,'n')) v(a,b) ON t.a = v.a " +
			"WHEN MATCHED THEN UPDATE SET b = v.b WHEN NOT MATCHED THEN INSERT VALUES (v.a, v.b)",
			"bs BEFORE INSERT n=3|bs BEFORE UPDATE n=3|br BEFORE UPDATE old=u new=m n=3|br BEFORE INSERT new=7 n=3|" +
				"ar AFTER UPDATE old=u new=m n=4|ar AFTER INSERT new=7 n=4|" + logged + "|as_ AFTER UPDATE n=4|as_ AFTER INSERT n=4", false},
	}
	for _, c := range cases {
		runSQL(t, ctx, c.sql)
		got, want := ctx.TakeNotices(), strings.Split(c.want, "|")
		if c.multiset {
			// The early CTE also changes what BEFORE STATEMENT INSERT sees:
			// in goopg the DELETE has already run, so n is 2 rather than 3.
			got, want = bsUncounted(got), bsUncounted(want)
			sort.Strings(got)
			sort.Strings(want)
		}
		if g, w := strings.Join(got, "|"), strings.Join(want, "|"); g != w {
			t.Errorf("%s notices:\ngot  %s\nwant %s", c.sql, g, w)
		}
	}

	// A statement that errors fires none of its queued AFTER events.
	if _, err := runSQLCtxErr(t, ctx, "INSERT INTO tt VALUES (50,'e'),(99,'e')"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("INSERT 99: err = %v, want boom", err)
	}
	if got, want := strings.Join(ctx.TakeNotices(), "|"),
		"bs BEFORE INSERT n=4|br BEFORE INSERT new=50 n=4|br BEFORE INSERT new=99 n=5"; got != want {
		t.Errorf("failed INSERT notices:\ngot  %s\nwant %s", got, want)
	}
	// The test runs in one transaction, so the failed statement's row 50
	// is not rolled back here and is excluded from the check below.
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT a, b FROM tt WHERE a <> 50 ORDER BY a")), ";"); got != "1|m;5|r;7|n;106|c" {
		t.Errorf("tt = %s, want 1|m;5|r;7|n;106|c", got)
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT ev FROM tt_log ORDER BY ev")), ";"); got != "INSERT 1;INSERT 106;INSERT 2;INSERT 5;INSERT 6;INSERT 7" {
		t.Errorf("tt_log = %s", got)
	}
}

// TestUpdateOfTriggersFilterOnTargetColumns pins two parts of M0146-0076
// against PG 18.3.
//
// Column filter: a column-specific `UPDATE OF` trigger fires only when the
// UPDATE's SET list names one of its columns. This holds at both levels,
// because TriggerEnabled receives ExecGetAllUpdatedCols (for MERGE, the union
// of its UPDATE actions). Before the fix the newly firing statement
// triggers, and the row triggers too, fired for every UPDATE.
//
// Name order: triggers for the same event fire in name order, not creation
// order.
func TestUpdateOfTriggersFilterOnTargetColumns(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE uo (a int, b int)",
		"INSERT INTO uo VALUES (1, 1)",
		`CREATE FUNCTION uo_trg() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE NOTICE '% %', tg_name, tg_level;
			IF tg_level = 'ROW' THEN RETURN new; END IF; RETURN NULL; END $$`,
		"CREATE TRIGGER s_a BEFORE UPDATE OF a ON uo FOR EACH STATEMENT EXECUTE FUNCTION uo_trg()",
		"CREATE TRIGGER s_b AFTER UPDATE OF b ON uo FOR EACH STATEMENT EXECUTE FUNCTION uo_trg()",
		// Created last but named first: PG fires same-event triggers in
		// name order, not creation order.
		"CREATE TRIGGER s_0 AFTER UPDATE ON uo FOR EACH STATEMENT EXECUTE FUNCTION uo_trg()",
		"CREATE TRIGGER r_a BEFORE UPDATE OF a ON uo FOR EACH ROW EXECUTE FUNCTION uo_trg()",
		"CREATE TRIGGER r_b AFTER UPDATE OF b ON uo FOR EACH ROW EXECUTE FUNCTION uo_trg()",
	} {
		runSQL(t, ctx, q)
	}
	_ = ctx.TakeNotices()
	for _, c := range []struct{ sql, want string }{
		{"UPDATE uo SET b = 2", "r_b ROW|s_0 STATEMENT|s_b STATEMENT"},
		{"UPDATE uo SET a = 2", "s_a STATEMENT|r_a ROW|s_0 STATEMENT"},
		{"MERGE INTO uo USING (VALUES (2)) v(x) ON uo.a = v.x WHEN MATCHED THEN UPDATE SET a = 3", "s_a STATEMENT|r_a ROW|s_0 STATEMENT"},
	} {
		runSQL(t, ctx, c.sql)
		if got := strings.Join(ctx.TakeNotices(), "|"); got != c.want {
			t.Errorf("%s notices = %q, want %q", c.sql, got, c.want)
		}
	}
}

// bsUncounted drops the row count from BEFORE STATEMENT notices.
func bsUncounted(ns []string) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		if j := strings.Index(n, " n="); j >= 0 && strings.HasPrefix(n, "bs ") {
			n = n[:j]
		}
		out[i] = n
	}
	return out
}
