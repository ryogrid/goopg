package executor

import (
	"math"
	"testing"
	"time"
)

// TestAddMonthsClamped pins timestamp_pl_interval's month rule: a day past the
// end of the target month becomes that month's last day.
func TestAddMonthsClamped(t *testing.T) {
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 30, 0, 0, time.UTC) }
	for _, tc := range []struct {
		from   time.Time
		months int
		want   time.Time
	}{
		{at(2020, 1, 31), 1, at(2020, 2, 29)},
		{at(2021, 1, 31), 1, at(2021, 2, 28)},
		{at(2020, 3, 31), -1, at(2020, 2, 29)},
		{at(2020, 2, 29), 12, at(2021, 2, 28)},
		{at(2020, 12, 15), 1, at(2021, 1, 15)},
		{at(2020, 1, 15), -1, at(2019, 12, 15)},
		{at(2020, 5, 31), 0, at(2020, 5, 31)},
	} {
		if got := addMonthsClamped(tc.from, tc.months); !got.Equal(tc.want) {
			t.Errorf("%v + %d months = %v, want %v", tc.from, tc.months, got, tc.want)
		}
	}
}

// TestTimeArithmeticBeyondDurationRange: differences longer than the 292 years a
// time.Duration holds stay exact, and an overflowing sum is an error.
func TestTimeArithmeticBeyondDurationRange(t *testing.T) {
	early := NewTimeDatum(time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC))
	late := NewTimeDatum(time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC))
	iv, err := subTimeTime(late, early, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantDays := int32(late.Int/usecsPerDay - early.Int/usecsPerDay)
	if iv.IntervalDaysValue() != wantDays || iv.IntervalMicrosValue() != 0 {
		t.Errorf("interval = %d days %d us, want %d days", iv.IntervalDaysValue(), iv.IntervalMicrosValue(), wantDays)
	}
	d1, _ := newDateDatumFromPGDays(-365000)
	d2, _ := newDateDatumFromPGDays(365000)
	days, err := subDateDate(d2, d1, 0)
	if err != nil || days.Int != 730000 {
		t.Errorf("date difference = %d (%v), want 730000", days.Int, err)
	}
	if _, ok := addTimeCarrierMicros(math.MaxInt64-1, 1); ok {
		t.Error("a sum landing on the infinity sentinel must be refused")
	}
	if _, ok := addTimeCarrierMicros(math.MaxInt64-1, 5); ok {
		t.Error("an overflowing sum must be refused")
	}
	if got, ok := addTimeCarrierMicros(-5, 3); !ok || got != -2 {
		t.Errorf("-5 + 3 = %d, %v", got, ok)
	}
}

// TestPGDaysOfDatum covers days before the PG epoch and the infinity sentinels.
func TestPGDaysOfDatum(t *testing.T) {
	for _, days := range []int32{0, 1, -1, 7305, -36524, math.MaxInt32, math.MinInt32} {
		d, ok := newDateDatumFromPGDays(days)
		if !ok {
			t.Fatalf("day %d refused", days)
		}
		if got := pgDaysOfDatum(d); got != days {
			t.Errorf("day %d came back as %d", days, got)
		}
	}
	if _, ok := newDateDatumFromPGDays(pgDateEndDay); ok {
		t.Error("a day the carrier cannot hold must be refused")
	}
	beforeMidnight := NewTimeDatum(time.Date(1999, 12, 31, 23, 0, 0, 0, time.UTC))
	if got := pgDaysOfDatum(beforeMidnight); got != -1 {
		t.Errorf("1999-12-31 23:00 is day %d, want -1", got)
	}
}

// TestDateBinFollowsTimestampBin pins date_bin against PG 18.3: sub-day strides,
// rounding toward -infinity before the origin, and PG's errors.
func TestDateBinFollowsTimestampBin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, tc := range []struct{ sql, want string }{
		{`SELECT date_bin('15 minutes', '2020-02-11 15:44:17'::timestamp, '2001-01-01'::timestamp)`, "2020-02-11 15:30:00.000000"},
		{`SELECT date_bin('7 days', '1999-02-11 15:44:17'::timestamp, '2001-01-01'::timestamp)`, "1999-02-08 00:00:00.000000"},
		{`SELECT date_bin('1 day', 'infinity'::timestamp, '2001-01-01'::timestamp)`, "infinity"},
	} {
		rows, err := runQueryErr(t, ctx, tc.sql)
		if err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		if got := rows[0][0].Format(); got != tc.want {
			t.Errorf("%s = %s, want %s", tc.sql, got, tc.want)
		}
	}
	for sql, want := range map[string]string{
		`SELECT date_bin('1 month', '2020-02-11'::timestamp, '2001-01-01'::timestamp)`: "0A000",
		`SELECT date_bin('0 days', '2020-02-11'::timestamp, '2001-01-01'::timestamp)`:  "22008",
		`SELECT date_bin('1 day', '2020-02-11'::timestamp, 'infinity'::timestamp)`:     "22008",
	} {
		_, err := runQueryErr(t, ctx, sql)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != want {
			t.Errorf("%s: error %v, want SQLSTATE %s", sql, err, want)
		}
	}
}

// TestTimestampRangeBounds pins PG's limits: 4714-11-24 BC is the first valid
// timestamp and 294277-01-01 is the first one past the end.
func TestTimestampRangeBounds(t *testing.T) {
	for _, in := range []string{"4714-11-24 00:00:00 BC", "294276-12-31 23:59:59.999999", "12345-06-07 08:09:10"} {
		if _, err := parseCopyTimestamp(in); err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	for _, in := range []string{"4714-11-23 23:59:59 BC", "294277-01-01 00:00:00"} {
		if _, err := parseCopyTimestamp(in); err == nil {
			t.Errorf("%q must be out of range", in)
		}
	}
	end := Datum{Kind: KindTime, Int: pgTimestampEnd - 1}
	if _, err := addTimeInterval(end, NewIntervalDatumFull(0, 0, 1), false, 0, nil); err == nil {
		t.Error("adding past the end of the range must fail")
	}
	last, _ := newDateDatumFromPGDays(pgDateEndDay - 1)
	if _, err := addDateTimeInt(last, NewIntDatum(1), false, 0); err == nil {
		t.Error("adding a day past the last date must fail")
	}
	if !NewTimestampInfinity(true).TimeValue().After(timeFromCarrier(pgTimestampEnd - 1)) {
		t.Error("infinity must sort after every finite instant")
	}
}

// TestTimestampInputErrorNamesTheType pins the wording PG's timestamp_in and
// timestamptz_in use for text they cannot read.
func TestTimestampInputErrorNamesTheType(t *testing.T) {
	for _, tc := range []struct {
		withZone bool
		want     string
	}{
		{false, `invalid input syntax for type timestamp: "x"`},
		{true, `invalid input syntax for type timestamp with time zone: "x"`},
	} {
		_, err := parseCopyTimestamp("x")
		got := dateTimeInputError(err, timestampInputTypeName(tc.withZone), "x", 0)
		if got.Code != "22007" || got.Message != tc.want {
			t.Errorf("withZone=%v: %s %q, want 22007 %q", tc.withZone, got.Code, got.Message, tc.want)
		}
	}
}

// TestTimestamptzIntervalUsesTheZone pins results captured from PostgreSQL 18.3:
// month and day steps work on the wall clock of the zone, so they cross DST
// changes, and a time inside a change is settled as DetermineTimeZoneOffset does.
func TestTimestamptzIntervalUsesTheZone(t *testing.T) {
	const newYork = "America/New_York"
	parse := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04:05-07", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		start        string
		months, days int32
		micros       int64
		zone, want   string
	}{
		{"2020-03-07 12:00:00-05", 0, 1, 0, newYork, "2020-03-08 12:00:00-04"},
		{"2020-03-07 12:00:00-05", 0, 0, 24 * 3600 * 1000000, newYork, "2020-03-08 13:00:00-04"},
		{"2020-03-07 12:00:00-05", 1, 1, 3600 * 1000000, newYork, "2020-04-08 13:00:00-04"},
		{"2020-03-07 02:30:00-05", 0, 1, 0, newYork, "2020-03-08 03:30:00-04"},
		{"2020-10-31 01:30:00-04", 0, 1, 0, newYork, "2020-11-01 01:30:00-05"},
		{"2020-02-08 02:30:00-05", 1, 0, 0, newYork, "2020-03-08 03:30:00-04"},
		{"2020-01-31 12:00:00-05", 1, 0, 0, newYork, "2020-02-29 12:00:00-05"},
		{"2022-10-30 00:00:00+01", 0, 1, 0, "Europe/Warsaw", "2022-10-30 20:00:00-04"},
	} {
		iv := NewIntervalDatumFull(tc.months, tc.days, tc.micros)
		got, err := addTimeIntervalInZone(NewTimestampTZDatum(parse(tc.start)), iv, false, 0, tc.zone)
		if err != nil {
			t.Errorf("%s: %v", tc.start, err)
			continue
		}
		if !got.IsTimestampTZ() || !got.TimeValue().Equal(parse(tc.want)) {
			t.Errorf("%s + (%d,%d,%d) = %v, want %s", tc.start, tc.months, tc.days, tc.micros, got.TimeValue(), tc.want)
		}
	}
}

// TestFarDatesKeepTheirOrder pins the day-count range of the date carrier: it sits
// between every timestamp and +infinity, and round-trips through time.Time.
func TestFarDatesKeepTheirOrder(t *testing.T) {
	first, ok := newDateDatumFromPGDays(pgDateFarDay)
	last, ok2 := newDateDatumFromPGDays(pgDateEndDay - 1)
	if !ok || !ok2 {
		t.Fatal("far dates must be representable")
	}
	if _, ok := newDateDatumFromPGDays(pgDateEndDay); ok {
		t.Error("the day after 5874897-12-31 must be refused")
	}
	if !(pgTimestampEnd-1 < first.Int && first.Int < last.Int && last.Int < NewDateInfinity(true).Int) {
		t.Errorf("carriers out of order: %d %d", first.Int, last.Int)
	}
	if got := last.TimeValue(); got.Year() != 5874897 || got.Month() != 12 || got.Day() != 31 {
		t.Errorf("last date = %v", got)
	}
	if again := NewDateDatum(last.TimeValue()); again.Int != last.Int || pgDaysOfDatum(again) != pgDateEndDay-1 {
		t.Errorf("round trip: %d, day %d", again.Int, pgDaysOfDatum(again))
	}
	if _, err := addTimeInterval(first, NewIntervalDatumFull(0, 1, 0), false, 0, nil); err == nil {
		t.Error("a far date has no timestamp to add an interval to")
	}
	if _, err := evalCast(first, "timestamp", 0, nil); err == nil {
		t.Error("a far date cannot become a timestamp")
	}
}
