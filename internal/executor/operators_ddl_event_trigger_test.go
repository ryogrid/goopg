package executor

// operators_ddl_event_trigger_test.go — CREATE/DROP EVENT TRIGGER pins the
// runtime pg_event_trigger registry goopg needs only for pg_dump round-trip
// fidelity (goopg never fires event triggers). DU-002 (M0119-0004).

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestCreateEventTriggerRegistersRow(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start WHEN TAG IN ('CREATE TABLE', 'ALTER TABLE') EXECUTE FUNCTION et_func()`); err != nil {
		t.Fatalf("CREATE EVENT TRIGGER: %v", err)
	}

	im := ctx.Catalog.(*catalog.InMemory)
	ets := im.ListEventTriggers()
	if len(ets) != 1 {
		t.Fatalf("ListEventTriggers len=%d want 1", len(ets))
	}
	et := ets[0]
	if et.Name != "et1" || et.Event != "ddl_command_start" || et.Enabled != "O" {
		t.Errorf("et=%+v", et)
	}
	if got, want := et.Tags, []string{"CREATE TABLE", "ALTER TABLE"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Tags=%v want %v", got, want)
	}
	if et.FuncOID == 0 {
		t.Error("FuncOID=0, want a resolved function OID")
	}
	if et.Owner != 10 {
		t.Errorf("Owner=%d want 10 (bootstrap superuser default)", et.Owner)
	}
}

// TestCreateEventTriggerDuplicateNameErrors pins PG's 42710 duplicate_object
// behavior for a repeated trigger name.
func TestCreateEventTriggerDuplicateNameErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()`); err != nil {
		t.Fatalf("first CREATE EVENT TRIGGER: %v", err)
	}
	err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_end EXECUTE FUNCTION et_func()`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42710" {
		t.Errorf("Code=%q want 42710", ee.Code)
	}
}

// TestCreateEventTriggerUnknownFunctionErrors pins the 42883 undefined
// error when EXECUTE FUNCTION names a nonexistent niladic routine.
func TestCreateEventTriggerUnknownFunctionErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION nosuchfunc()`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42883" {
		t.Errorf("Code=%q want 42883", ee.Code)
	}
}

// TestCreateEventTriggerUnrecognizedEventErrors pins the 42601 syntax_error
// PostgreSQL raises for an event name outside the fixed 5-value set.
func TestCreateEventTriggerUnrecognizedEventErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON bogus_event EXECUTE FUNCTION et_func()`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42601" {
		t.Errorf("Code=%q want 42601", ee.Code)
	}
}

// TestCreateEventTriggerNonSuperuserErrors pins PG's 42501
// insufficient_privilege when a non-superuser session issues CREATE EVENT
// TRIGGER (CreateEventTrigger, event_trigger.c: "It would be nice to allow
// database owners or even regular users to do this, but there are obvious
// privilege escalation risks").
func TestCreateEventTriggerNonSuperuserErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}

	im := ctx.Catalog.(*catalog.InMemory)
	im.RegisterRole("app_owner")
	ctx.NonSuperuserRole = "app_owner"

	err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42501" {
		t.Errorf("Code=%q want 42501", ee.Code)
	}
	if got := im.ListEventTriggers(); len(got) != 0 {
		t.Errorf("ListEventTriggers=%v want empty (CREATE must have been rejected)", got)
	}
}

// TestCreateEventTriggerNonSuperuserHint pins the HINT PG attaches to the
// 42501 error above (errhint("Must be superuser to create an event
// trigger."), event_trigger.c) — M0134-0122.
func TestCreateEventTriggerNonSuperuserHint(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	im.RegisterRole("app_owner")
	ctx.NonSuperuserRole = "app_owner"

	err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Hint != "Must be superuser to create an event trigger." {
		t.Errorf("Hint=%q want the PG hint text", ee.Hint)
	}
}

// TestCreateEventTriggerDuplicateFilterVarErrors pins
// error_duplicate_filter_variable (event_trigger.c): two AND-joined `tag IN
// (...)` clauses in the same WHEN must raise "filter variable \"tag\"
// specified more than once" (42601) instead of silently merging their tag
// lists — M0134-0122 (event_trigger.sql).
func TestCreateEventTriggerDuplicateFilterVarErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start WHEN TAG IN ('create table') AND TAG IN ('CREATE FUNCTION') EXECUTE FUNCTION et_func()`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42601" {
		t.Errorf("Code=%q want 42601", ee.Code)
	}
	if ee.Message != `filter variable "tag" specified more than once` {
		t.Errorf("Message=%q", ee.Message)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	if got := im.ListEventTriggers(); len(got) != 0 {
		t.Errorf("ListEventTriggers=%v want empty (CREATE must have been rejected)", got)
	}
}

// TestCreateEventTriggerTagValidation pins validate_ddl_tags/
// validate_table_rewrite_tags (event_trigger.c), verified against a real
// PG 18.3 instance: an unrecognized tag on a ddl_command_*/sql_drop trigger
// is 42601, a recognized-but-disallowed tag (e.g. "VACUUM", which PG never
// fires event triggers for) is 0A000, table_rewrite tags are validated
// against the disjoint table_rewrite_ok flag (an unrecognized tag there is
// ALSO 0A000, not 42601 — PG's validate_table_rewrite_tags has no
// CMDTAG_UNKNOWN special case), and tag lookup is case-insensitive
// (GetCommandTagEnum's pg_strcasecmp bsearch).
func TestCreateEventTriggerTagValidation(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantErr string // "" means success
	}{
		{"unknown tag on ddl_command_start", `CREATE EVENT TRIGGER et1 ON ddl_command_start WHEN TAG IN ('BOGUS TAG') EXECUTE FUNCTION et_func()`, "42601"},
		{"known but disallowed tag on ddl_command_start", `CREATE EVENT TRIGGER et2 ON ddl_command_start WHEN TAG IN ('VACUUM') EXECUTE FUNCTION et_func()`, "0A000"},
		{"allowed tag on ddl_command_start", `CREATE EVENT TRIGGER et3 ON ddl_command_start WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION et_func()`, ""},
		{"case-insensitive allowed tag", `CREATE EVENT TRIGGER et4 ON ddl_command_start WHEN TAG IN ('create table') EXECUTE FUNCTION et_func()`, ""},
		{"unknown tag on table_rewrite is also 0A000", `CREATE EVENT TRIGGER et5 ON table_rewrite WHEN TAG IN ('BOGUS TAG') EXECUTE FUNCTION et_func()`, "0A000"},
		{"known but non-rewrite tag on table_rewrite", `CREATE EVENT TRIGGER et6 ON table_rewrite WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION et_func()`, "0A000"},
		{"allowed tag on table_rewrite", `CREATE EVENT TRIGGER et7 ON table_rewrite WHEN TAG IN ('ALTER TABLE') EXECUTE FUNCTION et_func()`, ""},
		{"sql_drop uses the same ddl-tag table", `CREATE EVENT TRIGGER et8 ON sql_drop WHEN TAG IN ('VACUUM') EXECUTE FUNCTION et_func()`, "0A000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, _, cleanup := newStorageFixture(t)
			defer cleanup()
			if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
				t.Fatalf("CREATE FUNCTION: %v", err)
			}
			err := runDDL(t, ctx, c.sql)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", c.sql, err)
				}
				return
			}
			var ee *ExecError
			if !errors.As(err, &ee) {
				t.Fatalf("%s: err type = %T, want *ExecError; err=%v", c.sql, err, err)
			}
			if ee.Code != c.wantErr {
				t.Errorf("%s: Code=%q want %q", c.sql, ee.Code, c.wantErr)
			}
		})
	}
}

// TestDropEventTriggerRemovesRow pins that a create-then-drop-then-dump
// sequence leaves no ghost row — DropEventTrigger is wired through
// execDropCompat's "event trigger" case (DROP EVENT TRIGGER's grammar
// previously mis-parsed TRIGGER as the object name; see the ddl.go fix in
// this loop).
func TestDropEventTriggerRemovesRow(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()`); err != nil {
		t.Fatalf("CREATE EVENT TRIGGER: %v", err)
	}
	if err := runDDL(t, ctx, `DROP EVENT TRIGGER et1`); err != nil {
		t.Fatalf("DROP EVENT TRIGGER: %v", err)
	}

	im := ctx.Catalog.(*catalog.InMemory)
	if got := im.ListEventTriggers(); len(got) != 0 {
		t.Errorf("ListEventTriggers=%v want empty after DROP", got)
	}
}

// TestAlterEventTriggerEnableDisable pins evtenabled mutation through
// ALTER EVENT TRIGGER {DISABLE|ENABLE [REPLICA|ALWAYS]}. DU-002 (M0119-0004,
// loop #69 ledger follow-up).
func TestAlterEventTriggerEnableDisable(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()`); err != nil {
		t.Fatalf("CREATE EVENT TRIGGER: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)

	cases := []struct {
		sql  string
		want string
	}{
		{"ALTER EVENT TRIGGER et1 DISABLE", "D"},
		{"ALTER EVENT TRIGGER et1 ENABLE REPLICA", "R"},
		{"ALTER EVENT TRIGGER et1 ENABLE ALWAYS", "A"},
		{"ALTER EVENT TRIGGER et1 ENABLE", "O"},
	}
	for _, c := range cases {
		if err := runDDL(t, ctx, c.sql); err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		ets := im.ListEventTriggers()
		if len(ets) != 1 || ets[0].Enabled != c.want {
			t.Errorf("%s: Enabled=%v want %q", c.sql, ets, c.want)
		}
	}
}

// TestAlterEventTriggerRenameTo pins the registry re-key ALTER EVENT
// TRIGGER RENAME TO performs.
func TestAlterEventTriggerRenameTo(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()`); err != nil {
		t.Fatalf("CREATE EVENT TRIGGER: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER EVENT TRIGGER et1 RENAME TO et2`); err != nil {
		t.Fatalf("ALTER EVENT TRIGGER RENAME TO: %v", err)
	}

	im := ctx.Catalog.(*catalog.InMemory)
	ets := im.ListEventTriggers()
	if len(ets) != 1 || ets[0].Name != "et2" {
		t.Fatalf("ets=%+v want a single et2", ets)
	}

	// Renaming to an already-taken name errors 42710, mirroring
	// RegisterEventTrigger's own duplicate check.
	if err := runDDL(t, ctx, `CREATE EVENT TRIGGER et3 ON ddl_command_end EXECUTE FUNCTION et_func()`); err != nil {
		t.Fatalf("CREATE EVENT TRIGGER et3: %v", err)
	}
	err := runDDL(t, ctx, `ALTER EVENT TRIGGER et2 RENAME TO et3`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42710" {
		t.Errorf("Code=%q want 42710", ee.Code)
	}
}

// TestAlterEventTriggerOwnerTo pins evtowner mutation via the CURRENT_USER
// sentinel resolving to the bootstrap superuser OID (10), and that OWNER TO
// a non-superuser role is rejected (AlterEventTriggerOwner_internal requires
// the new owner to be a superuser; goopg's role model treats OID 10 as the
// only superuser — see TestAlterEventTriggerOwnerToNonSuperuserErrors).
func TestAlterEventTriggerOwnerTo(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()`); err != nil {
		t.Fatalf("CREATE EVENT TRIGGER: %v", err)
	}

	im := ctx.Catalog.(*catalog.InMemory)

	if err := runDDL(t, ctx, `ALTER EVENT TRIGGER et1 OWNER TO CURRENT_USER`); err != nil {
		t.Fatalf("ALTER EVENT TRIGGER OWNER TO CURRENT_USER: %v", err)
	}
	if ets := im.ListEventTriggers(); len(ets) != 1 || ets[0].Owner != 10 {
		t.Fatalf("ets=%+v want Owner=10 (bootstrap superuser)", ets)
	}
}

// TestAlterEventTriggerOwnerToNonSuperuserErrors pins PG's 42501
// insufficient_privilege when OWNER TO names a non-superuser role — real PG
// requires the new owner of an event trigger to be a superuser
// (event_trigger.c AlterEventTriggerOwner_internal); goopg's role model
// never marks a CREATE ROLE'd role as superuser (rolsuper is always 'f' for
// non-bootstrap roles), so any named role is rejected.
func TestAlterEventTriggerOwnerToNonSuperuserErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION et_func() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN END $$`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()`); err != nil {
		t.Fatalf("CREATE EVENT TRIGGER: %v", err)
	}

	im := ctx.Catalog.(*catalog.InMemory)
	im.RegisterRole("alice")

	err := runDDL(t, ctx, `ALTER EVENT TRIGGER et1 OWNER TO alice`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42501" {
		t.Errorf("Code=%q want 42501", ee.Code)
	}
	if ets := im.ListEventTriggers(); len(ets) != 1 || ets[0].Owner != 10 {
		t.Fatalf("ets=%+v want Owner unchanged (still 10)", ets)
	}
}

// TestAlterEventTriggerUnknownNameErrors pins the 42704 undefined_object
// PostgreSQL raises for ALTER EVENT TRIGGER on a nonexistent name.
func TestAlterEventTriggerUnknownNameErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `ALTER EVENT TRIGGER nosuchtrigger DISABLE`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42704" {
		t.Errorf("Code=%q want 42704", ee.Code)
	}
}
