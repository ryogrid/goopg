package misc

import (
	"os"
	"testing"
	"time"
)

// TestCheckTimeZone pins check_timezone (commands/variable.c): the stored
// value is the name SHOW TimeZone reports. Expected values and errors are PG
// 18.3's for the same SET statements (the INTERVAL forms arrive flattened by
// the grammar, as flatten_set_variable_args hands them to the hook).
func TestCheckTimeZone(t *testing.T) {
	if IntervalParser == nil {
		// The parser package registers interval_in; stand in for it with the
		// one shape these cases use, hh:mm[:ss] plus "n day" / "n mon".
		IntervalParser = func(s string) (int32, int32, int64, bool) {
			switch s {
			case "02:00:00":
				return 0, 0, 2 * 3600e6, true
			case "-08:00:00":
				return 0, 0, -8 * 3600e6, true
			case "05:30:15":
				return 0, 0, (5*3600 + 30*60 + 15) * 1e6, true
			case "200:00:00":
				return 0, 0, 200 * 3600e6, true
			case "1 day":
				return 0, 1, 0, true
			case "1 mon":
				return 1, 0, 0, true
			}
			return 0, 0, 0, false
		}
		defer func() { IntervalParser = nil }()
	}
	for _, c := range []struct{ in, want string }{
		{"INTERVAL '02:00:00'", "<+02>-02"},
		{"INTERVAL '-08:00:00'", "<-08>+08"},
		{"interval '05:30:15'", "<+05:30:15>-05:30:15"},
		// a number is hours east of UTC
		{"-8", "<-08>+08"},
		{"5.5", "<+05:30>-05:30"},
		{"20", "<+20>-20"},
		// POSIX specifications are upper-cased
		{"UTC+3", "UTC+3"},
		{"<+02>-02", "<+02>-02"},
		{"est5edt,m3.2.0,m11.1.0", "EST5EDT,M3.2.0,M11.1.0"},
	} {
		got, err := checkTimeZone(c.in)
		if err != nil || got != c.want {
			t.Errorf("checkTimeZone(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, c := range []struct{ in, code, detail string }{
		{"INTERVAL '1 day'", "22023", "Cannot specify days in time zone interval."},
		{"INTERVAL '1 mon'", "22023", "Cannot specify months in time zone interval."},
		{"INTERVAL '200:00:00'", "22023", "UTC timezone offset is out of range."},
		{"INTERVAL 'garbage'", "22007", ""},
		{"Inf", "22023", "UTC timezone offset is out of range."},
		{"NaN", "22023", "UTC timezone offset is out of range."},
		{"No/Such_Zone", "22023", ""},
		{"", "22023", ""},
	} {
		_, err := checkTimeZone(c.in)
		verr, ok := err.(*ValidationError)
		if !ok {
			t.Errorf("checkTimeZone(%q) error = %v; want a ValidationError", c.in, err)
			continue
		}
		if verr.SQLState() != c.code || verr.Detail != c.detail {
			t.Errorf("checkTimeZone(%q) = %s %q / %q; want %s / %q", c.in, verr.SQLState(), verr.Msg, verr.Detail, c.code, c.detail)
		}
	}
	// check_log_timezone has no INTERVAL or hours-east forms: "-3" is a POSIX
	// spec with an empty abbreviation (3 hours EAST, POSIX sign), and even
	// INTERVAL '02:00:00' parses as one (std "INTERVAL '", dst "'") — PG
	// 18.3's ALTER SYSTEM SET log_timezone accepts both; "abc" has no offset.
	for _, in := range []string{"-3", "INTERVAL '02:00:00'"} {
		if got, err := checkLogTimeZone(in); err != nil || got != in {
			t.Errorf("checkLogTimeZone(%q) = %q, %v; want it stored as given", in, got, err)
		}
	}
	if _, err := checkLogTimeZone("abc"); err == nil {
		t.Errorf("checkLogTimeZone(abc) accepted a name with no offset")
	}
	if got, err := checkLogTimeZone("UTC+3"); err != nil || got != "UTC+3" {
		t.Errorf("checkLogTimeZone(UTC+3) = %q, %v", got, err)
	}
}

// TestCheckTimeZoneZoneFiles: names resolve case-insensitively to the tz
// file's own spelling, and a tzdata file that is not a zone is rejected.
func TestCheckTimeZoneZoneFiles(t *testing.T) {
	if _, err := os.Stat("/usr/share/zoneinfo/America/New_York"); err != nil {
		t.Skip("no system tzdata")
	}
	for _, c := range []struct{ in, want string }{
		{"america/new_york", "America/New_York"},
		{"utc", "UTC"},
		{"est5edt", "EST5EDT"},
	} {
		if got, err := checkTimeZone(c.in); err != nil || got != c.want {
			t.Errorf("checkTimeZone(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := checkTimeZone("zone.tab"); err == nil {
		t.Errorf("checkTimeZone(zone.tab) accepted a non-zone tzdata file")
	}
}

// TestZoneLocationPOSIX: the stored POSIX names render with their own offset
// (SET TIME ZONE INTERVAL '+02:00' HOUR TO MINUTE shows 14:00:00+02 for
// 12:00 UTC); rule strings switch to daylight time; unresolvable is UTC.
func TestZoneLocationPOSIX(t *testing.T) {
	jan := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	jul := time.Date(2024, 7, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		zone string
		at   time.Time
		off  int
	}{
		{"<+02>-02", jan, 2 * 3600},
		{"<-08>+08", jan, -8 * 3600},
		{"<+05:30:15>-05:30:15", jan, 5*3600 + 30*60 + 15},
		{"UTC+3", jan, -3 * 3600},
		// tzparse allows an empty abbreviation: SET timezone = '+05:30'
		{"+05:30", jan, -(5*3600 + 30*60)},
		{"EST5EDT,M3.2.0,M11.1.0", jan, -5 * 3600},
		{"EST5EDT,M3.2.0,M11.1.0", jul, -4 * 3600},
		{"not a zone", jan, 0},
		{"", jan, 0},
	} {
		_, off := c.at.In(ZoneLocation(c.zone)).Zone()
		if off != c.off {
			t.Errorf("ZoneLocation(%q) offset at %s = %d; want %d", c.zone, c.at.Format("Jan"), off, c.off)
		}
	}
}

// TestParsePOSIXTZ pins tzparse's acceptance decision.
func TestParsePOSIXTZ(t *testing.T) {
	for _, s := range []string{"UTC+3", "<+02>-02", "EST5EDT", "EST5EDT,M3.2.0,M11.1.0", "EST5EDT4,J60/2,300", "<+167:59:59>-167:59:59", "+3", "+05:30"} {
		if _, ok := parsePOSIXTZ(s); !ok {
			t.Errorf("parsePOSIXTZ(%q) rejected", s)
		}
	}
	for _, s := range []string{"UTC", "", "EST", "EST5EDT,M13.1.0,M11.1.0", "EST5EDT,M3.2.0", "<+02-02", "<+168>-168", "EST5EDT,J0,J1"} {
		if _, ok := parsePOSIXTZ(s); ok {
			t.Errorf("parsePOSIXTZ(%q) accepted", s)
		}
	}
}
