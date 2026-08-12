package executor

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/goopg/goopg/internal/catalog"
)

// bpcharWidthCase is driven through BOTH COPY renderers from one table, so the
// text and binary halves cannot answer the same column differently — the
// sibling-paths rule (.ralph/PROMPT.md hard-won rule #2) that the 53rd-56th
// slices kept finding defects with.
type bpcharWidthCase struct {
	name string
	typ  catalog.Type
	in   string
	want string
}

// bpcharWidthCases were measured against PG 18.3: `COPY zz TO STDOUT` piped
// through `cat -A`, and `COPY zz TO STDOUT (FORMAT binary)` through `xxd`, on a
// `char(10)`/`char(3)` table. bpcharsend IS textsend, so the two formats carry
// the SAME bytes — which is exactly why one table drives both.
var bpcharWidthCases = []bpcharWidthCase{
	{"char(10) short pads to 10", catalog.Type{Name: "char", Args: []int64{10}}, "ab", "ab        "},
	{"char(10) empty pads to 10", catalog.Type{Name: "char", Args: []int64{10}}, "", "          "},
	{"char(10) exact unchanged", catalog.Type{Name: "char", Args: []int64{10}}, "abcdefghij", "abcdefghij"},
	{"char(3) short pads to 3", catalog.Type{Name: "char", Args: []int64{3}}, "x", "x  "},
	{"bpchar(4) pads", catalog.Type{Name: "bpchar", Args: []int64{4}}, "hi", "hi  "},
	{"multibyte pads by rune count", catalog.Type{Name: "char", Args: []int64{5}}, "あい", "あい   "},
	{"bare char (OID 18) does not pad", catalog.Type{Name: "char"}, "x", "x"},
	{"varchar(10) does not pad", catalog.Type{Name: "varchar", Args: []int64{10}}, "ab", "ab"},
	{"text does not pad", catalog.Type{Name: "text"}, "ab", "ab"},
}

// TestCopyTextBpcharCarriesDeclaredWidth pins the TEXT/CSV half. Upstream's
// CopyOneRowTo calls the column's output function, and bpcharout is a bare
// TextDatumGetCString that trims NOTHING (postgres/src/backend/utils/adt/
// varchar.c) — so PG writes all N characters where goopg, which stores the
// value trimmed, used to write only the significant ones.
func TestCopyTextBpcharCarriesDeclaredWidth(t *testing.T) {
	for _, tc := range bpcharWidthCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := datumToCopyText(tc.typ, NewStringDatum(tc.in), "ISO", "MDY", "")
			if err != nil {
				t.Fatalf("datumToCopyText: %v", err)
			}
			if got != tc.want {
				t.Errorf("datumToCopyText(%+v, %q) = %q, want %q", tc.typ, tc.in, got, tc.want)
			}
		})
	}
}

// TestCopyBinaryBpcharCarriesDeclaredWidth pins the BINARY half. The field is
// length-prefixed, so the padding is observable as the field LENGTH too: a
// `char(10)` field is 10 bytes on the wire, and a reader that trusts the length
// (every real PG client does) sees a value of the wrong width otherwise.
func TestCopyBinaryBpcharCarriesDeclaredWidth(t *testing.T) {
	for _, tc := range bpcharWidthCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := datumToCopyBinary(tc.typ, NewStringDatum(tc.in))
			if err != nil {
				t.Fatalf("datumToCopyBinary: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("datumToCopyBinary(%+v, %q) = %q, want %q", tc.typ, tc.in, got, tc.want)
			}
			if len(got) != len(tc.want) {
				t.Errorf("binary field length %d, want %d", len(got), len(tc.want))
			}
		})
	}
}

// TestCopyBinaryBpcharRoundTripsToTrimmedStorage pins the DECODE half, which
// deliberately has no bpchar arm of its own. Upstream's bpchar_recv runs
// bpchar_input, which pads to the typmod; goopg's storage convention is the
// opposite (trimmed, so compareDatum's padding-insensitive bpchar equality and
// the compact heap image hold), and coerceTextLikeDatum is the single place
// that convention is applied — including its 22001, whose wording is
// bpchar_input's own. Adding a padding arm to copyBinaryToDatum would put a
// padded value into a column an INSERT stores trimmed, i.e. make the same
// column two different widths depending on how it was loaded.
func TestCopyBinaryBpcharRoundTripsToTrimmedStorage(t *testing.T) {
	typ := catalog.Type{Name: "char", Args: []int64{10}}
	// A field exactly as real PG writes it.
	wire, err := datumToCopyBinary(typ, NewStringDatum("ab"))
	if err != nil {
		t.Fatalf("datumToCopyBinary: %v", err)
	}
	if len(wire) != 10 {
		t.Fatalf("wire field is %d bytes, want 10", len(wire))
	}
	back, err := copyBinaryToDatum(typ, wire)
	if err != nil {
		t.Fatalf("copyBinaryToDatum: %v", err)
	}
	stored, err := coerceTextLikeDatum(typ, back)
	if err != nil {
		t.Fatalf("coerceTextLikeDatum: %v", err)
	}
	if stored != "ab" {
		t.Errorf("stored %q, want %q (goopg stores bpchar trimmed)", stored, "ab")
	}
	// And the over-length rule bpchar_input applies at input still fires on a
	// foreign stream whose extra characters are NOT spaces.
	if _, err := coerceTextLikeDatum(typ, NewStringDatum("abcdefghijk")); err == nil {
		t.Error("coerceTextLikeDatum accepted an 11-character char(10) value; want 22001")
	}
}

// TestCoerceTextLikeDatumMeasuresCharactersNotBytes pins the unit the declared
// length is counted in. Upstream varchar_input and bpchar_input both measure
// with pg_mbstrlen_with_len before converting maxlen to a byte length
// (postgres/src/backend/utils/adt/varchar.c). Measured on PG 18.3:
// `octet_length('あいうえお'::varchar(5))` = 15 (accepted) and
// `octet_length('あい'::char(5))` = 9 with `length()` = 2. Counting BYTES here
// rejected both with a spurious 22001 — and disagreed with the rune-counting
// truncation the explicit-cast path in expr.go already performed and with the
// rune-counting pad catalog.PadBpchar applies on the way out.
func TestCoerceTextLikeDatumMeasuresCharactersNotBytes(t *testing.T) {
	accept := []struct {
		typ catalog.Type
		in  string
	}{
		{catalog.Type{Name: "varchar", Args: []int64{5}}, "あいうえお"}, // 5 runes, 15 bytes
		{catalog.Type{Name: "char", Args: []int64{5}}, "あい"},      // 2 runes, 6 bytes
		{catalog.Type{Name: "char", Args: []int64{2}}, "あい"},      // exact fit
		{catalog.Type{Name: "varchar", Args: []int64{5}}, "abcde"},
	}
	for _, tc := range accept {
		got, err := coerceTextLikeDatum(tc.typ, NewStringDatum(tc.in))
		if err != nil {
			t.Errorf("coerceTextLikeDatum(%s(%d), %q [%d runes, %d bytes]): %v",
				tc.typ.Name, tc.typ.Args[0], tc.in, utf8.RuneCountInString(tc.in), len(tc.in), err)
			continue
		}
		if got != tc.in {
			t.Errorf("coerceTextLikeDatum(%s(%d), %q) = %q, want it unchanged",
				tc.typ.Name, tc.typ.Args[0], tc.in, got)
		}
	}

	reject := []struct {
		typ  catalog.Type
		in   string
		want string
	}{
		{catalog.Type{Name: "varchar", Args: []int64{5}}, "あいうえおか", "character varying(5)"},
		{catalog.Type{Name: "char", Args: []int64{2}}, "あいう", "character(2)"},
		{catalog.Type{Name: "varchar", Args: []int64{5}}, "abcdef", "character varying(5)"},
	}
	for _, tc := range reject {
		_, err := coerceTextLikeDatum(tc.typ, NewStringDatum(tc.in))
		if err == nil {
			t.Errorf("coerceTextLikeDatum(%s(%d), %q) accepted; want 22001",
				tc.typ.Name, tc.typ.Args[0], tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("coerceTextLikeDatum(%s(%d), %q) error %q, want it to name %q",
				tc.typ.Name, tc.typ.Args[0], tc.in, err.Error(), tc.want)
		}
	}
}
