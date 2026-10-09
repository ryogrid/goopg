package executor

import (
	"strings"
	"testing"
)

// TestAlterTableEnableDisableTrigger pins M0146-0082 against PG 18.3
// (regress triggers.sql "Test enable/disable triggers").
//
// ALTER TABLE … ENABLE/DISABLE TRIGGER was a no-op: a disabled trigger kept
// firing, so a disabled BEFORE ROW DELETE trigger returning NEW (NULL for a
// DELETE) still suppressed the delete (`DELETE 0`). pg_trigger.tgenabled
// was always 'O', and session_replication_role did not exist. PG's
// TriggerEnabled fires an 'O' trigger in origin/local, 'R' only in replica,
// 'A' always, 'D' never. DISABLE TRIGGER ALL also disables the internal RI
// triggers, so a delete no longer cascades. Every want is PG's output.
func TestAlterTableEnableDisableTrigger(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	role := "origin"
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "session_replication_role" {
			return role, true
		}
		return "", false
	}
	for _, q := range []string{
		"CREATE TABLE trigtest (i int PRIMARY KEY)",
		"CREATE TABLE trigtest2 (i int REFERENCES trigtest(i) ON DELETE CASCADE)",
		`CREATE FUNCTION trigtest() RETURNS trigger AS $$
		BEGIN RAISE NOTICE '% % %', TG_OP, TG_WHEN, TG_LEVEL; RETURN new; END $$ LANGUAGE plpgsql`,
		"CREATE TRIGGER trigtest_b_row_tg BEFORE INSERT OR UPDATE OR DELETE ON trigtest FOR EACH ROW EXECUTE PROCEDURE trigtest()",
		"CREATE TRIGGER trigtest_a_row_tg AFTER INSERT OR UPDATE OR DELETE ON trigtest FOR EACH ROW EXECUTE PROCEDURE trigtest()",
		"CREATE TRIGGER trigtest_b_stmt_tg BEFORE INSERT OR UPDATE OR DELETE ON trigtest FOR EACH STATEMENT EXECUTE PROCEDURE trigtest()",
		"CREATE TRIGGER trigtest_a_stmt_tg AFTER INSERT OR UPDATE OR DELETE ON trigtest FOR EACH STATEMENT EXECUTE PROCEDURE trigtest()",
	} {
		runSQL(t, ctx, q)
	}
	_ = ctx.TakeNotices()
	step := func(sql, want string) {
		t.Helper()
		runSQL(t, ctx, sql)
		if got := strings.Join(ctx.TakeNotices(), "|"); got != want {
			t.Errorf("%s\ngot  %q\nwant PG's %q", sql, got, want)
		}
	}
	step("INSERT INTO trigtest VALUES (1)",
		"INSERT BEFORE STATEMENT|INSERT BEFORE ROW|INSERT AFTER ROW|INSERT AFTER STATEMENT")
	runSQL(t, ctx, "ALTER TABLE trigtest DISABLE TRIGGER trigtest_b_row_tg")
	step("INSERT INTO trigtest VALUES (2)",
		"INSERT BEFORE STATEMENT|INSERT AFTER ROW|INSERT AFTER STATEMENT")
	runSQL(t, ctx, "ALTER TABLE trigtest DISABLE TRIGGER USER")
	step("INSERT INTO trigtest VALUES (3)", "")
	runSQL(t, ctx, "ALTER TABLE trigtest ENABLE TRIGGER trigtest_a_stmt_tg")
	step("INSERT INTO trigtest VALUES (4)", "INSERT AFTER STATEMENT")
	role = "replica"
	step("INSERT INTO trigtest VALUES (5)", "") // an 'O' trigger does not fire in replica
	runSQL(t, ctx, "ALTER TABLE trigtest ENABLE ALWAYS TRIGGER trigtest_a_stmt_tg")
	step("INSERT INTO trigtest VALUES (6)", "INSERT AFTER STATEMENT")
	role = "origin"

	if got := strings.Join(renderRows(runSQL(t, ctx,
		"SELECT tgname, tgenabled FROM pg_trigger WHERE tgrelid = 'trigtest'::regclass ORDER BY tgname")), ";"); got !=
		"trigtest_a_row_tg|D;trigtest_a_stmt_tg|A;trigtest_b_row_tg|D;trigtest_b_stmt_tg|D" {
		t.Errorf("tgenabled = %s, want PG's trigtest_a_row_tg|D;trigtest_a_stmt_tg|A;trigtest_b_row_tg|D;trigtest_b_stmt_tg|D", got)
	}

	// The disabled BEFORE ROW trigger used to fire and return NEW (NULL for a
	// DELETE), skipping the delete; the cascade then never ran.
	runSQL(t, ctx, "INSERT INTO trigtest2 VALUES (1), (2)")
	runSQL(t, ctx, "DELETE FROM trigtest WHERE i = 2")
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT i FROM trigtest2 ORDER BY i")), ";"); got != "1" {
		t.Errorf("trigtest2 after cascading delete = %s, want PG's 1", got)
	}
	// ALL disables the RI action trigger too: no cascade.
	runSQL(t, ctx, "ALTER TABLE trigtest DISABLE TRIGGER ALL")
	runSQL(t, ctx, "DELETE FROM trigtest WHERE i = 1")
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT i FROM trigtest2 ORDER BY i")), ";"); got != "1" {
		t.Errorf("trigtest2 after DISABLE TRIGGER ALL delete = %s, want PG's 1", got)
	}
	_ = ctx.TakeNotices()
	step("INSERT INTO trigtest VALUES (7)", "")

	if _, err := runSQLCtxErr(t, ctx, "ALTER TABLE trigtest ENABLE TRIGGER nosuch"); err == nil ||
		!strings.Contains(err.Error(), `trigger "nosuch" for table "trigtest" does not exist`) {
		t.Errorf("unknown trigger: err = %v, want PG's 42704", err)
	}
}
