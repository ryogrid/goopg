package parser

import "testing"

// TestParseCreateConversion verifies that CREATE [DEFAULT] CONVERSION captures
// the name, FOR/TO encoding-name literals, FROM function name, and the DEFAULT
// flag onto a CompatNoopStmt so the executor can round-trip it through pg_dump.
// DU-002 slice 399.
func TestParseCreateConversion(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		wantName   string
		wantSchema string
		wantFor    string
		wantTo     string
		wantFunc   string
		wantFuncNS string
		wantDflt   bool
	}{
		{
			name:       "plain schema-qualified",
			sql:        "CREATE CONVERSION public.myconv FOR 'UTF8' TO 'LATIN1' FROM public.myconv_func",
			wantName:   "myconv",
			wantSchema: "public",
			wantFor:    "UTF8",
			wantTo:     "LATIN1",
			wantFunc:   "myconv_func",
			wantFuncNS: "public",
			wantDflt:   false,
		},
		{
			name:     "default bare names",
			sql:      "CREATE DEFAULT CONVERSION myconv2 FOR 'LATIN1' TO 'UTF8' FROM myconv_func2",
			wantName: "myconv2",
			wantFor:  "LATIN1",
			wantTo:   "UTF8",
			wantFunc: "myconv_func2",
			wantDflt: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			ns, ok := stmts[0].(*CompatNoopStmt)
			if !ok {
				t.Fatalf("expected *CompatNoopStmt, got %T", stmts[0])
			}
			if ns.ObjType != "conversion" {
				t.Errorf("ObjType = %q, want conversion", ns.ObjType)
			}
			if ns.ObjName.Name != tc.wantName || ns.ObjName.Schema != tc.wantSchema {
				t.Errorf("ObjName = %+v, want name=%q schema=%q", ns.ObjName, tc.wantName, tc.wantSchema)
			}
			if ns.ConvForEncoding != tc.wantFor {
				t.Errorf("ConvForEncoding = %q, want %q", ns.ConvForEncoding, tc.wantFor)
			}
			if ns.ConvToEncoding != tc.wantTo {
				t.Errorf("ConvToEncoding = %q, want %q", ns.ConvToEncoding, tc.wantTo)
			}
			if ns.ConvFuncName.Name != tc.wantFunc || ns.ConvFuncName.Schema != tc.wantFuncNS {
				t.Errorf("ConvFuncName = %+v, want name=%q schema=%q", ns.ConvFuncName, tc.wantFunc, tc.wantFuncNS)
			}
			if ns.ConvDefault != tc.wantDflt {
				t.Errorf("ConvDefault = %v, want %v", ns.ConvDefault, tc.wantDflt)
			}
		})
	}
}
