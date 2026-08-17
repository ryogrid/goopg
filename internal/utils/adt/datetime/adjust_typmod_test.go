package datetime

import "testing"

func TestAdjustTimeForTypmod(t *testing.T) {
	cases := []struct {
		name   string
		micros int64
		typmod int32
		want   int64
	}{
		{"no precision (-1) is a no-op", 12345678, -1, 12345678},
		{"precision 6 is a no-op", 12345678, 6, 12345678},
		{"precision 0 rounds to whole second, up", 5900000, 0, 6000000},
		{"precision 0 rounds to whole second, down", 5499999, 0, 5000000},
		// 12:34:56.789012 → 12:34:56.79 (third digit 9 rounds up).
		{"precision 2 rounds up", 12*3600000000 + 34*60000000 + 56*1000000 + 789012, 2, 12*3600000000 + 34*60000000 + 56*1000000 + 790000},
		// 12:34:56.784444 → 12:34:56.78 (third digit 4 rounds down).
		{"precision 2 rounds down", 12*3600000000 + 34*60000000 + 56*1000000 + 784444, 2, 12*3600000000 + 34*60000000 + 56*1000000 + 780000},
		// '23:59:59.999999' at precision 2 carries through the second into
		// 24:00:00 (usecsPerDay), the value goopg's output truncation lost.
		{"carry to 24:00:00", 23*3600000000 + 59*60000000 + 59999999, 2, usecsPerDay},
		{"24:00:00 stays 24:00:00", usecsPerDay, 2, usecsPerDay},
		{"zero stays zero", 0, 0, 0},
		// Negative TimeADT is unreachable for time/timetz (input range-checks to
		// [0, usecsPerDay]) but the port keeps PG's arm: half away from zero.
		{"negative rounds away from zero", -5900000, 0, -6000000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AdjustTimeForTypmod(c.micros, c.typmod)
			if got != c.want {
				t.Fatalf("AdjustTimeForTypmod(%d, %d) = %d, want %d", c.micros, c.typmod, got, c.want)
			}
		})
	}
}
