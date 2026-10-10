package parser

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

// TestParseSetArgList pins the var_list element typing the SET-family
// flattening depends on (gram.y var_value / NumericOnly): integers that fit
// int32 print as integers whatever their spelling, larger ones and other
// numbers keep their text, signs apply, identifiers are downcased unless
// quoted, and anything that is not a pure list is rejected.
func TestParseSetArgList(t *testing.T) {
	i := func(v string) misc.SetArg { return misc.SetArg{Kind: misc.SetArgInteger, Val: v} }
	f := func(v string) misc.SetArg { return misc.SetArg{Kind: misc.SetArgFloat, Val: v} }
	s := func(v string) misc.SetArg { return misc.SetArg{Kind: misc.SetArgString, Val: v} }
	cases := []struct {
		in   string
		want []misc.SetArg
	}{
		{`'x''y\z', public`, []misc.SetArg{s(`x'y\z`), s("public")}},
		{`Foo, "Bar", 'Baz'`, []misc.SetArg{s("foo"), s("Bar"), s("Baz")}},
		{`007, 0x10, -5, +3`, []misc.SetArg{i("7"), i("16"), i("-5"), i("3")}},
		{`3000000000, 2.5, -1e3, .5`, []misc.SetArg{f("3000000000"), f("2.5"), f("-1e3"), f(".5")}},
		{`true, on, off`, []misc.SetArg{s("true"), s("on"), s("off")}},
		{`'64MB';`, []misc.SetArg{s("64MB")}},
	}
	for _, c := range cases {
		got, ok := ParseSetArgList(c.in)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseSetArgList(%q) = %v, %v; want %v", c.in, got, ok, c.want)
		}
	}
	for _, in := range []string{``, `DEFAULT`, `default`, `64MB`, `a,`, `a b`, `INTERVAL '1' HOUR`, `(1)`} {
		if got, ok := ParseSetArgList(in); ok {
			t.Errorf("ParseSetArgList(%q) = %v, true; want not a var_list", in, got)
		}
	}
}

// TestSetStatementArgs checks that every grammar path carrying a SET value
// list hands the typed elements to the executor: plain SET, ALTER SYSTEM, and
// the CREATE / ALTER FUNCTION SET clause (whose value list is found by
// position, not by the statement's first '=').
func TestSetStatementArgs(t *testing.T) {
	want := []misc.SetArg{{Kind: misc.SetArgString, Val: "a b"}, {Kind: misc.SetArgString, Val: "public"}}
	parse := func(sql string) Stmt {
		t.Helper()
		stmts, err := Parse(sql)
		if err != nil || len(stmts) != 1 {
			t.Fatalf("Parse(%q) = %v, %v", sql, stmts, err)
		}
		return stmts[0]
	}
	if st, ok := parse(`SET search_path = 'a b', public`).(*SetStmt); !ok || !reflect.DeepEqual(st.Args, want) {
		t.Errorf("SET Args = %#v", st)
	}
	if st, ok := parse(`ALTER SYSTEM SET search_path TO 'a b', public`).(*AlterSystemStmt); !ok || !reflect.DeepEqual(st.Args, want) {
		t.Errorf("ALTER SYSTEM Args = %#v", st)
	}
	cf, ok := parse(`CREATE FUNCTION f() RETURNS int LANGUAGE sql SET work_mem = '1MB' SET search_path = 'a b', public AS 'select 1'`).(*CreateFunctionStmt)
	if !ok || len(cf.ConfigOps) != 2 || !reflect.DeepEqual(cf.ConfigOps[1].Args, want) {
		t.Errorf("CREATE FUNCTION ConfigOps = %#v", cf)
	}
	af, ok := parse(`ALTER FUNCTION f(int) SET search_path TO 'a b', public`).(*AlterFunctionStmt)
	if !ok || len(af.ConfigOps) != 1 || !reflect.DeepEqual(af.ConfigOps[0].Args, want) {
		t.Errorf("ALTER FUNCTION ConfigOps = %#v", af)
	}
	if st, ok := parse(`SET search_path TO DEFAULT`).(*SetStmt); !ok || st.Args != nil || !st.Default {
		t.Errorf("SET TO DEFAULT = %#v", st)
	}
}
