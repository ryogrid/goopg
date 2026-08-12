package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006: a TIMETZ literal that carries no zone field of its own does not
// take +00 — it takes the SESSION TimeZone's offset. Before this slice
// `'10:00'::timetz` was `10:00:00+00` on every session, so a client on a
// non-UTC session read back a different instant than the one it wrote.
//
// Upstream: DecodeTimeOnly's "timezone not specified? then use session
// timezone" arm (postgres/src/backend/utils/adt/datetime.c) builds a struct
// pg_tm from GetCurrentDateTime() — or from the DATE the input itself carries,
// when it carries one — plus the parsed tm_hour/tm_min/tm_sec, and hands it to
// DetermineTimeZoneOffset. The date matters because the answer is DST-sensitive.
//
// Every expectation below was captured from PG 18.3 on port 65432 by SETting
// TimeZone and selecting the literal (see the table in
// docs/design/0125-0007-pg-faithful-date-field-decode.md §18).

// TestTimeTZDatePrefixedTakesSessionZoneAtThatDate pins the date-carrying
// inputs, whose expectations are stable in time and therefore assertable as
// exact offsets. `2020-01-05` and `2020-07-05` straddle the US DST boundary,
// so America/New_York must answer -05 for one and -04 for the other from the
// SAME session — the property a fixed fallback offset cannot have.
func TestTimeTZDatePrefixedTakesSessionZoneAtThatDate(t *testing.T) {
	cases := []struct {
		zone string
		in   string
		want int // seconds east of UTC
		why  string
	}{
		{"America/New_York", "2020-01-05 10:00", -5 * 3600, "EST — standard time"},
		{"America/New_York", "2020-07-05 10:00", -4 * 3600, "EDT — the same zone, six months on"},
		{"Asia/Tokyo", "2020-01-05 10:00", 9 * 3600, "JST has no DST"},
		{"Asia/Tokyo", "2020-07-05 10:00", 9 * 3600, "JST has no DST"},
		{"Asia/Kolkata", "2020-01-05 10:00", 5*3600 + 30*60, "half-hour offset"},
		{"UTC", "2020-01-05 10:00", 0, "the boot default spelled explicitly"},
		{"", "2020-01-05 10:00", 0, "unset GUC keeps the pre-slice answer"},

		// An input that DOES carry a zone field keeps it: the session zone is a
		// fallback, never an override.
		{"America/New_York", "2020-07-05 10:00 UTC", 0, "explicit zone wins over the session"},
		{"Asia/Tokyo", "2020-01-05 10:00+03", 3 * 3600, "explicit displacement wins"},
	}
	for _, tc := range cases {
		got, off, err := parseTimeTZString(tc.in, tc.zone)
		if err != nil {
			t.Errorf("parseTimeTZString(%q, %q): %v", tc.in, tc.zone, err)
			continue
		}
		if off != tc.want {
			t.Errorf("parseTimeTZString(%q, %q) offset = %d, want %d (%s)",
				tc.in, tc.zone, off, tc.want, tc.why)
		}
		if h, m, s := got.Clock(); h != 10 || m != 0 || s != 0 {
			t.Errorf("parseTimeTZString(%q, %q) time = %02d:%02d:%02d, want 10:00:00",
				tc.in, tc.zone, h, m, s)
		}
	}
}

// TestTimeTZBareTakesSessionZoneToday covers the no-date form, whose PG answer
// depends on TODAY's date and so cannot be pinned to a literal offset without
// the test rotting at the next DST transition. The assertion is therefore the
// same computation upstream performs — Go's own tzdata lookup for today in the
// zone — which still fails loudly if the fallback is dropped back to +00.
func TestTimeTZBareTakesSessionZoneToday(t *testing.T) {
	for _, zone := range []string{"America/New_York", "Asia/Tokyo", "Asia/Kolkata", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("tzdata for %s unavailable: %v", zone, err)
		}
		now := time.Now().In(loc)
		_, want := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, loc).Zone()

		_, off, err := parseTimeTZString("10:00", zone)
		if err != nil {
			t.Fatalf("parseTimeTZString(%q, %q): %v", "10:00", zone, err)
		}
		if off != want {
			t.Errorf("parseTimeTZString(\"10:00\", %q) offset = %d, want %d", zone, off, want)
		}
	}
	// The unset GUC must not move: it is the boot default, UTC, and the whole
	// pre-slice corpus of timetz expectations is written against it.
	if _, off, err := parseTimeTZString("10:00", ""); err != nil || off != 0 {
		t.Errorf("parseTimeTZString(\"10:00\", \"\") = offset %d, err %v; want 0, nil", off, err)
	}
}

// TestTimeTZExplicitZeroZoneIsNotTheFallback is the regression guard for the
// sentinel this slice had to replace: the pre-slice code spelled "no zone
// field" as `offsetSecs == 0`, which is indistinguishable from a zone field
// that really resolved to +00. Under a non-UTC session those two must produce
// DIFFERENT answers.
func TestTimeTZExplicitZeroZoneIsNotTheFallback(t *testing.T) {
	const zone = "Asia/Tokyo"
	for _, in := range []string{"10:00 UTC", "10:00+00", "10:00 Z", "10:00 GMT"} {
		if _, off, err := parseTimeTZString(in, zone); err != nil || off != 0 {
			t.Errorf("parseTimeTZString(%q, %q) = offset %d, err %v; want 0, nil "+
				"(an explicit UTC zone must not fall back to the session zone)", in, zone, off, err)
		}
	}
	if _, off, err := parseTimeTZString("10:00", zone); err != nil || off != 9*3600 {
		t.Errorf("parseTimeTZString(\"10:00\", %q) = offset %d, err %v; want 32400, nil", zone, off, err)
	}
}

// TestTimeTZInsertCoercionUsesSessionZone guards the sibling of the cast path:
// an INSERT/UPDATE literal is coerced to its column type by
// coerceRowForConstraintChecks, and until this slice `timetz` was absent from
// its switch — so the still-KindString literal travelled all the way to
// encodeValuePG, which has no ctx and stored +00 for every session. The parse
// fix alone does NOT reach that path (pattern_sibling_paths_must_agree).
func TestTimeTZInsertCoercionUsesSessionZone(t *testing.T) {
	ctx := &Context{GetSetting: func(name string) (string, bool) {
		if name == "timezone" {
			return "Asia/Tokyo", true
		}
		return "", false
	}}
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "timetz"}}}
	row := Row{NewStringDatum("10:00")}
	if err := coerceRowForConstraintChecks(cols, row, func(int) bool { return true }, ctx, 0); err != nil {
		t.Fatalf("coerceRowForConstraintChecks: %v", err)
	}
	if row[0].Kind != KindTime {
		t.Fatalf("row[0].Kind = %d, want KindTime — the literal was left as a string", row[0].Kind)
	}
	if got := row[0].TimeTZOffsetSecs(); got != 9*3600 {
		t.Errorf("stored offset = %d, want 32400 (PG 18.3: '10:00' in a timetz column on "+
			"an Asia/Tokyo session reads back 10:00:00+09)", got)
	}
}
