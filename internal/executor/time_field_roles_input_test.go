package executor

import (
	"testing"
	"time"
)

// PostgreSQL decides what a numeric run MEANS after splitting the input into
// fields (DecodeTimeCommon / DecodeNumberField, datetime.c), so the role of a
// field depends on how many there are, whether one carries a fraction, and
// whether a meridiem follows. goopg matched time input against a fixed Go
// layout table instead, which cannot express any of that: it rejected the
// run-together form ('040506'), the empty subfield ('10::00'), the leading
// delimiter (':10:00') and the MINUTE TO SECOND reading ('10:00.5') outright —
// and, worse, silently answered 12:00:00 for '12:00 AM', where PG answers
// 00:00:00. Twelve hours wrong, no diagnostic.
//
// pgdatetime.ParseTimeOfDay now owns field decoding for every time-bearing text
// input path; these tests pin the executor-visible half. Each want is what PG
// 18.3 returns for the same literal (local cluster, socket /tmp port 5599).
func TestParseTimeStringPGFieldRoles(t *testing.T) {
	tod := func(h, m, s, ns int) time.Time { return time.Date(1970, 1, 1, h, m, s, ns, time.UTC) }
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"10:00", tod(10, 0, 0, 0)},
		{"1:2:3", tod(1, 2, 3, 0)},
		{"10:00.5", tod(0, 10, 0, 500_000_000)}, // MINUTE TO SECOND
		{"10::00", tod(10, 0, 0, 0)},
		{"10:", tod(10, 0, 0, 0)},
		{":10:00", tod(10, 0, 0, 0)},
		{"040506", tod(4, 5, 6, 0)},
		{"0405", tod(4, 5, 0, 0)},
		{"040506.5", tod(4, 5, 6, 500_000_000)},
		{"T040506", tod(4, 5, 6, 0)},
		{"allballs", tod(0, 0, 0, 0)},
		{"12:00 AM", tod(0, 0, 0, 0)}, // the silent-wrong-answer repro
		{"12:00:00 AM", tod(0, 0, 0, 0)},
		{"12:00 PM", tod(12, 0, 0, 0)},
		{"00:00 PM", tod(12, 0, 0, 0)},
		{"4:05 PM", tod(16, 5, 0, 0)},
		{"10:00AM", tod(10, 0, 0, 0)},
		{"040506.5PM", tod(16, 5, 6, 500_000_000)},
		// Unchanged behaviour, kept here so the rewrite cannot quietly drop it:
		// hour 24, the leap second, and a zone that a time has no date to apply.
		{"24:00:00", time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)},
		{"23:59:60", time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)},
		{"10:00 PST", tod(10, 0, 0, 0)},
		{"2020-01-01 10:00:00", tod(10, 0, 0, 0)},
		{"2020-01-01 12:00 AM", tod(0, 0, 0, 0)},
	} {
		got, err := parseTimeString(tc.in)
		if err != nil {
			t.Errorf("parseTimeString(%q): %v — PG reads this as %v", tc.in, err, tc.want)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("parseTimeString(%q) = %v, want %v (PG 18.3)", tc.in, got, tc.want)
		}
	}
}

// The timetz path reaches the same decoder, so the meridiem and field-role
// rules must hold there too — it used to strip " AM" and never re-attach it.
func TestParseTimeTZStringPGFieldRoles(t *testing.T) {
	for _, tc := range []struct {
		in     string
		want   time.Time
		offset int
	}{
		{"12:00 AM", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), 0},
		{"4:05 PM PST", time.Date(1970, 1, 1, 16, 5, 0, 0, time.UTC), -8 * 3600},
		{"040506+02", time.Date(1970, 1, 1, 4, 5, 6, 0, time.UTC), 2 * 3600},
		{"10:00.5", time.Date(1970, 1, 1, 0, 10, 0, 500_000_000, time.UTC), 0},
		{"allballs", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), 0},
	} {
		got, off, err := parseTimeTZString(tc.in)
		if err != nil {
			t.Errorf("parseTimeTZString(%q): %v — PG reads this as %v (offset %d)", tc.in, err, tc.want, tc.offset)
			continue
		}
		if !got.Equal(tc.want) || off != tc.offset {
			t.Errorf("parseTimeTZString(%q) = %v, %d; want %v, %d (PG 18.3)", tc.in, got, off, tc.want, tc.offset)
		}
	}
}

// A timestamp is a date plus a time of day, so every spelling above is legal
// after a date too. The layout table decodes the date and the zone; only the
// time token goes through pgdatetime.CanonicalizeTimeToken.
func TestParsePGTimestampTextPGFieldRoles(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"2020-01-01 040506", time.Date(2020, 1, 1, 4, 5, 6, 0, time.UTC)},
		{"2020-01-01 10:00.5", time.Date(2020, 1, 1, 0, 10, 0, 500_000_000, time.UTC)},
		{"2020-01-01 12:00 AM", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"2020-01-01 12:30 am", time.Date(2020, 1, 1, 0, 30, 0, 0, time.UTC)},
		{"2020-01-01 10:00 PM", time.Date(2020, 1, 1, 22, 0, 0, 0, time.UTC)},
		{"2020-01-01T10:00 PM", time.Date(2020, 1, 1, 22, 0, 0, 0, time.UTC)},
		{"2020-01-01 10::00", time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)},
		{"2020-01-01 4:05 PM", time.Date(2020, 1, 1, 16, 5, 0, 0, time.UTC)},
		// Unchanged spellings, re-asserted because the retry path now runs
		// after them: they must still be decoded by the plain layout pass.
		{"2020-01-01 10:00", time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)},
		{"2020-01-01 10:00:00.5", time.Date(2020, 1, 1, 10, 0, 0, 500_000_000, time.UTC)},
		{"2020-01-01", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		got, err := parsePGTimestampText(tc.in)
		if err != nil {
			t.Errorf("parsePGTimestampText(%q): %v — PG reads this as %v", tc.in, err, tc.want)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("parsePGTimestampText(%q) = %v, want %v (PG 18.3)", tc.in, got, tc.want)
		}
	}

	// No longer deferred: hour 24 and the leap second roll into the next day
	// (tm2timestamp), and the total-vs-24:00:00 check decides which spellings
	// are legal. Owned by TestTimestampInputHour24AndLeapSecond; kept here as
	// the one assertion that the RETRY path — this test's subject — is what
	// carries them, since the plain layout pass rejects both outright.
	for _, in := range []string{"2020-01-01 24:00:00", "2020-01-01 23:59:60"} {
		got, err := parsePGTimestampText(in)
		if err != nil {
			t.Errorf("parsePGTimestampText(%q): %v — PG reads this as 2020-01-02 00:00:00", in, err)
			continue
		}
		if want := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
			t.Errorf("parsePGTimestampText(%q) = %v, want %v (PG 18.3)", in, got, want)
		}
	}
}
