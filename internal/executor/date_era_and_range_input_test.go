package executor

import (
	"errors"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/pgdatetime"
	"github.com/goopg/goopg/internal/planner"
)

// PostgreSQL's date/time input carries an era: '2020-01-01 BC' is an ordinary
// date, spelled with the ADBC token DecodeDateTime resolves out of datetbl
// (postgres/src/backend/utils/adt/datetime.c). goopg raised 22007 for every
// spelling of it, and the era is only half the story — probing the gap turned
// up a much larger one underneath, which these tests pin.
//
// goopg's KindTime Datum stores an int64 count of NANOSECONDS since 1970
// (datum.go: NewTimeDatum / TimeValue), i.e. Go's UnixNano domain, which spans
// only 1677-09-21 .. 2262-04-11. PostgreSQL's timestamp is an int64 count of
// MICROSECONDS since 2000-01-01 (4713 BC .. 294276 AD). Outside the narrower
// domain UnixNano OVERFLOWS and Go leaves the wrapped value in place, so goopg
// answered ordinary historical input with a plausible-looking WRONG DATE and no
// diagnostic at all — the M0125-0007 failure mode:
//
//	'1000-01-01'::date  ->  2169-02-08   (PG: 1000-01-01)
//	'2300-01-01'::date  ->  1715-06-13   (PG: 2300-01-01)
//	'0000-01-01'::date  ->  1753-08-29   (PG: 22008, there is no year zero)
//
// Until the carrier moves to microseconds (deferral ledger, M0119-0006), the
// input paths refuse what they cannot represent. Wrong answers become loud
// 22008s; the representable range is untouched.
func TestDateInputRejectsUnrepresentableInsteadOfWrapping(t *testing.T) {
	for _, in := range []string{
		"1000-01-01", "1600-12-31", "2300-01-01", "9999-12-31",
		"1000-01-01 12:00:00", "2500-06-01 00:00:00",
		// The era spellings: read correctly by the field decoder now, and then
		// refused by the carrier rather than stored as a wrapped AD date.
		"2020-01-01 BC", "0001-01-01 BC", "2020-01-01bc",
	} {
		got, err := parseCopyTimestamp(in)
		if err == nil {
			t.Errorf("parseCopyTimestamp(%q) = %v, want a range error — the nanosecond carrier cannot hold this and must not wrap it", in, got)
			continue
		}
		if !errors.Is(err, errTimeCarrierRange) && !errors.Is(err, pgdatetime.ErrFieldOutOfRange) {
			t.Errorf("parseCopyTimestamp(%q) err = %v, want a RANGE error (22008), not a syntax miss", in, err)
		}
	}
}

// The no-year-zero rule is PG's own (ValidateDate), not a carrier artifact, and
// applies in both eras.
func TestDateInputRejectsYearZero(t *testing.T) {
	for _, in := range []string{"0000-01-01", "0000-01-01 BC", "0000-06-15 10:00:00"} {
		if _, err := parseCopyTimestamp(in); !errors.Is(err, pgdatetime.ErrFieldOutOfRange) {
			t.Errorf("parseCopyTimestamp(%q) err = %v, want ErrFieldOutOfRange — PG raises 22008 here", in, err)
		}
	}
}

// A range failure and a syntax failure are different SQLSTATEs upstream: 22008
// "date/time field value out of range" vs 22007 "invalid input syntax". goopg
// reported 22007 for both, which points the user at the spelling of a string
// whose spelling is fine.
func TestDateTimeInputErrorSeparatesRangeFromSyntax(t *testing.T) {
	for _, c := range []struct {
		in       string
		wantCode string
	}{
		{"0000-01-01", "22008"},
		{"1000-01-01", "22008"},
		{"2020-01-01 BC", "22008"},
		{"not-a-date", "22007"},
		// M0119-0006: ValidateDate()'s month/day range check—a month/day
		// no layout can hold is a RANGE failure to PG (DecodeDateTime
		// recognised the shape; only ValidateDate rejected the values),
		// not a syntax miss. '2020-13-99' has BOTH fields out of range;
		// ValidateMonthDay reports the first one it checks (month).
		{"2020-13-99", "22008"},
		{"2020-01-32", "22008"},
		// M0119-0006 §13.3: ValidateDate()'s THIRD check — day-in-month, once
		// the astronomical year is known. Feb 30 / Apr 31 are <=31 (so the
		// first two checks pass) but impossible for their month.
		{"2020-02-30", "22008"}, // 2020 is a leap year; day 30 still overflows Feb
		{"2021-02-29", "22008"}, // 2021 is NOT a leap year; day 29 overflows Feb
		{"2021-04-31", "22008"}, // April has 30 days
		{"2020-02-29", ""},      // 2020 IS a leap year: accepted (wantCode "" checked below)
		{"2021-04-30", ""},
		// M0119-0006 §14.3: a BC date where the LITERAL year's leap-ness
		// disagrees with the ASTRONOMICAL year's. '0001-02-29 BC' is
		// astronomical year 0 (1 BC), which 0%400==0 makes leap — PG 18.3
		// accepts it — but literal year 1 is not leap, so time.Parse's own
		// day-out-of-range check rejects it a layer below
		// validateDateTokenFull's (correct) astronomical-year check. Must
		// not regress to the pre-fix 22007 ("day out of range" read as a
		// syntax miss); the carrier's own range (1677-2262) still refuses
		// year 0, so the code is 22008 either way — the assertion that
		// matters is which PATH produces it.
		{"0001-02-29 BC", "22008"},
	} {
		if c.wantCode == "" {
			if _, err := parsePGDateText(c.in); err != nil {
				t.Errorf("parsePGDateText(%q) unexpectedly failed: %v", c.in, err)
			}
			continue
		}
		_, err := parsePGDateText(c.in)
		if err == nil {
			t.Errorf("parsePGDateText(%q) unexpectedly succeeded", c.in)
			continue
		}
		if got := dateTimeInputError(err, "date", c.in, 0).Code; got != c.wantCode {
			t.Errorf("dateTimeInputError(%q).Code = %s, want %s", c.in, got, c.wantCode)
		}
	}
}

// The era token must not widen anything else. Everything here is either still
// -unimplemented PG input or genuinely invalid, and PG rejects all of it too
// ('2020-01-01 BC BC' and 'BC' are 22007; '2020-01-01 B.C.' fails as an
// unrecognised TIMEZONE name, since B.C. is not an era spelling).
func TestEraSplitStopsAtTheUnhandledForms(t *testing.T) {
	for _, in := range []string{
		"2020-01-01 BC BC", "BC", "AD", "2020-01-01 B.C.", "2020-01-01 BCX", "bc 2020-01-01",
	} {
		if got, err := parseCopyTimestamp(in); err == nil {
			t.Errorf("parseCopyTimestamp(%q) = %v, want an error — the era split must not guess", in, got)
		}
	}
}

// The representable range itself must be untouched by this slice: the dates
// goopg actually stores (TPC-H/TPC-DS live in the 1990s-2000s) still parse, and
// the two ends of the carrier domain still work.
func TestRepresentableDatesStillParse(t *testing.T) {
	for _, in := range []string{
		"1998-12-01", "2020-01-01", "2262-04-11", "1678-01-01",
		"2020-01-01 10:00:00", "2020-01-01T10:00:00Z", "2020-1-1 3:4:5",
		"2020-01-01 AD", // AD is a no-op marker, not a rejection
	} {
		if _, err := parseCopyTimestamp(in); err != nil {
			t.Errorf("parseCopyTimestamp(%q): %v — this is inside the representable range", in, err)
		}
	}
}

// The sibling-path guard (Hard-won Rule #2), extended to the era and range
// rules: the typed-literal path and the COPY/encode path must agree on every
// form, or an INSERT can raise on text a SELECT accepts. Agreement is the
// assertion, so the still-refused forms belong here too — when the carrier
// widens, both sides must widen together.
func TestDateEraLiteralAndCopyPathsAgree(t *testing.T) {
	forms := []string{
		"2020-01-01", "2020-01-01 AD", "1998-12-01",
		"2020-01-01 BC", "0001-01-01 BC", "2020-01-01bc",
		"0000-01-01", "1000-01-01", "2300-01-01",
		"2020-01-01 BC BC", "BC",
		"0001-02-29 BC", // M0119-0006 §14.3: BC-era leap-day fallback
	}
	for _, in := range forms {
		_, copyErr := parsePGDateText(in)
		_, litErr := evalTypedStringLit(&planner.TypedStringLit{Type: "date", Value: in})
		if (copyErr == nil) != (litErr == nil) {
			t.Errorf("date %q: shared-entry err=%v but literal path err=%v — the date input paths have drifted apart",
				in, copyErr, litErr)
		}
	}
}

// M0119-0006 §15.2: the TIMESTAMP half of the BC leap-day race. The preceding
// slice fixed parsePGDateText, whose date token goes through ONE layout; the
// timestamp entry point composes the same token with a time of day and a zone
// inside a single time.Parse per layout, so it kept losing the same race:
// time.Parse checks day-in-month against the token's LITERAL year (1) while
// PostgreSQL checks the ASTRONOMICAL one (0, leap because 0%400==0).
//
// Verified against PG 18.3, which accepts all of these:
//
//	'0001-02-29 10:00:00 BC'::timestamp        -> 0001-02-29 10:00:00 BC
//	'0001-02-29 00:30:00+05:30 BC'::timestamp  -> 0001-02-29 00:30:00 BC (offset dropped)
//	'0001-02-29 00:30:00+05:30 BC'::timestamptz-> 0001-02-28 19:00:00 BC (offset applied)
//	'0001-02-29 24:00:00 BC'::timestamp        -> 0001-03-01 00:00:00 BC (whole-day carry)
//	'0005-02-29 10:00:00 BC'::timestamp        -> 0005-02-29 10:00:00 BC (astronomical -4)
//
// The DECODED FIELDS are what this pins, because goopg's nanosecond carrier
// refuses every BC value regardless (1677..2262) — asserting only the error
// would pass with the fields still wrong. So the parts-level entry point is
// checked for the value, and the composed one for the SQLSTATE.
func TestBCLeapDayTimestampDecodesAtTheAstronomicalYear(t *testing.T) {
	for _, c := range []struct {
		in   string
		zone tsZoneMode
		want time.Time
	}{
		// Astronomical year 0 is 1 BC, and it IS a leap year.
		{"0001-02-29 10:00:00 BC", tsDiscardZone, time.Date(0, 2, 29, 10, 0, 0, 0, time.UTC)},
		{"0001-02-29 BC", tsDiscardZone, time.Date(0, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"0001-02-29T10:00:00 BC", tsDiscardZone, time.Date(0, 2, 29, 10, 0, 0, 0, time.UTC)},
		{"0001-02-29 10:00:00.25 BC", tsDiscardZone, time.Date(0, 2, 29, 10, 0, 0, 250000000, time.UTC)},
		// The zone rule still separates the two types, and applying an offset
		// may cross midnight — the day the value lands on must move with it.
		{"0001-02-29 00:30:00+05:30 BC", tsDiscardZone, time.Date(0, 2, 29, 0, 30, 0, 0, time.UTC)},
		{"0001-02-29 00:30:00+05:30 BC", tsApplyZone, time.Date(0, 2, 28, 19, 0, 0, 0, time.UTC)},
		// 5 BC is astronomical -4: divisible by 4, so leap as well.
		{"0005-02-29 10:00:00 BC", tsDiscardZone, time.Date(-4, 2, 29, 10, 0, 0, 0, time.UTC)},
	} {
		got, carry, err := parsePGTimestampTextParts(c.in, c.zone)
		if err != nil {
			t.Errorf("parsePGTimestampTextParts(%q) failed: %v — PG 18.3 accepts this", c.in, err)
			continue
		}
		if carry != 0 {
			t.Errorf("parsePGTimestampTextParts(%q) carry = %d, want 0", c.in, carry)
		}
		if !got.Equal(c.want) {
			t.Errorf("parsePGTimestampTextParts(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// The whole-day carry composes on top of the rebuilt date, so an hour-24 /
	// leap-second time of day on the BC leap day rolls into March 1st exactly
	// as it does upstream.
	for _, in := range []string{"0001-02-29 24:00:00 BC", "0001-02-29 23:59:60 BC"} {
		got, carry, err := parsePGTimestampTextParts(in, tsDiscardZone)
		if err != nil {
			t.Errorf("parsePGTimestampTextParts(%q) failed: %v", in, err)
			continue
		}
		if carry != 1 {
			t.Errorf("parsePGTimestampTextParts(%q) carry = %d, want 1 (the whole day)", in, carry)
		}
		if want := time.Date(0, 3, 1, 0, 0, 0, 0, time.UTC); !got.AddDate(0, 0, carry).Equal(want) {
			t.Errorf("parsePGTimestampTextParts(%q)+carry = %v, want %v", in, got.AddDate(0, 0, carry), want)
		}
	}
}

// The fallback must not widen anything: a day that is impossible even at the
// astronomical year stays a RANGE error (22008, as upstream), and a genuine
// syntax miss stays 22007. The composed entry point is the one the SQLSTATE
// mapping sees, and for a BC value the carrier refuses it either way — the
// assertion that matters is which PATH produced the code, so the impossible-day
// and the leap-day cases are checked against each other.
func TestBCLeapDayTimestampFallbackKeepsTheErrorClasses(t *testing.T) {
	for _, c := range []struct {
		in       string
		wantCode string
	}{
		{"0001-02-29 10:00:00 BC", "22008"}, // valid fields, carrier range
		{"0001-02-30 10:00:00 BC", "22008"}, // no such day in ANY year
		{"0002-02-29 10:00:00 BC", "22008"}, // astronomical -1: NOT leap
		{"0000-02-29 10:00:00 BC", "22008"}, // no year zero
		{"not-a-timestamp BC", "22007"},     // still a syntax miss
		{"0001-02-29 25:00:00 BC", "22008"}, // hour out of range, not a spelling
	} {
		_, err := parsePGTimestampText(c.in)
		if err == nil {
			t.Errorf("parsePGTimestampText(%q) unexpectedly succeeded", c.in)
			continue
		}
		if got := dateTimeInputError(err, "timestamp", c.in, 0).Code; got != c.wantCode {
			t.Errorf("dateTimeInputError(%q).Code = %s, want %s", c.in, got, c.wantCode)
		}
	}
	// Non-BC input never reaches the fallback (literal and astronomical years
	// agree there), so the ordinary path's answers are untouched.
	for _, in := range []string{"2020-02-29 10:00:00", "2020-01-01 10:00:00", "1998-12-01"} {
		if _, err := parsePGTimestampText(in); err != nil {
			t.Errorf("parsePGTimestampText(%q): %v — inside the representable range", in, err)
		}
	}
	if _, err := parsePGTimestampText("2021-02-29 10:00:00"); !errors.Is(err, pgdatetime.ErrFieldOutOfRange) {
		t.Errorf("parsePGTimestampText(2021-02-29 10:00:00) err = %v, want ErrFieldOutOfRange — 2021 is not leap", err)
	}
}
