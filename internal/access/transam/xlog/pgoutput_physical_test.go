package xlog

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestEncodePgoTuplePhysical verifies the M0111-0002 S2a header-driven path:
// a PG-physical heap tuple (natts set, null bitmap present) is decoded via the
// physical reader rather than the legacy [flag][value] walk, producing the same
// pgoutput text the legacy path would for the same logical values, and honoring
// the null bitmap.
func TestEncodePgoTuplePhysical(t *testing.T) {
	cols := []ColumnDef{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "text"}, Ordinal: 1},
		{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
	}

	// PG-physical body: int4=42 (LE), short-varlena "hi", c is NULL (no bytes).
	body := make([]byte, 0, 8)
	var i4 [4]byte
	binary.LittleEndian.PutUint32(i4[:], 42)
	body = append(body, i4[:]...) // off 0..3 (int4 align 4)
	// short varlena "hi": header = (total<<1)|1, total = len+1 = 3.
	body = append(body, byte((3<<1)|0x01), 'h', 'i') // off 4..6 (text align 4, already aligned)

	// Null bitmap, PG convention (bit set = NOT NULL): a,b set; c clear.
	bitmap := []byte{0x03}

	tup := storage.NewHeapTupleWithNulls(storage.FrozenTransactionID, storage.InvalidTransactionID, bitmap, body)
	tup.Header.SetNatts(len(cols))
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	out, err := encodePgoTuple(cols, raw, nil)
	if err != nil {
		t.Fatalf("encodePgoTuple: %v", err)
	}

	ncol, vals := parsePgoTuple(t, out)
	if ncol != 3 {
		t.Fatalf("ncol = %d, want 3", ncol)
	}
	if vals[0].null || string(vals[0].text) != "42" {
		t.Errorf("col a = %+v, want text 42", vals[0])
	}
	if vals[1].null || string(vals[1].text) != "hi" {
		t.Errorf("col b = %+v, want text hi", vals[1])
	}
	if !vals[2].null {
		t.Errorf("col c = %+v, want NULL", vals[2])
	}
}

type pgoCol struct {
	null bool
	text []byte
}

// parsePgoTuple parses the pgoutput logicalrep tuple shape produced by
// encodePgoTuple: uint16 ncol (BE), then per column either 'n' (null) or
// 't' + uint32 len (BE) + bytes.
func parsePgoTuple(t *testing.T, b []byte) (int, []pgoCol) {
	t.Helper()
	if len(b) < 2 {
		t.Fatalf("pgo tuple too short: %d", len(b))
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	off := 2
	cols := make([]pgoCol, 0, n)
	for i := 0; i < n; i++ {
		if off >= len(b) {
			t.Fatalf("pgo tuple truncated at col %d", i)
		}
		kind := b[off]
		off++
		switch kind {
		case pgoColNull:
			cols = append(cols, pgoCol{null: true})
		case pgoColText:
			if off+4 > len(b) {
				t.Fatalf("pgo tuple truncated len at col %d", i)
			}
			l := int(binary.BigEndian.Uint32(b[off : off+4]))
			off += 4
			if off+l > len(b) {
				t.Fatalf("pgo tuple truncated value at col %d", i)
			}
			cols = append(cols, pgoCol{text: b[off : off+l]})
			off += l
		default:
			t.Fatalf("unexpected pgo column kind %q at col %d", kind, i)
		}
	}
	return n, cols
}
