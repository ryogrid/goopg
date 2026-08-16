package datetime

import (
	"errors"
	"testing"
	"time"
)

// Every `want` below is what PG 18.3 answers for the same input (local 18.3
// cluster, socket /tmp port 5599, `set timezone='UTC'`), not a reading of the
// source.
func TestSplitEra(t *testing.T) {
	for _, c := range []struct {
		in       string
		wantRest string
		wantBC   bool
	}{
		// PG: '2020-01-01 BC'::date -> 2020-01-01 BC
		{"2020-01-01 BC", "2020-01-01", true},
		{"2020-01-01 bc", "2020-01-01", true},
		{"2020-01-01BC", "2020-01-01", true}, // PG: attached, still the era
		{"2020-01-01  BC", "2020-01-01", true},
		{"2020-01-01 BC ", "2020-01-01", true},
		// AD is the same token with the other era; PG prints no marker for it.
		{"2020-01-01 AD", "2020-01-01", false},
		{"2020-01-01 ad", "2020-01-01", false},
		{"2020-01-01 10:00:00 BC", "2020-01-01 10:00:00", true},
		{"2020-1-1 BC", "2020-1-1", true}, // unpadded fields survive for NormalizeInput
		// No era token at all — returned byte-identical.
		{"2020-01-01", "2020-01-01", false},
		{"2020-01-01 10:00:00", "2020-01-01 10:00:00", false},
		{"", "", false},
		// Not an era, and must not be treated as one. PG rejects each of these
		// (a doubled era and a bare token are 22007; 'B.C.' is read as a
		// timezone name and fails with "time zone \"b.c.\" not recognized"), so
		// leaving them intact for the caller's parser reproduces that.
		{"2020-01-01 BC BC", "2020-01-01 BC BC", false},
		{"BC", "BC", false},
		{"2020-01-01 B.C.", "2020-01-01 B.C.", false},
		{"2020-01-01 BCX", "2020-01-01 BCX", false},
	} {
		gotRest, gotBC := SplitEra(c.in)
		if gotRest != c.wantRest || gotBC != c.wantBC {
			t.Errorf("SplitEra(%q) = (%q, %v), want (%q, %v)", c.in, gotRest, gotBC, c.wantRest, c.wantBC)
		}
	}
}

// ApplyEra owns the era→astronomical-year conversion AND the no-year-zero rule.
// The year math is upstream's: 1 BC is year 0, 2 BC is year -1, so a BC year Y
// becomes -(Y-1) (datetime.c, DecodeDateTime's ADBC arm).
func TestApplyEra(t *testing.T) {
	mk := func(y int) time.Time { return time.Date(y, 3, 4, 5, 6, 7, 0, time.UTC) }
	for _, c := range []struct {
		year     int
		bc       bool
		wantYear int
	}{
		{2020, true, -2019}, // PG: '2020-01-01 BC' prints back as 2020-01-01 BC
		{1, true, 0},        // PG: '0001-01-01 BC' — the first year before the era
		{2, true, -1},
		{4713, true, -4712}, // PG's earliest supported year
		{2020, false, 2020}, // AD is a no-op
		{1, false, 1},
	} {
		got, err := ApplyEra(mk(c.year), c.bc)
		if err != nil {
			t.Errorf("ApplyEra(year=%d, bc=%v): %v", c.year, c.bc, err)
			continue
		}
		if got.Year() != c.wantYear {
			t.Errorf("ApplyEra(year=%d, bc=%v).Year() = %d, want %d", c.year, c.bc, got.Year(), c.wantYear)
		}
		// Only the year is rewritten; every other field is carried through.
		if got.Month() != 3 || got.Day() != 4 || got.Hour() != 5 || got.Minute() != 6 || got.Second() != 7 {
			t.Errorf("ApplyEra(year=%d, bc=%v) disturbed a non-year field: %v", c.year, c.bc, got)
		}
	}

	// There is no year zero in either era. PG: '0000-01-01'::date and
	// '0000-01-01 BC'::date both raise 22008 "date/time field value out of
	// range" — goopg used to accept the former and store a value that printed
	// back as 1753-08-29.
	for _, bc := range []bool{false, true} {
		if _, err := ApplyEra(mk(0), bc); !errors.Is(err, ErrFieldOutOfRange) {
			t.Errorf("ApplyEra(year=0, bc=%v) err = %v, want ErrFieldOutOfRange", bc, err)
		}
	}
	// A sign AND an era is not a PG spelling; refuse rather than pick one.
	if _, err := ApplyEra(mk(-5), true); !errors.Is(err, ErrFieldOutOfRange) {
		t.Errorf("ApplyEra(year=-5, bc=true) err = %v, want ErrFieldOutOfRange", err)
	}
}

// EraYear is the output-side inverse: the digits PG prints plus whether " BC"
// follows (EncodeDateOnly: `(tm_year > 0) ? tm_year : -(tm_year - 1)`).
func TestEraYearRoundTripsApplyEra(t *testing.T) {
	for _, c := range []struct {
		astro    int
		wantYear int
		wantBC   bool
	}{
		{2020, 2020, false},
		{1, 1, false},
		{0, 1, true},
		{-1, 2, true},
		{-2019, 2020, true},
		{-4712, 4713, true},
	} {
		gotYear, gotBC := EraYear(c.astro)
		if gotYear != c.wantYear || gotBC != c.wantBC {
			t.Errorf("EraYear(%d) = (%d, %v), want (%d, %v)", c.astro, gotYear, gotBC, c.wantYear, c.wantBC)
		}
		// And it is exactly ApplyEra's inverse, so input and output cannot drift.
		back, err := ApplyEra(time.Date(gotYear, 1, 1, 0, 0, 0, 0, time.UTC), gotBC)
		if err != nil {
			t.Fatalf("ApplyEra(EraYear(%d)): %v", c.astro, err)
		}
		if back.Year() != c.astro {
			t.Errorf("ApplyEra(EraYear(%d)) = year %d, want %d", c.astro, back.Year(), c.astro)
		}
	}
}
