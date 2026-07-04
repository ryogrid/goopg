package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateForeignDataWrapperHandlerValidatorResolved pins the DU-002
// (M0119-0004) closure of the long-open "HANDLER/VALIDATOR func references
// are skipped (goopg tracks no funcs)" deferral: a `CREATE FOREIGN DATA
// WRAPPER ... HANDLER h VALIDATOR v` now resolves h/v against the live
// routine registry (CREATE FUNCTION) and stores their real pg_proc OIDs, so
// pg_dump's `fdwhandler::regproc`/`fdwvalidator::regproc` casts render the
// function names instead of always folding to '-'.
func TestCreateForeignDataWrapperHandlerValidatorResolved(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("expected *catalog.InMemory")
	}

	if err := runDDL(t, ctx, `CREATE FUNCTION myfdw_handler() RETURNS fdw_handler LANGUAGE c AS 'myfdw_handler'`); err != nil {
		t.Fatalf("create handler function: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION myfdw_validator(text[], oid) RETURNS void LANGUAGE c AS 'myfdw_validator'`); err != nil {
		t.Fatalf("create validator function: %v", err)
	}
	handlerOID := runQuery(t, ctx, `SELECT 'myfdw_handler'::regproc`)[0][0].Int
	validatorOID := runQuery(t, ctx, `SELECT 'myfdw_validator'::regproc`)[0][0].Int

	if err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER fdw_hv HANDLER myfdw_handler VALIDATOR myfdw_validator`); err != nil {
		t.Fatalf("CREATE FOREIGN DATA WRAPPER: %v", err)
	}

	fdw, found := im.LookupForeignDataWrapper("fdw_hv")
	if !found {
		t.Fatal("fdw_hv not found")
	}
	if int64(fdw.HandlerOID) != handlerOID {
		t.Errorf("HandlerOID = %d, want %d", fdw.HandlerOID, handlerOID)
	}
	if int64(fdw.ValidatorOID) != validatorOID {
		t.Errorf("ValidatorOID = %d, want %d", fdw.ValidatorOID, validatorOID)
	}

	// End-to-end: pg_dump's own getForeignDataWrappers cast shape resolves the
	// stored OIDs back to the function names, not '-'.
	rows := runQuery(t, ctx, `SELECT fdwhandler::regproc::text, fdwvalidator::regproc::text FROM pg_foreign_data_wrapper WHERE fdwname = 'fdw_hv'`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "myfdw_handler" {
		t.Errorf("fdwhandler::regproc = %q, want %q", got, "myfdw_handler")
	}
	if got := rows[0][1].StringValue(); got != "myfdw_validator" {
		t.Errorf("fdwvalidator::regproc = %q, want %q", got, "myfdw_validator")
	}

	// A bare FDW with no HANDLER/VALIDATOR clause still folds to '-' (the
	// slice-375 no-handler case must not regress).
	if err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER fdw_bare`); err != nil {
		t.Fatalf("CREATE FOREIGN DATA WRAPPER fdw_bare: %v", err)
	}
	bareRows := runQuery(t, ctx, `SELECT fdwhandler::regproc::text, fdwvalidator::regproc::text FROM pg_foreign_data_wrapper WHERE fdwname = 'fdw_bare'`)
	if got := bareRows[0][0].StringValue(); got != "-" {
		t.Errorf("bare fdwhandler::regproc = %q, want %q", got, "-")
	}
	if got := bareRows[0][1].StringValue(); got != "-" {
		t.Errorf("bare fdwvalidator::regproc = %q, want %q", got, "-")
	}
}

// TestCreateForeignDataWrapperHandlerErrors pins the two error paths
// lookup_fdw_handler_func/lookup_fdw_validator_func raise in
// foreigncmds.c: 42883 undefined_function for a name that does not resolve
// to a matching fixed-signature routine, and 42809 wrong_object_type for a
// handler whose return type isn't fdw_handler. Neither error should leave a
// half-created FDW behind.
func TestCreateForeignDataWrapperHandlerErrors(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	wantCode := func(t *testing.T, err error, code string) {
		t.Helper()
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != code {
			t.Fatalf("error = %v, want *ExecError %s", err, code)
		}
	}

	wantCode(t, runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER fdw_nohandler HANDLER nosuchfunc`), "42883")
	if _, found := im.LookupForeignDataWrapper("fdw_nohandler"); found {
		t.Error("fdw_nohandler should not have been created after handler resolution failed")
	}

	if err := runDDL(t, ctx, `CREATE FUNCTION badret_fdw_handler() RETURNS int4 LANGUAGE c AS 'badret_fdw_handler'`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	wantCode(t, runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER fdw_badret HANDLER badret_fdw_handler`), "42809")
	if _, found := im.LookupForeignDataWrapper("fdw_badret"); found {
		t.Error("fdw_badret should not have been created after wrong-return-type handler")
	}

	wantCode(t, runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER fdw_noval VALIDATOR nosuchvalidator`), "42883")
}

// TestAlterForeignDataWrapperHandlerValidatorSetAndClear covers the ALTER
// tri-state this closes: an absent HANDLER/VALIDATOR clause leaves the
// existing OID unchanged (proven by an OPTIONS-only ALTER between the SET and
// CLEAR steps), `HANDLER f`/`VALIDATOR f` resolves and sets a new OID, and
// `NO HANDLER`/`NO VALIDATOR` clears it back to InvalidOid(0).
func TestAlterForeignDataWrapperHandlerValidatorSetAndClear(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	if err := runDDL(t, ctx, `CREATE FUNCTION alt_fdw_handler() RETURNS fdw_handler LANGUAGE c AS 'alt_fdw_handler'`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	handlerOID := runQuery(t, ctx, `SELECT 'alt_fdw_handler'::regproc`)[0][0].Int

	if err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER fdw_alt`); err != nil {
		t.Fatalf("CREATE FOREIGN DATA WRAPPER: %v", err)
	}
	fdw, _ := im.LookupForeignDataWrapper("fdw_alt")
	if fdw.HandlerOID != 0 {
		t.Fatalf("initial HandlerOID = %d, want 0", fdw.HandlerOID)
	}

	if err := runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw_alt HANDLER alt_fdw_handler`); err != nil {
		t.Fatalf("ALTER ... HANDLER: %v", err)
	}
	if int64(fdw.HandlerOID) != handlerOID {
		t.Fatalf("after ALTER HANDLER, HandlerOID = %d, want %d", fdw.HandlerOID, handlerOID)
	}

	// An OPTIONS-only ALTER (no HANDLER/VALIDATOR clause) must not disturb the
	// handler set above.
	if err := runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw_alt OPTIONS (ADD debug 'true')`); err != nil {
		t.Fatalf("ALTER ... OPTIONS: %v", err)
	}
	if int64(fdw.HandlerOID) != handlerOID {
		t.Fatalf("after unrelated OPTIONS ALTER, HandlerOID = %d, want unchanged %d", fdw.HandlerOID, handlerOID)
	}

	if err := runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw_alt NO HANDLER`); err != nil {
		t.Fatalf("ALTER ... NO HANDLER: %v", err)
	}
	if fdw.HandlerOID != 0 {
		t.Fatalf("after ALTER NO HANDLER, HandlerOID = %d, want 0", fdw.HandlerOID)
	}
}
