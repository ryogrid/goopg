package datetime

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// refFormatTimestamp / refFormatTimeOfDay are the pre-UT-9 implementations,
// kept so the differential test can assert the rewrite did not change a byte.
func refFormatTimestamp(micros int64) string {
	dateDays := micros / usecsPerDay
	timeOfDay := micros % usecsPerDay
	if timeOfDay < 0 {
		timeOfDay += usecsPerDay
		dateDays--
	}
	y, m, d := j2date(dateDays + postgresEpochJDate)
	year := y
	if year <= 0 {
		year = -(year - 1)
	}
	s := fmt.Sprintf("%s %s", fmt.Sprintf("%04d-%02d-%02d", year, m, d), refFormatTimeOfDay(timeOfDay))
	if y <= 0 {
		s += " BC"
	}
	return s
}

func refFormatTimeOfDay(micros int64) string {
	neg := ""
	if micros < 0 {
		neg = "-"
		micros = -micros
	}
	h := micros / usecsPerHour
	rem := micros % usecsPerHour
	m := rem / usecsPerMinute
	rem %= usecsPerMinute
	s := rem / usecsPerSec
	frac := rem % usecsPerSec
	out := neg + fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if frac != 0 {
		f := strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
		out += "." + f
	}
	return out
}

// TestFormatMatchesSprintfReference pins review/260831 UT-9: the hand-rolled
// renderers must agree with the fmt-based ones they replaced, over BC dates,
// years past 9999, negative times and every fractional shape.
func TestFormatMatchesSprintfReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20260831))
	values := []int64{
		0, 1, -1, usecsPerDay, -usecsPerDay,
		usecsPerDay*730120 + 1, // far future
		-usecsPerDay * 800000,  // deep BC
	}
	for i := 0; i < 5000; i++ {
		values = append(values, rng.Int63n(1<<52)-(1<<51))
	}
	for _, v := range values {
		if got, want := FormatTimestamp(v), refFormatTimestamp(v); got != want {
			t.Fatalf("FormatTimestamp(%d) = %q, reference = %q", v, got, want)
		}
		tod := v % usecsPerDay
		if got, want := formatTimeOfDay(tod), refFormatTimeOfDay(tod); got != want {
			t.Fatalf("formatTimeOfDay(%d) = %q, reference = %q", tod, got, want)
		}
	}
}

// BenchmarkFormatTimestampWire measures the COPY TO / array-element output
// path for a timestamp value.
func BenchmarkFormatTimestampWire(b *testing.B) {
	const noFrac = int64(644_500_530) * 1_000_000
	const withFrac = noFrac + 123_000
	b.Run("timestamp", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if FormatTimestamp(noFrac) == "" {
				b.Fatal("empty")
			}
		}
	})
	b.Run("timestamp-frac", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if FormatTimestamp(withFrac) == "" {
				b.Fatal("empty")
			}
		}
	})
}
