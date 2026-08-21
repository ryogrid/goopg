package executor

import (
	"errors"
	"strings"
	"testing"
)

// TestAbsGcdLcmModErrorsCarryNoPos verifies the abs/gcd/lcm/mod-pos-strip
// follow-on to M0134-0070: goopg no longer attaches a source position to
// ExecErrors raised by abs()/gcd()/lcm()/mod() (internal/executor/expr.go),
// matching PostgreSQL — postgres/src/backend/utils/adt/int.c, int8.c,
// numeric.c never call errposition() at their overflow/division-by-zero
// ereport(ERROR, ...) sites for these builtins, so the server never renders
// "LINE N: ... ^" for these errors.
func TestAbsGcdLcmModErrorsCarryNoPos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		name    string
		sql     string
		code    string
		message string
	}{
		{
			name:    "abs bigint overflow (MinInt64)",
			sql:     `SELECT abs(-9223372036854775807 - 1)`,
			code:    "22003",
			message: "bigint out of range",
		},
		{
			name:    "gcd bigint overflow",
			sql:     `SELECT gcd(-9223372036854775807 - 1, -9223372036854775807 - 1)`,
			code:    "22003",
			message: "bigint out of range",
		},
		{
			name:    "lcm bigint overflow",
			sql:     `SELECT lcm(9223372036854775807, 9223372036854775806)`,
			code:    "22003",
			message: "bigint out of range",
		},
		{
			name:    "mod division by zero",
			sql:     `SELECT mod(5, 0)`,
			code:    "22012",
			message: "division by zero",
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
				t.Errorf("%s: Pos = %d, want 0 (PG attaches no errposition to abs/gcd/lcm/mod errors)", tc.name, ee.Pos)
			}
		})
	}
}
