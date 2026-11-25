package executor

import "testing"

// TestTimestampPrecisionIsApplied pins M0149-0001h1a45 against PG 18.3:
// timestamp(p) and timestamptz(p) round on storage and in casts, including
// precision 0, which is a real modifier and not "none".
func TestTimestampPrecisionIsApplied(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	exec := func(sql string) []Row {
		t.Helper()
		rows, err := runSQLCtxErr(t, ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return rows
	}
	one := func(sql string) string {
		t.Helper()
		rows := exec(sql)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: %d rows, want one value", sql, len(rows))
		}
		return rows[0][0].Format()
	}

	exec(`CREATE TABLE tp (ts0 timestamp(0), ts3 timestamp(3), plain timestamp)`)
	exec(`INSERT INTO tp VALUES ('2020-01-01 00:00:01.6', '2020-01-01 00:00:01.23456', '2020-01-01 00:00:01.654321')`)
	// Datum.Format prints six fractional digits; the wire layer trims them.
	for _, tc := range []struct{ sql, want string }{
		{`SELECT ts0 FROM tp`, "2020-01-01 00:00:02.000000"},
		{`SELECT ts3 FROM tp`, "2020-01-01 00:00:01.235000"},
		{`SELECT plain FROM tp`, "2020-01-01 00:00:01.654321"},
		// Before the PG epoch a half rounds away from it, so toward the past.
		{`SELECT '1999-12-31 23:59:58.5'::timestamp(0)`, "1999-12-31 23:59:58.000000"},
		{`SELECT '2020-12-31 23:59:59.9'::timestamp(0)`, "2021-01-01 00:00:00.000000"},
		{`SELECT '2020-01-01 00:00:01.26'::timestamp(1)`, "2020-01-01 00:00:01.300000"},
		{`SELECT 'infinity'::timestamp(0)`, "infinity"},
		{`SELECT '00:00:01.6'::time(0)`, "00:00:02"},
	} {
		if got := one(tc.sql); got != tc.want {
			t.Errorf("%s = %s, want %s", tc.sql, got, tc.want)
		}
	}
	if got := one(`UPDATE tp SET ts0 = '2020-06-01 00:00:00.5' RETURNING ts0`); got != "2020-06-01 00:00:01.000000" {
		t.Errorf("UPDATE ... RETURNING = %s, want the rounded value", got)
	}
}
