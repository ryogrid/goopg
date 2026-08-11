package pgdatetime

import (
	"errors"
	"strings"
	"testing"
)

// TestNormalizeInputPGAcceptedForms pins the spellings PostgreSQL's
// DecodeDate/DecodeNumber accept but a fixed Go layout does not. Every "want"
// here is the padded rendering of what PG 18.3 actually returns for the input:
// the left column was fed to `select '<in>'::date` (or ::time / ::timestamp) on
// a stock 18.3 server while developing M0125-0007, and the normalised string
// must parse under goopg's existing layouts to the same instant.
func TestNormalizeInputPGAcceptedForms(t *testing.T) {
	cases := []struct{ in, want string }{
		// --- the defect: unpadded month and/or day (PG: 2002-05-01) ---
		{"2002-5-01", "2002-05-01"},
		{"2002-05-1", "2002-05-01"},
		{"2002-5-1", "2002-05-01"},
		{"2002-05-01", "2002-05-01"}, // already canonical: untouched
		// PG trims surrounding whitespace before decoding.
		{" 2002-5-1 ", "2002-05-01"},
		{"\t2002-05-01\n", "2002-05-01"},
		// A three-digit leading field is still unambiguously the year
		// (DecodeNumber's `flen >= 3`): PG reads '002-1-1' as 0002-01-01.
		{"002-1-1", "0002-01-01"},
		{"0002-1-1", "0002-01-01"},
		// Years wider than four digits keep every digit.
		{"12002-5-1", "12002-05-01"},

		// --- unpadded time-of-day fields (PG: 03:04:05) ---
		{"3:4:5", "03:04:05"},
		{"03:4:05", "03:04:05"},
		{"3:04:05", "03:04:05"},
		{"03:04:5", "03:04:05"},
		{"03:04:05", "03:04:05"},
		// Seconds are OPTIONAL to DecodeTime and default to 0, so the missing
		// field is supplied rather than passed through: PG reads '3:4' as
		// 03:04:00 (verified on 18.3), and goopg's layout tables are not
		// uniformly willing to parse a secondless time.
		{"3:4", "03:04:00"},
		{"10:00", "10:00:00"},
		{"10:00:00", "10:00:00"}, // already canonical: untouched
		// An empty trailing seconds field is unambiguously "no seconds"
		// (PG: '10:00:' = 10:00:00) and the stray ':' is dropped.
		{"10:00:", "10:00:00"},
		// The seconds default rides in front of whatever followed the minute.
		{"10:00+05", "10:00:00+05"},
		{"10:00 PM", "10:00:00 PM"},
		{"2002-5-1T3:4Z", "2002-05-01T03:04:00Z"},
		// Fractional seconds, offsets and AM/PM markers ride along verbatim.
		{"3:4:5.25", "03:04:05.25"},
		{"3:4:5-07", "03:04:05-07"},
		{"3:4:5+05:30", "03:04:05+05:30"},
		{"3:4:5 PM", "03:04:05 PM"},
		{"3:4:5 America/New_York", "03:04:05 America/New_York"},
		{"24:00:00", "24:00:00"},

		// --- date + time together, both separators ---
		{"2002-5-1 3:4:5", "2002-05-01 03:04:05"},
		{"2002-5-1T3:4:5Z", "2002-05-01T03:04:05Z"},
		{"2002-5-1 3:4:5.25-04", "2002-05-01 03:04:05.25-04"},
		{"2002-5-1 3:4", "2002-05-01 03:04:00"},
		{"2002-05-01 03:04:05", "2002-05-01 03:04:05"},
		// An unrecognised tail is padded around but never interpreted: the date
		// fields normalise, the 'BC' era marker is passed through untouched and
		// the downstream parser still rejects the whole literal (goopg has no
		// era support). Normalising the date part costs nothing here because the
		// result is discarded.
		{"2002-5-1 BC", "2002-05-01 BC"},
	}
	for _, c := range cases {
		if got := NormalizeInput(c.in); got != c.want {
			t.Errorf("NormalizeInput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeInputLeavesForeignSpellingsAlone pins the other half of the
// contract: NormalizeInput must not invent a reading for anything outside the
// ISO numeric subset. Each input below is either rejected by PG too, or is a
// form whose meaning depends on machinery goopg does not have yet (textual
// months, the DateStyle field order, the run-together and slash spellings,
// day-of-year). Normalising them would turn a loud error into a wrong answer —
// exactly the failure mode M0125-0007 was filed for. They stay byte-identical
// (modulo the surrounding-whitespace trim) so the downstream layout tables keep
// rejecting them; see .ralph/deferral_ledger.md for the follow-up rows.
func TestNormalizeInputLeavesForeignSpellingsAlone(t *testing.T) {
	unchanged := []string{
		"2002-May-1",  // textual month — DecodeSpecial, not implemented
		"May 1, 2002", // textual month, MDY word order
		"20020501",    // run-together digits (DecodeNumberField)
		"2002/5/1",    // '/' separator
		"5-1-2002",    // DateStyle MDY order
		"02-5-1",      // two-digit leading field: DateStyle-dependent
		"2002-005-01", // three-digit second field is PG's day-of-year, not a month
		"2002-5",      // too few fields
		"2002-5-1abc", // trailing garbage
		"garbage",     // not a date at all
		"infinity",    // handled upstream by the ±infinity literal probe
		"epoch",       //          "
		"1234567890",  // bare integer
		"",            //
		"-",           //
		"::",          //
		"12:34:5678",  // over-wide seconds run: left for the layouts to reject
		"12:34:ab",    // malformed seconds field: left for the layouts to reject
		// PG accepts both of these, and both mean something a "supply the
		// missing seconds" rewrite would get WRONG: '10:00.5' decodes as
		// 00:10:00.5 (DecodeNumberField reads the fractional field as MM:SS.f,
		// not HH:MM plus fractional minutes), and '10::00' is an empty MINUTE
		// field, not an empty seconds one. Deferral-ledger rows; a loud 22007 is
		// the right answer until the real DecodeTime field walk is ported.
		"10:00.5",
		"10::00",
		"1-2-3",        // one-digit leading field: DateStyle-dependent
		"999999999999", //
	}
	for _, in := range unchanged {
		if got := NormalizeInput(in); got != in {
			t.Errorf("NormalizeInput(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestNormalizeInputDoesNotValidate documents that range checking is NOT this
// function's job — PostgreSQL likewise decodes fields first and only then calls
// ValidateDate(), so an out-of-range field must survive normalisation and be
// rejected by the parser that follows. If NormalizeInput silently dropped or
// clamped these, a bad literal would become a valid date.
func TestNormalizeInputDoesNotValidate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2002-13-1", "2002-13-01"},
		{"2002-5-32", "2002-05-32"},
		{"2002-0-1", "2002-00-01"},
		{"2002-5-0", "2002-05-00"},
		{"2002-2-30", "2002-02-30"},
		{"99:99:99", "99:99:99"},
	}
	for _, c := range cases {
		if got := NormalizeInput(c.in); got != c.want {
			t.Errorf("NormalizeInput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeInputIsIdempotent guards the property every call site relies on:
// NormalizeInput sits in front of parsers that already handle canonical input,
// so applying it twice must not change the result.
func TestNormalizeInputIsIdempotent(t *testing.T) {
	inputs := []string{
		"2002-5-1", "2002-05-01", " 3:4:5 ", "2002-5-1T3:4:5.25Z",
		"2002-May-1", "garbage", "", "24:0:0", "12002-5-1",
	}
	for _, in := range inputs {
		once := NormalizeInput(in)
		if twice := NormalizeInput(once); twice != once {
			t.Errorf("NormalizeInput not idempotent for %q: %q -> %q", in, once, twice)
		}
	}
}

// TestNormalizeInputFoldsSeparatorAndZuluCase covers the ISO 8601 spelling
// variants PostgreSQL's field splitter absorbs but Go's layouts cannot express:
// ParseDateTime() (datetime.c) breaks fields on a 'T' in either case, and
// DecodeDateTime() finds the UTC zone token 'Z' in datetbl case-insensitively
// and whether or not it arrives as a separate whitespace-delimited field. Go
// needs one canonical spelling, so both are folded here — upper 'T', an
// uppercase 'Z' attached directly — leaving the layout table to enumerate only
// the separator/offset shapes. M0119-0006.
func TestNormalizeInputFoldsSeparatorAndZuluCase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2020-01-01t10:00:00", "2020-01-01T10:00:00"},
		{"2020-01-01T10:00:00", "2020-01-01T10:00:00"},
		{"2020-1-1t3:4:5", "2020-01-01T03:04:05"},
		{"2020-01-01T10:00:00z", "2020-01-01T10:00:00Z"},
		{"2020-01-01 10:00:00 Z", "2020-01-01 10:00:00Z"},
		{"2020-01-01T10:00 z", "2020-01-01T10:00:00Z"},
		{"2020-01-01t10:00", "2020-01-01T10:00:00"},
		{"10:00:00 z", "10:00:00Z"}, // bare time-of-day path
		{"2020-01-01T10:00:00.25Z", "2020-01-01T10:00:00.25Z"},
		// The space separator must survive as a space: several call sites still
		// carry space-only layouts, and rewriting it would be a silent break.
		{"2020-01-01 10:00:00", "2020-01-01 10:00:00"},
	}
	for _, c := range cases {
		if got := NormalizeInput(c.in); got != c.want {
			t.Errorf("NormalizeInput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeInputZuluFoldIsNarrow is the mutation guard for the fold above.
// A trailing 'Z'/'z' is only the UTC zone token when it stands alone: inside a
// timezone ABBREVIATION ('NZ') or a name it is just a letter, and rewriting it
// would turn input PG reads as New Zealand time into UTC. The rule is that what
// precedes the letter (after any space) must be a digit.
func TestNormalizeInputZuluFoldIsNarrow(t *testing.T) {
	unchanged := []string{
		"2020-01-01 10:00:00 NZ", // zone abbreviation, not a Zulu token
		"2020-01-01 10:00:00 nz",
		"z", "Z", " Z", // no time to attach to
		"2020-01-01Z", // a zone on a bare DATE: PG accepts, goopg does not yet
	}
	for _, in := range unchanged {
		want := strings.TrimSpace(in)
		if got := NormalizeInput(in); got != want {
			t.Errorf("NormalizeInput(%q) = %q, want %q (fold must not touch this)", in, got, want)
		}
	}
}

// TestNormalizeDateTimeInputRunTogetherDate pins DecodeNumberField's date arm.
// Every "want" is the padded rendering of what PG 18.3 actually answered for
// `select '<in>'::date` / `::timestamp` on a stock 18.3 server (port 5599,
// captured while developing this slice).
func TestNormalizeDateTimeInputRunTogetherDate(t *testing.T) {
	cases := []struct {
		in   string
		bc   bool
		want string
	}{
		// "Start from end and consider first 2 as Day, next 2 as Month, and the
		// rest as Year" — the year is whatever is left, at any width.
		{"20200101", false, "2020-01-01"},
		{"2020101", false, "0202-01-01"},    // PG: 0202-01-01
		{"202001011", false, "20200-10-11"}, // PG: 20200-10-11
		// A two-digit year is windowed onto 1970..2069 by ValidateDate().
		{"200101", false, "2020-01-01"},
		{"690101", false, "2069-01-01"},
		{"700101", false, "1970-01-01"},
		{"990101", false, "1999-01-01"},
		{"000101", false, "2000-01-01"},
		// ...but the windowing is an `else if` after the BC branch, so an era
		// suffix suppresses it: PG reads '200101 BC' as 0020-01-01 BC.
		{"200101", true, "0020-01-01"},
		{"20200101", true, "2020-01-01"}, // 4-digit year: BC changes nothing here
		// A time of day may follow, on either ISO separator.
		{"20200101 040506", false, "2020-01-01 040506"},
		{"20200101T040506", false, "2020-01-01T040506"},
		{"20200101 04:05:06", false, "2020-01-01 04:05:06"},
		{"20200101 0405", false, "2020-01-01 0405"},
		{"20200101 10:00", false, "2020-01-01 10:00:00"},
		{" 20200101 ", false, "2020-01-01"},
		// Fewer than six digits is not a date; PG rejects '20200' outright and
		// reads '0405' as a time only, so both pass through untouched.
		{"20200", false, "20200"},
		{"0405", false, "0405"},
		// A decimal point sends upstream down the fractional-seconds branch,
		// which then requires a 4- or 6-digit remainder: '20200101.5' is a
		// syntax error to PG, so nothing is rewritten here either.
		{"20200101.5", false, "20200101.5"},
		// Separated spellings keep going through padDateFields unchanged.
		{"2002-5-1", false, "2002-05-01"},
		// A textual month goes through monthname.go instead (see
		// TestNormalizeDateTimeInputTextualMonth), not through padDateFields.
		{"2002-May-1", false, "2002-05-01"},
	}
	for _, c := range cases {
		if got := NormalizeDateTimeInput(c.in, c.bc); got != c.want {
			t.Errorf("NormalizeDateTimeInput(%q, bc=%v) = %q, want %q", c.in, c.bc, got, c.want)
		}
	}
}

// TestNormalizeInputKeepsTimeOnlyReading is the sibling-path guard: the same six
// digits that are a date to DecodeDateTime must stay a time of day to
// DecodeTimeOnly, which is the context NormalizeInput models. '040506'::time is
// 04:05:06 on PG 18.3 even though '040506'::date is 2004-05-06.
func TestNormalizeInputKeepsTimeOnlyReading(t *testing.T) {
	for _, in := range []string{"040506", "0405", "20200101", "200101"} {
		if got := NormalizeInput(in); got != in {
			t.Errorf("NormalizeInput(%q) = %q, want it untouched (time-only context)", in, got)
		}
	}
}

// TestValidateDateToken pins ValidateDate()'s month/day range checks
// (DTERR_MD_FIELD_OVERFLOW), the piece M0119-0006 found missing: a
// month/day that no layout can represent ('2020-13-01', '2020-01-32') fell
// through every entry in goopg's layout table and came back as the generic
// "no timestamp layout matched" (22007), where PostgreSQL raises 22008
// because DecodeDateTime recognised the shape and only ValidateDate rejected
// the values.
func TestValidateDateToken(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"2020-01-01", false},
		{"2020-12-31", false},
		{"2020-02-29", false}, // leap day: accepted (day 1..31), not yet
		{"2020-02-30", false}, // full days-in-month check is a separate,
		// still-deferred ValidateDate() arm — see ValidateMonthDay's doc.
		{"0202-01-01", false},          // run-together's padded 4-digit year
		{"20200-10-11", false},         // run-together's wide (>4-digit) year
		{"2020-13-01", true},           // month 13
		{"2020-00-01", true},           // month 0
		{"2020-01-32", true},           // day 32
		{"2020-01-00", true},           // day 0
		{"", false},                    // not the shape: left to the caller's parser
		{"10:00:00", false},            // bare time: not the shape
		{"2020-01-01 10:00:00", false}, // has a space; not a bare date token
	}
	for _, c := range cases {
		err := ValidateDateToken(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateDateToken(%q) = %v, wantErr %v", c.in, err, c.wantErr)
		}
		if err != nil && !errors.Is(err, ErrFieldOutOfRange) {
			t.Errorf("ValidateDateToken(%q) = %v, want ErrFieldOutOfRange", c.in, err)
		}
	}
}

// TestValidateDayOfMonth pins ValidateDate()'s third check (M0119-0006 §13.3):
// day-in-month, given the ASTRONOMICAL year (not the era-relative digits the
// input spelled). Verified against PG 18.3.
func TestValidateDayOfMonth(t *testing.T) {
	cases := []struct {
		year, month, day int
		wantErr          bool
	}{
		{2020, 2, 29, false}, // 2020 is a Gregorian leap year
		{2020, 2, 30, true},
		{2021, 2, 28, false},
		{2021, 2, 29, true}, // 2021 is not a leap year
		{1900, 2, 28, false},
		{1900, 2, 29, true}, // divisible by 100, not by 400: not leap
		{2000, 2, 29, false},
		{2000, 2, 30, true}, // divisible by 400: leap, but still caps at 29
		{2021, 4, 30, false},
		{2021, 4, 31, true}, // April has 30 days
		{2020, 1, 31, false},
		{2020, 12, 31, false},
		{0, 2, 29, false},  // astronomical year 0 (1 BC) is leap
		{-4, 2, 29, false}, // astronomical year -4 (5 BC) is leap
		{-3, 2, 29, true},  // astronomical year -3 (4 BC) is not leap
	}
	for _, c := range cases {
		err := ValidateDayOfMonth(c.year, c.month, c.day)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateDayOfMonth(%d, %d, %d) = %v, wantErr %v",
				c.year, c.month, c.day, err, c.wantErr)
		}
		if err != nil && !errors.Is(err, ErrFieldOutOfRange) {
			t.Errorf("ValidateDayOfMonth(%d, %d, %d) = %v, want ErrFieldOutOfRange",
				c.year, c.month, c.day, err)
		}
	}
}

// TestDateTokenYear pins the year-digit extraction DateTokenYear does for
// validateDateTokenFull's pre-time.Parse day-in-month check.
func TestDateTokenYear(t *testing.T) {
	cases := []struct {
		in       string
		wantYear int
		wantOK   bool
	}{
		{"2020-01-01", 2020, true},
		{"0202-01-01", 202, true},    // run-together's padded 4-digit year
		{"20200-10-11", 20200, true}, // run-together's wide (>4-digit) year
		{"", 0, false},
		{"10:00:00", 0, false},            // not the shape
		{"2020-01-01 10:00:00", 0, false}, // has a space
		{"x020-01-01", 0, false},          // non-digit in the year run
	}
	for _, c := range cases {
		year, ok := DateTokenYear(c.in)
		if ok != c.wantOK || (ok && year != c.wantYear) {
			t.Errorf("DateTokenYear(%q) = (%d, %v), want (%d, %v)", c.in, year, ok, c.wantYear, c.wantOK)
		}
	}
}

// TestAstronomicalYear pins ApplyEra's year conversion, computed without a
// time.Time — see AstronomicalYear's doc for why it must agree with ApplyEra.
func TestAstronomicalYear(t *testing.T) {
	cases := []struct {
		year   int
		bc     bool
		want   int
		wantOK bool
	}{
		{2020, false, 2020, true},
		{1, true, 0, true},  // 1 BC -> astronomical year 0
		{2, true, -1, true}, // 2 BC -> astronomical year -1
		{4, true, -3, true}, // 4 BC -> astronomical year -3
		{0, false, 0, true}, // not BC: no-op even for the year-zero case (ApplyEra rejects it downstream)
		{0, true, 0, false}, // BC year 0: "no year zero" is ApplyEra's refusal, not this function's
	}
	for _, c := range cases {
		got, ok := AstronomicalYear(c.year, c.bc)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("AstronomicalYear(%d, %v) = (%d, %v), want (%d, %v)",
				c.year, c.bc, got, ok, c.want, c.wantOK)
		}
	}
}

// TestRunTogetherDateIsTimeAmbiguous pins the widths that BOTH DecodeNumberField
// arms accept, which is the only case goopg's target-type-less coercion path
// must not resolve in favour of the date reading.
func TestRunTogetherDateIsTimeAmbiguous(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"040506", true}, // hhmmss / yymmdd
		{"0405", true},   // hhmm / (too short for a date)
		{"040506 PM", true},
		{"20200101", false}, // 8 digits: a date only
		{"2020101", false},  // 7 digits: a date only
		{"20200", false},    // 5 digits: neither
		{"2020-01-01", false},
		{"10:00:00", false},
		{"", false},
	}
	for _, c := range cases {
		if got := RunTogetherDateIsTimeAmbiguous(c.in); got != c.want {
			t.Errorf("RunTogetherDateIsTimeAmbiguous(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
