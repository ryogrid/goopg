package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgoDecodeBpcharCarriesDeclaredWidth pins the fourth render boundary: a
// goopg publisher must emit all N characters of a bpchar, as a real PG
// publisher does, or a goopg->PG subscription delivers a value of the wrong
// width into a char(N) column.
//
// Since M0143-0007b the heap image is padded too, so the pad here is a no-op
// on newly written rows. The cases below deliberately feed TRIMMED payloads:
// that is what rows written before the convention changed look like on disk,
// and they must still render at the declared width. This is why the
// PadBpchar call at this boundary must not be deleted as "now redundant".
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
			got, n, err := pgoDecodePhysicalValue(tc.typ, raw, nil)
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
