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
	if _, ok := newDateDatumFromPGDays(pgDateDayLimit + 1); ok {
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
