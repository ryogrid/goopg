package datetime

import "testing"

// TestNormalizeDateTimeInputTextualMonth pins the textual-month date forms
// against PostgreSQL 18.3, probed on the reference cluster (port 65432,
// `DateStyle = ISO, MDY`) while this was written. Upstream's own comment in
// DecodeNumber names the three variants that are unambiguous regardless of the
// DateOrder GUC — MON-DD-YYYY, DD-MON-YYYY and YYYY-MON-DD — and those are
// exactly the ones goopg reproduces, since goopg does not model DateOrder on
// input at all.
func TestNormalizeDateTimeInputTextualMonth(t *testing.T) {
	cases := []struct {
		in   string
		bc   bool
		want string
	}{
		// The three unambiguous variants, in every separator PG's field
		// splitter breaks on.
		{in: "2002-May-1", want: "2002-05-01"},         // YYYY-MON-DD
		{in: "May-1-2002", want: "2002-05-01"},         // MON-DD-YYYY
		{in: "1-May-2002", want: "2002-05-01"},         // DD-MON-YYYY
		{in: "May 1 2002", want: "2002-05-01"},         // space-separated
		{in: "1 May 2002", want: "2002-05-01"},         //
		{in: "2002 May 1", want: "2002-05-01"},         //
		{in: "May 2002 1", want: "2002-05-01"},         // year run >= 3 wins wherever it sits
		{in: "May 1, 2002", want: "2002-05-01"},        // the comma is just a separator
		{in: "1/May/2002", want: "2002-05-01"},         // '/' between the fields
		{in: "1.Feb.2020", want: "2020-02-01"},         // '.' likewise
		{in: "  1-May-2002  ", want: "2002-05-01"},     // PG trims surrounding space
		{in: "1-may-2002", want: "2002-05-01"},         // case-insensitive
		{in: "1-MAY-2002", want: "2002-05-01"},         //
		{in: "2020-JANUARY-1", want: "2020-01-01"},     // full name
		{in: "sept 1 2002", want: "2002-09-01"},        // datetktbl's third September spelling
		{in: "1-September-2002", want: "2002-09-01"},   //
		{in: "2002-May-31", want: "2002-05-31"},        // day needs no padding
		{in: "2002-Feb-30", want: "2002-02-30"},        // NO range validation here — 22008 comes later
		{in: "1-May-69", want: "2069-05-01"},           // ValidateDate's 1970..2069 window
		{in: "1-May-70", want: "1970-05-01"},           //
		{in: "02-May-1", want: "2001-05-02"},           // both runs short: MDY reading, year windowed
		{in: "1-May-02", want: "2002-05-01"},           //
		{in: "1-May-02", bc: true, want: "0002-05-01"}, // an era suffix suppresses the window
		// A time of day and/or a zone after the date is passed through the
		// ordinary time canonicaliser.
		{in: "2002-May-1 10:20:30", want: "2002-05-01 10:20:30"},
		{in: "May 1, 2002 10:20", want: "2002-05-01 10:20:00"},
		{in: "May 1, 2002 10:20:30.5+05:30", want: "2002-05-01 10:20:30.5+05:30"},
		{in: "2002 May 1 24:00:00", want: "2002-05-01 24:00:00"},
		{in: "1-May-2002 10:00 z", want: "2002-05-01 10:00:00Z"},

		// Not this shape — returned with only surrounding space removed, so the
		// caller's own parser keeps deciding (all of these are errors to PG).
		{in: "2002-May-1T10:20:30", want: "2002-May-1T10:20:30"}, // 'T' is not a field break here
		{in: "septem 1 2002", want: "septem 1 2002"},             // exact table match required
		{in: "10:00 May", want: "10:00:00 May"},                  // a time plus a month, not a date
		{in: "2002-May-100", want: "2002-May-100"},               // 3-digit day: left for the parser
		{in: "May 2002", want: "May 2002"},                       // only one numeric field
		// A THIRD numeric field is a divergence, not a shared refusal: PG reads
		// 'May 1 2 2002' as month/day/2-digit-year plus a run-together TIME
		// ('2002' -> 20:02), answering 2002-05-01 20:02:00. goopg's scan stops
		// once the date is complete and leaves the extra run in the remainder,
		// where no time layout matches it — so the input errors (22007) instead
		// of decoding (ledger, M0119-0006).
		{in: "May 1 2 2002", want: "2002-05-01 2002"},
		{in: "May Jun 2002", want: "May Jun 2002"}, // two month names
		{in: "-May-1-2002", want: "-May-1-2002"},   // leading separator
		{in: "2002-05-01", want: "2002-05-01"},     // numeric path, unchanged
	}
	for _, c := range cases {
		if got := NormalizeDateTimeInput(c.in, c.bc); got != c.want {
			t.Errorf("NormalizeDateTimeInput(%q, bc=%v) = %q, want %q", c.in, c.bc, got, c.want)
		}
	}
}

// TestNormalizeInputTextualMonthIsDateTimeOnly pins the entry-point split: the
// textual month belongs to DecodeDateTime's context. DecodeTimeOnly never
// reaches DecodeDate, so `'May 1, 2002'::time` is a syntax error upstream and
// NormalizeInput must not hand a time layout something that looks decodable.
func TestNormalizeInputTextualMonthIsDateTimeOnly(t *testing.T) {
	for _, in := range []string{"May 1, 2002", "2002-May-1", "1-May-2002 10:20:30"} {
		if got := NormalizeInput(in); got != in {
			t.Errorf("NormalizeInput(%q) = %q, want it left alone (DecodeTimeOnly context)", in, got)
		}
	}
}
