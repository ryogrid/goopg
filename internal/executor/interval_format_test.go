package executor

import "testing"

// TestFormatIntervalMatchesPGIntervalOut pins Datum.Format()'s KindInterval
// case against real PostgreSQL 18.3's interval_out (default 'postgres'
// IntervalStyle), verified live via psql against postgres/local_install.
// Before this fix, Format() always rendered "%d months %d days" regardless
// of sign/plurality/zero-collapsing, e.g. "-1 months 0 days" instead of PG's
// "-1 mons" and "0 months 0 days" instead of PG's "00:00:00".
func TestFormatIntervalMatchesPGIntervalOut(t *testing.T) {
	cases := []struct {
		months, days int32
		want         string
	}{
		{14, 3, "1 year 2 mons 3 days"},
		{-1, 0, "-1 mons"},
		{0, 0, "00:00:00"},
		{24, 0, "2 years"},
		{1, 0, "1 mon"},
		{-27, -4, "-2 years -3 mons -4 days"},
		{13, 0, "1 year 1 mon"},
		{0, 25, "25 days"},
		{0, -1, "-1 days"},
		{0, 1, "1 day"},
		{-15, 0, "-1 years -3 mons"},
		{-13, 0, "-1 years -1 mons"},
		{-12, 0, "-1 years"},
		{-21, 0, "-1 years -9 mons"},
		{21, 0, "1 year 9 mons"},
		{-24, -1, "-2 years -1 days"},
		{11, 0, "11 mons"},
		{-11, 0, "-11 mons"},
	}
	for _, c := range cases {
		got := NewIntervalDatum(c.months, c.days).Format()
		if got != c.want {
			t.Errorf("formatInterval(months=%d, days=%d) = %q, want %q", c.months, c.days, got, c.want)
		}
	}
}
