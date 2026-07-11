package executor

import (
	"strings"
	"testing"
)

// TestTimestampSubInfinity pins `timestamp − timestamp` when one or both
// operands are goopg's ±infinity timestamp sentinels (unimplemented_feat
// #5(d-iv) follow-up). subTimeTime previously called TimeValue() directly on
// both operands, silently treating the INT64-extreme sentinel as an ordinary
// (~year 2262 / far-past) timestamp and producing a nonsense finite interval.
// It now mirrors upstream timestamp_mi's infinity block exactly
// (postgres/src/backend/utils/adt/timestamp.c): a single infinite operand
// yields the correspondingly-signed infinite interval, while any
// same-signed "infinity − infinity" is a 22008 "interval out of range" error
// (the interval type has no NaN). Timestamp sentinels are reachable via
// `timestamp ± interval 'infinity'` arithmetic. Every `want` is
// byte-identical to upstream PostgreSQL 18.3 (verified on 18.3, socket
// /tmp:5599).
func TestTimestampSubInfinity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	const posInf = "(timestamp '2001-02-16 20:38:40' + interval 'infinity')"
	const negInf = "(timestamp '2001-02-16 20:38:40' - interval 'infinity')"
	const fin = "timestamp '1999-01-01 00:00:00'"

	cases := []struct {
		sql  string
		want string
	}{
		// +inf − finite = +inf ; -inf − finite = -inf
		{"SELECT " + posInf + " - " + fin + " FROM t", "infinity"},
		{"SELECT " + negInf + " - " + fin + " FROM t", "-infinity"},
		// finite − +inf = -inf ; finite − -inf = +inf
		{"SELECT " + fin + " - " + posInf + " FROM t", "-infinity"},
		{"SELECT " + fin + " - " + negInf + " FROM t", "infinity"},
		// mixed-sign infinities: +inf − -inf = +inf ; -inf − +inf = -inf
		{"SELECT " + posInf + " - " + negInf + " FROM t", "infinity"},
		{"SELECT " + negInf + " - " + posInf + " FROM t", "-infinity"},
		// finite control (justify_hours: 777 days 20:38:40 upstream)
		{"SELECT timestamp '2001-02-16 20:38:40' - " + fin + " FROM t", "777 days 20:38:40"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if rows[0][0].IsNull() {
				t.Fatalf("%s: got NULL, want %q", c.sql, c.want)
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}

	// Same-signed infinity − infinity → 22008 "interval out of range".
	errCases := []string{
		"SELECT " + posInf + " - " + posInf + " FROM t",
		"SELECT " + negInf + " - " + negInf + " FROM t",
	}
	for _, sql := range errCases {
		t.Run("err:"+sql, func(t *testing.T) {
			_, err := runQueryErr(t, ctx, sql)
			if err == nil {
				t.Fatalf("%s: got no error, want 'interval out of range'", sql)
			}
			if !strings.Contains(err.Error(), "interval out of range") {
				t.Errorf("%s: error = %q, want to contain 'interval out of range'", sql, err.Error())
			}
		})
	}
}
