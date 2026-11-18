package executor

import (
	"errors"
	"testing"
)

// TestNormalizeDatetimeArray pins array_in's output, checked against PG 18.3 with
// TimeZone UTC and the default DateStyle.
func TestNormalizeDatetimeArray(t *testing.T) {
	for _, tc := range []struct{ in, elem, want string }{
		{"{2020-07-01,2020-01-01}", "timestamptz", `{"2020-07-01 00:00:00+00","2020-01-01 00:00:00+00"}`},
		{"{2020-07-01 10:00+05,NULL}", "timestamptz", `{"2020-07-01 05:00:00+00",NULL}`},
		{"{2020-07-01 10:00+05}", "timestamp", `{"2020-07-01 10:00:00"}`},
		{`{"Jul 1 2020"}`, "date", "{2020-07-01}"},
		{"{{2020-07-01},{2020-01-01}}", "date", "{{2020-07-01},{2020-01-01}}"},
		{"{10:00}", "time", "{10:00:00}"},
		{"{10:00+02,11:30:15.5-05:30}", "timetz", "{10:00:00+02,11:30:15.5-05:30}"},
		{`{"1 hour","2 days"}`, "interval", `{01:00:00,"2 days"}`},
		{"{0 second, @ 1 hour @ 42 minutes @ 20 seconds}", "interval", "{00:00:00,01:42:20}"},
		{"{}", "date", "{}"},
		{" { 2020-07-01 , null } ", "date", "{2020-07-01,NULL}"},
	} {
		got, err := normalizeDatetimeArray(tc.in, tc.elem, 0, nil)
		if err != nil || got != tc.want {
			t.Errorf("%q as %s[] = %q, %v; want %q", tc.in, tc.elem, got, err, tc.want)
		}
	}
	for in, code := range map[string]string{"{bad}": "22007", "{2020-07-01": "22P02", "{2020-07-01}x": "22P02", "{,}": "22P02"} {
		_, err := normalizeDatetimeArray(in, "date", 0, nil)
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Code != code {
			t.Errorf("%q: got %v, want %s", in, err, code)
		}
	}
}
