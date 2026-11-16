package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestFixedLengthGeoTidHeapImage pins M0149-0001k on the heap codec: in a
// cluster with the fixed_length_geometric_tid capability a row
// (int4, point, tid, int4) is laid out as PG lays it out — point at the next
// 8-byte boundary (offset 8, 16 bytes), tid at 's' (offset 24, 6 bytes), the
// trailing int4 at offset 32 — 36 bytes in all, and decodes back to the same
// canonical text with the following column intact. Without the capability
// the same row keeps goopg's older varlena form and round-trips unchanged.
func TestFixedLengthGeoTidHeapImage(t *testing.T) {
	defer catalog.SetFixedLengthGeoTid(catalog.FixedLengthGeoTid())
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "p", Type: catalog.Type{Name: "point"}},
		{Name: "t", Type: catalog.Type{Name: "tid"}},
		{Name: "after", Type: catalog.Type{Name: "int4"}},
	}
	row := Row{NewIntDatum(1), NewStringDatum("(1.5,2)"), NewStringDatum("(3,4)"), NewIntDatum(99)}
	for _, on := range []bool{true, false} {
		catalog.SetFixedLengthGeoTid(on)
		data, err := EncodeRowPG(cols, row)
		if err != nil {
			t.Fatalf("capability=%v: encode: %v", on, err)
		}
		if on && len(data) != 36 {
			t.Errorf("capable cluster: row is %d bytes, want PG's 36", len(data))
		}
		got := make(Row, len(cols))
		if err := DecodeRowIntoMctxPGTuple(got, cols, data, nil, len(cols), nil); err != nil {
			t.Fatalf("capability=%v: decode: %v", on, err)
		}
		if got[1].StringValue() != "(1.5,2)" || got[2].StringValue() != "(3,4)" || got[3].Int != 99 {
			t.Errorf("capability=%v: decoded %v %v %v", on, got[1].StringValue(), got[2].StringValue(), got[3].Int)
		}
	}
}
