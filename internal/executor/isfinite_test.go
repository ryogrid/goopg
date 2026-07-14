package executor

import "testing"

// TestIsFiniteNullPropagates pins isfinite()'s NULL handling (M0097-0004
// follow-up): evalIsFinite previously computed `!d.IsNull()` directly as the
// BoolDatum, so a NULL argument produced the value FALSE instead of SQL NULL.
// isfinite has no NotStrict marker for any of its pg_proc_seed_data.go OIDs
// (1373 date, 1389 timestamp, 1390 interval, 2048 timestamptz — see
// internal/initdb/pg_proc_seed_data.go), so like every other strict PostgreSQL
// function it must propagate a NULL argument to a NULL result rather than
// evaluating it. A finite non-NULL argument is TRUE; the ±infinity sentinels
// are covered by TestIsFiniteInfinity below.
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

// TestIsFiniteInfinity pins isfinite() over goopg's ±infinity sentinels
// (unimplemented_feat #5(d-iv) follow-up). PG's date_finite / timestamp_finite
// / interval_finite (postgres/src/backend/utils/adt/{date,timestamp}.c) return
// FALSE for a non-finite value; evalIsFinite previously always returned TRUE
// for any non-NULL argument, silently mis-reporting the interval sentinel
// (creatable directly as `interval 'infinity'`) and the timestamp sentinel
// (creatable via `timestamp ± interval 'infinity'` arithmetic) as finite.
// Every `want` is byte-identical to upstream PostgreSQL 18.3.
func TestIsFiniteInfinity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	cases := []struct {
		sql  string
		want string
	}{
		// interval sentinels — reachable directly from a typed literal.
		{"SELECT isfinite(interval 'infinity') FROM t", "f"},
		{"SELECT isfinite(interval '-infinity') FROM t", "f"},
		{"SELECT isfinite(interval '1 day') FROM t", "t"},
		// timestamp sentinels — reachable via timestamp ± interval 'infinity'.
		{"SELECT isfinite(timestamp '2001-02-16 20:38:40' + interval 'infinity') FROM t", "f"},
		{"SELECT isfinite(timestamp '2001-02-16 20:38:40' - interval 'infinity') FROM t", "f"},
		{"SELECT isfinite(timestamp '2001-02-16 20:38:40' + interval '-infinity') FROM t", "f"},
		{"SELECT isfinite(timestamp '2001-02-16 20:38:40') FROM t", "t"},
		{"SELECT isfinite(timestamptz '2001-02-16 20:38:40+00' + interval 'infinity') FROM t", "f"},
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
}
