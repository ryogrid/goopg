package pgdatetime

import (
	"math"
	"testing"
)

// Every `want` below is the text PG 18.3 printed for the same value on the
// reference cluster (bench/tpch, port 65432, TimeZone=UTC) — captured, not
// derived from goopg's own behaviour:
//
//	SELECT ARRAY['2020-01-01','0001-01-01','0001-01-01 BC','4713-01-01 BC',
//	             '5874897-12-31','infinity','-infinity']::date[];
//	  → {2020-01-01,0001-01-01,"0001-01-01 BC","4713-01-01 BC",5874897-12-31,
//	     infinity,-infinity}
//	SELECT ARRAY['00:00:00','24:00:00','01:02:03.000001','12:34:56.100000']::time[];
//	  → {00:00:00,24:00:00,01:02:03.000001,12:34:56.1}
//	SELECT ARRAY['2020-01-01 10:00:00','0001-01-01 00:00:00 BC',
//	             '294276-12-31 23:59:59','2000-01-01 00:00:00.000001',
//	             'infinity','-infinity']::timestamp[];
//	  → {"2020-01-01 10:00:00","0001-01-01 00:00:00 BC","294276-12-31 23:59:59",
//	     "2000-01-01 00:00:00.000001",infinity,-infinity}
//	SELECT ARRAY['01:02:03+05','12:00:00-03:30','00:00:00+00',
//	             '23:59:59.999999+14']::timetz[];
//	  → {01:02:03+05,12:00:00-03:30,00:00:00+00,23:59:59.999999+14}

const (
	usecDay  = int64(86400) * 1000000
	usecHour = int64(3600) * 1000000
	usecMin  = int64(60) * 1000000
)

func TestFormatDateMatchesDateOut(t *testing.T) {
	cases := []struct {
		days int32
		want string
	}{
		{0, "2000-01-01"},
		{7305, "2020-01-01"}, // 2020-01-01 is 7305 days after the PG epoch
		{-730119, "0001-01-01"},
		{-730485, "0001-01-01 BC"},
		{-2451507, "4713-01-01 BC"}, // the low end of PG's date range
		{2145031948, "5874897-12-31"},
		{math.MaxInt32, "infinity"},
		{math.MinInt32, "-infinity"},
	}
	for _, tc := range cases {
		if got := FormatDate(tc.days); got != tc.want {
			t.Errorf("FormatDate(%d) = %q, want %q", tc.days, got, tc.want)
		}
	}
}

func TestFormatTimeMatchesTimeOut(t *testing.T) {
	cases := []struct {
		micros int64
		want   string
	}{
		{0, "00:00:00"},
		{usecDay, "24:00:00"}, // PG's TIME upper bound is 24:00:00, not 23:59:59
		{usecHour + 2*usecMin + 3000000 + 1, "01:02:03.000001"},
		{12*usecHour + 34*usecMin + 56000000 + 100000, "12:34:56.1"},
		{4*usecHour + 5*usecMin + 6000000 + 789000, "04:05:06.789"},
	}
	for _, tc := range cases {
		if got := FormatTime(tc.micros); got != tc.want {
			t.Errorf("FormatTime(%d) = %q, want %q", tc.micros, got, tc.want)
		}
	}
}

func TestFormatTimestampMatchesTimestampOut(t *testing.T) {
	cases := []struct {
		micros int64
		want   string
	}{
		{0, "2000-01-01 00:00:00"},
		{1, "2000-01-01 00:00:00.000001"},
		{7305*usecDay + 10*usecHour, "2020-01-01 10:00:00"},
		// The BC marker trails the whole value, after the time part.
		{-63113904000000000, "0001-01-01 00:00:00 BC"},
		{9223371331199000000, "294276-12-31 23:59:59"},
		{math.MaxInt64, "infinity"},
		{math.MinInt64, "-infinity"},
	}
	for _, tc := range cases {
		if got := FormatTimestamp(tc.micros); got != tc.want {
			t.Errorf("FormatTimestamp(%d) = %q, want %q", tc.micros, got, tc.want)
		}
	}
	// timestamptz is the same rendering plus the session zone, which is UTC on
	// every goopg cluster (SHOW TimeZone = UTC).
	if got := FormatTimestampTZUTC(7305*usecDay + 10*usecHour); got != "2020-01-01 10:00:00+00" {
		t.Errorf("FormatTimestampTZUTC = %q, want %q", got, "2020-01-01 10:00:00+00")
	}
	if got := FormatTimestampTZUTC(math.MaxInt64); got != "infinity" {
		t.Errorf("FormatTimestampTZUTC(inf) = %q, want infinity", got)
	}
}

// TestFormatTimeTZSignConvention is the assertion a whole-value table would
// miss: TimeTzADT.zone is seconds WEST of UTC, so the printed offset is its
// NEGATION. Getting it backwards still prints a plausible timetz and reverses
// the meaning of every value.
func TestFormatTimeTZSignConvention(t *testing.T) {
	cases := []struct {
		micros int64
		zone   int32 // PG's convention: seconds WEST of UTC
		want   string
	}{
		{usecHour + 2*usecMin + 3000000, -5 * 3600, "01:02:03+05"},
		{12 * usecHour, 3*3600 + 30*60, "12:00:00-03:30"},
		{0, 0, "00:00:00+00"},
		{23*usecHour + 59*usecMin + 59999999, -14 * 3600, "23:59:59.999999+14"},
		// A zone with a seconds part prints all three fields (PG keeps LMT-style
		// offsets such as -00:44:30 exact).
		{0, 44*60 + 30, "00:00:00-00:44:30"},
	}
	for _, tc := range cases {
		if got := FormatTimeTZ(tc.micros, tc.zone); got != tc.want {
			t.Errorf("FormatTimeTZ(%d, %d) = %q, want %q", tc.micros, tc.zone, got, tc.want)
		}
	}
}
