package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestArrayCodecNullElements pins PG's ArrayType layout for an array with
// NULL elements (array_in / construct_md_array, M0149-0046a): a null bitmap
// right after lbound[0] (bit i set = element i present), dataoffset =
// ARR_OVERHEAD_WITHNULLS = MAXALIGN(24 + ceil(n/8)), and no data for a NULL
// element. goopg refused any NULL element ("NULL array elements are not
// supported"), which failed subscription/t/002_types.pl's inserts. Only the
// unquoted NULL token is a NULL; "NULL" quoted stays the string. The blob
// must round-trip through the shared renderer (array.RenderText, also the
// pgoutput decoder's).
func TestArrayCodecNullElements(t *testing.T) {
	int4arr := catalog.Type{Name: "int4", IsArray: true}
	blob, err := encodeArrayValuePG(int4arr, NewStringDatum("{5,NULL,3}"))
	if err != nil {
		t.Fatalf("encode {5,NULL,3}: %v", err)
	}
	le := binary.LittleEndian
	// PG 18.3: 16-byte ArrayType + dims/lbound (8) + 1 bitmap byte = 25,
	// MAXALIGN 32, + 2 x int4 = 40 bytes (pg_column_size 37 once the heap
	// converts it to a 1-byte header).
	if len(blob) != 40 || le.Uint32(blob[0:4])>>2 != 40 {
		t.Fatalf("blob size %d (header %d), want 40", len(blob), le.Uint32(blob[0:4])>>2)
	}
	if got := le.Uint32(blob[8:12]); got != 32 {
		t.Errorf("dataoffset = %d, want 32", got)
	}
	if blob[24] != 0b101 {
		t.Errorf("null bitmap = %08b, want 00000101", blob[24])
	}
	if le.Uint32(blob[32:36]) != 5 || le.Uint32(blob[36:40]) != 3 {
		t.Errorf("element data = %x, want 5 then 3", blob[32:40])
	}

	for _, tc := range []struct {
		typ      catalog.Type
		in, want string
	}{
		{int4arr, "{5,NULL,3}", "{5,NULL,3}"},
		{int4arr, "{null}", "{NULL}"},
		{int4arr, "{1,2,3}", "{1,2,3}"},
		{catalog.Type{Name: "text", IsArray: true}, "{e,NULL,b}", "{e,NULL,b}"},
		{catalog.Type{Name: "text", IsArray: true}, `{"NULL",q}`, `{"NULL",q}`},
		{catalog.Type{Name: "text", IsArray: true}, "{NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,x}", "{NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,x}"},
		{catalog.Type{Name: "float8", IsArray: true}, "{5.5,1.1,NULL}", "{5.5,1.1,NULL}"},
	} {
		b, err := encodeArrayValuePG(tc.typ, NewStringDatum(tc.in))
		if err != nil {
			t.Fatalf("encode %s %q: %v", tc.typ.Name, tc.in, err)
		}
		d, _, err := decodeArrayValuePG(tc.typ, b)
		if err != nil {
			t.Fatalf("decode %s %q: %v", tc.typ.Name, tc.in, err)
		}
		if d.StringValue() != tc.want {
			t.Errorf("%s %q round-trip = %q, want %q", tc.typ.Name, tc.in, d.StringValue(), tc.want)
		}
	}
	// No NULLs: dataoffset stays 0 and there is no bitmap.
	if b, _ := encodeArrayValuePG(int4arr, NewStringDatum("{1,2,3}")); le.Uint32(b[8:12]) != 0 || len(b) != 36 {
		t.Errorf("{1,2,3}: dataoffset %d, size %d; want 0, 36", le.Uint32(b[8:12]), len(b))
	}
}
