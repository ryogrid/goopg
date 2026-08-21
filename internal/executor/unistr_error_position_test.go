package executor

import (
	"errors"
	"strings"
	"testing"
)

// TestUnistrErrorsCarryNoPos verifies the unistr-pos-strip follow-on to
// M0134-0070: goopg no longer attaches a source position to ExecErrors
// raised by unistr(text) (internal/executor/unistr.go), matching PostgreSQL
// — postgres/src/backend/utils/adt/varlena.c:6762-6929 (unistr) never calls
// errposition() at any of its ereport(ERROR, ...) sites, so the server never
// renders "LINE N: ... ^" for these errors.
func TestUnistrErrorsCarryNoPos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name    string
		sql     string
		code    string
		message string
	}{
		{
			name:    "invalid escape",
			sql:     `SELECT unistr('\zzzz')`,
			code:    "42601",
			message: "invalid Unicode escape",
		},
		{
			name:    "invalid code point (> 0x10FFFF)",
			sql:     `SELECT unistr('\+110000')`,
			code:    "22023",
			message: "invalid Unicode code point",
		},
		{
			name:    "lone low surrogate (invalid pair)",
			sql:     `SELECT unistr('\dc00')`,
			code:    "42601",
			message: "invalid Unicode surrogate pair",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSQLCtxErr(t, ctx, tc.sql)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.sql)
			}
			var ee *ExecError
			if !errors.As(err, &ee) {
				t.Fatalf("expected *ExecError for %q, got %T: %v", tc.sql, err, err)
			}
			if ee.Code != tc.code {
				t.Errorf("%s: code = %s, want %s (err=%v)", tc.name, ee.Code, tc.code, err)
			}
			if !strings.Contains(ee.Message, tc.message) {
				t.Errorf("%s: message = %q, want to contain %q", tc.name, ee.Message, tc.message)
			}
			if ee.Pos != 0 {
				t.Errorf("%s: Pos = %d, want 0 (PG attaches no errposition to unistr errors)", tc.name, ee.Pos)
			}
		})
	}
}
