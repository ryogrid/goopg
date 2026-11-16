package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCopyBinaryGeoTidMoney pins M0149-0001k4: COPY (FORMAT binary) ships
// point / lseg / box / line / circle / tid / money in their send formats
// (geo_ops.c *_send: big-endian float8 fields; tidsend: uint32 block +
// uint16 offset; cash_send: int64) rather than their text, the recv twins
// read them back, and circle_recv's negative-radius rejection is 22P02.
// The streams were measured byte-identical to PG 18.3's for the same rows.
func TestCopyBinaryGeoTidMoney(t *testing.T) {
	for _, tc := range []struct {
		typ, text string
		size      int
	}{
		{"point", "(1.5,2)", 16}, {"lseg", "[(0,0),(1,1)]", 32}, {"box", "(5,5),(0,0)", 32},
		{"line", "{1,-1,0}", 24}, {"circle", "<(1,2),3.25>", 24}, {"tid", "(3,4)", 6},
	} {
		typ := catalog.Type{Name: tc.typ}
		b, err := datumToCopyBinary(typ, NewStringDatum(tc.text))
		if err != nil || len(b) != tc.size {
			t.Fatalf("%s send: %d bytes, %v; want %d", tc.typ, len(b), err, tc.size)
		}
		d, err := copyBinaryToDatum(typ, b)
		if err != nil || d.StringValue() != tc.text {
			t.Errorf("%s recv = %q, %v; want %q", tc.typ, d.StringValue(), err, tc.text)
		}
	}
	m, err := datumToCopyBinary(catalog.Type{Name: "money"}, moneyDatum(-50))
	if err != nil || int64(binary.BigEndian.Uint64(m)) != -50 {
		t.Errorf("cash_send = % x, %v", m, err)
	}
	if d, err := copyBinaryToDatum(catalog.Type{Name: "money"}, m); err != nil || MoneyText(d) != "-$0.50" {
		t.Errorf("cash_recv = %q, %v", MoneyText(d), err)
	}
	neg := make([]byte, 24)
	binary.BigEndian.PutUint64(neg[16:], 0xBFF0000000000000) // radius -1.0
	if _, err := copyBinaryToDatum(catalog.Type{Name: "circle"}, neg); err == nil || err.(*ExecError).Code != "22P02" {
		t.Errorf("negative radius: err = %v, want 22P02", err)
	}
}
