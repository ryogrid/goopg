package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
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

// M0119-0006 (93rd slice, deferral row 1343): a BARE user-defined arg type in
// CREATE FUNCTION/PROCEDURE must store its owner schema in ArgTypeSchemas[i] so
// the regprocedure arglist renders `g(offpath.myenum)` — PG 18.3 resolves the
// bare name to its pg_type tuple at routine creation (parse_type.c:291
// typenameTypeId → LookupTypeNameExtended, name→namespace) and format_type_be
// then schema-qualifies when !TypeIsVisible (format_type.c:315/318/322,
// get_namespace_name_or_temp(typeform->typnamespace)). goopg's capture half
// mirrors that by probing the user-type registries (enum → domain → composite →
// range → multirange, the userTypeOIDForName order) and storing the element
// type's ACTUAL owner schema from its NamespaceOID — NOT the explicit qualifier
// (there is none) and NOT the visibility predicate's output. A bare BUILTIN arg
// hits no registry and keeps "".
func TestCreateFunctionCapturesBareArgTypeSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	im.RegisterSchema("offpath")

	// One user type of each kind in a non-public schema.
	if err := runDDL(t, ctx, `CREATE TYPE offpath.myenum AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("CREATE TYPE offpath.myenum: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TYPE offpath.mycomp AS (a int4, b text)`); err != nil {
		t.Fatalf("CREATE TYPE offpath.mycomp: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TYPE offpath.myrange AS RANGE (subtype = int4)`); err != nil {
		t.Fatalf("CREATE TYPE offpath.myrange: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE DOMAIN offpath.mydom AS int4`); err != nil {
		t.Fatalf("CREATE DOMAIN offpath.mydom: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION g_bare(myenum, mycomp, myrange, mydom, integer) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create function: %v", err)
	}

	cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "g_bare"})
	if len(cands) != 1 {
		t.Fatalf("expected 1 g_bare routine, got %d", len(cands))
	}
	r := cands[0]
	want := []string{"offpath", "offpath", "offpath", "offpath", ""}
	if len(r.ArgTypeSchemas) != len(want) {
		t.Fatalf("ArgTypeSchemas = %v (len %d), want %v (len %d)", r.ArgTypeSchemas, len(r.ArgTypeSchemas), want, len(want))
	}
	for i := range want {
		if r.ArgTypeSchemas[i] != want[i] {
			t.Errorf("ArgTypeSchemas[%d] = %q, want %q (full %v)", i, r.ArgTypeSchemas[i], want[i], r.ArgTypeSchemas)
		}
	}

	// Default session search_path ("$user", public): offpath is off the path,
	// so the BARE arg types render schema-qualified; the bare builtin stays bare.
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, r.OID))[0][0].StringValue(); got != "g_bare(offpath.myenum,offpath.mycomp,offpath.myrange,offpath.mydom,integer)" {
		t.Errorf("%d::regprocedure::text = %q, want %q", r.OID, got, "g_bare(offpath.myenum,offpath.mycomp,offpath.myrange,offpath.mydom,integer)")
	}

	// When the type's schema IS on the effective search_path, the renderer
	// emits the bare name (TypeIsVisible → no qualification). The capture
	// stores the ACTUAL owner schema; the visibility predicate decides at
	// render time (regprocedureArglist), so this arm exercises the visible
	// path against the same captured schemas.
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return "offpath, public", true
		}
		return "", false
	}
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, r.OID))[0][0].StringValue(); got != "g_bare(myenum,mycomp,myrange,mydom,integer)" {
		t.Errorf("on-path %d::regprocedure::text = %q, want %q", r.OID, got, "g_bare(myenum,mycomp,myrange,mydom,integer)")
	}

	// Array form: the schema is the ELEMENT type's, so a bare `myenum[]` arg
	// must also resolve to offpath (the [] is stripped before probing).
	ctx.GetSetting = nil
	if err := runDDL(t, ctx, `CREATE FUNCTION g_bare_arr(myenum[]) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create function (array arg): %v", err)
	}
	acands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "g_bare_arr"})
	if len(acands) != 1 {
		t.Fatalf("expected 1 g_bare_arr routine, got %d", len(acands))
	}
	ar := acands[0]
	if len(ar.ArgTypeSchemas) != 1 || ar.ArgTypeSchemas[0] != "offpath" {
		t.Errorf("g_bare_arr ArgTypeSchemas = %v, want [offpath]", ar.ArgTypeSchemas)
	}
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, ar.OID))[0][0].StringValue(); got != "g_bare_arr(offpath.myenum[])" {
		t.Errorf("%d::regprocedure::text = %q, want %q", ar.OID, got, "g_bare_arr(offpath.myenum[])")
	}

	// Sibling capture path: execCreateProcedure must record the bare arg's
	// owner schema identically (Hard-won Rule #2 — change both, test both).
	if err := runDDL(t, ctx, `CREATE PROCEDURE p_bare(myenum) LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create procedure: %v", err)
	}
	pcands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "p_bare"})
	if len(pcands) != 1 {
		t.Fatalf("expected 1 p_bare routine, got %d", len(pcands))
	}
	pr := pcands[0]
	if len(pr.ArgTypeSchemas) != 1 || pr.ArgTypeSchemas[0] != "offpath" {
		t.Errorf("p_bare ArgTypeSchemas = %v, want [offpath]", pr.ArgTypeSchemas)
	}
	if got := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, pr.OID))[0][0].StringValue(); got != "p_bare(offpath.myenum)" {
		t.Errorf("%d::regprocedure::text = %q, want %q", pr.OID, got, "p_bare(offpath.myenum)")
	}
}

// M0119-0006 (77th slice, deferral row 1351): execCreateFunction/
// execCreateProcedure capture each arg type's RESOLVED OID at CREATE time
// (ArgTypeOIDs, parallel to ArgTypes/ArgTypeSchemas) — NON-ZERO only for the
// one ambiguous `char` spelling AND its array forms: a BARE char (bpchar,
// parser stamp Args=[1]) stores OIDBpChar(1042), a quoted `"char"` (CHAROID,
// no stamp, Args nil) stores OIDChar(18). Array forms ride the SAME arms but
// resolve to the ARRAY OIDs (row 1364): `char[]` → OIDArrayBpChar(1014),
// `"char"[]` → OIDArrayChar(1002); every other arg stays 0. `oid::regprocedure`
// then renders the disambiguated arglist (bare → `character`, quoted → `"char"`,
// both with the re-appended `[]` for arrays).
func TestCreateFunctionCapturesCharArgOID(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name       string
		ddl        string
		want       []uint32
		wantRender string
	}{
		{"f_capchar_bare", `CREATE FUNCTION f_capchar_bare(char) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`, []uint32{catalog.OIDBpChar}, "f_capchar_bare(character)"},
		{"f_capchar_quoted", `CREATE FUNCTION f_capchar_quoted("char") RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`, []uint32{catalog.OIDChar}, `f_capchar_quoted("char")`},
		{"f_capchar_arr_bare", `CREATE FUNCTION f_capchar_arr_bare(char[]) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`, []uint32{catalog.OIDArrayBpChar}, "f_capchar_arr_bare(character[])"},
		{"f_capchar_arr_quoted", `CREATE FUNCTION f_capchar_arr_quoted("char"[]) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`, []uint32{catalog.OIDArrayChar}, `f_capchar_arr_quoted("char"[])`},
	}
	for _, tc := range cases {
		if err := runDDL(t, ctx, tc.ddl); err != nil {
			t.Fatalf("create function: %v", err)
		}
		cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: tc.name})
		if len(cands) != 1 {
			t.Fatalf("expected 1 %s routine, got %d", tc.name, len(cands))
		}
		r := cands[0]
		if len(r.ArgTypeOIDs) != len(tc.want) {
			t.Errorf("%s: ArgTypeOIDs = %v (len %d), want %v (len %d)", tc.name, r.ArgTypeOIDs, len(r.ArgTypeOIDs), tc.want, len(tc.want))
			continue
		}
		for i := range tc.want {
			if r.ArgTypeOIDs[i] != tc.want[i] {
				t.Errorf("%s: ArgTypeOIDs[%d] = %d, want %d (full %v)", tc.name, i, r.ArgTypeOIDs[i], tc.want[i], r.ArgTypeOIDs)
			}
		}
		// oid::regprocedure renders the disambiguated arglist (the cast sibling).
		rows := runQuery(t, ctx, fmt.Sprintf(`SELECT %d::regprocedure::text`, r.OID))
		if len(rows) != 1 || rows[0][0].StringValue() != tc.wantRender {
			t.Errorf("%s: %d::regprocedure::text = %v, want %q", tc.name, r.OID, rows, tc.wantRender)
		}
	}
}

// M0119-0006 (deferral row 1364): array-typed CREATE FUNCTION args and RETURNS
// capture the ARRAY OIDs — quoted `"char"[]` → OIDArrayChar(1002), bare `char[]`
// → OIDArrayBpChar(1014) — for BOTH the per-arg ArgTypeOIDs and the
// ReturnTypeOID. PG oracle (PG 18.3, immutable pg_type OIDs): _char=1002,
// _bpchar=1014 — verify: SELECT 'char[]'::regtype::oid (1014),
// '"char"[]'::regtype::oid (1002). The renderers then disambiguate the char
// element on OID exactly like the scalar rows 1351/1361 do.
func TestCreateFunctionCapturesCharArrayArgOID(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name       string
		ddl        string
		wantArgOID uint32
		wantRetOID uint32
	}{
		{"g_carr", `CREATE FUNCTION g_carr("char"[]) RETURNS "char"[] LANGUAGE sql AS $$ SELECT 1 $$`, catalog.OIDArrayChar, catalog.OIDArrayChar},
		{"g_bcarr", `CREATE FUNCTION g_bcarr(char[]) RETURNS char[] LANGUAGE sql AS $$ SELECT 1 $$`, catalog.OIDArrayBpChar, catalog.OIDArrayBpChar},
	}
	for _, tc := range cases {
		if err := runDDL(t, ctx, tc.ddl); err != nil {
			t.Fatalf("create function %s: %v", tc.name, err)
		}
		cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: tc.name})
		if len(cands) != 1 {
			t.Fatalf("expected 1 %s routine, got %d", tc.name, len(cands))
		}
		r := cands[0]
		if len(r.ArgTypeOIDs) != 1 || r.ArgTypeOIDs[0] != tc.wantArgOID {
			t.Errorf("%s: ArgTypeOIDs = %v, want [%d]", tc.name, r.ArgTypeOIDs, tc.wantArgOID)
		}
		if r.ReturnTypeOID != tc.wantRetOID {
			t.Errorf("%s: ReturnTypeOID = %d, want %d", tc.name, r.ReturnTypeOID, tc.wantRetOID)
		}
	}
}

// M0119-0006 (77th slice, deferral row 1351, sibling audit): the
// ::regprocedure CAST sibling, the SELECT wire (RegOutArgVisible — what
// appendTypedCellText calls for a regprocedure-typed column), and COPY TO
// (EncodeCopyTextRow → datumToCopyText) must agree on `character` vs `"char"`
// for the same two routines. Under the default session search_path both
// routines live in "public" (visible), so qualify=false on every path.
func TestRegprocedureCharArgCastAndWireAgree(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION f_char_bare(char) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create f_char_bare: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION f_char_quoted("char") RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create f_char_quoted: %v", err)
	}
	for _, tc := range []struct{ name, want string }{
		{"f_char_bare", "f_char_bare(character)"},
		{"f_char_quoted", `f_char_quoted("char")`},
	} {
		cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: tc.name})
		if len(cands) != 1 {
			t.Fatalf("expected 1 %s routine, got %d", tc.name, len(cands))
		}
		oid := cands[0].OID

		// ::regprocedure cast sibling.
		cast := runQuery(t, ctx, fmt.Sprintf("SELECT %d::regprocedure::text", oid))
		if len(cast) != 1 {
			t.Fatalf("%s: %d::regprocedure::text = %v", tc.name, oid, cast)
		}
		if got := cast[0][0].StringValue(); got != tc.want {
			t.Errorf("%s cast = %q, want %q", tc.name, got, tc.want)
		}

		// SELECT wire: appendTypedCellText → RegOutArgVisible (qualify=false
		// mirrors the default-path visibility: public is on the search_path).
		if got := RegOutArgVisible("regprocedure", oid, ctx.Catalog, false, nil); got != tc.want {
			t.Errorf("%s SELECT wire = %q, want %q", tc.name, got, tc.want)
		}

		// COPY TO: EncodeCopyTextRow → datumToCopyText → RegOutArgVisible with
		// the same qualify flag.
		copyRow, err := EncodeCopyTextRow(nil, Row{NewIntDatum(int64(oid))},
			[]catalog.Column{{Name: "r", Type: catalog.Type{Name: "regprocedure"}}},
			"ISO", "MDY", "", "hex", ctx.Catalog, false)
		if err != nil {
			t.Fatalf("%s COPY TO: %v", tc.name, err)
		}
		if got := strings.TrimSuffix(string(copyRow), "\n"); got != tc.want {
			t.Errorf("%s COPY TO = %q, want %q", tc.name, got, tc.want)
		}
	}
}
