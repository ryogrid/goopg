package executor

// operators_ddl_access_method_test.go — CREATE/DROP ACCESS METHOD pins the
// runtime pg_am registry goopg needs only for pg_dump round-trip fidelity
// (goopg has no pluggable table/index storage engine and never invokes a
// user-defined AM's handler). DU-002 (M0119-0004).

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestCreateAccessMethodRegistersRow(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION myam_handler(internal) RETURNS index_am_handler LANGUAGE c AS 'myam_handler'`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE ACCESS METHOD myam TYPE INDEX HANDLER myam_handler`); err != nil {
		t.Fatalf("CREATE ACCESS METHOD: %v", err)
	}

	im := ctx.Catalog.(*catalog.InMemory)
	ams := im.ListAccessMethods()
	if len(ams) != 1 {
		t.Fatalf("ListAccessMethods len=%d want 1", len(ams))
	}
	am := ams[0]
	if am.Name != "myam" || am.AMType != "i" {
		t.Errorf("am=%+v", am)
	}
	if am.HandlerOID == 0 {
		t.Error("HandlerOID=0, want a resolved function OID")
	}
	if am.OID == 0 {
		t.Error("OID=0, want an allocated catalog OID")
	}
}

// TestCreateAccessMethodTableType pins the TYPE TABLE form (table_am_handler
// return type, distinct pg_am.amtype 't').
func TestCreateAccessMethodTableType(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION mytam_handler(internal) RETURNS table_am_handler LANGUAGE c AS 'mytam_handler'`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE ACCESS METHOD mytam TYPE TABLE HANDLER mytam_handler`); err != nil {
		t.Fatalf("CREATE ACCESS METHOD: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	ams := im.ListAccessMethods()
	if len(ams) != 1 || ams[0].AMType != "t" {
		t.Fatalf("ams=%+v want one row with AMType=t", ams)
	}
}

// TestCreateAccessMethodUnknownFunctionErrors pins the 42883 undefined
// error when HANDLER names a nonexistent/mismatched-signature routine.
func TestCreateAccessMethodUnknownFunctionErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `CREATE ACCESS METHOD myam TYPE INDEX HANDLER nosuchfunc`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42883" {
		t.Errorf("Code=%q want 42883", ee.Code)
	}
}

// TestCreateAccessMethodWrongReturnTypeErrors pins the 42809
// wrong_object_type error when the resolved HANDLER function's return type
// doesn't match TYPE INDEX/TABLE's expected pseudo-type.
func TestCreateAccessMethodWrongReturnTypeErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION badret_handler(internal) RETURNS table_am_handler LANGUAGE c AS 'badret_handler'`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	err := runDDL(t, ctx, `CREATE ACCESS METHOD myam TYPE INDEX HANDLER badret_handler`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42809" {
		t.Errorf("Code=%q want 42809", ee.Code)
	}
}

// TestCreateAccessMethodDuplicateNameErrors pins PG's 42710 duplicate_object
// behavior for a repeated AM name, including a collision with a built-in AM.
func TestCreateAccessMethodDuplicateNameErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION myam_handler(internal) RETURNS index_am_handler LANGUAGE c AS 'myam_handler'`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE ACCESS METHOD myam TYPE INDEX HANDLER myam_handler`); err != nil {
		t.Fatalf("first CREATE ACCESS METHOD: %v", err)
	}
	err := runDDL(t, ctx, `CREATE ACCESS METHOD myam TYPE INDEX HANDLER myam_handler`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42710" {
		t.Errorf("Code=%q want 42710", ee.Code)
	}

	err = runDDL(t, ctx, `CREATE ACCESS METHOD btree TYPE INDEX HANDLER myam_handler`)
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42710" {
		t.Errorf("Code=%q want 42710 for built-in name collision", ee.Code)
	}
}

// TestDropAccessMethodRemovesRow pins DROP ACCESS METHOD removing the
// dump-visible registry entry via execDropCompat's "access method" case.
func TestDropAccessMethodRemovesRow(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION myam_handler(internal) RETURNS index_am_handler LANGUAGE c AS 'myam_handler'`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE ACCESS METHOD myam TYPE INDEX HANDLER myam_handler`); err != nil {
		t.Fatalf("CREATE ACCESS METHOD: %v", err)
	}
	if err := runDDL(t, ctx, `DROP ACCESS METHOD myam`); err != nil {
		t.Fatalf("DROP ACCESS METHOD: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	if got := im.ListAccessMethods(); len(got) != 0 {
		t.Errorf("ListAccessMethods=%+v want empty after DROP", got)
	}
}

// TestCommentOnAccessMethodStoresDescription pins execCommentOn's "access
// method" case (DU-002 slice 434): COMMENT ON ACCESS METHOD must key
// pg_description on the AM's own pg_am OID (classoid 2601, objsubid 0) so
// pg_dump's dumpAccessMethod re-emits the comment. Before this slice the
// parser had no "access method" branch in parseCommentOnTail, so the
// statement was silently discarded and never reached SetComment.
func TestCommentOnAccessMethodStoresDescription(t *testing.T) {
	const oidPgAm = 2601

	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION myam_handler(internal) RETURNS index_am_handler LANGUAGE c AS 'myam_handler'`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE ACCESS METHOD myam TYPE INDEX HANDLER myam_handler`); err != nil {
		t.Fatalf("CREATE ACCESS METHOD: %v", err)
	}
	if err := runDDL(t, ctx, `COMMENT ON ACCESS METHOD myam IS 'a test access method'`); err != nil {
		t.Fatalf("COMMENT ON ACCESS METHOD: %v", err)
	}

	im := ctx.Catalog.(*catalog.InMemory)
	ams := im.ListAccessMethods()
	if len(ams) != 1 {
		t.Fatalf("ListAccessMethods len=%d want 1", len(ams))
	}
	desc, ok := im.GetComment(oidPgAm, ams[0].OID, 0)
	if !ok {
		t.Fatalf("GetComment(pg_am, %d, 0) not found", ams[0].OID)
	}
	if desc != "a test access method" {
		t.Errorf("description=%q want %q", desc, "a test access method")
	}
}

// TestCommentOnUnknownAccessMethodErrors pins the DU-002 slice 436 fix: an
// unresolvable COMMENT ON ACCESS METHOD name now raises the same 42704
// `access method "..." does not exist` error real PostgreSQL 18.3 raises
// (verified live), replacing the silent no-op the slice 434 row had flagged
// as a pre-existing, out-of-scope divergence.
func TestCommentOnUnknownAccessMethodErrors(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `COMMENT ON ACCESS METHOD nosuchmethod IS 'x'`)
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err type = %T, want *ExecError; err=%v", err, err)
	}
	if ee.Code != "42704" {
		t.Errorf("Code=%q want 42704", ee.Code)
	}
	want := `access method "nosuchmethod" does not exist`
	if ee.Message != want {
		t.Errorf("Message=%q want %q", ee.Message, want)
	}
}
