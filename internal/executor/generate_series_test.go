package executor

import (
	"errors"
	"testing"
	"time"
)

func collectSeries(t *testing.T, s *datumSeries) []Datum {
	t.Helper()
	var out []Datum
	for {
		d, ok, err := s.next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, d)
	}
}

// TestDatumSeriesTimestamptzFollowsTheZone pins PG 18.3's result: a one-day step
// keeps the wall clock across the March DST change in the given zone.
func TestDatumSeriesTimestamptzFollowsTheZone(t *testing.T) {
	start := time.Date(2020, 3, 7, 17, 0, 0, 0, time.UTC)
	args := [4]Datum{
		NewTimestampTZDatum(start),
		NewTimestampTZDatum(time.Date(2020, 3, 9, 16, 0, 0, 0, time.UTC)),
		NewIntervalDatumFull(0, 1, 0),
		NewStringDatum("America/New_York"),
	}
	s, err := newDatumSeries("timestamptz", args, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := collectSeries(t, s)
	want := []time.Time{start, time.Date(2020, 3, 8, 16, 0, 0, 0, time.UTC), time.Date(2020, 3, 9, 16, 0, 0, 0, time.UTC)}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].IsTimestampTZ() || !got[i].TimeValue().Equal(want[i]) {
			t.Errorf("row %d = %v, want %v", i, got[i].TimeValue(), want[i])
		}
	}
}

func TestDatumSeriesNumericKeepsTheScale(t *testing.T) {
	args := [4]Datum{NewNumericInt64Datum(15, 1), NewIntDatum(3), NewNumericInt64Datum(50, 2)}
	s, err := newDatumSeries("numeric", args, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range collectSeries(t, s) {
		got = append(got, numericText(d))
	}
	want := []string{"1.5", "2.00", "2.50", "3.00"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

func TestDatumSeriesRejectsBadArguments(t *testing.T) {
	ts := NewTimeDatum(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		typeName string
		args     [4]Datum
		want     string
	}{
		{"numeric", [4]Datum{NewStringDatum("NaN"), NewIntDatum(1), NewIntDatum(1)}, "start value cannot be NaN"},
		{"numeric", [4]Datum{NewIntDatum(1), NewStringDatum("Infinity"), NewIntDatum(1)}, "stop value cannot be infinity"},
		{"numeric", [4]Datum{NewIntDatum(1), NewIntDatum(2), NewIntDatum(0)}, "step size cannot equal zero"},
		{"timestamp", [4]Datum{ts, ts, NewIntervalDatumFull(0, 0, 0)}, "step size cannot equal zero"},
		{"timestamp", [4]Datum{ts, ts, NewStringDatum("infinity")}, "step size cannot be infinite"},
	} {
		_, err := newDatumSeries(tc.typeName, tc.args, true, false, nil)
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Code != "22023" || ee.Message != tc.want {
			t.Errorf("got %v, want 22023 %q", err, tc.want)
		}
	}
}
