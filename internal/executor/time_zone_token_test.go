package executor

import (
	"strings"
	"testing"
	"time"
)

// M0119-0006: the trailing field of a TIME / TIMETZ literal is now classified
// the way PostgreSQL's ParseDateTime → DecodeTimeOnly pair classifies it,
// instead of being assumed to be a timezone and discarded. Before this slice
// every case marked wantCode "22007"/"22023" below silently PARSED, so a
// mistyped or nonsense zone landed in the column as a plain time with no
// diagnostic at all.
//
// Every expectation here was captured from PG 18.3 on port 65432 (session
// TimeZone Asia/Tokyo) — see the table in
// docs/design/0125-0007-pg-faithful-date-field-decode.md §17.4.

// timeZoneTokenCase is one oracle-pinned input.
type timeZoneTokenCase struct {
	in string
	// wantCode is "" when PG accepts the input; otherwise the SQLSTATE.
	wantCode string
	// wantTime is the HH:MM:SS goopg must produce when wantCode is "".
	wantTime string
	// wantMsgFragment, when set, must appear in the error message.
	wantMsgFragment string
	why             string
}

var timeZoneTokenCases = []timeZoneTokenCase{
	// --- accepted: the field really is a zone, and a time discards it ---
	{in: "10:00 PST", wantTime: "10:00:00", why: "core-table abbreviation"},
	{in: "10:00 UTC", wantTime: "10:00:00", why: "core-table abbreviation"},
	{in: "10:00 Z", wantTime: "10:00:00", why: "Z is the UTC entry in datetbl"},
	{in: "10:00 +05", wantTime: "10:00:00", why: "explicit displacement, spaced"},
	{in: "10:00+05", wantTime: "10:00:00", why: "explicit displacement, attached"},
	{in: "10:00 Etc/GMT", wantTime: "10:00:00", why: "zone NAME, fixed offset — resolvable without a date"},
	{in: "10:00 UTC-5", wantTime: "10:00:00", why: "POSIX-style zone spec"},

	// --- accepted: era and meridiem are ordinary fields, not zones ---
	{in: "10:00 BC", wantTime: "10:00:00", why: "ADBC field; a time has no year to shift"},
	{in: "10:00 AD", wantTime: "10:00:00", why: "ADBC field"},
	{in: "10:00:00 PST BC", wantTime: "10:00:00", why: "a zone field AND an era field may both follow"},
	{in: "10:00 AM BC", wantTime: "10:00:00", why: "era follows the meridiem; the meridiem must survive"},
	{in: "12:00 AM", wantTime: "00:00:00", why: "hour-12 AM is hour 0 — the meridiem is never a zone"},

	// --- 22023: shaped like a zone NAME, names no zone ---
	{in: "10:00 A.M.", wantCode: "22023", wantMsgFragment: `time zone "a.m." not recognized`,
		why: "alpha run followed by '.' is DTK_DATE, so it reaches pg_tzset and fails there"},
	{in: "10:00 P.M.", wantCode: "22023", wantMsgFragment: `time zone "p.m." not recognized`,
		why: "the dotted meridiem spelling is NOT the AMPM keyword"},
	{in: "10:00 ABC-DEF", wantCode: "22023", wantMsgFragment: `time zone "abc-def" not recognized`,
		why: "PG reports the LOWERCASED token, not the input"},
	{in: "10:00 Foo.Bar", wantCode: "22023", wantMsgFragment: `time zone "foo.bar" not recognized`},

	// --- 22007: never reaches the zone database at all ---
	{in: "10:00 GARBAGE", wantCode: "22007", why: "bare word, absent from the core keyword table"},
	{in: "10:00 zzz", wantCode: "22007"},
	{in: "10:00 Japan", wantCode: "22007",
		why: "a REAL zone name spelled without punctuation stays DTK_STRING, so pg_tzset is never consulted"},
	{in: "10:00 EST5EDT", wantCode: "22007",
		why: "letters+digit IS the zone-name shape (est is no datetktbl keyword), and EST5EDT is a real DST zone — so it needs a date"},
	{in: "10:00 allballs", wantCode: "22007", why: "a core keyword, but not a zone one"},
	{in: "10:00 today", wantCode: "22007", why: "likewise"},
	{in: "10:00 Mon", wantCode: "22007", why: "DOW keyword, not a zone"},
	{in: "10:00 January", wantCode: "22007", why: "MONTH keyword, not a zone"},
	{in: "10:00 a_b", wantCode: "22007", why: "'_' cannot be the first punctuation, so no DTK_DATE"},
	{in: "10:00 xy:zw", wantCode: "22007", why: "':' cannot be the first punctuation either"},
	{in: "10:00 12", wantCode: "22007"},
	{in: "10:00 -", wantCode: "22007"},
	{in: "10:00 .", wantCode: "22007"},
	{in: "10:00 pst pdt", wantCode: "22007", why: "two zone fields — DecodeTimeOnly's fmask rejects the repeat"},
	{in: "10:00 America/New_York", wantCode: "22007",
		why: "a DST zone needs a date to resolve; the bare time has none"},
}

func TestParseTimeStringValidatesTrailingZoneField(t *testing.T) {
	for _, tc := range timeZoneTokenCases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseTimeString(tc.in)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("parseTimeString(%q) = error %v, want %s (%s)", tc.in, err, tc.wantTime, tc.why)
				}
				if h := got.Format("15:04:05"); h != tc.wantTime {
					t.Fatalf("parseTimeString(%q) = %s, want %s (%s)", tc.in, h, tc.wantTime, tc.why)
				}
				return
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("parseTimeString(%q) = (%v, %v), want SQLSTATE %s (%s)",
					tc.in, got.Format("15:04:05"), err, tc.wantCode, tc.why)
			}
			if ee.Code != tc.wantCode {
				t.Fatalf("parseTimeString(%q) code = %s (%s), want %s (%s)",
					tc.in, ee.Code, ee.Message, tc.wantCode, tc.why)
			}
			if tc.wantMsgFragment != "" && !strings.Contains(ee.Message, tc.wantMsgFragment) {
				t.Fatalf("parseTimeString(%q) message = %q, want it to contain %q",
					tc.in, ee.Message, tc.wantMsgFragment)
			}
		})
	}
}

// The timetz path shares the classifier, so it must reach the same verdicts —
// with its own type name in the 22007 message and PG's unchanged 22023 text
// (which names the zone, not the input, so wrapTimeTZError must not rewrite it).
func TestParseTimeTZStringValidatesTrailingZoneField(t *testing.T) {
	for _, tc := range timeZoneTokenCases {
		t.Run(tc.in, func(t *testing.T) {
			got, off, err := parseTimeTZString(tc.in)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("parseTimeTZString(%q) = error %v, want %s (%s)", tc.in, err, tc.wantTime, tc.why)
				}
				if h := got.Format("15:04:05"); h != tc.wantTime {
					t.Fatalf("parseTimeTZString(%q) = %s (offset %d), want %s (%s)",
						tc.in, h, off, tc.wantTime, tc.why)
				}
				return
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("parseTimeTZString(%q) = (%v, %v), want SQLSTATE %s (%s)",
					tc.in, got.Format("15:04:05"), err, tc.wantCode, tc.why)
			}
			if ee.Code != tc.wantCode {
				t.Fatalf("parseTimeTZString(%q) code = %s (%s), want %s (%s)",
					tc.in, ee.Code, ee.Message, tc.wantCode, tc.why)
			}
			if tc.wantCode == "22007" && !strings.Contains(ee.Message, "time with time zone") {
				t.Fatalf("parseTimeTZString(%q) message = %q, want the timetz type name", tc.in, ee.Message)
			}
			if tc.wantMsgFragment != "" && !strings.Contains(ee.Message, tc.wantMsgFragment) {
				t.Fatalf("parseTimeTZString(%q) message = %q, want it to contain %q",
					tc.in, ee.Message, tc.wantMsgFragment)
			}
		})
	}
}

// The offsets a timetz KEEPS. The POSIX sign inversion is the interesting one:
// TZ='UTC-5' states the offset to add to local time to reach UTC, so it is
// UTC+05:00 — PG 18.3 renders '10:00 UTC-5'::timetz as 10:00:00+05.
func TestParseTimeTZStringZoneOffsets(t *testing.T) {
	cases := []struct {
		in   string
		want int
		why  string
	}{
		{in: "10:00 UTC", want: 0},
		{in: "10:00 JST", want: 9 * 3600},
		{in: "10:00 PST", want: -8 * 3600},
		{in: "10:00 +05:30", want: 5*3600 + 30*60},
		{in: "10:00 UTC-5", want: 5 * 3600, why: "POSIX sign is inverted"},
		{in: "10:00 UTC+5", want: -5 * 3600, why: "POSIX sign is inverted"},
		{in: "10:00 GMT+3", want: -3 * 3600, why: "POSIX sign is inverted"},
		{in: "10:00 UTC-5:30", want: 5*3600 + 30*60},
		{in: "10:00 Etc/GMT", want: 0, why: "fixed-offset zone name"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, off, err := parseTimeTZString(tc.in)
			if err != nil {
				t.Fatalf("parseTimeTZString(%q) = error %v", tc.in, err)
			}
			if off != tc.want {
				t.Fatalf("parseTimeTZString(%q) offset = %d, want %d (%s)", tc.in, off, tc.want, tc.why)
			}
		})
	}
}

// A date prefix is what lets a DST zone name resolve, so the two shapes that
// differ ONLY in the presence of a date must land on different verdicts. This
// is the pair that a naive "reject anything with a slash" rule gets wrong in
// one direction and a "strip anything" rule gets wrong in the other.
func TestTrailingZoneFieldNeedsDateOnlyForDSTZones(t *testing.T) {
	if _, err := parseTimeString("2003-03-07 15:36:39 America/New_York"); err != nil {
		t.Fatalf("dated DST zone: unexpected error %v", err)
	}
	if _, err := parseTimeString("15:36:39 America/New_York"); err == nil {
		t.Fatal("bare DST zone: want 22007, got success")
	}
	// The 22023/22007 split survives the date prefix too.
	for _, tc := range []struct{ in, code string }{
		{"2020-01-01 10:00 GARBAGE", "22007"},
		{"2020-01-01 10:00 A.M.", "22023"},
	} {
		_, err := parseTimeString(tc.in)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != tc.code {
			t.Fatalf("parseTimeString(%q) = %v, want SQLSTATE %s", tc.in, err, tc.code)
		}
	}
}

// fixedZoneOffset is the sampling stand-in for pg_get_timezone_offset(); if it
// ever called a DST zone fixed, every DST zone name would start being accepted
// on a bare time with a silently wrong offset.
func TestFixedZoneOffsetSeparatesFixedFromDSTZones(t *testing.T) {
	for _, name := range []string{"Etc/GMT", "Etc/GMT-5", "UTC"} {
		loc := mustLoadLocation(t, name)
		if _, fixed := fixedZoneOffset(loc); !fixed {
			t.Fatalf("fixedZoneOffset(%s) = not fixed, want fixed", name)
		}
	}
	for _, name := range []string{"America/New_York", "Europe/Paris", "Australia/Sydney"} {
		loc := mustLoadLocation(t, name)
		if off, fixed := fixedZoneOffset(loc); fixed {
			t.Fatalf("fixedZoneOffset(%s) = fixed at %d, want not fixed", name, off)
		}
	}
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zoneinfo %s unavailable: %v", name, err)
	}
	return loc
}
