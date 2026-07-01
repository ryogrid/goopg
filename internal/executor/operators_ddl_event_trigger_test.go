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
