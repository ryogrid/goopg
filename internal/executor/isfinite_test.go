package executor

import "testing"

// TestIsFiniteNullPropagates pins isfinite()'s NULL handling (M0097-0004
// follow-up): evalIsFinite previously computed `!d.IsNull()` directly as the
// BoolDatum, so a NULL argument produced the value FALSE instead of SQL NULL.
// isfinite has no NotStrict marker for any of its pg_proc_seed_data.go OIDs
// (1373 date, 1389 timestamp, 1390 interval, 2048 timestamptz — see
// internal/initdb/pg_proc_seed_data.go), so like every other strict PostgreSQL
// function it must propagate a NULL argument to a NULL result rather than
// evaluating it. goopg v0 stores no infinity values at all, so a non-NULL
// argument is always finite (TRUE).
func TestIsFiniteNullPropagates(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	cases := []struct {
		sql      string
		wantNull bool
		want     string
	}{
		{"SELECT isfinite(NULL::date) FROM t", true, ""},
		{"SELECT isfinite(NULL::timestamp) FROM t", true, ""},
		{"SELECT isfinite(NULL::interval) FROM t", true, ""},
		{"SELECT isfinite(NULL::timestamptz) FROM t", true, ""},
		{"SELECT isfinite(date '2026-01-01') FROM t", false, "t"},
		{"SELECT isfinite(interval '1 day') FROM t", false, "t"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if got := rows[0][0].IsNull(); got != c.wantNull {
				t.Errorf("%s: IsNull() = %v, want %v", c.sql, got, c.wantNull)
			}
			if !c.wantNull {
				if got := rows[0][0].Format(); got != c.want {
					t.Errorf("%s = %q, want %q", c.sql, got, c.want)
				}
			}
		})
	}
}
