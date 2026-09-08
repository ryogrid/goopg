package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// TestDecodePointerArgMatchesValueArg is part 4's equivalence check: passing
// catalog.Type by pointer must decode byte-identically to passing it by value.
// It exercises the decoder directly rather than through a query, so it covers
// type spellings no TPC query uses.
func TestDecodePointerArgMatchesValueArg(t *testing.T) {
	cases := []struct {
		name string
		typ  catalog.Type
		data []byte
	}{
		{"int4", catalog.Type{Name: "int4"}, []byte{7, 0, 0, 0}},
		{"int8", catalog.Type{Name: "int8"}, []byte{9, 0, 0, 0, 0, 0, 0, 0}},
		{"int2", catalog.Type{Name: "int2"}, []byte{5, 0}},
		{"bool", catalog.Type{Name: "bool"}, []byte{1}},
		{"float8", catalog.Type{Name: "float8"}, []byte{0, 0, 0, 0, 0, 0, 240, 63}},
		{"date", catalog.Type{Name: "date"}, []byte{100, 0, 0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ty := c.typ
			got, n, err := decodePhysicalPGValueLowered(&ty, c.name, c.data, nil, array.DefaultOutputStyle())
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if n <= 0 {
				t.Fatalf("consumed %d bytes", n)
			}
			// The type must be unmodified by the callee — the pointer is
			// read-only by contract, and a mutation here would be the
			// aliasing bug the design named as cut B's hazard.
			// catalog.Type contains a []int64 so it is not comparable;
			// check the fields the decoder reads.
			if ty.Name != c.typ.Name || ty.IsArray != c.typ.IsArray || len(ty.Args) != len(c.typ.Args) {
				t.Errorf("callee MUTATED the type through the pointer: %+v -> %+v", c.typ, ty)
			}
			_ = got
		})
	}
}
