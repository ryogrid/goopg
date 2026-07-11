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
		// Trailing fractional-seconds run split off the year-month field: PG's
		// ParseDateTime lexer reads `1-2` as a DTK_DATE field and starts a fresh
		// DTK_NUMBER field at the '.', so the fraction decodes as seconds
		// (unimplemented_feat #5(d-iii-rest), year-month tokenizer quirk).
		{"SELECT interval '1-2.5'", "1 year 2 mons 00:00:00.5"},
		{"SELECT interval '0-2.5'", "2 mons 00:00:00.5"},
		{"SELECT interval '1-11.5'", "1 year 11 mons 00:00:00.5"},
		{"SELECT interval '2-11.999999'", "2 years 11 mons 00:00:00.999999"},
		{"SELECT interval '05-2.5'", "5 years 2 mons 00:00:00.5"},
		{"SELECT interval '1-2.'", "1 year 2 mons"},
		{"SELECT interval '1-2.0'", "1 year 2 mons"},
		{"SELECT interval '0-0.5'", "00:00:00.5"},
		// Empty month tail after the '.' split (`1-.5` → `1-` + `.5`).
		{"SELECT interval '1-.5'", "1 year 00:00:00.5"},
		{"SELECT interval '10-.5'", "10 years 00:00:00.5"},
		{"SELECT interval '9-.999999'", "9 years 00:00:00.999999"},
		{"SELECT interval '1-.0'", "1 year"},
		// The fraction remainder itself splits again into a magnitude + unit
		// word, so `.5day` = 12 hours (0.5 * 24h).
		{"SELECT interval '1-2.5day'", "1 year 2 mons 12:00:00"},
		// Bare year: an empty month tail with no fraction is a whole year-month
		// field contributing years only (PG's strtoint on the empty tail = 0).
		{"SELECT interval '1-'", "1 year"},
		{"SELECT interval '5-'", "5 years"},
		{"SELECT interval '100-'", "100 years"},
		{"SELECT interval '0-'", "00:00:00"},
		{"SELECT interval '-1-'", "-1 years"},
		{"SELECT '1-.5'::interval", "1 year 00:00:00.5"},
		{"SELECT CAST('1-2.5' AS interval)", "1 year 2 mons 00:00:00.5"},
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

	// A SIGNED year-month field with a fractional tail is lexed whole by PG (a
	// single DTK_TZ token) and rejected by its years-months branch; the extra
	// forms below likewise mismatch PG's tokenizer and error. Every rejection
	// was confirmed against a live PostgreSQL 18.3 instance.
	reject := []string{
		"SELECT interval '-1-2.5' FROM t",   // signed → whole DTK_TZ → bad format
		"SELECT interval '-0-1.5' FROM t",   // signed, even 0 years
		"SELECT interval '+1-2.5' FROM t",   // leading '+' is a sign too
		"SELECT interval '1-2.5.5' FROM t",  // two fractional runs
		"SELECT interval '1-2.5 3' FROM t",  // fraction pairs with trailing number
		"SELECT interval '1-2.5 mons' FROM t", // fraction+MONTH collides with year-month
		"SELECT interval '3 1-2.5' FROM t",  // bare number preceding the year-month
		"SELECT interval '1-2.5 3days' FROM t",
	}
	for _, sql := range reject {
		if _, err := runQueryErr(t, ctx, sql); err == nil {
			t.Errorf("%s: expected error, got none", sql)
		}
	}
}

// TestYearMonthTimeGluedUnitAbsorb covers a bare unit word swallowed as a no-op
// by a preceding year-month or time field (unimplemented_feat #5(d-iii-rest)).
// PostgreSQL's DecodeInterval reads fields right-to-left: a unit word sets the
// pending `type`/`parsing_unit_val`, and a year-month DTK_DATE field (which
// forces type=DTK_MONTH) or a DTK_TIME field (type=DTK_DAY) to its left resets
// both without consuming a magnitude, so the unit contributes nothing:
// `1-2h`/`1-2 h` → 1 year 2 mons, `12:00 h` → 12:00:00. A lone unit word
// anywhere else (leading, after a number pair, or after another absorbed unit)
// is DTERR_BAD_FORMAT. Glued forms tokenise the same as spaced ones: PG's
// DTK_DATE / DTK_TIME lexer stops at the first non-delimiter letter (only when
// the year-month part carries at least one month DIGIT — an empty month glues
// the letter into one malformed token, so `1-h`/`1-day` error). Every accept /
// reject was captured byte-for-byte from a live PostgreSQL 18.3 instance.
func TestYearMonthTimeGluedUnitAbsorb(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	accept := []struct{ sql, want string }{
		// Unit word absorbed after a year-month field, glued and spaced — any
		// unit designator works because it is discarded entirely.
		{"SELECT interval '1-2h'", "1 year 2 mons"},
		{"SELECT interval '1-2d'", "1 year 2 mons"},
		{"SELECT interval '1-2 h'", "1 year 2 mons"},
		{"SELECT interval '1-2mon'", "1 year 2 mons"},
		{"SELECT interval '1-2 days'", "1 year 2 mons"},
		{"SELECT interval '1-2day'", "1 year 2 mons"},
		{"SELECT interval '1-2 s'", "1 year 2 mons"},
		{"SELECT interval '1-2sec'", "1 year 2 mons"},
		{"SELECT interval '1-2minute'", "1 year 2 mons"},
		{"SELECT interval '1-2 hour'", "1 year 2 mons"},
		{"SELECT interval '1-2year'", "1 year 2 mons"},
		{"SELECT interval '1-2 mons'", "1 year 2 mons"},
		{"SELECT interval '10-3d'", "10 years 3 mons"},
		{"SELECT interval '1-0h'", "1 year"},
		// Absorbed unit followed by more real fields.
		{"SELECT interval '1-2 h 3 d'", "1 year 2 mons 3 days"},
		{"SELECT interval '1-2 3d'", "1 year 2 mons 3 days"},
		{"SELECT interval '1-2 days 3'", "1 year 2 mons 00:00:03"},
		{"SELECT interval '1-2 day 3 hour'", "1 year 2 mons 03:00:00"},
		{"SELECT interval '1-2 h -3 d'", "1 year 2 mons -3 days"},
		{"SELECT interval '1-2 h 12:00'", "1 year 2 mons 12:00:00"},
		// Glued forms that split at the letter and re-tokenise the remainder.
		{"SELECT interval '1-2h30m'", "1 year 2 mons 00:30:00"},
		{"SELECT interval '1-2mon3d'", "1 year 2 mons 3 days"},
		{"SELECT interval '1-2.5h'", "1 year 2 mons 00:30:00"},
		// Unit word absorbed after a time field (`12:00 h`, glued `12:00h`).
		{"SELECT interval '12:00 h'", "12:00:00"},
		{"SELECT interval '12:00h'", "12:00:00"},
		{"SELECT interval '12:00mon'", "12:00:00"},
		{"SELECT interval '12:00:00h'", "12:00:00"},
		{"SELECT interval '05:00 mon'", "05:00:00"},
		{"SELECT interval '12:00 h 3 d'", "3 days 12:00:00"},
		// SIGNED year-month glued unit word. PG lexes a sign-prefixed field as one
		// DTK_TZ token (collects digits/':'/'.'/'-', stops at a letter), so the
		// glued unit splits off and is absorbed exactly as in the unsigned case,
		// but the sign flows into BOTH the year and month components
		// (postgres DecodeInterval `if (*field[i] == '-') val2 = -val2`).
		{"SELECT interval '-1-2h'", "-1 years -2 mons"},
		{"SELECT interval '+1-2h'", "1 year 2 mons"},
		{"SELECT interval '-1-2 days'", "-1 years -2 mons"},
		{"SELECT interval '-1-2day'", "-1 years -2 mons"},
		{"SELECT interval '-1-2sec'", "-1 years -2 mons"},
		{"SELECT interval '-1-2minute'", "-1 years -2 mons"},
		{"SELECT interval '-1-0h'", "-1 years"},
		{"SELECT interval '-10-3d'", "-10 years -3 mons"},
		// The DTK_TZ token collects the '-' even with an empty month, so a glued
		// letter after a bare signed year splits (`-1-h` → `-1-` + `h`) and the
		// year decodes alone — unlike the unsigned `1-h`, which errors.
		{"SELECT interval '-1-h'", "-1 years"},
		{"SELECT interval '-1-day'", "-1 years"},
		// Absorbed signed unit followed by more real fields (each keeps its own
		// sign; the year-month sign does NOT propagate to later fields).
		{"SELECT interval '-1-2 h 3 d'", "-1 years -2 mons +3 days"},
		{"SELECT interval '-1-2 3d'", "-1 years -2 mons +3 days"},
		{"SELECT interval '-1-2 days 3'", "-1 years -2 mons +00:00:03"},
		{"SELECT interval '-1-2 h -3 d'", "-1 years -2 mons -3 days"},
		{"SELECT interval '-1-2 h -3'", "-1 years -2 mons -00:00:03"},
		{"SELECT interval '-1-2 h 12:00'", "-1 years -2 mons +12:00:00"},
		{"SELECT interval '-1-2mon3d'", "-1 years -2 mons +3 days"},
		{"SELECT interval '-1-2h30m'", "-1 years -2 mons +00:30:00"},
		{"SELECT interval '+10-3mon2d'", "10 years 3 mons 2 days"},
		// '+'-separated numeric continuation: PG's lexer always starts a fresh
		// field at a sign, so a '+' ends the year-month DTK_DATE/DTK_TZ token
		// and the remainder decodes as its own sign-led field (default unit
		// SECONDS for a trailing bare number). Works off signed and unsigned
		// year-month heads, empty months, and composes with glued unit words,
		// times and further fields.
		{"SELECT interval '-1-2+3'", "-1 years -2 mons +00:00:03"},
		{"SELECT interval '+1-2+3'", "1 year 2 mons 00:00:03"},
		{"SELECT interval '1-2+3'", "1 year 2 mons 00:00:03"},
		{"SELECT interval '1-+3'", "1 year 00:00:03"},
		{"SELECT interval '-1-+3'", "-1 years +00:00:03"},
		{"SELECT interval '-1-2+3.5'", "-1 years -2 mons +00:00:03.5"},
		{"SELECT interval '-1-2+3s'", "-1 years -2 mons +00:00:03"},
		{"SELECT interval '-1-2+3h'", "-1 years -2 mons +03:00:00"},
		{"SELECT interval '-1-2+3 h'", "-1 years -2 mons +03:00:00"},
		{"SELECT interval '-1-2+3.5h'", "-1 years -2 mons +03:30:00"},
		{"SELECT interval '-1-2+3h30m'", "-1 years -2 mons +03:30:00"},
		{"SELECT interval '-1-2+3:30'", "-1 years -2 mons +03:30:00"},
		{"SELECT interval '-1-+3h'", "-1 years +03:00:00"},
		{"SELECT interval '1-2+3d'", "1 year 2 mons 3 days"},
		{"SELECT interval '1-2+3 days'", "1 year 2 mons 3 days"},
		{"SELECT interval '-1-2+3 days'", "-1 years -2 mons +3 days"},
		// The '+' continuation also splits off a glued unit-word run (`1-2h+3`)
		// and a plain hour pair (`3h+2` → `3`+`h`+`+2`).
		{"SELECT interval '1-2h+3'", "1 year 2 mons 00:00:03"},
		{"SELECT interval '3h+2'", "03:00:02"},
		// A '+'-continuation carrying its own colon is a signed TIME field, and
		// can itself trail an absorbed unit word.
		{"SELECT interval '1-2+3:30h'", "1 year 2 mons 03:30:00"},
		// PG's sign lexer soaks up whitespace between a lone sign and its
		// digits, so `- 3` ≡ `-3` and the sign rides the whole glued field.
		{"SELECT interval '- 3'", "-00:00:03"},
		{"SELECT interval '- 3-4'", "-3 years -4 mons"},
		{"SELECT interval '+ 3h30m'", "03:30:00"},
		// The soak also applies inside the single-field Form-1 body (the typmod
		// qualifier then truncates below the unit: `- 1.5` day keeps -1 days).
		{"SELECT interval '- 3' day", "-3 days"},
		{"SELECT interval '- 1.5' day", "-1 days"},
		// Cast / :: siblings share the tokenizer.
		{"SELECT '1-2h'::interval", "1 year 2 mons"},
		{"SELECT CAST('1-2 days' AS interval)", "1 year 2 mons"},
		{"SELECT '12:00h'::interval", "12:00:00"},
		{"SELECT '-1-2h'::interval", "-1 years -2 mons"},
		{"SELECT CAST('-1-2 days' AS interval)", "-1 years -2 mons"},
		{"SELECT '-1-2+3'::interval", "-1 years -2 mons +00:00:03"},
		{"SELECT CAST('1-2+3d' AS interval)", "1 year 2 mons 3 days"},
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
		"SELECT interval '1-h' FROM t",            // empty month + glued letter → whole malformed token
		"SELECT interval '1-day' FROM t",          // ditto
		"SELECT interval '1-2 foo' FROM t",        // 'foo' is not a unit word
		"SELECT interval '1-2foo' FROM t",         // ditto glued
		"SELECT interval '1-2 x' FROM t",          // 'x' is not a unit word
		"SELECT interval '1-2 h h' FROM t",        // consecutive unhandled units
		"SELECT interval '1-2 hour minute' FROM t", // ditto
		"SELECT interval '5 h mon' FROM t",        // lone unit after a number pair
		"SELECT interval '1 day mon' FROM t",      // ditto
		"SELECT interval '12:00 mon 3' FROM t",    // trailing 3 → seconds collides with time SECOND slot
		"SELECT interval '12:00h30m' FROM t",      // 30m MINUTE collides with the time-word MINUTE slot
		"SELECT interval '12:00hh' FROM t",        // 'hh' is not a unit word
		// Signed year-month rejects. PG's DTK_TZ token swallows a '.', so a
		// fractional tail stays inside the token and the years-months branch
		// rejects it (`*cp != '\0'`); a non-unit trailing word still errors.
		"SELECT interval '-1-2.5h' FROM t",        // '.' swallowed by DTK_TZ → malformed year-month
		"SELECT interval '-1-2.5' FROM t",         // ditto, no trailer
		"SELECT interval '-1-2 foo' FROM t",       // 'foo' is not a unit word
		"SELECT interval '-1-2foo' FROM t",        // ditto glued
		"SELECT interval '-1-2 h h' FROM t",       // consecutive unhandled units
		// '+'-continuation rejects (all confirmed against live PG 18.3): a
		// second year-month field collides on MONTH, a second bare number
		// re-uses the carried/default field, and a '+' continuation off a
		// non-year-month head (plain number, decimal, time word) collides with
		// the default-SECONDS slot its own head already occupies.
		"SELECT interval '-1-2+3-4' FROM t",       // MONTH collision
		"SELECT interval '-1-2+3+4' FROM t",       // second continuation → seconds re-use
		"SELECT interval '-1-2+3 4' FROM t",       // bare number after the continuation
		"SELECT interval '-1-2+' FROM t",          // sign with nothing after it
		"SELECT interval '-1-2++3' FROM t",        // doubled sign
		"SELECT interval '-1-2+h' FROM t",         // sign must precede a digit
		"SELECT interval '5+3' FROM t",            // seconds collision off a plain number
		"SELECT interval '1.5+3' FROM t",          // ditto, decimal head
		"SELECT interval '1-2.5+3' FROM t",        // fraction pairs with the continuation
		"SELECT interval '12:00+3' FROM t",        // time word occupies the SECOND slot
		"SELECT interval '12:00h+3' FROM t",       // ditto with an absorbed unit between
		"SELECT interval '-1:30+2' FROM t",        // signed time word, same collision
		// Sign lexer fidelity: PG only forms a signed token when a DIGIT
		// follows the sign ('+.5'/'-.5' are DTERR_BAD_FORMAT, and a lone sign
		// must glue onto a following digit-led field).
		"SELECT interval '+.5' FROM t",
		"SELECT interval '-.5' FROM t",
		"SELECT interval '+.5 sec' FROM t",
		"SELECT interval '-.5 day' FROM t",
		"SELECT interval '-.5' day FROM t", // Form-1 single-field sibling
		"SELECT interval '+.5' second FROM t",
		"SELECT interval '1 day +.5' FROM t",
		"SELECT interval '-1-2+.5' FROM t",        // continuation is sign-led too
		"SELECT interval '+h' FROM t",             // sign + letter is DTK_SPECIAL → rejected
		"SELECT interval '-' FROM t",              // lone sign, nothing follows
		"SELECT interval '- h' FROM t",            // lone sign before a non-digit field
		"SELECT interval '- 3 4' FROM t",          // glued `- 3` then a colliding bare number
	}
	for _, sql := range reject {
		if _, err := runQueryErr(t, ctx, sql); err == nil {
			t.Errorf("%s: expected error, got none", sql)
		}
	}
}

// TestIntervalTypmodRangeAndPrecision pins the Form-1 interval typmod
// range/precision grammar (unimplemented_feat #5(d-iv)): a `<hi> TO <lo>` range
// interprets a bare magnitude as, and truncates it to, its LOW field
// (postgres datetime.c DecodeInterval `switch (range)` + timestamp.c
// AdjustIntervalForTypmod, which collapse to the low field), and a `SECOND(p)`
// precision rounds the fractional seconds. Every `want` was captured
// byte-for-byte from live PostgreSQL 18.3. The plural cases confirm the
// fidelity fix: a plural / abbreviation is a column ALIAS on the bare interval
// (`interval '5' days` = 00:00:05), never a typmod field.
func TestIntervalTypmodRangeAndPrecision(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	accept := []struct{ sql, want string }{
		// Range: bare magnitude interpreted as (and truncated to) the low field.
		{"SELECT interval '5' hour to minute", "00:05:00"},
		{"SELECT interval '1.5' hour to minute", "00:01:00"},
		{"SELECT interval '90' minute to second", "00:01:30"},
		{"SELECT interval '5' day to second", "00:00:05"},
		{"SELECT interval '5' year to month", "5 mons"},
		{"SELECT interval '5' day to hour", "05:00:00"},
		{"SELECT interval '5' day to minute", "00:05:00"},
		{"SELECT interval '1.5' day to minute", "00:01:00"},
		{"SELECT interval '100' minute to second", "00:01:40"},
		{"SELECT interval '1.5' minute to second", "00:00:01.5"},
		{"SELECT interval '-1.5' hour to minute", "-00:01:00"},
		// Complex (multi-field) body under a range: the TRAILING unitless number
		// resolves via the range's low field, then AdjustIntervalForTypmod's range
		// truncation applies (unimplemented_feat #5(d-iv) complex-body-under-range).
		// Higher-order explicit fields are kept; verified byte-for-byte vs live PG
		// 18.3: `interval '1 day 5' hour to minute` → 1 day 00:05:00, etc.
		{"SELECT interval '1 day 5' hour to minute", "1 day 00:05:00"},
		{"SELECT interval '1 day 5' day to hour", "1 day 05:00:00"},
		{"SELECT interval '2 hour 5' hour to minute", "02:05:00"},
		{"SELECT interval '1 day 90' minute to second", "1 day 00:01:30"},
		{"SELECT interval '1 day 1.5' hour to minute", "1 day 00:01:00"},
		{"SELECT interval '1 mon 2 day 5' hour to minute", "1 mon 2 days 00:05:00"},
		{"SELECT interval '1 day 5' minute to second", "1 day 00:00:05"},
		{"SELECT interval '1 day 30' day to minute", "1 day 00:30:00"},
		{"SELECT interval '1 day 5' hour to second", "1 day 00:00:05"},
		{"SELECT interval '-1 day 5' hour to minute", "-1 days +00:05:00"},
		{"SELECT interval '1 day 5' minute to second(0)", "1 day 00:00:05"},
		// Bare number to the LEFT of a time word takes DAY via PG's right-to-left
		// carry (unimplemented_feat #5(d-iv) leftward carry); the DAY assignment
		// overrides the typmod range default, then range truncation applies.
		{"SELECT interval '1 2:03:04' day to second", "1 day 02:03:04"},
		{"SELECT interval '1 2:03:04' hour to minute", "1 day 02:03:00"},
		{"SELECT interval '10 2:03:04' minute to second", "10 days 02:03:04"},
		{"SELECT interval '5 2:03:04' second", "5 days 02:03:04"},
		// Single field (existing behaviour, now via the same helper).
		{"SELECT interval '5' second", "00:00:05"},
		{"SELECT interval '5' minute", "00:05:00"},
		{"SELECT interval '5' hour", "05:00:00"},
		{"SELECT interval '1.5' hour", "01:00:00"},
		{"SELECT interval '1.23456789' second", "00:00:01.234568"},
		// SECOND(p) fractional-seconds precision (round-half-away-from-zero).
		{"SELECT interval '1.23456789' second(2)", "00:00:01.23"},
		{"SELECT interval '-1.23456789' second(2)", "-00:00:01.23"},
		{"SELECT interval '1.999999' second(2)", "00:00:02"},
		{"SELECT interval '5' second(2)", "00:00:05"},
		{"SELECT interval '5' minute to second(3)", "00:00:05"},
		{"SELECT interval '1.2345' minute to second(1)", "00:00:01.2"},
		// Plural / abbreviation trailing token = column alias on the bare
		// interval (bare number defaults to SECONDS), NOT a typmod field.
		{"SELECT interval '5' days", "00:00:05"},
		{"SELECT interval '5' hours", "00:00:05"},
		{"SELECT interval '5' millisecond", "00:00:05"},
		{"SELECT interval '5' min", "00:00:05"},
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

	// Invalid range pairs are a syntax error (postgres opt_interval rejects
	// them); goopg errors too (the unconsumed TO/field trips the parser).
	reject := []string{
		"SELECT interval '5' year to second FROM t",
		"SELECT interval '5' minute to hour FROM t",
		"SELECT interval '5' second to minute FROM t",
		"SELECT interval '5' minute to minute FROM t",
		"SELECT interval '5' hour to hour FROM t",
	}
	for _, sql := range reject {
		if _, err := runQueryErr(t, ctx, sql); err == nil {
			t.Errorf("%s: expected error, got none", sql)
		}
	}
}

// TestIntervalLeftwardTimeCarry covers PostgreSQL's right-to-left DecodeInterval
// type carry: a bare magnitude immediately to the LEFT of a time field takes
// DTK_DAY (datetime.c L3549 for an unsigned DTK_TIME, L3587 for a signed DTK_TZ
// with ':'), so `interval '1 2:03:04'` → 1 day 02:03:04. This carry runs in the
// free-form decoder (bare `interval '…'` and `::interval`/CAST), independent of
// any typmod range; TestIntervalTypmodRangeAndPrecision covers the range-qualified
// forms where the DAY assignment overrides the range default. All accept/reject
// results captured byte-for-byte from live PG 18.3 (port 5601).
func TestIntervalLeftwardTimeCarry(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	accept := []struct{ sql, want string }{
		// Bare integer/fraction/sign before an unsigned time word → DAY.
		{"SELECT interval '1 2:03:04'", "1 day 02:03:04"},
		{"SELECT interval '1.5 2:03:04'", "1 day 14:03:04"},
		{"SELECT interval '-1 2:03:04'", "-1 days +02:03:04"},
		{"SELECT interval '10 2:03:04'", "10 days 02:03:04"},
		{"SELECT interval '1 2:03'", "1 day 02:03:00"},
		{"SELECT interval '1 100:00:00'", "1 day 100:00:00"},
		// Signed time word (DTK_TZ with ':'): still sets DAY for the number left.
		{"SELECT interval '1 -2:03:04'", "1 day -02:03:04"},
		// The time word discards a pending unit word to its right (absorb), and
		// the number to the time's left still takes DAY.
		{"SELECT interval '1 12:00 h'", "1 day 12:00:00"},
		// Same carry via the ::interval / CAST sibling path.
		{"SELECT '1 2:03:04'::interval", "1 day 02:03:04"},
		{"SELECT CAST('1.5 2:03:04' AS interval)", "1 day 14:03:04"},
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

	// PG rejects a SECOND bare number carried into the same slot (both DAY →
	// tmask&fmask collision), a bare number left of a year-month field (forced
	// DTK_MONTH collides with the number's MONTH), a trailing bare number after
	// a time field (SECOND collides with the time's SECOND slot), and a
	// day/hour pair whose HOUR collides with the time word's HOUR.
	reject := []string{
		"SELECT interval '1 2 2:03:04' FROM t",
		"SELECT interval '1 day 2 2:03:04' FROM t",
		"SELECT interval '1 hour 2 2:03:04' FROM t",
		"SELECT interval '1 1-2' FROM t",
		"SELECT interval '2:03:04 1' FROM t",
		"SELECT '1 2 2:03:04'::interval FROM t",
	}
	for _, sql := range reject {
		if _, err := runQueryErr(t, ctx, sql); err == nil {
			t.Errorf("%s: expected error, got none", sql)
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

// TestIntervalInfinityLiterals covers interval ±infinity literals
// (unimplemented_feat #5(d-iv)): `infinity`, `-infinity`, `+infinity`
// (case-insensitive, sign optionally space-separated, surrounding whitespace
// tolerated) map to PostgreSQL's INTERVAL_NOEND / INTERVAL_NOBEGIN sentinel
// (DTK_LATE / DTK_EARLY in DecodeInterval) and round-trip to the bare word on
// output. A trailing typmod qualifier is ignored (`interval 'infinity' hour to
// minute` is still infinity). Both the typed-literal and `::interval`/CAST entry
// points are exercised (they share parser.ParseIntervalBody). Every `want` /
// rejection was captured byte-for-byte from live PostgreSQL 18.3 (port 5599).
func TestIntervalInfinityLiterals(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	accept := []struct{ sql, want string }{
		{"SELECT interval 'infinity'", "infinity"},
		{"SELECT interval '-infinity'", "-infinity"},
		{"SELECT interval '+infinity'", "infinity"},
		{"SELECT interval 'Infinity'", "infinity"},
		{"SELECT interval 'INFINITY'", "infinity"},
		{"SELECT interval '  infinity  '", "infinity"},
		{"SELECT interval '- infinity'", "-infinity"},
		{"SELECT interval '+ infinity'", "infinity"},
		// Typmod qualifier is ignored for infinity.
		{"SELECT interval 'infinity' hour to minute", "infinity"},
		{"SELECT interval 'infinity' day", "infinity"},
		{"SELECT interval '-infinity' second(2)", "-infinity"},
		// The ::interval / CAST path shares the tokenizer.
		{"SELECT 'infinity'::interval", "infinity"},
		{"SELECT '-infinity'::interval", "-infinity"},
		{"SELECT CAST('infinity' AS interval)", "infinity"},
		{"SELECT CAST('-infinity' AS interval)", "-infinity"},
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
		"SELECT interval 'inf' FROM t",         // must be the full word
		"SELECT interval '-inf' FROM t",
		"SELECT interval 'infi' FROM t",
		"SELECT interval 'infinityx' FROM t",   // trailing garbage
		"SELECT interval '-infinityy' FROM t",
		"SELECT interval '1 infinity' FROM t",  // infinity must be the sole field
		"SELECT interval 'infinity 1' FROM t",
		"SELECT interval '--infinity' FROM t",  // doubled / mixed sign
		"SELECT interval '-+infinity' FROM t",
		"SELECT interval '- -infinity' FROM t",
	}
	for _, sql := range reject {
		if _, err := runQueryErr(t, ctx, sql); err == nil {
			t.Errorf("%s: expected error, got none", sql)
		}
	}
}
