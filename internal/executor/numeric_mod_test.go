package executor

import (
	"strings"
	"testing"
)

// TestNumericMod pins M0146-0044 against PG 18.3: mod(numeric, numeric) and
// `numeric % numeric` are numeric_mod — exact, x - trunc(x/y)*y, the
// dividend's sign, scale max(dscale) — and mod() resolves to the numeric
// overload when either argument is numeric, otherwise to the widest integer.
func TestNumericMod(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, c := range []struct{ sql, want string }{
		{"SELECT mod(10.5::numeric, 3), mod(12345678901234567890::numeric, 123), mod(999999999999999999999::numeric, 1000000000000000000000)",
			"1.5|78|999999999999999999999"},
		{"SELECT 999::numeric % 7, 10.5 % 3, -10.5 % 3, 10.5 % -3, 7.25 % 0.5, 1.000 % 0.3, -7 % 2.5",
			"5|1.5|-1.5|1.5|0.25|0.100|-2.0"},
		{"SELECT mod(-10, 3), mod(10, -3), 10 % 3, mod(5::int2, 3), mod(7::int8, 2), mod(7, 2.5)",
			"-1|1|1|2|1|2.0"},
		{"SELECT pg_typeof(mod(10.5, 3))::text, pg_typeof(10.5 % 3)::text, pg_typeof(mod(10, 3))::text, pg_typeof(mod(5::int2, 3::int2))::text, pg_typeof(mod(5::int8, 3))::text",
			"numeric|numeric|integer|smallint|bigint"},
	} {
		rows := runSQL(t, ctx, c.sql)
		parts := make([]string, len(rows[0]))
		for i, d := range rows[0] {
			parts[i] = datumTestString(d)
		}
		if got := strings.Join(parts, "|"); got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
	for _, sql := range []string{"SELECT mod(1.5, 0)", "SELECT 1.5 % 0"} {
		if _, err := runQueryWithErr(ctx, sql); err == nil || !strings.Contains(err.Error(), "division by zero") {
			t.Errorf("%s: err = %v, want division by zero", sql, err)
		}
	}
}
