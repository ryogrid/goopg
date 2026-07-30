package executor

import (
	"testing"
	"time"
)

// TestTryParseStringAsUnpaddedDate pins the coercion behind every
// date-versus-literal comparison. `d_date = '2002-5-01'` never raised an error
// on goopg: promoteCrossKind asked tryParseStringAs to read the unknown-typed
// literal as the column's type, the fixed Go layouts rejected the unpadded
// month, and the failure was reported as "leave it a string" — after which the
// comparison simply came out false. Three TPC-DS queries (Q16/Q94/Q95) returned
// 0/NULL/NULL that way, all three carrying the same wrong-answer checksum
// against three different oracle checksums, and nothing in the log said so.
//
// PostgreSQL has no such case: DecodeDate reads each numeric field separately
// (postgres/src/backend/utils/adt/datetime.c), so '2002-5-01' and '2002-05-01'
// decode to the same date and an undecodable literal raises 22007 at coercion
// time. M0125-0007.
func TestTryParseStringAsUnpaddedDate(t *testing.T) {
	want := time.Date(2002, 5, 1, 0, 0, 0, 0, time.UTC)
	for _, lit := range []string{"2002-5-01", "2002-05-1", "2002-5-1", "2002-05-01", " 2002-5-1 "} {
		got := tryParseStringAs(KindTime, lit)
		if got.Kind != KindTime {
			t.Errorf("tryParseStringAs(KindTime, %q) left it a %v — a date comparison against this literal would silently be false", lit, got.Kind)
			continue
		}
		if !got.TimeValue().Equal(want) {
			t.Errorf("tryParseStringAs(KindTime, %q) = %v, want %v", lit, got.TimeValue(), want)
		}
	}
}

// TestTryParseStringAsTimestampLiteralKeepsItsDate pins the sibling defect the
// unpadded-date work uncovered: a literal that carries a DATE part must coerce
// to that timestamp, not to its bare time-of-day. parseTimeString strips the
// date prefix and anchors the remainder at 1970-01-01, and it used to be tried
// before parseCopyTimestamp — so `ts_col = '2002-05-01 03:04:05'` compared
// 2002-05-01T03:04:05 against 1970-01-01T03:04:05 and reported no match. The
// literal was fully padded; this one was never about padding. M0125-0007.
func TestTryParseStringAsTimestampLiteralKeepsItsDate(t *testing.T) {
	want := time.Date(2002, 5, 1, 3, 4, 5, 0, time.UTC)
	for _, lit := range []string{"2002-05-01 03:04:05", "2002-5-1 3:4:5"} {
		got := tryParseStringAs(KindTime, lit)
		if got.Kind != KindTime {
			t.Fatalf("tryParseStringAs(KindTime, %q) left it a %v", lit, got.Kind)
		}
		if !got.TimeValue().UTC().Equal(want) {
			t.Errorf("tryParseStringAs(KindTime, %q) = %v, want %v (date part must survive)",
				lit, got.TimeValue().UTC(), want)
		}
	}
}

// TestTryParseStringAsTimeTZStillWins guards the M0097-0004 ordering the fix
// above had to step in front of: an offset-bearing time-of-day with NO date
// part must still coerce to timetz so the offset is preserved.
func TestTryParseStringAsTimeTZStillWins(t *testing.T) {
	got := tryParseStringAs(KindTime, "05:06:07-07")
	if got.Kind != KindTime {
		t.Fatalf("tryParseStringAs(KindTime, %q) = kind %v", "05:06:07-07", got.Kind)
	}
	if got.TimeTZOffsetSecs() != -7*3600 {
		t.Errorf("'05:06:07-07' coerced with offset %ds, want %ds — the date-prefix fast path must not swallow bare times",
			got.TimeTZOffsetSecs(), -7*3600)
	}
}

// TestParseTimeStringUnpaddedFields covers the time-of-day sibling of the date
// defect. Go's "15:04:05" layout takes a one-digit hour but demands two digits
// for minute and second, so '03:4:05' and '3:4:5' were rejected where PG
// accepts both. parseTimeString also indexes fixed byte offsets (s[:2] for the
// hour-24 rewrite, s[5] for the leap-second probe, s[4]/s[7] for a date
// prefix), all of which only ever worked on padded input.
func TestParseTimeStringUnpaddedFields(t *testing.T) {
	cases := []struct {
		in      string
		h, m, s int
	}{
		{"3:04:05", 3, 4, 5},
		{"03:4:05", 3, 4, 5},
		{"3:4:5", 3, 4, 5},
		{"03:04:5", 3, 4, 5},
		{"3:4", 3, 4, 0},
		{"2002-5-1 3:4:5", 3, 4, 5}, // date prefix stripped, unpadded on both sides
	}
	for _, c := range cases {
		got, err := parseTimeString(c.in)
		if err != nil {
			t.Errorf("parseTimeString(%q): %v", c.in, err)
			continue
		}
		if got.Hour() != c.h || got.Minute() != c.m || got.Second() != c.s {
			t.Errorf("parseTimeString(%q) = %02d:%02d:%02d, want %02d:%02d:%02d",
				c.in, got.Hour(), got.Minute(), got.Second(), c.h, c.m, c.s)
		}
	}
}

// TestParseCopyTimestampUnpaddedFields covers the COPY TEXT reader and the
// implicit date coercion in codec.go, both of which funnel through
// parseCopyTimestamp.
func TestParseCopyTimestampUnpaddedFields(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2002-5-1", time.Date(2002, 5, 1, 0, 0, 0, 0, time.UTC)},
		{"2002-5-1 3:4:5", time.Date(2002, 5, 1, 3, 4, 5, 0, time.UTC)},
		{"2002-05-01 03:04:05", time.Date(2002, 5, 1, 3, 4, 5, 0, time.UTC)},
	}
	for _, c := range cases {
		got, err := parseCopyTimestamp(c.in)
		if err != nil {
			t.Errorf("parseCopyTimestamp(%q): %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parseCopyTimestamp(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestParseCopyTimestampStillRejectsForeignSpellings is the other half: the
// normaliser must not have widened acceptance beyond the ISO numeric subset.
// Each of these is still a gap versus PG (see the M0125-0007 deferral rows),
// and each must keep failing LOUDLY rather than decode to some invented date.
func TestParseCopyTimestampStillRejectsForeignSpellings(t *testing.T) {
	for _, in := range []string{"2002-5-32", "2002-13-1", "2002-2-30", "2002-005-01", "2002/5/1", "02-5-1", "garbage"} {
		if got, err := parseCopyTimestamp(in); err == nil {
			t.Errorf("parseCopyTimestamp(%q) unexpectedly succeeded with %v", in, got)
		}
	}
}
