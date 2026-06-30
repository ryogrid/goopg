package executor

import (
	"strings"
	"testing"
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
