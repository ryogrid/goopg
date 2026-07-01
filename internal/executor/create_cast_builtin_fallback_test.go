package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateCastWithFunctionResolvesBuiltin exercises the fallback to
// catalog.LookupBuiltinProc when a CAST's WITH FUNCTION clause references a
// built-in (not user-created) routine — mirrors resolveTransformFunc's and
// resolveConversionFunc's identical fallback. This is the exact upstream
// pg_dump `002_pg_dump.pl` "CREATE CAST FOR timestamptz" fixture's WITH
// FUNCTION reference (`age(timestamptz)`, pg_proc OID 1386), which previously
// left pg_cast.castfunc at 0 (unresolved) because goopg's routine registry
// only holds user-CREATE FUNCTION routines.
func TestCreateCastWithFunctionResolvesBuiltin(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE CAST (timestamptz AS interval) WITH FUNCTION age(timestamptz) AS ASSIGNMENT`); err != nil {
		t.Fatalf("CREATE CAST (timestamptz AS interval) WITH FUNCTION age(timestamptz): %v", err)
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	cs := im.CastByTypes("timestamptz", "interval")
	if cs == nil {
		t.Fatal("CastByTypes(\"timestamptz\", \"interval\") = nil after CREATE CAST")
	}
	if cs.FuncOID != 1386 {
		t.Fatalf("castfunc = %d, want 1386 (builtin age(timestamptz))", cs.FuncOID)
	}
	if cs.Context != "a" {
		t.Fatalf("castcontext = %q, want \"a\" (ASSIGNMENT)", cs.Context)
	}
}

// TestCreateCastWithFunctionRejectsBuiltinSignatureMismatch confirms the
// builtin fallback still enforces CreateCast's argument/return-type rules —
// synthesizing a *catalog.Routine from the curated table (rather than
// skipping validation for builtins) means a builtin whose signature doesn't
// match the declared cast is rejected exactly like a user-routine mismatch.
func TestCreateCastWithFunctionRejectsBuiltinSignatureMismatch(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// age(timestamptz) returns interval, not text — the declared target type
	// (text) does not match, so CreateCast must reject it (42P17).
	err := runDDL(t, ctx, `CREATE CAST (timestamptz AS text) WITH FUNCTION age(timestamptz)`)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42P17" {
		t.Fatalf("expected 42P17, got %v", err)
	}
}
