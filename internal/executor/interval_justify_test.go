package executor

import "testing"

// TestJustifyIntervalFunctions pins justify_hours/justify_days/justify_interval
// (M0097-0004): all three were stubs that returned their argument unchanged.
// goopg's v0 KindInterval Datum carries only months+days (no sub-day field),
// so justify_hours — which upstream only ever uses to move a *time* remainder
// into days — is exactly the identity here; justify_days/justify_interval
// normalize whole 30-day chunks of the day field into the month field,
// mirroring interval_justify_days/interval_justify_interval
// (postgres/src/backend/utils/adt/timestamp.c). Expected values verified
// live against real PostgreSQL 18.3 (postgres/local_install).
func TestJustifyIntervalFunctions(t *testing.T) {
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
		{"SELECT justify_days(interval '35 days') FROM t", "1 mon 5 days"},
		{"SELECT justify_days(interval '-35 days') FROM t", "-1 mons -5 days"},
		{"SELECT justify_days(interval '30 days') FROM t", "1 mon"},
		{"SELECT justify_hours(interval '35 days') FROM t", "35 days"},
		{"SELECT justify_interval(interval '35 days') FROM t", "1 mon 5 days"},
		{"SELECT justify_interval(interval '-35 days') FROM t", "-1 mons -5 days"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}

// TestJustifyIntervalDaysSignReconciliation pins justifyIntervalDays' sign
// equalization branches (the part of interval_justify_days/
// interval_justify_interval this loop's SQL-level test above can't reach:
// goopg has no interval+interval addition operator to build a mixed-sign
// months+days value from typed-literal SQL, so this exercises the shared
// pure helper directly). Expected values verified live against real
// PostgreSQL 18.3: `justify_interval('5 mons -33 days')` = '3 mons 27 days',
// `justify_interval('-5 mons 33 days')` = '-3 mons -27 days'.
func TestJustifyIntervalDaysSignReconciliation(t *testing.T) {
	cases := []struct {
		months, days int32
		wantM, wantD int32
	}{
		{5, -33, 3, 27},
		{-5, 33, -3, -27},
		{0, 0, 0, 0},
		{2, 5, 2, 5}, // already normalized, same sign — no-op
	}
	for _, c := range cases {
		gotM, gotD := justifyIntervalDays(c.months, c.days)
		if gotM != c.wantM || gotD != c.wantD {
			t.Errorf("justifyIntervalDays(%d, %d) = (%d, %d), want (%d, %d)",
				c.months, c.days, gotM, gotD, c.wantM, c.wantD)
		}
	}
}
