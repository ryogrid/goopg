package executor

import (
	"errors"
	"strings"
	"testing"
)

// TestRuntimeEvalErrorsCarryNoPos verifies M0134-0070: goopg no longer
// attaches a source position to ExecErrors raised by pure row-by-row runtime
// evaluation (arithmetic overflow/division-by-zero, invalid regex,
// substring), matching PostgreSQL — the server never renders "LINE N:" text
// itself, it only sets ErrorData.cursorpos via errposition()
// (postgres/src/backend/utils/error/elog.c:1468), and PG's own runtime
// operator/function C code (int8.c:445-530, int.c, timestamp.c, regexp.c,
// varlena.c, pg_lsn.c — none of them call errposition) never sets it. A
// non-literal (column) operand is used throughout so these cases cannot be
// confused with the (out-of-scope, still Pos>0) literal-cast-at-parse-time
// convention docs/design/0134-0001 established.
func TestRuntimeEvalErrorsCarryNoPos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE m0134_0070_t (i2 smallint, i4 int, i8 bigint, s text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO m0134_0070_t VALUES (32767, 2147483647, 0, 'x')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	cases := []struct {
		name    string
		sql     string
		code    string
		message string
	}{
		{
			name:    "division by zero via column",
			sql:     `SELECT 1 / i8 FROM m0134_0070_t`,
			code:    "22012",
			message: "division by zero",
		},
		{
			name:    "smallint overflow via column",
			sql:     `SELECT i2 + 1::smallint FROM m0134_0070_t`,
			code:    "22003",
			message: "smallint out of range",
		},
		{
			// `i4 + <int-literal>` resolves to int8 (exprType widens) — the
			// int4 overflow arm is only reachable column-to-column or
			// through an explicit cast, mirroring expr_sibling_parity_test.go.
			name:    "integer overflow via column",
			sql:     `SELECT i4 + 1::int4 FROM m0134_0070_t`,
			code:    "22003",
			message: "integer out of range",
		},
		{
			name:    "invalid regex via column pattern",
			sql:     `SELECT s ~ ('(' || s) FROM m0134_0070_t`,
			code:    "2201B",
			message: "invalid regular expression",
		},
		{
			name:    "negative substring length via column",
			sql:     `SELECT substring(s FROM 1 FOR (i4 - 2147483647 - 2)) FROM m0134_0070_t`,
			code:    "22011",
			message: "negative substring length not allowed",
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
				t.Errorf("%s: Pos = %d, want 0 (PG attaches no errposition to runtime evaluation)", tc.name, ee.Pos)
			}
		})
	}
}

// TestNumericToIntCastOverflowsCarryNoPos verifies the roundNumericToInt
// (and int2/int4 narrowing-check) pos-strip follow-on to M0134-0070: goopg
// no longer attaches a source position to ExecErrors raised when a numeric
// value overflows a CAST to int2/int4/int8, matching PostgreSQL —
// postgres/src/backend/utils/adt/numeric.c numeric_int8_opt_error/
// numeric_int4_opt_error/numeric_int2's ereport(ERROR, ...) sites never
// call errposition(), so the server never renders "LINE N: ... ^" for these
// errors, regardless of whether the overflowing value came from a bare
// literal or a column. This corrects the (backwards) prior assumption in
// this file's predecessor test, TestLiteralCastOverflowStillCarriesPos,
// which asserted the opposite.
func TestNumericToIntCastOverflowsCarryNoPos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE m0134_0070_numint_t (n numeric)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO m0134_0070_numint_t VALUES (99999999999999999999)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	cases := []struct {
		name    string
		sql     string
		code    string
		message string
	}{
		{
			name:    "bigint overflow, scale-0 branch (literal)",
			sql:     `SELECT 99999999999999999999::int8`,
			code:    "22003",
			message: "bigint out of range",
		},
		{
			name:    "bigint overflow, scaled/rounding branch (literal)",
			sql:     `SELECT 9223372036854775807.5::int8`,
			code:    "22003",
			message: "bigint out of range",
		},
		{
			name:    "smallint overflow (literal)",
			sql:     `SELECT 99999.9::int2`,
			code:    "22003",
			message: "smallint out of range",
		},
		{
			// Note: a plain integer literal like `99999999999::int4` parses
			// as KindInt (int8) and hits the separate d.Int bounds check at
			// expr.go's KindInt CAST arm — a different, out-of-scope raise
			// site that still carries Pos. Forcing a numeric intermediate
			// (decimal literal) routes through roundNumericToInt's KindNumeric
			// arm, the site this brief actually strips Pos from. M0134-0070.
			name:    "integer overflow (literal, numeric-typed)",
			sql:     `SELECT 99999999999.0::int4`,
			code:    "22003",
			message: "integer out of range",
		},
		{
			name:    "bigint overflow, scale-0 branch (column-sourced)",
			sql:     `SELECT n::int8 FROM (VALUES (99999999999999999999::numeric)) t(n)`,
			code:    "22003",
			message: "bigint out of range",
		},
		{
			name:    "bigint overflow, scale-0 branch (real column)",
			sql:     `SELECT n::int8 FROM m0134_0070_numint_t`,
			code:    "22003",
			message: "bigint out of range",
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
				t.Errorf("%s: Pos = %d, want 0 (PG attaches no errposition to numeric-to-int CAST overflow)", tc.name, ee.Pos)
			}
		})
	}
}
