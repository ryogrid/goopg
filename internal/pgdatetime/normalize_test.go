package pgdatetime

import (
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
