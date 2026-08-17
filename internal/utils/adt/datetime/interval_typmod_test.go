package datetime

import "testing"

// packIntervalTypmod packs a range mask and precision into the INTERVAL_TYPMOD
// value a column typmod carries, mirroring PG's INTERVAL_TYPMOD(p,r) macro
// ((r & 0x7FFF) << 16) | (p & 0xFFFF). prec = -1 means "full precision" (0xFFFF).
func packIntervalTypmod(rng, prec int) int32 {
	p := intervalFullPrecision
	if prec >= 0 {
		p = prec
	}
	return int32((rng<<16)&0xFFFF0000) | int32(p&intervalFullPrecision)
}

func TestAdjustIntervalForTypmod(t *testing.T) {
	cases := []struct {
		name                 string
		typmod               int32
		months, days         int32
		micros               int64
		wantMonths, wantDays int32
		wantMicros           int64
	}{
		{
			name: "no modifier is a no-op",
			typmod: -1,
			months: 14, days: 3, micros: 4500000,
			wantMonths: 14, wantDays: 3, wantMicros: 4500000,
		},
		{
			name: "year zeroes days and time, months round to year",
			typmod: packIntervalTypmod(intervalMaskYear, -1),
			months: 14, days: 3, micros: 4500000,
			wantMonths: 12, wantDays: 0, wantMicros: 0,
		},
		{
			name: "month keeps months, zeroes days and time",
			typmod: packIntervalTypmod(intervalMaskMonth, -1),
			months: 14, days: 3, micros: 4500000,
			wantMonths: 14, wantDays: 0, wantMicros: 0,
		},
		{
			name: "year to month collapses to month",
			typmod: packIntervalTypmod(intervalMaskYear|intervalMaskMonth, -1),
			months: 14, days: 3, micros: 4500000,
			wantMonths: 14, wantDays: 0, wantMicros: 0,
		},
		{
			name: "day zeroes only the time field",
			typmod: packIntervalTypmod(intervalMaskDay, -1),
			months: 14, days: 3, micros: 4500000,
			wantMonths: 14, wantDays: 3, wantMicros: 0,
		},
		{
			name: "hour truncates below the hour",
			typmod: packIntervalTypmod(intervalMaskHour, -1),
			months: 0, days: 0, micros: 4500000000, // 01:15:00
			wantMonths: 0, wantDays: 0, wantMicros: 3600000000, // 01:00:00
		},
		{
			name: "day to hour truncates below the hour",
			typmod: packIntervalTypmod(intervalMaskDay|intervalMaskHour, -1),
			months: 0, days: 0, micros: 4500000000,
			wantMonths: 0, wantDays: 0, wantMicros: 3600000000,
		},
		{
			name: "minute truncates below the minute",
			typmod: packIntervalTypmod(intervalMaskMinute, -1),
			months: 0, days: 0, micros: 90000000, // 00:01:30
			wantMonths: 0, wantDays: 0, wantMicros: 60000000, // 00:01:00
		},
		{
			name: "full range keeps everything",
			typmod: packIntervalTypmod(intervalFullRange, -1),
			months: 14, days: 3, micros: 4500000,
			wantMonths: 14, wantDays: 3, wantMicros: 4500000,
		},
		{
			name: "second(2) rounds half away from zero (up)",
			typmod: packIntervalTypmod(intervalMaskSecond, 2),
			months: 0, days: 0, micros: 1234567, // 00:00:01.234567 → .23
			wantMonths: 0, wantDays: 0, wantMicros: 1230000,
		},
		{
			name: "second(2) rounds half away from zero (down)",
			typmod: packIntervalTypmod(intervalMaskSecond, 2),
			months: 0, days: 0, micros: 1239999, // → .24
			wantMonths: 0, wantDays: 0, wantMicros: 1240000,
		},
		{
			name: "second(2) negative rounds half away from zero",
			typmod: packIntervalTypmod(intervalMaskSecond, 2),
			months: 0, days: 0, micros: -1234567,
			wantMonths: 0, wantDays: 0, wantMicros: -1230000,
		},
		{
			name: "second(6) is full precision, no rounding",
			typmod: packIntervalTypmod(intervalMaskSecond, 6),
			months: 0, days: 0, micros: 123456,
			wantMonths: 0, wantDays: 0, wantMicros: 123456,
		},
		{
			name: "infinity sentinel is a no-op",
			typmod: packIntervalTypmod(intervalMaskYear, -1),
			months: 2147483647, days: 2147483647, micros: 9223372036854775807,
			wantMonths: 2147483647, wantDays: 2147483647, wantMicros: 9223372036854775807,
		},
		{
			name: "-infinity sentinel is a no-op",
			typmod: packIntervalTypmod(intervalMaskYear, -1),
			months: -2147483648, days: -2147483648, micros: -9223372036854775808,
			wantMonths: -2147483648, wantDays: -2147483648, wantMicros: -9223372036854775808,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMonths, gotDays, gotMicros := AdjustIntervalForTypmod(c.months, c.days, int64(c.micros), c.typmod)
			if gotMonths != c.wantMonths || gotDays != c.wantDays || gotMicros != c.wantMicros {
				t.Fatalf("AdjustIntervalForTypmod(%d,%d,%d,%d) = (%d,%d,%d), want (%d,%d,%d)",
					c.months, c.days, c.micros, c.typmod,
					gotMonths, gotDays, gotMicros,
					c.wantMonths, c.wantDays, c.wantMicros)
			}
		})
	}
}
