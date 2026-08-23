package executor

import (
	"strings"
	"testing"
)

// TestAlterFunctionRenameConflict guards the M0134-0088 (alter_generic.sql)
// sizing finding: ALTER FUNCTION/PROCEDURE ... RENAME TO and ... SET SCHEMA
// silently displaced an existing routine of the same name+signature instead
// of rejecting the collision. Real PG calls IsThereFunctionInNamespace()
// (postgres/src/backend/commands/functioncmds.c) before re-keying pg_proc
// and raises 42723 "function ... already exists in schema ...". The
// downstream fallout was severe: alter_generic.sql's later statements
// assumed the FIRST rename failed (name still free) and cascaded into
// unrelated wrong errors for the rest of the file.
func TestAlterFunctionRenameConflict(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	mustDDL := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustDDL(`CREATE FUNCTION alt_func1(int) RETURNS int AS 'select $1' LANGUAGE sql`)
	mustDDL(`CREATE FUNCTION alt_func2(int) RETURNS int AS 'select $1' LANGUAGE sql`)

	// RENAME TO an existing name+signature must be rejected, not silently
	// applied (which would leave alt_func1 renamed away and alt_func2
	// untouched — exactly the bug: PG keeps both functions unchanged here).
	err := runDDL(t, ctx, `ALTER FUNCTION alt_func1(int) RENAME TO alt_func2`)
	if err == nil {
		t.Fatal("ALTER FUNCTION RENAME TO onto an existing signature should fail, got nil error")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "42723" {
		t.Errorf("Code = %q, want 42723 (duplicate_function)", ee.Code)
	}
	if !strings.Contains(ee.Message, "already exists in schema") {
		t.Errorf("Message = %q, want it to mention 'already exists in schema'", ee.Message)
	}

	// alt_func1 must still resolve under its original name after the
	// rejected rename (no partial mutation).
	if err := runDDL(t, ctx, `ALTER FUNCTION alt_func1(int) RENAME TO alt_func3`); err != nil {
		t.Fatalf("ALTER FUNCTION RENAME TO a free name should still work: %v", err)
	}

	// SET SCHEMA has the same guard: colliding with an existing routine in
	// the destination schema is rejected, but moving to the schema the
	// routine is already in ("no-op move") must still succeed.
	mustDDL(`CREATE SCHEMA alt_nsp2`)
	mustDDL(`CREATE FUNCTION alt_nsp2.alt_func3(int) RETURNS int AS 'select $1' LANGUAGE sql`)
	err = runDDL(t, ctx, `ALTER FUNCTION alt_func3(int) SET SCHEMA alt_nsp2`)
	if err == nil {
		t.Fatal("ALTER FUNCTION SET SCHEMA onto an existing signature should fail, got nil error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42723" {
		t.Errorf("SET SCHEMA conflict error = %#v, want *ExecError{Code: 42723}", err)
	}
	if err := runDDL(t, ctx, `ALTER FUNCTION alt_func2(int) SET SCHEMA public`); err != nil {
		t.Fatalf("SET SCHEMA to the schema already occupied by itself should be a no-op success: %v", err)
	}
}
