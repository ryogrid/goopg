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
