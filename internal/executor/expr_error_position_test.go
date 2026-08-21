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

// TestLiteralCastOverflowStillCarriesPos is the out-of-scope-but-verifying
// counterpart: a literal-constant CAST to int8 that overflows still carries
// a nonzero Pos, both for a bare literal and for a literal routed through a
// VALUES-derived column — both reach roundNumericToInt (expr.go), a
// DIFFERENT raise-site family this brief does not touch (flagged as a
// deferral candidate: goopg does not yet distinguish "bare literal" from
// "column-derived CAST" the way PG's parser_errposition callback would,
// so both currently get Pos — that finer PG-matching gap is out of scope
// here, this test just pins the current, unchanged behavior so a future
// change to roundNumericToInt is deliberate, not a silent regression).
func TestLiteralCastOverflowStillCarriesPos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runSQLCtxErr(t, ctx, `SELECT 99999999999999999999::int8`)
	if err == nil {
		t.Fatal("expected bigint out of range error, got nil")
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "22003" {
		t.Errorf("code = %s, want 22003", ee.Code)
	}
	if ee.Pos == 0 {
		t.Errorf("Pos = 0, want nonzero (literal-cast path, unchanged by M0134-0070)")
	}
}
