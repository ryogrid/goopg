package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestFixedGeoArrayElements pins M0149-0001k3: in a fixed_length_geometric_tid
// cluster a point[] / box[] / tid[] value is a PG ArrayType of the element's
// own fixed image (elemtype 600 / 603 / 27; point[] of two is 24 + 2×16 = 56
// bytes, box[] of two 24 + 2×32 = 88, tid[] of two 24 + 2×6 = 36), box[] uses its typdelim ';' on input
// and output, and every value renders back as PG's array_out text. Without
// the capability the same texts round-trip through the older text-element
// form.
func TestFixedGeoArrayElements(t *testing.T) {
	defer catalog.SetFixedLengthGeoTid(catalog.FixedLengthGeoTid())
	for _, tc := range []struct {
		elem, in, want string
		oid            uint32
		size           int
	}{
		{"point", `{"(1,2)","(3.5,-4)"}`, `{"(1,2)","(3.5,-4)"}`, 600, 56},
		{"box", `{(1,1),(0,0);(5,5),(2,2)}`, `{(1,1),(0,0);(5,5),(2,2)}`, 603, 88},
		{"box", `{"(2,2),(0,0)"}`, `{(2,2),(0,0)}`, 603, 56},
		{"tid", `{"(3,4)","(5,6)"}`, `{"(3,4)","(5,6)"}`, 27, 36},
	} {
		for _, on := range []bool{true, false} {
			catalog.SetFixedLengthGeoTid(on)
			typ := catalog.Type{Name: tc.elem, IsArray: true}
			enc, err := encodeValuePG(typ, NewStringDatum(tc.in))
			if err != nil {
				t.Fatalf("%s %s capability=%v: %v", tc.elem, tc.in, on, err)
			}
			if on {
				if et := binary.LittleEndian.Uint32(enc[12:16]); et != tc.oid || len(enc) != tc.size {
					t.Errorf("%s %s: elemtype %d size %d, want %d / %d", tc.elem, tc.in, et, len(enc), tc.oid, tc.size)
				}
			}
			d, _, err := decodePhysicalPGValueMctx(typ, enc, nil)
			if err != nil || d.StringValue() != tc.want {
				t.Errorf("%s %s capability=%v: decoded %q, %v; want %q", tc.elem, tc.in, on, d.StringValue(), err, tc.want)
			}
		}
	}
}
