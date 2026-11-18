package datetime

import (
	"errors"
	"testing"
	"time"
)

func testEnv(order DateOrder) *DecodeEnv {
	return &DecodeEnv{
		Order:   order,
		Now:     time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC),
		Session: time.UTC,
		LookupZone: func(name string) (*time.Location, bool) {
			if name == "america/new_york" {
				loc, err := time.LoadLocation("America/New_York")
				return loc, err == nil
			}
			return nil, false
		},
		WestOffset: func(wall time.Time, loc *time.Location) int {
			l := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc)
			_, east := l.Zone()
			return -east
		},
	}
}

// TestDecodeDateTimeInput pins values checked against PostgreSQL 18.3.
func TestDecodeDateTimeInput(t *testing.T) {
	for _, tc := range []struct {
		in                 string
		order              DateOrder
		y, mo, d, h, mi, s int
		west               int
	}{
		{"Mon Feb 10 17:32:01 1997 PST", DateOrderMDY, 1997, 2, 10, 17, 32, 1, 28800},
		{"19970210 173201 -0800", DateOrderMDY, 1997, 2, 10, 17, 32, 1, 28800},
		{"1997/02/10 17:32:01-0800", DateOrderMDY, 1997, 2, 10, 17, 32, 1, 28800},
		{"01-08-1999", DateOrderMDY, 1999, 1, 8, 0, 0, 0, 0},
		{"01-08-1999", DateOrderDMY, 1999, 8, 1, 0, 0, 0, 0},
		{"01/02/03", DateOrderYMD, 2001, 2, 3, 0, 0, 0, 0},
		{"99-Jan-08", DateOrderYMD, 1999, 1, 8, 0, 0, 0, 0},
		{"1999.008", DateOrderMDY, 1999, 1, 8, 0, 0, 0, 0},
		{"J2451187", DateOrderMDY, 1999, 1, 8, 0, 0, 0, 0},
		{"January 8, 99 BC", DateOrderMDY, -98, 1, 8, 0, 0, 0, 0},
		{"Feb 10 5:32PM 1997", DateOrderMDY, 1997, 2, 10, 17, 32, 0, 0},
		{"Jul 4 2000 10:00 america/new_york", DateOrderMDY, 2000, 7, 4, 10, 0, 0, 14400},
		{"20000101T100000-0700", DateOrderMDY, 2000, 1, 1, 10, 0, 0, 25200},
	} {
		got, err := DecodeDateTimeInput(tc.in, testEnv(tc.order))
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got.Year != tc.y || got.Mon != tc.mo || got.Mday != tc.d || got.Hour != tc.h ||
			got.Min != tc.mi || got.Sec != tc.s || got.TZWest != tc.west {
			t.Errorf("%q = %+v", tc.in, got)
		}
	}
}

func TestDecodeDateTimeInputErrors(t *testing.T) {
	var badZone *BadTimezoneError
	for in, check := range map[string]func(error) bool{
		"garbage":                func(e error) bool { return errors.Is(e, ErrBadFormat) },
		"10:00":                  func(e error) bool { return errors.Is(e, ErrBadFormat) },
		"2000-13-01":             func(e error) bool { return errors.Is(e, ErrMDFieldOverflow) },
		"Feb 30 2000":            func(e error) bool { return errors.Is(e, ErrFieldOutOfRange) && !errors.Is(e, ErrMDFieldOverflow) },
		"0000-01-01":             func(e error) bool { return errors.Is(e, ErrFieldOutOfRange) },
		"2000-01-01 10:00 +16":   func(e error) bool { return errors.Is(e, ErrTZDispOverflow) },
		"Jan 1 2000 10:00 No/Su": func(e error) bool { return errors.As(e, &badZone) },
	} {
		if _, err := DecodeDateTimeInput(in, testEnv(DateOrderMDY)); !check(err) {
			t.Errorf("%q: got %v", in, err)
		}
	}
}
