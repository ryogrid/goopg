package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0131-S14.2. pg_attribute.attmissingval is the catalog half of PG's
// fast-default mechanism: `ALTER TABLE … ADD COLUMN … DEFAULT <const>` does not
// rewrite the heap, it stores the evaluated default as a ONE-ELEMENT ArrayType
// and every physically short tuple materialises it on read
// (StoreAttrMissingVal, postgres/src/backend/catalog/heap.c:2030 →
// getmissingattr, postgres/src/backend/access/common/heaptuple.c).
//
// A hosted real PG deforms that blob with its OWN compiled reader, so the bytes
// must match upstream's construct_array output exactly. The end-to-end proof is
// TestE2E_PGColdStartOnGoopgDataDir (assertFastDefaultMaterialisesOnHostedPG,
// which reads tag = 'dflt' on 15 short tuples through a real PG 18.3); this
// test pins the byte layout cheaply so a regression names itself here first.
func TestPGSingletonArrayBytesMatchesConstructArray(t *testing.T) {
	cases := []struct {
		name     string
		typ      catalog.Type
		elemOID  uint32
		datum    Datum
		wantData []byte // bytes after the 24-byte ArrayType header
	}{
		{
			// text 'dflt' → a 4-byte-header varlena, NOT goopg's preferred
			// 1-byte short header: construct_array copies the datum as the
			// backend holds it, and pg_attribute is not TOASTed in place.
			name:     "text",
			typ:      catalog.Type{Name: "text"},
			elemOID:  25,
			datum:    NewStringDatum("dflt"),
			wantData: []byte{(4 + 4) << 2, 0, 0, 0, 'd', 'f', 'l', 't'},
		},
		{
			name:     "int4",
			typ:      catalog.Type{Name: "int4"},
			elemOID:  23,
			datum:    NewIntDatum(42),
			wantData: []byte{42, 0, 0, 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := pgSingletonArrayBytes(tc.elemOID, tc.typ, tc.datum)
			if err != nil {
				t.Fatalf("pgSingletonArrayBytes: %v", err)
			}
			if len(blob) != 24+len(tc.wantData) {
				t.Fatalf("blob length = %d, want %d (24-byte ArrayType header + %d)",
					len(blob), 24+len(tc.wantData), len(tc.wantData))
			}
			// vl_len_ is the 4-byte varlena header in its <<2 form.
			if got := binary.LittleEndian.Uint32(blob[0:4]) >> 2; int(got) != len(blob) {
				t.Errorf("vl_len_ = %d, want %d", got, len(blob))
			}
			for _, f := range []struct {
				name string
				off  int
				want uint32
			}{
				{"ndim", 4, 1},
				{"dataoffset", 8, 0}, // 0 = no null bitmap; a NULL default stores nothing
				{"elemtype", 12, tc.elemOID},
				{"dims[0]", 16, 1},
				{"lbound[0]", 20, 1},
			} {
				if got := binary.LittleEndian.Uint32(blob[f.off : f.off+4]); got != f.want {
					t.Errorf("%s = %d, want %d", f.name, got, f.want)
				}
			}
			if got := blob[24:]; string(got) != string(tc.wantData) {
				t.Errorf("element bytes = % x, want % x", got, tc.wantData)
			}
			// Round-trip: the sibling reader must recover the same value, or
			// whichever path S14.3 eventually points at the heap reads garbage.
			back, ok := pgSingletonArrayElement(blob, tc.typ)
			if !ok {
				t.Fatalf("pgSingletonArrayElement rejected the blob it is the inverse of")
			}
			if back.Format() != tc.datum.Format() {
				t.Errorf("round-trip = %q, want %q", back.Format(), tc.datum.Format())
			}
		})
	}
}

// A NULL default stores no missing value at all — ATExecAddColumn skips
// StoreAttrMissingVal when missingIsNull (tablecmds.c:7551) — and a column that
// never carried one keeps atthasmissing = false with attmissingval SQL NULL.
// The row builder must degrade that way rather than emitting an empty
// ArrayType shell: an all-NULL trailing group is exactly what M0131-S13 had to
// undo for pg_proc.proconfig, where the shell tripped an upstream Assert.
func TestBuildUserPGAttributeRowLeavesMissingValNullWithoutFastDefault(t *testing.T) {
	cols := pgAttributeColumnsPG18()
	idxHasMissing, idxMissingVal := -1, -1
	for i, c := range cols {
		switch c.Name {
		case "atthasmissing":
			idxHasMissing = i
		case "attmissingval":
			idxMissingVal = i
		}
	}
	if idxHasMissing < 0 || idxMissingVal < 0 {
		t.Fatalf("pgAttributeColumnsPG18 lost atthasmissing/attmissingval")
	}
	if got := cols[idxMissingVal].Type.Name; got != "anyarray" {
		t.Errorf("attmissingval declared %q, want \"anyarray\" (OID 2277, typalign='d') — "+
			"a text declaration makes a hosted PG dereference the blob as ArrayType* "+
			"at the wrong alignment (M0131-S14.2)", got)
	}

	tbl := &catalog.Table{
		OID:  90001,
		Name: "s14_plain",
		Columns: []catalog.Column{
			{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		},
	}
	row := buildUserPGAttributeRow(nil, tbl, tbl.Columns[0])
	if row[idxHasMissing].Format() != "false" && row[idxHasMissing].Format() != "f" {
		t.Errorf("atthasmissing = %q for a column with no default, want false", row[idxHasMissing].Format())
	}
	if !row[idxMissingVal].IsNull() {
		t.Errorf("attmissingval = %q for a column with no default, want SQL NULL",
			row[idxMissingVal].Format())
	}
}
