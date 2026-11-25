package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/fixedgeo"
)

// TestEncodePgoTuplePhysicalFixedGeoTid pins the pgoutput sibling of the heap
// codec's fixed_length_geometric_tid arm (M0149-0001k): a (point, tid, int4)
// row in PG's fixed-length layout replicates as the same canonical text the
// heap decoder renders, and the following column is read at the right offset.
func TestEncodePgoTuplePhysicalFixedGeoTid(t *testing.T) {
	defer catalog.SetFixedLengthGeoTid(catalog.FixedLengthGeoTid())
	catalog.SetFixedLengthGeoTid(true)
	p, _ := fixedgeo.Encode("point", "(1.5,2)")
	tid, _ := fixedgeo.Encode("tid", "(3,4)")
	body := append(append(append([]byte{}, p...), tid...), 0, 0) // pad to 4
	body = append(body, le32(99)...)
	cols := []ColumnDef{
		{Name: "p", Type: catalog.Type{Name: "point"}},
		{Name: "t", Type: catalog.Type{Name: "tid"}},
		{Name: "after", Type: catalog.Type{Name: "int4"}},
	}
	out, err := encodePgoTuplePhysical(cols, body, []byte{0x07}, 3, nil, false)
	if err != nil {
		t.Fatalf("encodePgoTuplePhysical: %v", err)
	}
	vals := parsePgoTupleTextValues(t, out)
	if len(vals) != 3 || vals[0] != "(1.5,2)" || vals[1] != "(3,4)" || vals[2] != "99" {
		t.Errorf("replicated %q, want [(1.5,2) (3,4) 99]", vals)
	}
}
