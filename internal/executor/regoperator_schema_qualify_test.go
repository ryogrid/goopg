package executor

import (
	"fmt"
	"testing"
)

// TestRegprocedureRegoperatorSchemaQualification pins DU-002 (M0119-0004)
// slice 412: pg_dump always connects with search_path='' (see upstream
// ALWAYS_SECURE_SEARCH_PATH_SQL), which makes format_procedure/
// format_operator (regproc.c) schema-qualify every object that is not in
// pg_catalog — pg_catalog is always implicitly searched regardless of
// search_path. Previously goopg's ::regprocedure/::regoperator casts never
// qualified, which produced a byte-diff against a live PG 18.3
// dumpOpclass/dumpOpfamily output (public.~=~(integer,integer) vs
// ~=~(integer,integer)).
func TestRegprocedureRegoperatorSchemaQualification(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION my_regprocedure_udf(a int4, b text) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	udfOID := runQuery(t, ctx, `SELECT 'my_regprocedure_udf'::regproc`)[0][0].Int

	if err := runDDL(t, ctx, `CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("create operator: %v", err)
	}
	opOID := runQuery(t, ctx, `SELECT oprname, oid FROM pg_operator WHERE oprname = '~=~'`)
	if len(opOID) != 1 {
		t.Fatalf("expected 1 pg_operator row, got %d: %v", len(opOID), opOID)
	}
	operOID := opOID[0][1].Int

	// Default session search_path ("$user", public): both objects live in
	// "public", which is on the effective path, so they render bare.
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, udfOID))[0][0].StringValue(); got != "my_regprocedure_udf(integer,text)" {
		t.Errorf("bare search_path: %d::regprocedure::text = %q, want %q", udfOID, got, "my_regprocedure_udf(integer,text)")
	}
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regoperator::text`, operOID))[0][0].StringValue(); got != "~=~(integer,integer)" {
		t.Errorf("bare search_path: %d::regoperator::text = %q, want %q", operOID, got, "~=~(integer,integer)")
	}
	// A builtin (pg_catalog) function stays bare regardless of search_path.
	if got := runQuery(t, ctx, `SELECT 43::regprocedure::text`)[0][0].StringValue(); got != "int4out(integer)" {
		t.Errorf("bare search_path: 43::regprocedure::text = %q, want %q", got, "int4out(integer)")
	}

	// pg_dump's own connection setup (ALWAYS_SECURE_SEARCH_PATH_SQL) always
	// runs `SET search_path = ''`; simulate that here.
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return "", true
		}
		return "", false
	}

	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, udfOID))[0][0].StringValue(); got != "public.my_regprocedure_udf(integer,text)" {
		t.Errorf("empty search_path: %d::regprocedure::text = %q, want %q", udfOID, got, "public.my_regprocedure_udf(integer,text)")
	}
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regoperator::text`, operOID))[0][0].StringValue(); got != "public.~=~(integer,integer)" {
		t.Errorf("empty search_path: %d::regoperator::text = %q, want %q", operOID, got, "public.~=~(integer,integer)")
	}
	// A builtin (pg_catalog) function is always implicitly on the search
	// path, so it must stay bare even under search_path=''.
	if got := runQuery(t, ctx, `SELECT 43::regprocedure::text`)[0][0].StringValue(); got != "int4out(integer)" {
		t.Errorf("empty search_path: 43::regprocedure::text = %q, want %q", got, "int4out(integer)")
	}
}
