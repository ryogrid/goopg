package executor

import (
	"strings"
	"testing"
)

// TestTriggerWhenConditionGatesFiring pins M0146-0077 against PG 18.3. goopg
// kept a trigger's WHEN expression only so pg_get_triggerdef could print it,
// so every trigger fired for every row. PG skips a trigger whose WHEN is not
// true for the event's OLD/NEW (TriggerEnabled, trigger.c). The cases
// cover:
//   - OLD.col / NEW.col;
//   - OLD.* / NEW.* and bare OLD / NEW, compared as records;
//   - a NULL comparison, which does not pass;
//   - a quoted text literal;
//   - statement-level WHEN, true and false;
//   - BEFORE and AFTER row triggers, including an AFTER trigger after a
//     BEFORE trigger.
//
// Every want is PG's NOTICE sequence for the same script.
func TestTriggerWhenConditionGatesFiring(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE w (a int, t text, b int)",
		`CREATE FUNCTION wf() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE NOTICE '% % % old=% new=%', tg_name, tg_when, tg_op, old, new;
			IF tg_when = 'BEFORE' AND tg_level = 'ROW' THEN RETURN new; END IF; RETURN NULL; END $$`,
		"CREATE TRIGGER w_ins_a AFTER INSERT ON w FOR EACH ROW WHEN (new.a = 123) EXECUTE FUNCTION wf()",
		"CREATE TRIGGER w_upd_dist BEFORE UPDATE ON w FOR EACH ROW WHEN (old.* IS DISTINCT FROM new.*) EXECUTE FUNCTION wf()",
		"CREATE TRIGGER w_upd_rec AFTER UPDATE ON w FOR EACH ROW WHEN (old IS DISTINCT FROM new) EXECUTE FUNCTION wf()",
		"CREATE TRIGGER w_b AFTER INSERT OR UPDATE ON w FOR EACH ROW WHEN (new.b > 0) EXECUTE FUNCTION wf()",
		"CREATE TRIGGER w_t AFTER INSERT ON w FOR EACH ROW WHEN (new.t = 'it''s') EXECUTE FUNCTION wf()",
		"CREATE TRIGGER w_stmt_f AFTER INSERT ON w FOR EACH STATEMENT WHEN (1 = 0) EXECUTE FUNCTION wf()",
		"CREATE TRIGGER w_stmt_t BEFORE DELETE ON w FOR EACH STATEMENT WHEN (2 > 1) EXECUTE FUNCTION wf()",
		"CREATE TABLE some_t (some_col boolean NOT NULL)",
		`CREATE FUNCTION dummy_update_func() RETURNS trigger AS $$ BEGIN
			RAISE NOTICE 'dummy_update_func(%) called: action = %, old = %, new = %', TG_ARGV[0], TG_OP, OLD, NEW;
			RETURN NEW; END; $$ LANGUAGE plpgsql`,
		"CREATE TRIGGER some_trig_before BEFORE UPDATE ON some_t FOR EACH ROW EXECUTE PROCEDURE dummy_update_func('before')",
		"CREATE TRIGGER some_trig_aftera AFTER UPDATE ON some_t FOR EACH ROW WHEN (NOT OLD.some_col AND NEW.some_col) EXECUTE PROCEDURE dummy_update_func('aftera')",
		"CREATE TRIGGER some_trig_afterb AFTER UPDATE ON some_t FOR EACH ROW WHEN (NOT NEW.some_col) EXECUTE PROCEDURE dummy_update_func('afterb')",
		"INSERT INTO some_t VALUES (TRUE)",
	} {
		runSQL(t, ctx, q)
	}
	_ = ctx.TakeNotices()
	for _, c := range []struct{ sql, want string }{
		{"INSERT INTO w VALUES (1, 'x', NULL), (123, 'it''s', 5), (7, 'y', -1)",
			"w_b AFTER INSERT old=<NULL> new=(123,it's,5)|w_ins_a AFTER INSERT old=<NULL> new=(123,it's,5)|" +
				"w_t AFTER INSERT old=<NULL> new=(123,it's,5)"},
		{"UPDATE w SET a = a WHERE a = 1", ""},
		{"UPDATE w SET b = 3 WHERE a = 7",
			"w_upd_dist BEFORE UPDATE old=(7,y,-1) new=(7,y,3)|w_b AFTER UPDATE old=(7,y,-1) new=(7,y,3)|" +
				"w_upd_rec AFTER UPDATE old=(7,y,-1) new=(7,y,3)"},
		{"DELETE FROM w WHERE a = 5", "w_stmt_t BEFORE DELETE old=<NULL> new=<NULL>"},
		{"UPDATE some_t SET some_col = TRUE",
			"dummy_update_func(before) called: action = UPDATE, old = (t), new = (t)"},
		{"UPDATE some_t SET some_col = FALSE",
			"dummy_update_func(before) called: action = UPDATE, old = (t), new = (f)|" +
				"dummy_update_func(afterb) called: action = UPDATE, old = (t), new = (f)"},
		{"UPDATE some_t SET some_col = TRUE",
			"dummy_update_func(before) called: action = UPDATE, old = (f), new = (t)|" +
				"dummy_update_func(aftera) called: action = UPDATE, old = (f), new = (t)"},
	} {
		runSQL(t, ctx, c.sql)
		if got := strings.Join(ctx.TakeNotices(), "|"); got != c.want {
			t.Errorf("%s notices:\ngot  %s\nwant %s", c.sql, got, c.want)
		}
	}
}
