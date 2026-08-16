package datetime

import (
	"errors"
	"testing"
)

// Every want below is what PG 18.3 answers for `select '<input>'::time` on a
// local 18.3 cluster (socket /tmp, port 5599). The point of the table is that
// PostgreSQL assigns a ROLE to each numeric run after splitting the string
// (DecodeTimeCommon / DecodeNumberField, datetime.c) rather than matching a
// layout, so the field count, an empty field, a fraction and a meridiem each
// change the reading.
func TestParseTimeOfDayPGAcceptedForms(t *testing.T) {
	for _, tc := range []struct {
		in      string
		h, m, s int
		nsec    int
	}{
		{in: "10:00", h: 10},
		{in: "10:00:00", h: 10},
		{in: "1:2:3", h: 1, m: 2, s: 3},
		{in: "04:05:06.789", h: 4, m: 5, s: 6, nsec: 789_000_000},
		// A fraction on a TWO-field time means MINUTE TO SECOND: the fields
		// shift right and the hour becomes zero. This is the spelling most
		// likely to be read wrong by a layout table — and 10:00:00.5 is what a
		// layout table would have answered.
		{in: "10:00.5", m: 10, nsec: 500_000_000},
		// An empty subfield decodes as 0: strtoint consumes nothing.
		{in: "10::00", h: 10},
		{in: "10:", h: 10},
		{in: "10:00:", h: 10},
		// Leading punctuation is only a field delimiter to ParseDateTime.
		{in: ":10:00", h: 10},
		// DecodeNumberField's run-together forms.
		{in: "040506", h: 4, m: 5, s: 6},
		{in: "0405", h: 4, m: 5},
		{in: "040506.5", h: 4, m: 5, s: 6, nsec: 500_000_000},
		{in: "0405.5", h: 4, m: 5, nsec: 500_000_000},
		{in: "2400", h: 24},
		// The ISO 8601 separator, in either case.
		{in: "T040506", h: 4, m: 5, s: 6},
		{in: "t040506", h: 4, m: 5, s: 6},
		// Meridiem: space-separated or run together, and hour 12 is special.
		{in: "4:05 PM", h: 16, m: 5},
		{in: "4:05pm", h: 16, m: 5},
		{in: "12:00 AM"},
		{in: "12:00:00 AM"},
		{in: "12:00 PM", h: 12},
		{in: "00:00 PM", h: 12},
		{in: "040506.5PM", h: 16, m: 5, s: 6, nsec: 500_000_000},
		{in: "0405 PM", h: 16, m: 5},
		// datetktbl's zero-time token.
		{in: "allballs"},
		{in: "ALLBALLS"},
		// Left for the caller to range-check, exactly as PG leaves them.
		{in: "24:00:00", h: 24},
		{in: "23:59:60", h: 23, m: 59, s: 60},
		{in: "25:00:00", h: 25},
		// Fractions beyond microseconds round, they do not error.
		{in: "00:00:00.123456789", nsec: 123_457_000},
		{in: " 10:00 ", h: 10},
	} {
		got, err := ParseTimeOfDay(tc.in)
		if err != nil {
			t.Errorf("ParseTimeOfDay(%q): %v — PG reads this as %02d:%02d:%02d.%09d",
				tc.in, err, tc.h, tc.m, tc.s, tc.nsec)
			continue
		}
		want := TimeOfDay{Hour: tc.h, Min: tc.m, Sec: tc.s, Nsec: tc.nsec}
		if got != want {
			t.Errorf("ParseTimeOfDay(%q) = %+v, want %+v (PG 18.3)", tc.in, got, want)
		}
	}
}

// Inputs PostgreSQL refuses, split by the SQLSTATE it refuses them with: a
// string no field decoder recognises is a format error (22007), while fields
// that decoded but cannot exist are a range error (22008).
func TestParseTimeOfDayRejects(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want error
	}{
		{"", ErrTimeBadFormat},
		{"04", ErrTimeBadFormat},      // a 2-digit run is not a time
		{"405", ErrTimeBadFormat},     // nor a 3-digit one
		{"4050607", ErrTimeBadFormat}, // nor 7
		{"10:00:00.5.5", ErrTimeBadFormat},
		{"::5", ErrTimeBadFormat}, // "5" alone after the delimiters
		{"pm", ErrTimeBadFormat},
		{"midnight", ErrTimeBadFormat}, // not in datetktbl for time input
		{"zulu", ErrTimeBadFormat},
		{"10:61", ErrTimeFieldOverflow},
		{"10:00:61", ErrTimeFieldOverflow},
		{"1061", ErrTimeFieldOverflow},
		{"13:00 PM", ErrTimeFieldOverflow}, // a meridiem past hour 12
		{"13:00 AM", ErrTimeFieldOverflow},
	} {
		got, err := ParseTimeOfDay(tc.in)
		if !errors.Is(err, tc.want) {
			t.Errorf("ParseTimeOfDay(%q) = %+v, %v; want error %v", tc.in, got, err, tc.want)
		}
	}
}

// CanonicalizeTimeToken must rewrite only the time token and leave the zone
// suffix byte-identical, so the timestamp layout table can decode the rest.
func TestCanonicalizeTimeToken(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  string
		carry int
		err   error
	}{
		{in: "12:00 AM", want: "00:00:00"},
		{in: "10:00 PM", want: "22:00:00"},
		{in: "040506", want: "04:05:06"},
		{in: "040506.25+02", want: "04:05:06.25+02"},
		{in: "10::00", want: "10:00:00"},
		{in: "10:00.5", want: "00:10:00.5"},
		{in: "10:00:00Z", want: "10:00:00Z"},
		{in: "10:00:00-04", want: "10:00:00-04"},
		{in: "10:00 PST", want: "10:00:00 PST"},
		// The whole day: legal to PG, and the canonical spelling carries it as
		// midnight plus one day for the caller to compose in (tm2timestamp).
		{in: "24:00:00", want: "00:00:00", carry: 1},
		{in: "23:59:60", want: "00:00:00", carry: 1},
		{in: "23:59:60+05:30", want: "00:00:00+05:30", carry: 1},
		// Below the day boundary a leap second just rolls the minute.
		{in: "10:00:60", want: "10:01:00"},
		{in: "10:59:60", want: "11:00:00"},
		// Past the day boundary: time_overflows() says no.
		{in: "24:00:00.5", want: "24:00:00.5", err: ErrTimeFieldOverflow},
		{in: "23:59:60.5", want: "23:59:60.5", err: ErrTimeFieldOverflow},
		{in: "24:00:01", want: "24:00:01", err: ErrTimeFieldOverflow},
		{in: "25:00:00", want: "25:00:00", err: ErrTimeFieldOverflow},
		// Nothing time-like at the head.
		{in: "PST", want: "PST", err: ErrTimeBadFormat},
		{in: "", want: "", err: ErrTimeBadFormat},
	} {
		got, carry, err := CanonicalizeTimeToken(tc.in)
		if got != tc.want || carry != tc.carry || !errors.Is(err, tc.err) {
			t.Errorf("CanonicalizeTimeToken(%q) = %q, %d, %v; want %q, %d, %v",
				tc.in, got, carry, err, tc.want, tc.carry, tc.err)
		}
	}
}
