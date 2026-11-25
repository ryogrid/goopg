package executor

import "testing"

func timeDatumAt(micros int64) Datum { return NewTimeDatum(pgTimeFromMicros(micros)) }

func TestTimePlusIntervalWraps(t *testing.T) {
	hour := int64(3_600_000_000)
	cases := []struct {
		start, span int64
		subtract    bool
		want        int64
	}{
		{10 * hour, hour, false, 11 * hour},
		{23 * hour, 2 * hour, false, hour},
		{hour, 2 * hour, true, 23 * hour},
		{10 * hour, 49 * hour, false, 11 * hour},
	}
	for _, c := range cases {
		got, err := timePlusInterval(timeDatumAt(c.start), NewIntervalDatumFull(0, 0, c.span), c.subtract, false)
		if err != nil {
			t.Fatal(err)
		}
		micros, _ := timeOperandMicros(got)
		if micros != c.want {
			t.Errorf("%d %v %d = %d, want %d", c.start, c.subtract, c.span, micros, c.want)
		}
	}
}

func TestTimeTZKeepsOffsetAndPrintsIt(t *testing.T) {
	tz := NewTimeTZDatum(pgTimeFromMicros(36_000_000_000), 7200)
	got, err := timePlusInterval(tz, NewIntervalDatumFull(0, 0, 3_600_000_000), false, true)
	if err != nil {
		t.Fatal(err)
	}
	if text := got.Format(); text != "11:00:00+02" {
		t.Errorf("Format = %q", text)
	}
	if text := string(got.AppendValueText(nil)); text != "11:00:00+02" {
		t.Errorf("AppendValueText = %q", text)
	}
	cast, err := evalCastTyped(got, "text", "timetz", 0, nil)
	if err != nil || cast.StringValue() != "11:00:00+02" {
		t.Errorf("cast = %v, %v", cast, err)
	}
}

func TestTimeToIntervalCast(t *testing.T) {
	got, err := evalCastTyped(timeDatumAt(36_000_000_000), "interval", "time", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindInterval || got.IntervalMicrosValue() != 36_000_000_000 {
		t.Errorf("got %v", got)
	}
}

func TestTimePlusInfiniteIntervalFails(t *testing.T) {
	inf := NewIntervalInfinity(true)
	_, err := timePlusInterval(timeDatumAt(0), inf, false, false)
	if ee, ok := err.(*ExecError); !ok || ee.Message != "cannot add infinite interval to time" {
		t.Errorf("add: %v", err)
	}
	_, err = timePlusInterval(timeDatumAt(0), inf, true, false)
	if ee, ok := err.(*ExecError); !ok || ee.Message != "cannot subtract infinite interval from time" {
		t.Errorf("subtract: %v", err)
	}
}

func TestDateWithTime(t *testing.T) {
	date := Datum{Kind: KindTime, Int: 0, TimeSub: TimeSubDate}
	got, err := dateWithTime(date, timeDatumAt(3_600_000_000), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.TimeSub != TimeSubTimestamp && got.TimeSub != 0 {
		t.Errorf("subtype = %v", got.TimeSub)
	}
	if got.Int != 3_600_000_000 {
		t.Errorf("Int = %d", got.Int)
	}
	zoned, err := dateWithTime(date, NewTimeTZDatum(pgTimeFromMicros(3_600_000_000), 3600), true)
	if err != nil {
		t.Fatal(err)
	}
	if zoned.TimeSub != TimeSubTimestampTZ || zoned.Int != 0 {
		t.Errorf("zoned = %+v", zoned)
	}
}

func TestLookupTimeZoneNameIgnoresCase(t *testing.T) {
	for _, name := range []string{"america/new_york", "AMERICA/NEW_YORK", "America/New_York"} {
		got, err := lookupTimeZoneName(name)
		if err != nil || got != "America/New_York" {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
	if got, err := lookupTimeZoneName("jst-9"); err != nil || got != "JST-9" {
		t.Errorf("POSIX spec = %q, %v", got, err)
	}
	if _, err := lookupTimeZoneName("no/such"); err == nil {
		t.Error("unknown zone accepted")
	}
}
