package executor

import (
	"math"
	"testing"
	"time"
)

// TestFastCompareDatumMatchesCompareDatum pins slice 27's in-place compare:
// for every pair it accepts, its ordering must be compareDatum's, and every
// pair it must not decide (timetz offsets, the big-numeric lane, mixed kinds)
// must be declined so evalBinary decides it. The pairs cover a date against a
// timestamp at the same instant and the next nanosecond (TPC-H Q6's
// `l_shipdate < '1995-01-01 00:00:00'::timestamp`), the ±infinity sentinels,
// timetz values whose UTC order differs from their local order, and numerics
// at different scales.
func TestFastCompareDatumMatchesCompareDatum(t *testing.T) {
	day := time.Date(1995, 1, 1, 0, 0, 0, 0, time.UTC)
	big := newNumeric(numericMant(Datum{Kind: KindNumeric, Int: 1}).Lsh(numericMant(Datum{Kind: KindNumeric, Int: 1}), 80), 2)
	tz := func(nanos int64, offsetMin int16) Datum { return Datum{Kind: KindTime, Int: nanos, Scale: offsetMin} }
	vals := []Datum{
		{Kind: KindInt, Int: -5}, {Kind: KindInt, Int: 0}, {Kind: KindInt, Int: 7},
		NewDateDatum(day), NewTimeDatum(day), NewTimeDatum(day.Add(time.Nanosecond)), NewTimeDatum(day.Add(-time.Hour)),
		NewTimestampInfinity(true), NewTimestampInfinity(false), NewDateInfinity(true),
		tz(int64(10*time.Hour), 60), tz(int64(9*time.Hour+30*time.Minute), -60),
		{Kind: KindNumeric, Int: 5, Scale: 2}, {Kind: KindNumeric, Int: 50, Scale: 3}, {Kind: KindNumeric, Int: 7, Scale: 2},
		{Kind: KindNumeric, Int: -24, Scale: 0}, {Kind: KindNumeric, Int: math.MaxInt64 / 10, Scale: 0},
		big,
	}
	decided := 0
	for i := range vals {
		for j := range vals {
			a, b := vals[i], vals[j]
			got, ok := fastCompareDatum(&a, &b)
			// A numeric against an int on the int64 lane is decided too
			// (M0122-0012 slice 84): compareDatum's NUMERIC ↔ INT arm.
			numInt := (a.Kind == KindNumeric && b.Kind == KindInt && !isBigNumeric(a)) ||
				(a.Kind == KindInt && b.Kind == KindNumeric && !isBigNumeric(b))
			mustDecline := (a.Kind != b.Kind && !numInt) ||
				(a.Kind == KindTime && (a.Scale != 0 || b.Scale != 0)) ||
				(a.Kind == KindNumeric && b.Kind == KindNumeric && (isBigNumeric(a) || isBigNumeric(b)))
			if mustDecline {
				if ok {
					t.Errorf("pair %d,%d (%v vs %v): decided %d, must decline", i, j, a, b, got)
				}
				continue
			}
			if !ok {
				t.Errorf("pair %d,%d (%v vs %v): declined", i, j, a, b)
				continue
			}
			want, err := compareDatum(a, b, 0)
			if err != nil {
				t.Fatalf("pair %d,%d: compareDatum: %v", i, j, err)
			}
			if got != want {
				t.Errorf("pair %d,%d (%v vs %v): fast %d, compareDatum %d", i, j, a, b, got, want)
			}
			decided++
		}
	}
	if decided < 50 {
		t.Fatalf("only %d pairs decided; the fixture exercises too little", decided)
	}
}
