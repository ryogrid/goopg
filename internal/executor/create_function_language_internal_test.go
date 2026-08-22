package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCreateFunctionLanguageInternal pins M0134-0071 Bucket A:
//
//   - (a) CREATE FUNCTION ... LANGUAGE internal AS 'int8eq' succeeds (the
//     allowlist now admits "internal", and the AS name binds to a pg_proc row).
//   - (b) an unknown internal name raises the same error PG's
//     fmgr_internal_validator produces — ERRCODE_UNDEFINED_FUNCTION (42883),
//     "there is no built-in function named \"%s\"" (pg_proc.c:768-771). NOTE:
//     the brief/design say 42704, but PG 18.3 raises 42883; the PG oracle wins.
//   - (c) the created function is callable and returns the correct result for
//     int8eq(1,1) — real dispatch through dispatchInternalFunction, not the
//     LANGUAGE c stub's default-value behavior.
//
// The FAIL-pre state is the allowlist gate: before Bucket A, (a) fails with
// `language "internal" is not supported (Stage A: plpgsql, sql)`.
func TestCreateFunctionLanguageInternal(t *testing.T) {
	cat := catalog.NewInMemory()

	// (a) Known internal name binds and registers.
	if err := runRoutineDDL(t,
		`CREATE FUNCTION int8eq_probe(int8, int8) RETURNS bool STRICT IMMUTABLE LANGUAGE internal AS 'int8eq'`,
		cat); err != nil {
		t.Fatalf("CREATE FUNCTION ... LANGUAGE internal AS 'int8eq': %v", err)
	}
	r, ok := cat.Routines().Lookup(parser.ObjectName{Name: "int8eq_probe"}, []catalog.Type{{Name: "int8"}, {Name: "int8"}})
	if !ok {
		t.Fatal("Lookup(\"int8eq_probe\"(int8,int8)) did not find registered routine")
	}
	if r.Language != "internal" {
		t.Errorf("Language = %q, want internal", r.Language)
	}
	if r.Body != "int8eq" {
		t.Errorf("Body = %q, want %q (the AS clause is the bound C name)", r.Body, "int8eq")
	}

	// (b) Unknown internal name -> 42883 (UNDEFINED_FUNCTION), PG's message.
	err := runRoutineDDL(t,
		`CREATE FUNCTION bad_int8eq(int8, int8) RETURNS bool LANGUAGE internal AS 'no_such_builtin_fn'`,
		cat)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42883" {
		t.Fatalf("unknown internal name: got err=%v, want ExecError SQLSTATE 42883", err)
	}
	if ee.Message != `there is no built-in function named "no_such_builtin_fn"` {
		t.Errorf("Message = %q, want PG's fmgr_internal_validator message", ee.Message)
	}

	// (c) The created function is callable and computes int8eq(1,1) == true.
	ctx := NewContext()
	ctx.Catalog = cat
	rows := runQuery(t, ctx, `SELECT int8eq_probe(1, 1)`)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("rows = %v, want one row with one column", rows)
	}
	if rows[0][0].Kind != KindBool || !rows[0][0].BoolValue() {
		t.Errorf("int8eq_probe(1,1) = %v, want true", rows[0][0])
	}
	// int8eq(1, 2) must be false — guards against a stub returning a constant.
	rows = runQuery(t, ctx, `SELECT int8eq_probe(1, 2)`)
	if rows[0][0].Kind != KindBool || rows[0][0].BoolValue() {
		t.Errorf("int8eq_probe(1,2) = %v, want false", rows[0][0])
	}
}
