package executor

import (
	"testing"
	"time"
)

// TestRunTogetherDateInput pins DecodeNumberField's date arm end-to-end through
// the executor's date/timestamp entry points. Every `want` is what PG 18.3
// answered for the same literal on a stock server (captured for this slice).
//
// The forms all share one property: the numeric run carries no separators, so
// PG's field splitter hands it to DecodeNumberField, which reads the last two
// digits as the day, the two before them as the month, and everything left as
// the year. goopg's fixed Go layouts cannot express that, and rejected all of
// them with 22007 before pgdatetime.NormalizeDateTimeInput existed.
func TestRunTogetherDateInput(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"20200101", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"20200101 040506", time.Date(2020, 1, 1, 4, 5, 6, 0, time.UTC)},
		{"20200101T040506", time.Date(2020, 1, 1, 4, 5, 6, 0, time.UTC)},
		{"20200101 04:05:06", time.Date(2020, 1, 1, 4, 5, 6, 0, time.UTC)},
		{"20200101 0405", time.Date(2020, 1, 1, 4, 5, 0, 0, time.UTC)},
		{"20200101 10:00", time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)},
		{"19991231T235959", time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC)},
		// A two-digit year is windowed onto 1970..2069 by ValidateDate().
		{"200101", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"690101", time.Date(2069, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"700101", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"990101", time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"000101", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)},
		// Six digits with no target type in hand are a TIME to DecodeTimeOnly
		// and a DATE to DecodeDateTime; these two entry points both know their
		// target, so here the date reading is the right one ('040506'::date is
		// 2004-05-06 on PG 18.3).
		{"040506", time.Date(2004, 5, 6, 0, 0, 0, 0, time.UTC)},
	} {
		got, err := parseCopyTimestamp(tc.in)
		if err != nil {
			t.Errorf("parseCopyTimestamp(%q): %v — PG reads this as %v", tc.in, err, tc.want)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("parseCopyTimestamp(%q) = %v, want %v (PG 18.3)", tc.in, got, tc.want)
		}
		// date_in() decodes the same fields but keeps only the calendar ones —
		// its caller drops the time of day, so only y/m/d is asserted here (that
		// the DAY is the date's and not the timestamp's is owned by the hour-24
		// carry tests).
		gotDate, err := parseDateInputText(tc.in)
		if err != nil {
			t.Errorf("parseDateInputText(%q): %v — PG reads this as %v", tc.in, err, tc.want)
			continue
		}
		wy, wm, wd := tc.want.Date()
		if gy, gm, gd := gotDate.Date(); gy != wy || gm != wm || gd != wd {
			t.Errorf("parseDateInputText(%q) = %04d-%02d-%02d, want %04d-%02d-%02d (PG 18.3)",
				tc.in, gy, gm, gd, wy, wm, wd)
		}
	}

	// Spellings PG itself rejects must stay rejected. '20200101.5' takes
	// DecodeNumberField's fractional-seconds branch, which then demands a 4- or
	// 6-digit remainder; '20200' and '0405' are too short to be a date at all.
	for _, in := range []string{"20200101.5", "20200", "0405"} {
		if got, err := parseCopyTimestamp(in); err == nil {
			t.Errorf("parseCopyTimestamp(%q) = %v, want an error (PG 18.3 rejects it)", in, got)
		}
	}
}

// TestRunTogetherTimeStaysTimeOnly is the sibling-path guard for the change
// above: DecodeTimeOnly starts with the date fields already set in fmask, so it
// never reaches the date arm — the same six digits that are 2004-05-06 to
// date_in are 04:05:06 to time_in on PG 18.3.
func TestRunTogetherTimeStaysTimeOnly(t *testing.T) {
	for _, tc := range []struct {
		in      string
		h, m, s int
	}{
		{"040506", 4, 5, 6},
		{"0405", 4, 5, 0},
		{"200101", 20, 1, 1},
		{"000101", 0, 1, 1},
	} {
		got, err := parseTimeString(tc.in)
		if err != nil {
			t.Errorf("parseTimeString(%q): %v — PG reads this as %02d:%02d:%02d", tc.in, err, tc.h, tc.m, tc.s)
			continue
		}
		if h, m, s := got.Clock(); h != tc.h || m != tc.m || s != tc.s {
			t.Errorf("parseTimeString(%q) = %02d:%02d:%02d, want %02d:%02d:%02d (PG 18.3)",
				tc.in, h, m, s, tc.h, tc.m, tc.s)
		}
	}
}
