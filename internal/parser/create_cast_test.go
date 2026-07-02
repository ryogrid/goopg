package parser

import "testing"

// TestParseCreateCastWithFunction covers the CREATE CAST … WITH FUNCTION form.
// The parser must capture the source/target types, castmethod='f', and the
// referenced function name + explicit arg-type list (CastFuncName/CastFuncArgs)
// so the executor can resolve pg_cast.castfunc to the function's pg_proc OID and
// dumpCast re-emits `WITH FUNCTION <ns>.<signature>`. WITHOUT FUNCTION / WITH
// INOUT carry no function reference. DU-002 slice 397.
func TestParseCreateCastWithFunction(t *testing.T) {
	cases := []struct {
		sql        string
		wantMethod string
		wantCtx    string
		wantFnSch  string
		wantFnName string
		wantArgs   []string
	}{
		{
			sql:        "CREATE CAST (text AS integer) WITH FUNCTION public.text_as_int(text)",
			wantMethod: "f", wantCtx: "e",
			wantFnSch: "public", wantFnName: "text_as_int", wantArgs: []string{"text"},
		},
		{
			sql:        "CREATE CAST (text AS integer) WITH FUNCTION conv(text, integer) AS ASSIGNMENT",
			wantMethod: "f", wantCtx: "a",
			wantFnSch: "", wantFnName: "conv", wantArgs: []string{"text", "integer"},
		},
		{
			// No parenthesised arg list — name captured, args empty.
			sql:        "CREATE CAST (text AS integer) WITH FUNCTION myfn AS IMPLICIT",
			wantMethod: "f", wantCtx: "i",
			wantFnSch: "", wantFnName: "myfn", wantArgs: nil,
		},
		{
			// WITHOUT FUNCTION carries no function reference.
			sql:        "CREATE CAST (text AS bytea) WITHOUT FUNCTION",
			wantMethod: "b", wantCtx: "e",
			wantFnSch: "", wantFnName: "", wantArgs: nil,
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("%q: unexpected parse error: %v", tc.sql, err)
			continue
		}
		if len(stmts) != 1 {
			t.Errorf("%q: got %d stmts, want 1", tc.sql, len(stmts))
			continue
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Errorf("%q: got %T, want *CompatNoopStmt", tc.sql, stmts[0])
			continue
		}
		if ns.ObjType != "cast" {
			t.Errorf("%q: ObjType=%q, want cast", tc.sql, ns.ObjType)
		}
		if ns.CastMethod != tc.wantMethod {
			t.Errorf("%q: CastMethod=%q, want %q", tc.sql, ns.CastMethod, tc.wantMethod)
		}
		if ns.CastContext != tc.wantCtx {
			t.Errorf("%q: CastContext=%q, want %q", tc.sql, ns.CastContext, tc.wantCtx)
		}
		if ns.CastFuncName.Schema != tc.wantFnSch || ns.CastFuncName.Name != tc.wantFnName {
			t.Errorf("%q: CastFuncName=%+v, want {%q %q}", tc.sql, ns.CastFuncName, tc.wantFnSch, tc.wantFnName)
		}
		if len(ns.CastFuncArgs) != len(tc.wantArgs) {
			t.Errorf("%q: CastFuncArgs=%v, want %v", tc.sql, ns.CastFuncArgs, tc.wantArgs)
			continue
		}
		for i := range tc.wantArgs {
			if ns.CastFuncArgs[i] != tc.wantArgs[i] {
				t.Errorf("%q: CastFuncArgs[%d]=%q, want %q", tc.sql, i, ns.CastFuncArgs[i], tc.wantArgs[i])
			}
		}
	}
}
