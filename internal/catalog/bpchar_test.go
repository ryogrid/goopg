package catalog

import "testing"

// TestPadBpchar pins the rule the four render boundaries share. Every `want`
// was measured against a throwaway PG 18.3 (initdb out of
// postgres/local_install, `pg_ctl -o "-p 5539 -k $D -h ''"`) with
// `SELECT octet_length(<value>::<type>)` and a `psql -A -t` DataRow read
// through `cat -A`; the multibyte rows are the ones that separate a
// character count from a byte count.
func TestPadBpchar(t *testing.T) {
	cases := []struct {
		name string
		typ  Type
		in   string
		want string
	}{
		// char(10) holding 'ab' is 10 bytes on PG; goopg stores 'ab'.
		{"char(10) short", Type{Name: "char", Args: []int64{10}}, "ab", "ab        "},
		{"char(10) empty", Type{Name: "char", Args: []int64{10}}, "", "          "},
		{"char(10) exact", Type{Name: "char", Args: []int64{10}}, "abcdefghij", "abcdefghij"},
		{"bpchar(3)", Type{Name: "bpchar", Args: []int64{3}}, "x", "x  "},
		{"character(3)", Type{Name: "character", Args: []int64{3}}, "xy", "xy "},
		{"case-insensitive name", Type{Name: "CHAR", Args: []int64{4}}, "z", "z   "},

		// The width counts CHARACTERS: `'あい'::char(5)` is octet_length 9 on
		// PG (2 three-byte runes + 3 pad spaces), not 5.
		{"multibyte pads by rune count", Type{Name: "char", Args: []int64{5}}, "あい", "あい   "},
		{"multibyte exact", Type{Name: "char", Args: []int64{2}}, "あい", "あい"},

		// A bare `char` with no typmod is pg_type OID 18, a 1-byte internal
		// type that is not bpchar; a bare `bpchar` is upstream typmod -1, whose
		// maxlen is the actual string length. Neither pads.
		{"bare char is OID 18", Type{Name: "char"}, "x", "x"},
		{"bare bpchar is unlimited", Type{Name: "bpchar"}, "abc", "abc"},

		// Not bpchar at all.
		{"varchar never pads", Type{Name: "varchar", Args: []int64{10}}, "ab", "ab"},
		{"text never pads", Type{Name: "text"}, "ab", "ab"},

		// A char(N)[] column's value is array_out text; padding belongs to the
		// ELEMENTS, which the array renderer already wrote.
		{"array is left alone", Type{Name: "char", Args: []int64{4}, IsArray: true}, "{a,b}", "{a,b}"},

		// Defensive: an over-length value (upstream clips at input) and a
		// nonsense typmod must not corrupt the value.
		{"over-length is untouched", Type{Name: "char", Args: []int64{2}}, "abcd", "abcd"},
		{"zero typmod is untouched", Type{Name: "char", Args: []int64{0}}, "ab", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PadBpchar(tc.typ, tc.in); got != tc.want {
				t.Errorf("PadBpchar(%+v, %q) = %q, want %q", tc.typ, tc.in, got, tc.want)
			}
		})
	}
}
