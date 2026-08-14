package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
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

// M0119-0006 (71st slice, deferral row 1338): the ::regprocedure cast is the
// SIBLING renderer of RegOut's regprocedure arm (the SELECT wire / COPY TO
// path). Both must qualify only the routine NAME — including the
// quote_identifier on a mixed-case name, which the pre-slice `schema + "." +
// sig` prefix skipped (`public.MyFunc(integer)` instead of
// `public."MyFunc"(integer)`).
func TestRegprocedureCastQuotesRoutineName(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION "MyFunc"(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create quoted-name function: %v", err)
	}
	// The OID is fetched from the live routine registry (mixed case preserved
	// by the parser). Since the 72nd slice, `'"MyFunc"'::regproc` resolves
	// quoted names too (see reg_input_quoted_test.go); the registry fetch is
	// kept so this test pins the OUTPUT rendering only, independent of the
	// input path.
	cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "MyFunc"})
	if len(cands) == 0 {
		t.Fatal(`"MyFunc" routine not found`)
	}
	udfOID := cands[0].OID

	// Default session search_path ("$user", public): the routine is visible, so
	// the name is quote_identifier'd bare and the arglist appended unquoted.
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, udfOID))[0][0].StringValue(); got != `"MyFunc"(integer)` {
		t.Errorf("bare search_path: %d::regprocedure::text = %q, want %q", udfOID, got, `"MyFunc"(integer)`)
	}

	// pg_dump's search_path='' — the routine is off the effective path, so the
	// qualified form quotes the name under the schema.
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return "", true
		}
		return "", false
	}
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, udfOID))[0][0].StringValue(); got != `public."MyFunc"(integer)` {
		t.Errorf("empty search_path: %d::regprocedure::text = %q, want %q", udfOID, got, `public."MyFunc"(integer)`)
	}

	// A routine in a NON-public schema qualifies with that schema on BOTH
	// paths — the cast path's regObjectSchemaVisible is per-object (unlike the
	// COPY/SELECT proxy qualify flag), so under the default path it must still
	// emit `other_schema.…` (PG 18.3 measured: `ragout71.other_func()`).
	if err := runDDL(t, ctx, `CREATE SCHEMA other_schema`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION other_schema.other_cast_func(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create non-public function: %v", err)
	}
	ocCands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Schema: "other_schema", Name: "other_cast_func"})
	if len(ocCands) == 0 {
		t.Fatal("other_schema.other_cast_func routine not found")
	}
	ocOID := ocCands[0].OID
	// Default path.
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, ocOID))[0][0].StringValue(); got != "other_schema.other_cast_func(integer)" {
		t.Errorf("bare search_path: %d::regprocedure::text = %q, want %q", ocOID, got, "other_schema.other_cast_func(integer)")
	}
	// Empty path (pg_dump).
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, ocOID))[0][0].StringValue(); got != "other_schema.other_cast_func(integer)" {
		t.Errorf("empty search_path: %d::regprocedure::text = %q, want %q", ocOID, got, "other_schema.other_cast_func(integer)")
	}
}

// M0119-0006 (73rd slice, deferral row 1342): the ::regprocedure cast is the
// SIBLING renderer of RegOut's regprocedure arm (the SELECT wire / COPY TO
// path). format_procedure_extended schema-qualifies each INPUT arg type whose
// namespace is off the session's effective search_path (format_type_be), so the
// cast and the column/COPY renderers must agree on the arglist too — under the
// default path the explicit `offpath.mytype` arg renders `offpath.mytype`, and
// `offpath.mytype[]` renders `offpath.mytype[]` (element quoted, suffix
// unquoted). These are the same expected strings the §1 oracle table pins.
func TestRegprocedureCastArgTypesQualify(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// goopg does not validate arg types at CREATE FUNCTION (pre-existing gap:
	// PG raises 42704 here), so the off-path composite need not exist — the DDL's
	// explicit qualifier is what the capture records. The schema must exist so
	// parseColumnType's qualified-name path is exercised for real.
	if err := runDDL(t, ctx, `CREATE SCHEMA offpath`); err != nil {
		t.Fatalf("create schema offpath: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION f_cast(offpath.mytype, pg_catalog.int4, int) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION f_cast_arr(offpath.mytype[]) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create function (array arg): %v", err)
	}

	// Default session search_path ("$user", public): offpath is off the path, so
	// the arg types qualify; pg_catalog and bare-builtin args stay alias-bare.
	// (The cast path's per-object RegObjectSchemaVisible drives this, not the
	// qualify proxy.)
	fc := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "f_cast"})
	if len(fc) != 1 {
		t.Fatalf("expected 1 f_cast routine, got %d", len(fc))
	}
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, fc[0].OID))[0][0].StringValue(); got != "f_cast(offpath.mytype,integer,integer)" {
		t.Errorf("f_cast: %d::regprocedure::text = %q, want %q", fc[0].OID, got, "f_cast(offpath.mytype,integer,integer)")
	}

	// The array arg: suffix split/re-appended — `offpath.mytype[]`, not
	// `offpath."mytype[]"`.
	fa := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "f_cast_arr"})
	if len(fa) != 1 {
		t.Fatalf("expected 1 f_cast_arr routine, got %d", len(fa))
	}
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, fa[0].OID))[0][0].StringValue(); got != "f_cast_arr(offpath.mytype[])" {
		t.Errorf("f_cast_arr: %d::regprocedure::text = %q, want %q", fa[0].OID, got, "f_cast_arr(offpath.mytype[])")
	}

	// NOTE: the search_path-dependence arm (putting offpath ON the path making
	// the arg bare) is covered at the renderer level in
	// TestRegOutRegprocedureQualifiesArgTypes; exercising it through a live
	// `SET search_path = …, offpath` is blocked here by searchPathSchemas'
	// pre-existing schema-existence proxy (a schema is "visible" only when a
	// table with that name resolves via LookupTable — an empty schema never
	// does, unrelated to this slice).
}

// M0119-0006 (73rd slice, deferral row 1342): execCreateFunction/
// execCreateProcedure capture each arg type's explicit schema at CREATE time
// (ArgTypeSchemas, parallel to ArgTypes) so the regprocedure output path can
// schema-qualify a non-visible arg type. An EXPLICIT qualifier yields its
// schema; a bare builtin keeps "" (equivalent rendering to pg_catalog). This is
// the capture half — the render half is covered by
// TestRegOutRegprocedureQualifiesArgTypes / TestRegprocedureCastArgTypesQualify.
func TestCreateFunctionCapturesArgTypeSchemas(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SCHEMA offpath`); err != nil {
		t.Fatalf("create schema offpath: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION f_capschemas(offpath.mytype, pg_catalog.int4, int) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "f_capschemas"})
	if len(cands) != 1 {
		t.Fatalf("expected 1 f_capschemas routine, got %d", len(cands))
	}
	want := []string{"offpath", "pg_catalog", ""}
	r := cands[0]
	if len(r.ArgTypeSchemas) != len(want) {
		t.Fatalf("ArgTypeSchemas = %v (len %d), want %v (len %d)", r.ArgTypeSchemas, len(r.ArgTypeSchemas), want, len(want))
	}
	for i := range want {
		if r.ArgTypeSchemas[i] != want[i] {
			t.Errorf("ArgTypeSchemas[%d] = %q, want %q (full %v)", i, r.ArgTypeSchemas[i], want[i], r.ArgTypeSchemas)
		}
	}
}
