package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestHasDatabasePrivilege pins M0149-0001h's port of acl.c's
// has_database_privilege: the superuser holds everything, PUBLIC holds the
// acldefault('d') TEMP + CONNECT (not CREATE) on a database with a NULL
// datacl, the privilege list is case-insensitive and comma-separated with
// ANY semantics, and the errors are PG's (22023 unknown privilege, 3D000
// unknown database, 42704 unknown role; an unknown database OID is NULL).
func TestHasDatabasePrivilege(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	s := NewStringDatum
	for _, tc := range []struct {
		args []Datum
		want string
	}{
		{[]Datum{s("postgres"), s("create")}, "t"},
		{[]Datum{s("public"), s("postgres"), s("create")}, "f"},
		{[]Datum{s("public"), s("postgres"), s("Connect , create")}, "t"},
		{[]Datum{s("public"), s("postgres"), s("temp")}, "t"},
		{[]Datum{s("public"), s("postgres"), s("connect with grant option")}, "f"},
		{[]Datum{s("public"), NewIntDatum(999999), s("connect")}, "NULL"},
	} {
		d, err := evalHasDatabasePrivilege(tc.args, ctx, 0)
		got := "NULL"
		if !d.IsNull() {
			got = d.Format()
		}
		if err != nil || got != tc.want {
			t.Errorf("%v: %s, %v; want %s", tc.args, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		args []Datum
		code string
	}{
		{[]Datum{s("postgres"), s("select")}, "22023"},
		{[]Datum{s("nodb"), s("connect")}, "3D000"},
		{[]Datum{s("nobody"), s("postgres"), s("connect")}, "42704"},
	} {
		if _, err := evalHasDatabasePrivilege(tc.args, ctx, 0); err == nil || err.(*ExecError).Code != tc.code {
			t.Errorf("%v: err = %v, want %s", tc.args, err, tc.code)
		}
	}
}

// TestHasTableSchemaPrivilege pins M0149-0001h1's first slice: superuser and
// owner defaults, PUBLIC holding no table privilege and no CREATE on a
// non-public schema, and PG's errors (42P01 unknown relation via regclass,
// 3F000 unknown schema, 22023 unknown privilege; an unknown OID is NULL).
func TestHasTableSchemaPrivilege(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	s := NewStringDatum
	check := func(f func([]Datum, *Context, int) (Datum, error), args []Datum, want string) {
		t.Helper()
		d, err := f(args, ctx, 0)
		got := "NULL"
		if !d.IsNull() {
			got = d.Format()
		}
		if err != nil || got != want {
			t.Errorf("%v: %s, %v; want %s", args, got, err, want)
		}
	}
	check(evalHasSchemaPrivilege, []Datum{s("pg_catalog"), s("usage")}, "t")
	check(evalHasSchemaPrivilege, []Datum{s("public"), s("pg_catalog"), s("create")}, "f")
	check(evalHasSchemaPrivilege, []Datum{s("public"), NewIntDatum(999999), s("usage")}, "NULL")
	check(evalHasTablePrivilege, []Datum{s("public"), NewIntDatum(999999), s("select")}, "NULL")
	for _, tc := range []struct {
		f    func([]Datum, *Context, int) (Datum, error)
		args []Datum
		code string
	}{
		{evalHasSchemaPrivilege, []Datum{s("nosch"), s("usage")}, "3F000"},
		{evalHasSchemaPrivilege, []Datum{s("pg_catalog"), s("select")}, "22023"},
		{evalHasTablePrivilege, []Datum{s("nosuch"), s("select")}, "42P01"},
	} {
		if _, err := tc.f(tc.args, ctx, 0); err == nil || err.(*ExecError).Code != tc.code {
			t.Errorf("%v: err = %v, want %s", tc.args, err, tc.code)
		}
	}
}
