package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestObjectVisibilityFollowsSearchPath pins M0149-0001h1a32 against PG
// 18.3: pg_table/type/function/statistics_obj_is_visible answer by the
// search_path (an earlier schema's same-named object hides a later one; a
// missing OID is NULL), and regtypein / regprocedurein / regprocin resolve
// a bare name through the path, by argument types for regprocedure.
func TestObjectVisibilityFollowsSearchPath(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	path := "public"
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return path, true
		}
		return "", false
	}
	for _, ddl := range []string{
		"CREATE SCHEMA vs1",
		"CREATE SCHEMA vs2",
		"CREATE TABLE vs1.t (a int)",
		"CREATE TABLE vs2.t (a int)",
		"CREATE TYPE vs2.ty AS (x int)",
		"CREATE FUNCTION vs2.f(int) RETURNS int LANGUAGE sql AS 'select 1'",
		"CREATE FUNCTION vs1.f(int) RETURNS int LANGUAGE sql AS 'select 2'",
		"CREATE FUNCTION vs2.f(text) RETURNS int LANGUAGE sql AS 'select 3'",
		"CREATE STATISTICS vs2.st ON a, (a * 2) FROM vs2.t",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	oid := func(spelling, reg string) int64 {
		t.Helper()
		d, err := regIdentifierInput(NewStringDatum(spelling), reg, ctx, 0)
		if err != nil || d.Kind != KindInt {
			t.Fatalf("%q::%s: %+v, %v", spelling, reg, d, err)
		}
		return d.Int
	}
	t1, t2 := oid("vs1.t", "regclass"), oid("vs2.t", "regclass")
	ty := oid("vs2.ty", "regtype")
	f1, f2 := oid("vs1.f(int)", "regprocedure"), oid("vs2.f(int)", "regprocedure")
	f2text := oid("vs2.f(text)", "regprocedure")
	if f1 == f2 || f2 == f2text {
		t.Fatalf("regprocedurein ignores schema or arguments: vs1.f(int)=%d vs2.f(int)=%d vs2.f(text)=%d", f1, f2, f2text)
	}
	vis := func(fn string, o int64) Datum {
		t.Helper()
		d, handled := pgObjectIsVisible(fn, NewIntDatum(o), ctx)
		if !handled {
			t.Fatalf("%s not handled", fn)
		}
		return d
	}
	want := func(label string, d Datum, w bool) {
		t.Helper()
		if d.Kind != KindBool || d.BoolValue() != w {
			t.Errorf("%s = %+v, want %v", label, d, w)
		}
	}

	path = "vs1, vs2"
	want("table vs1.t", vis("pg_table_is_visible", t1), true)
	want("table vs2.t (hidden by vs1.t)", vis("pg_table_is_visible", t2), false)
	want("type vs2.ty", vis("pg_type_is_visible", ty), true)
	want("function vs1.f(int)", vis("pg_function_is_visible", f1), true)
	want("function vs2.f(int) (hidden)", vis("pg_function_is_visible", f2), false)
	want("function vs2.f(text)", vis("pg_function_is_visible", f2text), true)
	if got := oid("f(int)", "regprocedure"); got != f1 {
		t.Errorf("'f(int)'::regprocedure on vs1,vs2 = %d, want vs1.f %d", got, f1)
	}
	if got := oid("f(text)", "regprocedure"); got != f2text {
		t.Errorf("'f(text)'::regprocedure = %d, want vs2.f(text) %d", got, f2text)
	}
	if _, err := regIdentifierInput(NewStringDatum("f"), "regproc", ctx, 0); err == nil {
		t.Errorf("'f'::regproc with two signatures on the path: no error, want 42725")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42725" {
		t.Errorf("'f'::regproc: %v, want 42725", err)
	}

	path = "public"
	want("table vs1.t off path", vis("pg_table_is_visible", t1), false)
	want("type vs2.ty off path", vis("pg_type_is_visible", ty), false)
	want("function vs2.f(int) off path", vis("pg_function_is_visible", f2), false)
	want("table pg_class", vis("pg_table_is_visible", oid("pg_class", "regclass")), true)
	want("function lower(text)", vis("pg_function_is_visible", oid("lower(text)", "regprocedure")), true)
	if d := vis("pg_table_is_visible", 99999999); !d.IsNull() {
		t.Errorf("pg_table_is_visible(missing) = %+v, want NULL", d)
	}
	if d := vis("pg_type_is_visible", 99999999); !d.IsNull() {
		t.Errorf("pg_type_is_visible(missing) = %+v, want NULL", d)
	}
	if d := vis("pg_function_is_visible", 99999999); !d.IsNull() {
		t.Errorf("pg_function_is_visible(missing) = %+v, want NULL", d)
	}
	for _, miss := range []struct{ spelling, reg, code string }{
		{"ty", "regtype", "42704"},
		{"f(int)", "regprocedure", "42883"},
		{"vs1.ty", "regtype", "42704"},
	} {
		_, err := regIdentifierInput(NewStringDatum(miss.spelling), miss.reg, ctx, 0)
		if ee, ok := err.(*ExecError); !ok || ee.Code != miss.code {
			t.Errorf("%q::%s off path: %v, want %s", miss.spelling, miss.reg, err, miss.code)
		}
	}
	if got := RegOutVisible("regprocedure", uint32(f1), ctx.Catalog, func(s string) bool { return RegObjectSchemaVisible(ctx, s) }); got != "vs1.f(integer)" {
		t.Errorf("regprocedureout(vs1.f) off path = %q, want vs1.f(integer)", got)
	}
	path = "vs1, vs2"
	if got := RegOutVisible("regprocedure", uint32(f1), ctx.Catalog, func(s string) bool { return RegObjectSchemaVisible(ctx, s) }); got != "f(integer)" {
		t.Errorf("regprocedureout(vs1.f) on path = %q, want f(integer)", got)
	}
}

// TestBuiltinFunctionsAreVisible: every pg_catalog function is visible on
// the default search_path (pg_catalog is searched implicitly), so its
// format_procedure text round-trips through regprocedurein to its own OID.
// psql's \df filters on pg_function_is_visible.
func TestBuiltinFunctionsAreVisible(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return `"$user", public`, true
		}
		return "", false
	}
	var failed []string
	total := 0
	for oid := uint32(1); oid < 16384; oid++ {
		if _, _, _, ok := catalog.RegprocedureNameParts(oid, nil); !ok {
			continue
		}
		total++
		d, handled := pgObjectIsVisible("pg_function_is_visible", NewIntDatum(int64(oid)), ctx)
		if !handled || d.Kind != KindBool || !d.BoolValue() {
			failed = append(failed, RegOut("regprocedure", oid, ctx.Catalog, false))
		}
	}
	if total < 3000 {
		t.Fatalf("only %d built-in functions enumerated", total)
	}
	if len(failed) > 0 {
		t.Errorf("%d of %d built-in functions not visible: %v", len(failed), total, failed)
	}
}
