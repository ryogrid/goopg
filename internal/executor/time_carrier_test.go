package executor

import (
	"testing"
	"time"
)

// TestTimeCarrierRoundTrip pins the helpers every KindTime user now goes through
// (M0149-0001h1a48), including the infinity sentinels, which spill frames and
// hash keys must carry unchanged.
func TestTimeCarrierRoundTrip(t *testing.T) {
	ts := time.Date(2020, 2, 29, 12, 34, 56, 123456000, time.UTC)
	if got := NewTimeDatum(ts).TimeValue(); !got.Equal(ts) {
		t.Errorf("timestamp round trip = %v, want %v", got, ts)
	}
	for _, d := range []Datum{
		NewTimeDatum(ts),
		NewDateDatum(time.Date(2020, 2, 29, 0, 0, 0, 0, time.UTC)),
		NewTimestampTZDatum(ts),
		NewTimeTZDatum(time.Date(1970, 1, 1, 10, 0, 0, 0, time.UTC), 3600),
		NewTimestampInfinity(true),
		NewTimestampInfinity(false),
		NewDateInfinity(true),
	} {
		got, _, err := decodeDatum(encodeDatum(d, nil))
		if err != nil {
			t.Fatalf("decode %+v: %v", d, err)
		}
		if got.Int != d.Int || got.TimeSub != d.TimeSub || got.Scale != d.Scale || got.Kind != KindTime {
			t.Errorf("spill round trip of %+v = %+v", d, got)
		}
	}
}

// TestTimeCarrierIsPGMicroseconds: Int is PG's Timestamp value, so instants on
// either side of the PG epoch convert exactly, with sub-second parts that stay
// positive before it, and an infinity stays infinite through time.Time.
func TestTimeCarrierIsPGMicroseconds(t *testing.T) {
	for _, tc := range []struct {
		ts   time.Time
		want int64
	}{
		{time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), 0},
		{time.Date(2000, 1, 1, 0, 0, 1, 500000000, time.UTC), 1500000},
		{time.Date(1999, 12, 31, 23, 59, 59, 500000000, time.UTC), -500000},
		{time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), -946684800000000},
		{time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC), 252455615999999999},
		{time.Date(-4712, 11, 24, 0, 0, 0, 0, time.UTC), -211781865600000000},
	} {
		d := NewTimeDatum(tc.ts)
		if d.Int != tc.want {
			t.Errorf("%v: carrier %d, want %d", tc.ts, d.Int, tc.want)
		}
		if got := d.TimeValue(); !got.Equal(tc.ts) {
			t.Errorf("carrier %d = %v, want %v", d.Int, got, tc.ts)
		}
	}
	for _, d := range []Datum{NewTimestampInfinity(true), NewTimestampInfinity(false)} {
		if back := NewTimeDatum(d.TimeValue()); back.Int != d.Int {
			t.Errorf("infinity %d came back as %d", d.Int, back.Int)
		}
	}
	if got := NewTimeDatum(time.Date(2000, 1, 1, 0, 0, 0, 1999, time.UTC)).Int; got != 1 {
		t.Errorf("sub-microsecond digits: carrier %d, want truncation to 1", got)
	}
}

// TestHashPartitionWordIsStableForTimeValues: partition routing must not move
// when the carrier changes, or stored rows could not be found. The expected
// words are the low 32 bits of the Unix nanoseconds the carrier used to hold.
func TestHashPartitionWordIsStableForTimeValues(t *testing.T) {
	ts := time.Date(2020, 2, 29, 12, 34, 56, 123456000, time.UTC)
	for _, d := range []Datum{NewTimeDatum(ts), NewTimestampTZDatum(ts), NewDateDatum(ts.Truncate(24 * time.Hour))} {
		if got, want := hashPartitionFallbackWord(d), uint32(d.TimeValue().UnixNano()); got != want {
			t.Errorf("%+v: word %d, want %d", d, got, want)
		}
	}
	if got, want := hashPartitionFallbackWord(NewTimeDatum(ts)), uint32(ts.UnixNano()); got != want {
		t.Errorf("word %d, want %d", got, want)
	}
	if got, want := hashPartitionFallbackWord(NewTimestampInfinity(true)), uint32(0xffffffff); got != want {
		t.Errorf("infinity word %d, want %d", got, want)
	}
	if got := hashPartitionFallbackWord(NewIntDatum(7)); got != 7 {
		t.Errorf("integer word %d, want 7", got)
	}
}

// TestTimeTZCompareKey: two timetz values are ordered by their UTC position, so
// 10:00+01 and 09:00+00 are the same instant and 10:00+00 is later.
func TestTimeTZCompareKey(t *testing.T) {
	at := func(h, offsetSecs int) Datum {
		return NewTimeTZDatum(time.Date(1970, 1, 1, h, 0, 0, 0, time.UTC), offsetSecs)
	}
	if timeTZCompareKey(at(10, 3600)) != timeTZCompareKey(at(9, 0)) {
		t.Error("10:00+01 and 09:00+00 must have the same UTC key")
	}
	if timeTZCompareKey(at(10, 0)) <= timeTZCompareKey(at(9, 0)) {
		t.Error("10:00+00 must sort after 09:00+00")
	}
}
