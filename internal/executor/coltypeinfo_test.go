package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestColTypeInfoArraySafety pins the hazard the take-6 review flagged (m6):
// for an ARRAY column catalog.Type.Name holds the ELEMENT type name and IsArray
// carries the array-ness, so anything derived from the name ALONE would give
// int4[] the alignment and decode path of int4.
//
// resolveColTypeInfo memoises the lowered NAME and passes the full catalog.Type
// alongside it, so every consumer still branches on t.IsArray (and on
// len(t.Args), which separates internal "char" from char(N)) exactly as before.
func TestColTypeInfoArraySafety(t *testing.T) {
	cases := []catalog.Type{
		{Name: "int4"},
		{Name: "int4", IsArray: true},
		{Name: "text"},
		{Name: "text", IsArray: true},
		{Name: "char"},                    // internal 1-byte char
		{Name: "char", Args: []int64{10}}, // char(10) => bpchar varlena
		{Name: "numeric"},
		{Name: "timestamptz"},
	}
	cols := make([]catalog.Column, len(cases))
	for i, ct := range cases {
		cols[i] = catalog.Column{Name: "c", Type: ct, Ordinal: i}
	}
	info := resolveColTypeInfo(cols)
	for i, ct := range cases {
		want := physicalPGTypeAlign(ct)
		if info[i].align != want {
			t.Errorf("%+v: memoised align %d, direct %d", ct, info[i].align, want)
		}
	}
	// The array cases must NOT inherit their element type's alignment.
	if info[1].align != 4 || info[3].align != 4 {
		t.Errorf("array columns must align 4 (varlena ArrayType); got int4[]=%d text[]=%d",
			info[1].align, info[3].align)
	}
	// char vs char(N) must still differ.
	if info[4].align == info[5].align {
		t.Errorf("internal char (align 1) and char(N) (align 4) collapsed to %d", info[4].align)
	}
}
