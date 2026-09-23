package executor

import "testing"

// ALTER RULE … RENAME must move the rule wherever DROP RULE looks for it, and
// DROP RULE IF EXISTS must drop an existing rule rather than skip it. Both were
// found live against PG 18.3 on 2026-09-23: after a rename, DROP RULE newname
// failed "does not exist"; and DROP RULE IF EXISTS on an existing rule emitted
// "does not exist, skipping" and dropped nothing. Covers the DO-NOTHING form
// (reified in tbl.Rules) and an action form that stays in the compat registry.
func TestAlterRuleRenameThenDrop(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	mustRun := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	wantCode := func(sql, code string) {
		t.Helper()
		err := runDDL(t, ctx, sql)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != code {
			t.Fatalf("%s: err = %v, want SQLSTATE %s", sql, err, code)
		}
	}
	mustRun("CREATE TABLE t (a int)")
	mustRun("CREATE TABLE log (a int)")

	// DO-NOTHING form.
	mustRun("CREATE RULE r AS ON UPDATE TO t DO INSTEAD NOTHING")
	mustRun("ALTER RULE r ON t RENAME TO r2")
	wantCode("DROP RULE r ON t", "42704")
	mustRun("DROP RULE r2 ON t")
	wantCode("DROP RULE r2 ON t", "42704")

	// Action form (not reified in tbl.Rules; tracked only by the registry).
	mustRun("CREATE RULE ra AS ON INSERT TO t DO ALSO INSERT INTO log VALUES (new.a)")
	mustRun("ALTER RULE ra ON t RENAME TO ra2")
	wantCode("ALTER RULE ra ON t RENAME TO ra3", "42704")
	mustRun("DROP RULE ra2 ON t")

	// Collision across the two stores.
	mustRun("CREATE RULE x1 AS ON UPDATE TO t DO INSTEAD NOTHING")
	mustRun("CREATE RULE x2 AS ON DELETE TO t DO ALSO INSERT INTO log VALUES (old.a)")
	wantCode("ALTER RULE x1 ON t RENAME TO x2", "42710")

	// DROP RULE IF EXISTS on an EXISTING rule drops it.
	mustRun("DROP RULE IF EXISTS x1 ON t")
	wantCode("DROP RULE x1 ON t", "42704")
	mustRun("DROP RULE IF EXISTS x1 ON t") // now absent: notice, no error
}
