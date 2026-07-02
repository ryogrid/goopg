package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestValidateCreateConversionEncodings exercises the encoding-name checks
// PostgreSQL enforces in CreateConversionCommand (conversioncmds.c), ported in
// DU-002 slice 401: an unknown source/destination encoding is rejected with
// SQLSTATE 42704 (undefined_object) and a conversion to or from SQL_ASCII is
// rejected with 42P17 (invalid_object_definition). Valid endpoints resolve to
// their canonical pg_enc IDs (aliases included).
func TestValidateCreateConversionEncodings(t *testing.T) {
	cases := []struct {
		name     string
		forName  string
		toName   string
		wantFor  int32
		wantTo   int32
		wantErr  string // substring; "" = success
		wantCode string
	}{
		{name: "latin1 to utf8 ok", forName: "LATIN1", toName: "UTF8", wantFor: 8, wantTo: 6},
		{name: "alias mskanji to unicode ok", forName: "mskanji", toName: "unicode", wantFor: 35, wantTo: 6},
		{name: "unknown source", forName: "NOSUCHENC", toName: "UTF8",
			wantErr: `source encoding "NOSUCHENC" does not exist`, wantCode: "42704"},
		{name: "unknown destination", forName: "UTF8", toName: "BOGUS",
			wantErr: `destination encoding "BOGUS" does not exist`, wantCode: "42704"},
		{name: "sql_ascii source rejected", forName: "SQL_ASCII", toName: "UTF8",
			wantErr: `encoding conversion to or from "SQL_ASCII" is not supported`, wantCode: "42P17"},
		{name: "sql_ascii destination rejected", forName: "UTF8", toName: "SQL_ASCII",
			wantErr: `encoding conversion to or from "SQL_ASCII" is not supported`, wantCode: "42P17"},
		// An unknown endpoint is reported before the SQL_ASCII check, matching PG's
		// ordering (pg_char_to_encoding < 0 raises first).
		{name: "unknown beats sql_ascii", forName: "NOPE", toName: "SQL_ASCII",
			wantErr: `source encoding "NOPE" does not exist`, wantCode: "42704"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEnc, toEnc, err := validateCreateConversionEncodings(tc.forName, tc.toName)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				if forEnc != tc.wantFor || toEnc != tc.wantTo {
					t.Fatalf("encodings = (%d,%d), want (%d,%d)", forEnc, toEnc, tc.wantFor, tc.wantTo)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("expected *ExecError, got %T", err)
			}
			if ee.Code != tc.wantCode {
				t.Fatalf("SQLSTATE = %q, want %q", ee.Code, tc.wantCode)
			}
		})
	}
}

// TestResolveConversionFunc exercises the FROM-function checks PostgreSQL
// performs in CreateConversionCommand after the encoding-name checks pass
// (DU-002 slice 401 deferral (b)-remainder): the function must resolve to a
// routine with the fixed signature (int4, int4, cstring, internal, int4,
// bool) -> int4, mirroring LookupFuncName + get_func_rettype.
func TestResolveConversionFunc(t *testing.T) {
	validArgs := []catalog.Type{{Name: "integer"}, {Name: "int4"}, {Name: "cstring"}, {Name: "internal"}, {Name: "integer"}, {Name: "boolean"}}

	cases := []struct {
		name     string
		routines []*catalog.Routine // pre-registered overloads of "public.myconv_func"
		wantErr  string             // substring; "" = success
		wantCode string
	}{
		{name: "missing function",
			wantErr:  `function public.myconv_func(integer, integer, cstring, internal, integer, boolean) does not exist`,
			wantCode: "42883"},
		{name: "valid signature ok",
			routines: []*catalog.Routine{{Name: "myconv_func", ArgTypes: validArgs, ReturnType: catalog.Type{Name: "integer"}}}},
		{name: "wrong arg count rejected",
			routines: []*catalog.Routine{{Name: "myconv_func", ArgTypes: validArgs[:5], ReturnType: catalog.Type{Name: "integer"}}},
			wantErr:  `function public.myconv_func(integer, integer, cstring, internal, integer, boolean) does not exist`,
			wantCode: "42883"},
		{name: "wrong arg type rejected",
			routines: []*catalog.Routine{{Name: "myconv_func",
				ArgTypes:   []catalog.Type{{Name: "integer"}, {Name: "integer"}, {Name: "text"}, {Name: "internal"}, {Name: "integer"}, {Name: "boolean"}},
				ReturnType: catalog.Type{Name: "integer"}}},
			wantErr:  `function public.myconv_func(integer, integer, cstring, internal, integer, boolean) does not exist`,
			wantCode: "42883"},
		{name: "wrong return type rejected",
			routines: []*catalog.Routine{{Name: "myconv_func", ArgTypes: validArgs, ReturnType: catalog.Type{Name: "boolean"}}},
			wantErr:  `encoding conversion function public.myconv_func must return type integer`,
			wantCode: "42P17"},
		{name: "out arg excluded from signature count",
			routines: []*catalog.Routine{{Name: "myconv_func",
				ArgTypes:   append(append([]catalog.Type{}, validArgs...), catalog.Type{Name: "text"}),
				ArgModes:   []string{"i", "i", "i", "i", "i", "i", "o"},
				ReturnType: catalog.Type{Name: "integer"}}}},
		{name: "overload set picks the matching signature",
			routines: []*catalog.Routine{
				{Name: "myconv_func", ArgTypes: []catalog.Type{{Name: "text"}}, ReturnType: catalog.Type{Name: "integer"}},
				{Name: "myconv_func", ArgTypes: validArgs, ReturnType: catalog.Type{Name: "integer"}},
			}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := catalog.NewRoutines()
			for _, r := range tc.routines {
				clone := *r
				clone.Schema = "public"
				if _, err := rs.Create(&clone, false); err != nil {
					t.Fatalf("seed routine: %v", err)
				}
			}
			name := parser.ObjectName{Schema: "public", Name: "myconv_func"}
			r, err := resolveConversionFunc(rs, name)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				if r == nil {
					t.Fatalf("expected a resolved routine")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("expected *ExecError, got %T", err)
			}
			if ee.Code != tc.wantCode {
				t.Fatalf("SQLSTATE = %q, want %q", ee.Code, tc.wantCode)
			}
		})
	}
}

// TestResolveConversionFuncBuiltinFallback exercises the fallback to
// catalog.LookupBuiltinProc when the FROM function is not a user-created
// routine (mirrors resolveTransformFunc's identical fallback). PG's real
// encoding-conversion functions (e.g. iso8859_1_to_utf8, the FROM function in
// upstream pg_dump's `002_pg_dump.pl` "CREATE CONVERSION dump_test.test_conversion"
// fixture) are pg_catalog builtins with no user-routine registry entry.
func TestResolveConversionFuncBuiltinFallback(t *testing.T) {
	rs := catalog.NewRoutines()

	t.Run("unqualified builtin resolves", func(t *testing.T) {
		r, err := resolveConversionFunc(rs, parser.ObjectName{Name: "iso8859_1_to_utf8"})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if r == nil || r.OID != 4374 {
			t.Fatalf("resolved routine = %+v, want OID 4374", r)
		}
	})
	t.Run("pg_catalog-qualified builtin resolves", func(t *testing.T) {
		r, err := resolveConversionFunc(rs, parser.ObjectName{Schema: "pg_catalog", Name: "iso8859_1_to_utf8"})
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if r == nil || r.OID != 4374 {
			t.Fatalf("resolved routine = %+v, want OID 4374", r)
		}
	})
	t.Run("other-schema-qualified builtin stays unresolved", func(t *testing.T) {
		_, err := resolveConversionFunc(rs, parser.ObjectName{Schema: "public", Name: "iso8859_1_to_utf8"})
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42883" {
			t.Fatalf("expected 42883, got %v", err)
		}
	})
	t.Run("curated builtin with wrong signature stays unresolved", func(t *testing.T) {
		// int4recv is a real curated builtin but takes a single `internal`
		// argument, not the 6-arg conversion signature — must not match.
		_, err := resolveConversionFunc(rs, parser.ObjectName{Name: "int4recv"})
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42883" {
			t.Fatalf("expected 42883, got %v", err)
		}
	})
}
