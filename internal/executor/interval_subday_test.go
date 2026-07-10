package executor

import "testing"

// TestFormatIntervalSubDay pins formatInterval's rendering of the
// sub-day (time) component against real PostgreSQL 18.3's EncodeInterval
// under the default 'postgres' IntervalStyle. Month/day-only cases must
// stay byte-identical to the pre-existing renderer (no regression); the
// new time-component cases exercise hours>24, fractional seconds, and
// per-field sign carrying.
func TestFormatIntervalSubDay(t *testing.T) {
	const us = int64(1_000_000)
	cases := []struct {
		months, days int32
		micros       int64
		want         string
	}{
		// Month/day-only — unchanged behaviour.
		{0, 0, 0, "00:00:00"},
		{0, 1, 0, "1 day"},
		{1, 5, 0, "1 mon 5 days"},
		{13, 0, 0, "1 year 1 mon"},
		{-1, -5, 0, "-1 mons -5 days"},
		// Pure time component.
		{0, 0, 12 * 3600 * us, "12:00:00"},
		{0, 0, 90000 * us, "25:00:00"}, // 25h — hours are not folded into days
		{0, 0, 30*60*us + 15*us, "00:30:15"},
		{0, 0, 500000, "00:00:00.5"},         // half a second
		{0, 0, 1500000, "00:00:01.5"},        // 1.5s
		{0, 0, 123456, "00:00:00.123456"},    // full micro precision
		// Day + time.
		{0, 1, 12 * 3600 * us, "1 day 12:00:00"},
		{0, 2, 0, "2 days"},
		// Negative day + time (result of a reversed timestamp subtraction).
		{0, -1, -12 * 3600 * us, "-1 days -12:00:00"},
		// Mixed sign: negative month, positive time → PG forces a '+'.
		{-1, 0, 2 * 3600 * us, "-1 mons +02:00:00"},
	}
	for _, c := range cases {
		got := formatInterval(c.months, c.days, c.micros)
		if got != c.want {
			t.Errorf("formatInterval(%d,%d,%d) = %q, want %q", c.months, c.days, c.micros, got, c.want)
		}
	}
}

// TestTimestampSubtractionInterval drives timestamp − timestamp (interval
// result), date − date (integer days), and interval ± interval end-to-end
// through the SQL executor, asserting the rendered output matches
// PostgreSQL 18.3.
func TestTimestampSubtractionInterval(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT timestamp '2020-01-03 00:00:00' - timestamp '2020-01-01 00:00:00'", "2 days"},
		{"SELECT timestamp '2020-01-01 12:30:00' - timestamp '2020-01-01 00:00:00'", "12:30:00"},
		{"SELECT timestamp '2020-01-02 12:00:00' - timestamp '2020-01-01 00:00:00'", "1 day 12:00:00"},
		{"SELECT timestamp '2020-01-01 00:00:00' - timestamp '2020-01-02 12:00:00'", "-1 days -12:00:00"},
		// NOTE: upstream date_mi returns integer 9; goopg represents DATE as a
		// timestamp so date − date yields an interval (documented divergence,
		// deferral_ledger.md).
		{"SELECT date '2020-01-10' - date '2020-01-01'", "9 days"},
		{"SELECT (timestamp '2020-01-02 12:00:00' - timestamp '2020-01-01 00:00:00') + interval '1 day'", "2 days 12:00:00"},
		{"SELECT interval '3 day' - interval '1 day'", "2 days"},
		// timestamp + (interval carrying sub-day micros): the diff is 1 day
		// 06:00:00, added back onto the base timestamp. goopg always renders
		// timestamps with 6 fractional digits.
		{"SELECT timestamp '2020-01-01 00:00:00' + (timestamp '2020-01-02 06:00:00' - timestamp '2020-01-01 00:00:00')", "2020-01-02 06:00:00.000000"},
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.sql+" FROM t")
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: expected 1x1 result, got %v", c.sql, rows)
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}
}

// TestSubDayIntervalLiterals drives sub-day interval literals
// (hour/minute/second/millisecond, singular + plural) through all four
// unit-parsing paths end-to-end: the trailing-unit typed literal
// (Form 1 `interval '2' hour`), the embedded-unit typed literal
// (Form 2 `interval '2 hours'`), the `::interval` cast, and interval
// arithmetic that combines a sub-day component with day/hour parts.
// Outputs are pinned to PostgreSQL 18.3 (default IntervalStyle=postgres).
// unimplemented_feat #5 — parser half of sub-day interval units.
func TestSubDayIntervalLiterals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	cases := []struct{ sql, want string }{
		// Form 2: embedded unit in the string literal.
		{"SELECT interval '2 hours'", "02:00:00"},
		{"SELECT interval '2 hour'", "02:00:00"},
		{"SELECT interval '90 minutes'", "01:30:00"},
		{"SELECT interval '45 seconds'", "00:00:45"},
		{"SELECT interval '500 milliseconds'", "00:00:00.5"},
		{"SELECT interval '-3 hours'", "-03:00:00"},
		// Form 1: trailing-unit identifier.
		{"SELECT interval '2' hour", "02:00:00"},
		{"SELECT interval '30' minute", "00:30:00"},
		{"SELECT interval '5' second", "00:00:05"},
		// Cast forms.
		{"SELECT '3 hours'::interval", "03:00:00"},
		{"SELECT CAST('15 minutes' AS interval)", "00:15:00"},
		// Arithmetic combining sub-day with larger units.
		{"SELECT interval '2 hours' + interval '30 minutes'", "02:30:00"},
		{"SELECT interval '1 day' + interval '2 hours'", "1 day 02:00:00"},
		{"SELECT interval '1 hour' - interval '90 minutes'", "-00:30:00"},
		// timestamp + sub-day interval literal.
		{"SELECT timestamp '2020-01-01 00:00:00' + interval '2 hours'", "2020-01-01 02:00:00.000000"},
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.sql+" FROM t")
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: expected 1x1 result, got %v", c.sql, rows)
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}
}

// TestFractionalIntervalLiterals covers fractional interval magnitudes
// (`interval '1.5 hours'`). Every `want` was captured byte-for-byte from a
// real PostgreSQL 18.3 instance. Two distinct semantics are exercised:
//
//   - the typmod-free forms (Form 2 embedded string + `::interval`/CAST)
//     spill the fraction into smaller units per PG's DecodeInterval
//     (1.5 hours → 01:30:00, 1.5 months → 1 mon 15 days);
//   - the trailing-qualifier form (Form 1, `interval '1.5' hour`) applies
//     an SQL interval typmod that TRUNCATES below the field's granularity
//     (1.5 hours → 01:00:00), toward zero for negatives.
func TestFractionalIntervalLiterals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	cases := []struct{ sql, want string }{
		// Form 2 (embedded string, no typmod): fraction spills down.
		{"SELECT interval '1.5 hours'", "01:30:00"},
		{"SELECT interval '0.5 hours'", "00:30:00"},
		{"SELECT interval '1.5 minutes'", "00:01:30"},
		{"SELECT interval '1.5 seconds'", "00:00:01.5"},
		{"SELECT interval '0.5 seconds'", "00:00:00.5"},
		{"SELECT interval '1.5 days'", "1 day 12:00:00"},
		{"SELECT interval '2.5 days'", "2 days 12:00:00"},
		{"SELECT interval '1.5 months'", "1 mon 15 days"},
		{"SELECT interval '2.5 months'", "2 mons 15 days"},
		{"SELECT interval '1.15 months'", "1 mon 4 days 12:00:00"},
		{"SELECT interval '1.5 years'", "1 year 6 mons"},
		{"SELECT interval '0.5 years'", "6 mons"},
		{"SELECT interval '1.5 milliseconds'", "00:00:00.0015"},
		{"SELECT interval '-1.5 hours'", "-01:30:00"},
		{"SELECT interval '-0.5 hours'", "-00:30:00"},
		// Cast / :: forms (no typmod either): same spill semantics.
		{"SELECT '1.5 hours'::interval", "01:30:00"},
		{"SELECT CAST('2.5 days' AS interval)", "2 days 12:00:00"},
		// Form 1 (trailing qualifier): typmod truncates below the field.
		{"SELECT interval '1.5' hour", "01:00:00"},
		{"SELECT interval '1.9' hour", "01:00:00"},
		{"SELECT interval '25.5' hour", "25:00:00"},
		{"SELECT interval '1.5' minute", "00:01:00"},
		{"SELECT interval '90.5' minute", "01:30:00"},
		{"SELECT interval '1.5' second", "00:00:01.5"},
		{"SELECT interval '1.5' day", "1 day"},
		{"SELECT interval '1.5' month", "1 mon"},
		{"SELECT interval '1.5' year", "1 year"},
		// Form 1 truncation is toward zero for negatives.
		{"SELECT interval '-1.9' hour", "-01:00:00"},
		{"SELECT interval '-90.9' minute", "-01:30:00"},
		{"SELECT interval '-1.9' year", "-1 years"},
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.sql+" FROM t")
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: expected 1x1 result, got %v", c.sql, rows)
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}
}
