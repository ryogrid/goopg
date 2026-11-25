package datetime

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func timeOnlyEnv(t *testing.T) *DecodeEnv {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no tz database")
	}
	return &DecodeEnv{
		Order:   DateOrderMDY,
		Now:     time.Date(2020, 1, 15, 12, 0, 0, 0, time.UTC),
		Session: la,
		LookupZone: func(name string) (*time.Location, bool) {
			// The tokenizer lowers names; pg_tzset matches without case.
			for _, z := range []string{"America/New_York", "America/Los_Angeles"} {
				if strings.EqualFold(z, name) {
					loc, err := time.LoadLocation(z)
					return loc, err == nil
				}
			}
			return nil, false
		},
		WestOffset: func(wall time.Time, loc *time.Location) int {
			_, east := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc).Zone()
			return -east
		},
	}
}

// TestDecodeTimeOnly pins time_in / timetz_in results measured on PG 18.3
// with TimeZone America/Los_Angeles.
func TestDecodeTimeOnly(t *testing.T) {
	env := timeOnlyEnv(t)
	cases := []struct {
		in                 string
		h, m, s, usec, tzw int
	}{
		{"10:00+05:30:15", 10, 0, 0, 0, -19815},
		{"10:00-0530", 10, 0, 0, 0, 19800},
		{"10:00 +05:30", 10, 0, 0, 0, -19800},
		{"01:02:03.", 1, 2, 3, 0, 28800},
		{"10:00-08:00:30", 10, 0, 0, 0, 28830},
		{"10:00+15:59:59", 10, 0, 0, 0, -57599},
		{"allballs", 0, 0, 0, 0, 0},
		{"2020-07-01 10:00 America/New_York", 10, 0, 0, 0, 14400},
		{"040506.789", 4, 5, 6, 789000, 28800},
	}
	for _, c := range cases {
		d, err := DecodeTimeOnlyInput(c.in, env, true)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if d.Hour != c.h || d.Min != c.m || d.Sec != c.s || d.Usec != c.usec || d.TZWest != c.tzw {
			t.Errorf("%q = %02d:%02d:%02d.%06d west %d", c.in, d.Hour, d.Min, d.Sec, d.Usec, d.TZWest)
		}
	}
	bad := map[string]error{
		"10:00+16":  ErrTZDispOverflow,
		"25:00":     ErrFieldOutOfRange,
		"10:00 Mon": ErrBadFormat,
	}
	for in, want := range bad {
		if _, err := DecodeTimeOnlyInput(in, env, true); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", in, err, want)
		}
	}
}
