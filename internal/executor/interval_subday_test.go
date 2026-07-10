package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

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

// TestMultiFieldIntervalLiterals covers multi-field interval bodies
// (`interval '1 day 05:00:00'`, `interval '1 year 2 mons 3 days'`) and bare
// `HH:MM[:SS[.ffffff]]` time bodies — the shapes goopg's own intervalout
// emits, so these are exactly the values a round-trip of goopg output must
// re-parse. Every `want` was captured byte-for-byte from a real PostgreSQL
// 18.3 instance. The parser's Form-2 typed-literal path and the executor's
// `::interval`/CAST path share one tokenizer (parser.ParseIntervalBody), so
// both entry points are exercised. unimplemented_feat #5(b).
func TestMultiFieldIntervalLiterals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	cases := []struct{ sql, want string }{
		// Date fields + a trailing HH:MM:SS time.
		{"SELECT interval '1 day 05:00:00'", "1 day 05:00:00"},
		{"SELECT interval '1 year 2 mons 3 days 04:05:06'", "1 year 2 mons 3 days 04:05:06"},
		{"SELECT interval '1 year 2 mons 3 days 04:05:06.789'", "1 year 2 mons 3 days 04:05:06.789"},
		{"SELECT interval '3 days 04:05:06'", "3 days 04:05:06"},
		{"SELECT interval '1 day 12:30:00.5'", "1 day 12:30:00.5"},
		// Multiple date fields, no time.
		{"SELECT interval '1 year 2 mons 3 days'", "1 year 2 mons 3 days"},
		{"SELECT interval '1 year 2 months'", "1 year 2 mons"},
		{"SELECT interval '2 mons'", "2 mons"},
		// Multiple sub-day fields as words.
		{"SELECT interval '1 day 2 hours 3 minutes 4 seconds'", "1 day 02:03:04"},
		{"SELECT interval '1 hr 30 mins'", "01:30:00"},
		{"SELECT interval '2 hrs 15 secs'", "02:00:15"},
		// Bare HH:MM[:SS[.f]] time bodies.
		{"SELECT interval '05:00:00'", "05:00:00"},
		{"SELECT interval '2:30:00'", "02:30:00"},
		{"SELECT interval '04:05'", "04:05:00"},
		{"SELECT interval '100:00:00'", "100:00:00"},
		{"SELECT interval '00:00:01.5'", "00:00:01.5"},
		{"SELECT interval '0 days'", "00:00:00"},
		// Fractional magnitude in a multi-field body spills down.
		{"SELECT interval '1.5 days 2 hours'", "1 day 14:00:00"},
		// Per-field signs: the leading '-' binds only its own field; the
		// time component carries its own sign independently.
		{"SELECT interval '-1 day 05:00:00'", "-1 days +05:00:00"},
		{"SELECT interval '1 day -05:00:00'", "1 day -05:00:00"},
		{"SELECT interval '-2 days 03:00:00'", "-2 days +03:00:00"},
		// Cast / :: forms use the same tokenizer.
		{"SELECT '1 day 05:00:00'::interval", "1 day 05:00:00"},
		{"SELECT CAST('1 year 2 mons 3 days' AS interval)", "1 year 2 mons 3 days"},
		// Arithmetic: micros are not normalised into days (matches PG).
		{"SELECT interval '10 days 20:00:00' + interval '1 day 05:00:00'", "11 days 25:00:00"},
		// Equality across an equivalent decomposition.
		{"SELECT (interval '3 days 04:05:06' = interval '3 days' + interval '4 hours 5 mins 6 secs')::text", "true"},
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

// TestGluedIntervalLiterals covers the glued `<magnitude><unit>` interval body
// forms (`2h30m`, `1.5h`, `1y2mon3d`, `-5h`) plus the field-mask collision
// rejections, both introduced by unimplemented_feat #5(d-iii). PostgreSQL's
// ParseDateTime lexer starts a fresh field at each digit↔letter boundary, so a
// glued literal decodes identically to its spaced equivalent — EXCEPT that a
// multi-letter unit glued before a digit is only accepted when the unit letters
// form a datetktbl token (`d`/`h`/`m`/`s`/`y`/`mon`/`dec`), so `1d2h`/`1mon2d`
// parse while `1day2h`/`1w2d` error. DecodeInterval's `tmask & fmask` check
// rejects a repeated field (`1h2h`, `1 mon 2 mons`) or a unit that collides with
// a `HH:MM:SS` time word (`1h 05:00:00`). Every `want` / rejection was captured
// from PostgreSQL 18.3.
func TestGluedIntervalLiterals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	accept := []struct{ sql, want string }{
		{"SELECT interval '1h'", "01:00:00"},
		{"SELECT interval '2h30m'", "02:30:00"},
		{"SELECT interval '1d'", "1 day"},
		{"SELECT interval '1m'", "00:01:00"},
		{"SELECT interval '90m'", "01:30:00"},
		{"SELECT interval '1.5h'", "01:30:00"},
		{"SELECT interval '1y2mon'", "1 year 2 mons"},
		{"SELECT interval '1y2mon3d'", "1 year 2 mons 3 days"},
		{"SELECT interval '1h30m'", "01:30:00"},
		{"SELECT interval '1h30m 5'", "01:30:05"},
		{"SELECT interval '1day'", "1 day"},
		{"SELECT interval '-5h'", "-05:00:00"},
		{"SELECT interval '2h30'", "02:00:30"},
		{"SELECT interval '1s2h'", "02:00:01"},
		{"SELECT interval '1d2h'", "1 day 02:00:00"},
		{"SELECT interval '1mon2day'", "1 mon 2 days"},
		{"SELECT interval '1dec2d'", "10 years 2 days"},
		{"SELECT interval '2h30m40s'", "02:30:40"},
		// Glued and spaced tokenise the same; the cast path shares the tokenizer.
		{"SELECT '2h30m'::interval", "02:30:00"},
		{"SELECT CAST('1y2mon3d' AS interval)", "1 year 2 mons 3 days"},
		// A trailing unitless number still defaults to seconds after glued units.
		{"SELECT interval '1h30m 40'", "01:30:40"},
	}
	for _, c := range accept {
		rows := runQuery(t, ctx, c.sql+" FROM t")
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: expected 1x1 result, got %v", c.sql, rows)
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}

	reject := []string{
		"SELECT interval '1days2hours' FROM t", // 'days' ∉ datetktbl → swallowed DTK_DATE
		"SELECT interval '1day2hour' FROM t",
		"SELECT interval '1days2h' FROM t",
		"SELECT interval '1mons2days' FROM t",
		"SELECT interval '1min2sec' FROM t",
		"SELECT interval '1w2d' FROM t", // 'w' ∉ datetktbl
		"SELECT interval '1c2d' FROM t", // 'c' ∉ datetktbl
		"SELECT interval '1h2h' FROM t", // HOUR field-mask collision
		"SELECT interval '1 mon 2 mons' FROM t",
		"SELECT interval '1-2 3 mons' FROM t", // year-month sets MONTH, then 3 mons collides
		"SELECT interval '1h 05:00:00' FROM t", // 1h HOUR vs time-word HOUR
		"SELECT interval '1.5 sec 200 ms' FROM t", // fractional SECOND (all-secs) vs ms
	}
	for _, sql := range reject {
		if _, err := runQueryErr(t, ctx, sql); err == nil {
			t.Errorf("%s: expected error, got none", sql)
		}
	}
}

// TestISO8601IntervalLiterals covers ISO 8601 duration interval bodies
// (unimplemented_feat #5(d-iii-rest)), which PostgreSQL decodes via
// DecodeISO8601Interval as a fallback when the free-form decoder fails
// (interval_in, postgres/src/backend/utils/adt/timestamp.c). Both the "format
// with designators" (`P1Y2M3DT4H5M6S`, `PT1H30M`, fractional fields, a `W` week
// field coexisting with others) and the "alternative format" — basic
// (`P00020607T013000`) and extended (`P0002-06-07T01:30:00`) — are exercised, at
// both the typed-literal and `::interval`/CAST entry points (they share
// parser.ParseIntervalBody). Every `want` / rejection was captured byte-for-byte
// from a live PostgreSQL 18.3 instance (port 5599).
func TestISO8601IntervalLiterals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	accept := []struct{ sql, want string }{
		// Format with designators.
		{"SELECT interval 'P1Y2M3DT4H5M6S'", "1 year 2 mons 3 days 04:05:06"},
		{"SELECT interval 'P1Y2M3DT4H5M6.789S'", "1 year 2 mons 3 days 04:05:06.789"},
		{"SELECT interval 'PT1H30M'", "01:30:00"},
		{"SELECT interval 'P1D'", "1 day"},
		{"SELECT interval 'P1W'", "7 days"},
		{"SELECT interval 'P1M'", "1 mon"},
		{"SELECT interval 'PT1M'", "00:01:00"}, // M after T is minutes, not months
		{"SELECT interval 'P2Y6M7DT1H30M'", "2 years 6 mons 7 days 01:30:00"},
		{"SELECT interval 'P0Y'", "00:00:00"},
		// Fractional fields spill into the next-smaller unit exactly as PG's
		// Adjust* helpers do.
		{"SELECT interval 'P1.5Y'", "1 year 6 mons"},
		{"SELECT interval 'P1.5M'", "1 mon 15 days"},
		{"SELECT interval 'P1.5D'", "1 day 12:00:00"},
		{"SELECT interval 'PT1.5H'", "01:30:00"},
		{"SELECT interval 'PT0.5M'", "00:00:30"},
		{"SELECT interval 'PT1.5S'", "00:00:01.5"},
		{"SELECT interval 'P1.5Y2M'", "1 year 8 mons"},
		// Per-field signs.
		{"SELECT interval 'P-1Y'", "-1 years"},
		{"SELECT interval 'PT-5H'", "-05:00:00"},
		// Alternative format — extended (hyphen/colon separated).
		{"SELECT interval 'P0002-06-07T01:30:00'", "2 years 6 mons 7 days 01:30:00"},
		{"SELECT interval 'P0001-02-03'", "1 year 2 mons 3 days"},
		{"SELECT interval 'P0002-06'", "2 years 6 mons"},
		{"SELECT interval 'PT01:30:00'", "01:30:00"},
		{"SELECT interval 'PT01:30'", "01:30:00"},
		// Alternative format — basic (packed digit runs).
		{"SELECT interval 'P00020607T013000'", "2 years 6 mons 7 days 01:30:00"},
		// Empty/short bodies and the fall-through year default.
		{"SELECT interval 'PT'", "00:00:00"},
		{"SELECT interval 'P1'", "1 year"}, // bare number = years (alt-format year)
		{"SELECT interval 'P1DT'", "1 day"},
		// Cast / :: forms share the tokenizer.
		{"SELECT 'P1Y2M3DT4H5M6S'::interval", "1 year 2 mons 3 days 04:05:06"},
		{"SELECT CAST('PT1H30M' AS interval)", "01:30:00"},
	}
	for _, c := range accept {
		rows := runQuery(t, ctx, c.sql+" FROM t")
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: expected 1x1 result, got %v", c.sql, rows)
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s = %q, want %q", c.sql, got, c.want)
		}
	}

	reject := []string{
		"SELECT interval 'P' FROM t",     // too short (< 2 chars)
		"SELECT interval 'PX' FROM t",    // no numeric field
		"SELECT interval 'P1Q' FROM t",   // Q is not a valid date unit
		"SELECT interval 'PT1X' FROM t",  // X is not a valid time unit
		"SELECT interval 'P1Y2' FROM t",  // trailing bare number after a field (havefield)
		"SELECT interval 'P1YY' FROM t",  // second field has no number
	}
	for _, sql := range reject {
		if _, err := runQueryErr(t, ctx, sql); err == nil {
			t.Errorf("%s: expected error, got none", sql)
		}
	}
}

// TestWeekDecadeCenturyIntervals covers the coarse interval units week /
// decade / century / millennium and their dec/cent/mil abbreviations
// (unimplemented_feat #5(c)). PG parses these only inside the interval body
// (`interval '3 weeks'`) — as a trailing token they are a column alias, not a
// typmod qualifier — so only the embedded / cast forms are exercised. Every
// `want` was captured from PostgreSQL 18.3 (intervalout): weeks scale to days,
// decade/century/millennium scale to months (1/10/100/1000 years).
func TestWeekDecadeCenturyIntervals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	cases := []struct{ sql, want string }{
		{"SELECT interval '3 weeks'", "21 days"},
		{"SELECT interval '2 decades'", "20 years"},
		{"SELECT interval '1 century'", "100 years"},
		{"SELECT interval '1 millennium'", "1000 years"},
		// Fractional coarse units spill exactly as AdjustFractDays/Years do.
		{"SELECT interval '1.5 weeks'", "10 days 12:00:00"},
		{"SELECT interval '1.5 decades'", "15 years"},
		{"SELECT interval '0.5 century'", "50 years"},
		// Multi-field bodies mix coarse units with each other and with a time.
		{"SELECT interval '2 centuries 5 decades'", "250 years"},
		{"SELECT interval '1 millennium 1 century'", "1100 years"},
		{"SELECT interval '1 year 2 weeks 3 days'", "1 year 17 days"},
		{"SELECT interval '1 century 2 weeks 04:05:06'", "100 years 14 days 04:05:06"},
		// Abbreviations dec / cent / mil.
		{"SELECT interval '3 dec'", "30 years"},
		{"SELECT interval '2 cent'", "200 years"},
		{"SELECT interval '5 mil'", "5000 years"},
		// Single-letter unit forms (PG deltatktbl): y/c/w/d/h/m/s. Critically
		// `m` is MINUTE in an interval literal, never month (unimplemented_feat
		// #5(d-ii)); values captured from PostgreSQL 18.3.
		{"SELECT interval '1 y'", "1 year"},
		{"SELECT interval '1 c'", "100 years"},
		{"SELECT interval '1 w'", "7 days"},
		{"SELECT interval '1 d'", "1 day"},
		{"SELECT interval '1 h'", "01:00:00"},
		{"SELECT interval '1 m'", "00:01:00"},
		{"SELECT interval '1 s'", "00:00:01"},
		{"SELECT interval '2 h 30 m'", "02:30:00"},
		{"SELECT interval '1 d 2 h 3 m 4 s'", "1 day 02:03:04"},
		{"SELECT interval '1.5 h'", "01:30:00"},
		// `m` stays minute even beside a YEAR field: `1 y 2 m` is 1 year + 2
		// minutes, not 1 year 2 months.
		{"SELECT interval '1 y 2 m'", "1 year 00:02:00"},
		// Single-letter forms via the cast / :: sibling path too.
		{"SELECT '3 w'::interval", "21 days"},
		{"SELECT CAST('90 m' AS interval)", "01:30:00"},
		// Microsecond unit + abbreviations (fractions below 1µs discarded).
		{"SELECT interval '500000 microseconds'", "00:00:00.5"},
		{"SELECT interval '1500000 us'", "00:00:01.5"},
		{"SELECT interval '250 usec'", "00:00:00.00025"},
		{"SELECT interval '3 weeks 2 days 06:00:00'", "23 days 06:00:00"},
		// Cast / :: form shares the same tokenizer.
		{"SELECT '2 weeks'::interval", "14 days"},
		{"SELECT CAST('3 decades' AS interval)", "30 years"},
		{"SELECT '1000 microseconds'::interval", "00:00:00.001"},
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

// TestTrailingBareNumberDefaultsToSeconds covers a unitless interval field
// defaulting to SECONDS (unimplemented_feat #5(d-i)) — PostgreSQL's
// DecodeInterval resolves an unspecified field via the default full-range
// typmod, which falls through to DTK_SECOND. The value is accepted only as a
// single trailing field with the SECOND slot still free; a non-final bare
// number, or one after a time word / explicit seconds unit, is a field-mask
// collision that errors (see TestIntervalCastFromStringInvalidSyntax). Every
// `want` was captured byte-for-byte from a real PostgreSQL 18.3 instance;
// both the typed-literal and `::interval`/CAST sibling paths are exercised.
func TestTrailingBareNumberDefaultsToSeconds(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	cases := []struct{ sql, want string }{
		// Lone bare number → seconds, normalised into HH:MM:SS.
		{"SELECT interval '5'", "00:00:05"},
		{"SELECT interval '90'", "00:01:30"},
		{"SELECT interval '3600'", "01:00:00"},
		{"SELECT interval '-5'", "-00:00:05"},
		// Fractional bare number spills the fraction into micros.
		{"SELECT interval '1.5'", "00:00:01.5"},
		{"SELECT interval '0.5'", "00:00:00.5"},
		// Trailing bare number after date/word fields (no seconds slot taken).
		{"SELECT interval '1 day 5'", "1 day 00:00:05"},
		{"SELECT interval '1 mon 5'", "1 mon 00:00:05"},
		{"SELECT interval '1 year 5'", "1 year 00:00:05"},
		{"SELECT interval '1 day 2 hours 5'", "1 day 02:00:05"},
		{"SELECT interval '5 minute 5'", "00:05:05"},
		// A millisecond unit occupies a distinct field mask, so a trailing bare
		// second is still allowed (matches PG's per-field bit tracking).
		{"SELECT interval '1 ms 5'", "00:00:05.001"},
		// Cast / :: forms share the same tokenizer.
		{"SELECT '5'::interval", "00:00:05"},
		{"SELECT CAST('90' AS interval)", "00:01:30"},
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

// TestYearMonthHyphenIntervals covers the SQL-standard "years-months" hyphen
// field (unimplemented_feat #5, year-month hyphen): a `<int>-<int>` token
// decodes to years*12 ± months (PostgreSQL's DecodeInterval DTK_NUMBER hyphen
// branch, type DTK_MONTH). A leading '-' flips both the year and month sign
// (`-1-2` → -14 months). The field is self-contained — it contributes months
// only and composes with other fields (`1-2 04:05:06`, `1-2 3` → trailing bare
// seconds, `1 year 1-2` → YEAR+MONTH bits are distinct). Every `want` was
// captured byte-for-byte from a real PostgreSQL 18.3 instance; both the
// typed-literal and `::interval`/CAST sibling paths are exercised.
func TestYearMonthHyphenIntervals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	cases := []struct{ sql, want string }{
		// Core year-month decompositions.
		{"SELECT interval '1-2'", "1 year 2 mons"},
		{"SELECT interval '0-5'", "5 mons"},
		{"SELECT interval '2-0'", "2 years"},
		{"SELECT interval '100-11'", "100 years 11 mons"},
		{"SELECT interval '10-6'", "10 years 6 mons"},
		{"SELECT interval '1-11'", "1 year 11 mons"},
		{"SELECT interval '0-0'", "00:00:00"},
		// Signs: a leading '-' flips BOTH the year and the month component;
		// '+' is accepted and ignored (PG prints -N as plural "years").
		{"SELECT interval '-1-2'", "-1 years -2 mons"},
		{"SELECT interval '-1-0'", "-1 years"},
		{"SELECT interval '-0-5'", "-5 mons"},
		{"SELECT interval '+1-2'", "1 year 2 mons"},
		// Composed with other fields. A year-month field contributes months
		// only, so it never occupies the SECOND slot: a trailing bare number
		// still defaults to seconds.
		{"SELECT interval '1-2 3 days'", "1 year 2 mons 3 days"},
		{"SELECT interval '3 days 1-2'", "1 year 2 mons 3 days"},
		{"SELECT interval '1-2 3'", "1 year 2 mons 00:00:03"},
		{"SELECT interval '1-2 04:05:06'", "1 year 2 mons 04:05:06"},
		// A YEAR field sets a distinct field-mask bit from the year-month
		// field's MONTH bit, so both compose (`1 year` + 14 months).
		{"SELECT interval '1 year 1-2'", "2 years 2 mons"},
		// Cast / :: forms share the same tokenizer.
		{"SELECT '1-2'::interval", "1 year 2 mons"},
		{"SELECT CAST('100-11' AS interval)", "100 years 11 mons"},
		// Equality across an equivalent decomposition.
		{"SELECT (interval '1-2' = interval '1 year 2 months')::text", "true"},
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

// TestParseIntervalBodySingleFieldMatchesUnitToParts guards the sibling-path
// invariant that the multi-field tokenizer (parser.ParseIntervalBody) and the
// per-field spill helper (parser.IntervalUnitToParts, used by the single-field
// typed-literal path) agree on every single `<magnitude> <unit>` input — so a
// future edit to one cannot silently drift the interval decoding of the other.
func TestParseIntervalBodySingleFieldMatchesUnitToParts(t *testing.T) {
	fields := []struct{ mag, unit string }{
		{"90", "day"}, {"-3", "hour"}, {"1.5", "hour"}, {"1.5", "month"},
		{"1.5", "year"}, {"0.5", "second"}, {"2", "minute"}, {"500", "millisecond"},
		{"1.15", "month"}, {"-1.9", "year"},
		{"3", "week"}, {"1.5", "week"}, {"2", "decade"}, {"0.5", "century"},
		{"1", "millennium"}, {"-1.5", "decade"},
	}
	for _, f := range fields {
		val, fval, ok := parser.ParseIntervalMagnitude(f.mag)
		if !ok {
			t.Fatalf("ParseIntervalMagnitude(%q) failed", f.mag)
		}
		wantMo, wantD, wantMu, wok := parser.IntervalUnitToParts(val, fval, f.unit)
		if !wok {
			t.Fatalf("IntervalUnitToParts(%q %q) failed", f.mag, f.unit)
		}
		gotMo, gotD, gotMu, gok := parser.ParseIntervalBody(f.mag + " " + f.unit)
		if !gok {
			t.Fatalf("ParseIntervalBody(%q %q) failed", f.mag, f.unit)
		}
		if gotMo != wantMo || gotD != wantD || gotMu != wantMu {
			t.Errorf("%q %q: ParseIntervalBody=(%d,%d,%d) IntervalUnitToParts=(%d,%d,%d)",
				f.mag, f.unit, gotMo, gotD, gotMu, wantMo, wantD, wantMu)
		}
	}
}
