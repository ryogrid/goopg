package pgdatetime

import "testing"

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
		{"3:4", "03:04"},
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
		{"2002-5-1 3:4", "2002-05-01 03:04"},
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
		"2002-May-1",   // textual month — DecodeSpecial, not implemented
		"May 1, 2002",  // textual month, MDY word order
		"20020501",     // run-together digits (DecodeNumberField)
		"2002/5/1",     // '/' separator
		"5-1-2002",     // DateStyle MDY order
		"02-5-1",       // two-digit leading field: DateStyle-dependent
		"2002-005-01",  // three-digit second field is PG's day-of-year, not a month
		"2002-5",       // too few fields
		"2002-5-1abc",  // trailing garbage
		"garbage",      // not a date at all
		"infinity",     // handled upstream by the ±infinity literal probe
		"epoch",        //          "
		"1234567890",   // bare integer
		"",             //
		"-",            //
		"::",           //
		"12:34:5678",   // over-wide seconds run: left for the layouts to reject
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
