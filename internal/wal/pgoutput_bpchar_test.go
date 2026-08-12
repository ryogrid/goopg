package wal

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgoDecodeBpcharCarriesDeclaredWidth pins the fourth render boundary. A
// bpchar column's heap image is TRIMMED (executor's coerceTextLikeDatum strips
// trailing spaces so the image stays compact and compareDatum's
// padding-insensitive bpchar equality holds), where upstream's is blank-padded
// to the declared width — so a real PG publisher emits all N characters in the
// change message and this decoder, which returns the varlena payload verbatim,
// emitted only the significant ones. A goopg->PG subscription therefore
// delivered a value of the wrong width into a char(N) column.
//
// The pad is catalog.PadBpchar, shared with appendTypedCellText and the two
// COPY renderers, so the four boundaries cannot drift (.ralph/PROMPT.md
// hard-won rule #2). M0119-0006 (57th slice).
func TestPgoDecodeBpcharCarriesDeclaredWidth(t *testing.T) {
	cases := []struct {
		name string
		typ  catalog.Type
		heap string
		want string
	}{
		{"char(10) short", catalog.Type{Name: "char", Args: []int64{10}}, "ab", "ab        "},
		{"char(10) empty", catalog.Type{Name: "char", Args: []int64{10}}, "", "          "},
		{"char(3) exact", catalog.Type{Name: "char", Args: []int64{3}}, "xyz", "xyz"},
		{"bpchar(4)", catalog.Type{Name: "bpchar", Args: []int64{4}}, "hi", "hi  "},
		{"multibyte by rune count", catalog.Type{Name: "char", Args: []int64{5}}, "あい", "あい   "},
		{"bare char (OID 18) untouched", catalog.Type{Name: "char"}, "x", "x"},
		{"varchar untouched", catalog.Type{Name: "varchar", Args: []int64{10}}, "ab", "ab"},
		{"text untouched", catalog.Type{Name: "text"}, "ab", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := shortVarlena([]byte(tc.heap))
			got, n, err := pgoDecodePhysicalValue(tc.typ, raw)
			if err != nil {
				t.Fatalf("pgoDecodePhysicalValue: %v", err)
			}
			if n != len(raw) {
				t.Fatalf("consumed %d bytes, want %d", n, len(raw))
			}
			if string(got) != tc.want {
				t.Errorf("decoded %q, want %q", got, tc.want)
			}
		})
	}
}
