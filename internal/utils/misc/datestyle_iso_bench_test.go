package misc

import (
	"math/rand"
	"testing"
	"time"
)

// refISODate / refISOTimestamp are the pre-UT-9 implementations, kept so the
// differential test below can assert the fast paths did not change a byte.
func refISODate(t time.Time, era string) string {
	return t.Format("2006-01-02") + era
}

func refISOTimestamp(t time.Time, frac string) string {
	return t.Format("2006-01-02 15:04:05") + frac
}

// TestISOFastPathsMatchTimeFormat pins review/260831 UT-9: the hand-rolled ISO
// renderers must agree with time.Format on every value, including BC years
// (where eraDisplay has already mapped the year), years past 9999, and every
// fractional-second shape.
func TestISOFastPathsMatchTimeFormat(t *testing.T) {
	rng := rand.New(rand.NewSource(20260831))
	cases := []time.Time{
		time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2020, 2, 29, 23, 59, 59, 999999000, time.UTC),
		time.Date(9999, 12, 31, 12, 0, 0, 0, time.UTC),
		time.Date(10000, 1, 1, 1, 2, 3, 0, time.UTC),
		time.Date(294276, 12, 31, 23, 59, 59, 0, time.UTC),
	}
	for i := 0; i < 2000; i++ {
		cases = append(cases, time.Date(
			1+rng.Intn(12000), time.Month(1+rng.Intn(12)), 1+rng.Intn(28),
			rng.Intn(24), rng.Intn(60), rng.Intn(60), rng.Intn(1000)*1000, time.UTC))
	}
	for _, tc := range cases {
		for _, era := range []string{"", " BC"} {
			if got, want := isoDate(tc, era), refISODate(tc, era); got != want {
				t.Fatalf("isoDate(%v, %q) = %q, time.Format says %q", tc, era, got, want)
			}
		}
		frac := fracSecondsSuffix(tc)
		if got, want := isoTimestamp(tc, frac), refISOTimestamp(tc, frac); got != want {
			t.Fatalf("isoTimestamp(%v) = %q, time.Format says %q", tc, got, want)
		}
	}
}

// BenchmarkFormatTimestampISO measures the per-cell output path for a
// timestamp under the default (ISO) DateStyle.
func BenchmarkFormatTimestampISO(b *testing.B) {
	ts := time.Date(2020, 6, 15, 10, 20, 30, 0, time.UTC)
	tsFrac := time.Date(2020, 6, 15, 10, 20, 30, 123000000, time.UTC)
	d := time.Date(2020, 6, 15, 0, 0, 0, 0, time.UTC)

	b.Run("timestamp", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if FormatTimestamp(ts, "ISO", "YMD") == "" {
				b.Fatal("empty")
			}
		}
	})
	b.Run("timestamp-frac", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if FormatTimestamp(tsFrac, "ISO", "YMD") == "" {
				b.Fatal("empty")
			}
		}
	})
	b.Run("date", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if FormatDate(d, "ISO", "YMD") == "" {
				b.Fatal("empty")
			}
		}
	})
}
