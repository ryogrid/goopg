package executor

import (
	"math"
	"testing"
)

// TestFloatIn pins floatIn (float_in.go) against float8in_internal
// (postgres/src/backend/utils/adt/float.c:395). Every expectation below is a
// line of postgres/src/test/regress/expected/float8.out or float4.out.
//
// M0134-0166: before this, `'x'::float8` had NO string arm in evalCast at all
// and returned the input UNCHANGED, so all of the "wantErr" rows below
// silently SUCCEEDED, storing raw text in a float8.
func TestFloatIn(t *testing.T) {
	const (
		f8 = 64
		f4 = 32
	)
	cases := []struct {
		in       string
		bits     int
		want     float64
		wantCode string
		wantMsg  string
	}{
		// Ordinary values, whitespace on both sides is skipped.
		{in: "34.5", bits: f8, want: 34.5},
		{in: "  34.5  ", bits: f8, want: 34.5},
		{in: "\t-34.84\n", bits: f8, want: -34.84},
		{in: "1.2345678901234e+200", bits: f8, want: 1.2345678901234e+200},

		// Special spellings are case-insensitive and whitespace-tolerant.
		{in: "NaN", bits: f8, want: math.NaN()},
		{in: "  NAN  ", bits: f8, want: math.NaN()},
		{in: "infinity", bits: f8, want: math.Inf(1)},
		{in: "  -INFINiTY  ", bits: f8, want: math.Inf(-1)},
		{in: "inf", bits: f8, want: math.Inf(1)},
		{in: "-Inf", bits: f8, want: math.Inf(-1)},

		// Overflow / underflow are 22003 and name the numeric TOKEN, with
		// leading whitespace already stripped (float8in_internal:469).
		{in: "10e400", bits: f8, wantCode: "22003",
			wantMsg: `"10e400" is out of range for type double precision`},
		{in: "-10e400", bits: f8, wantCode: "22003",
			wantMsg: `"-10e400" is out of range for type double precision`},
		{in: "10e-400", bits: f8, wantCode: "22003",
			wantMsg: `"10e-400" is out of range for type double precision`},
		{in: "  -10e-400  ", bits: f8, wantCode: "22003",
			wantMsg: `"-10e-400" is out of range for type double precision`},
		{in: "1e4000", bits: f8, wantCode: "22003",
			wantMsg: `"1e4000" is out of range for type double precision`},
		{in: "10e400", bits: f4, wantCode: "22003",
			wantMsg: `"10e400" is out of range for type real`},

		// A genuine zero is not an underflow, and a denormal is not an error
		// (float8in_internal's "we'd prefer not to throw error for that").
		{in: "0", bits: f8, want: 0},
		{in: "-0.0000", bits: f8, want: 0},
		{in: "0e500", bits: f8, want: 0},
		{in: "5e-324", bits: f8, want: 5e-324},
		{in: "2.2250738585072014E-308", bits: f8, want: 2.2250738585072014e-308},

		// Syntax errors report the ORIGINAL, untrimmed string.
		{in: "", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: ""`},
		{in: "  ", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: "  "`},
		{in: "xyz", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: "xyz"`},
		{in: "5.0.0", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: "5.0.0"`},
		{in: "5 . 0", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: "5 . 0"`},
		{in: "  - 3", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: "  - 3"`},
		{in: "123  5", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: "123  5"`},
		{in: "N A N", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: "N A N"`},
		{in: "NaN x", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: "NaN x"`},
		{in: " INFINITY  x", bits: f8, wantCode: "22P02",
			wantMsg: `invalid input syntax for type double precision: " INFINITY  x"`},
		{in: "xyz", bits: f4, wantCode: "22P02",
			wantMsg: `invalid input syntax for type real: "xyz"`},

		// float8in_internal checks the range BEFORE the trailing-junk test,
		// so an overflowing token with junk after it reports the overflow.
		{in: "10e400junk", bits: f8, wantCode: "22003",
			wantMsg: `"10e400" is out of range for type double precision`},
	}
	for _, c := range cases {
		got, err := floatIn(c.in, c.bits)
		if c.wantCode != "" {
			if err == nil {
				t.Errorf("floatIn(%q, %d) = %v, want error %s %q", c.in, c.bits, got, c.wantCode, c.wantMsg)
				continue
			}
			if err.Code != c.wantCode || err.Message != c.wantMsg {
				t.Errorf("floatIn(%q, %d) = %s %q, want %s %q",
					c.in, c.bits, err.Code, err.Message, c.wantCode, c.wantMsg)
			}
			continue
		}
		if err != nil {
			t.Errorf("floatIn(%q, %d) unexpected error %s %q", c.in, c.bits, err.Code, err.Message)
			continue
		}
		if math.IsNaN(c.want) {
			if !math.IsNaN(got) {
				t.Errorf("floatIn(%q, %d) = %v, want NaN", c.in, c.bits, got)
			}
			// get_float8_nan()'s canonical quiet NaN, not Go's payload NaN.
			if bits := math.Float64bits(got); bits != 0x7ff8000000000000 {
				t.Errorf("floatIn(%q) NaN bits = %#x, want 0x7ff8000000000000", c.in, bits)
			}
			continue
		}
		if got != c.want {
			t.Errorf("floatIn(%q, %d) = %v, want %v", c.in, c.bits, got, c.want)
		}
	}
}

// TestFloatInDatumRendersCanonicalText pins the second half of the fix: the
// value goopg keeps is float8out's canonical text, not the user's spelling.
// `float8 'NAN'` used to render as "NAN" (evalTypedStringLit fell back to the
// raw string whenever parseNumeric refused it).
func TestFloatInDatumRendersCanonicalText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"NAN", "NaN"},
		{"  -INFINiTY  ", "-Infinity"},
		{"infinity", "Infinity"},
		{"  34.50  ", "34.5"},
	} {
		d, err := floatInDatum(c.in, 64, 0)
		if err != nil {
			t.Fatalf("floatInDatum(%q) error %s %q", c.in, err.Code, err.Message)
		}
		if got := d.Format(); got != c.want {
			t.Errorf("floatInDatum(%q).Format() = %q, want %q", c.in, got, c.want)
		}
	}
}
