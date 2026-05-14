package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDecodeRowProjectionSkipsSerialColumn pins the fix for the
// `decodeValueSize` gap that caused CREATE INDEX to fail with
// "column \"data\" is null and cannot be indexed" whenever a SERIAL
// primary key appeared before the indexed column.
//
// Before the fix, decodeValueSize did not match "serial",
// "bigserial", or "smallserial" and fell through to the varlen
// default, which read the first 4 bytes of the int4-encoded SERIAL
// value as a length prefix and advanced the offset by 4+N (where N
// was whatever the encoded id happened to be). The subsequent int4
// column's flag byte was then misread, the column was decoded as
// NULL, and the bulk btree-build code rejected the row as having a
// null key. The same projection skip is hit on every UPDATE /
// DELETE index maintenance path that skips non-key columns, so the
// regression is not limited to CREATE INDEX.
func TestDecodeRowProjectionSkipsSerialColumn(t *testing.T) {
	cases := []struct {
		typeName string
	}{
		{"serial"},
		{"bigserial"},
	}
	for _, tc := range cases {
		t.Run(tc.typeName, func(t *testing.T) {
			cols := []catalog.Column{
				{Name: "id", Type: catalog.Type{Name: tc.typeName}, Ordinal: 0},
				{Name: "data", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
			}
			row := Row{
				{Kind: KindInt, Int: 5},
				{Kind: KindInt, Int: 5},
			}
			encoded, err := EncodeRow(cols, row)
			if err != nil {
				t.Fatalf("EncodeRow: %v", err)
			}
			// Skip the SERIAL column (mimics CREATE INDEX on `data`).
			keep := []bool{false, true}
			dst := make(Row, len(cols))
			if err := DecodeRowProjection(dst, cols, encoded, keep); err != nil {
				t.Fatalf("DecodeRowProjection: %v", err)
			}
			if dst[1].Kind != KindInt || dst[1].Int != 5 {
				t.Errorf("data column: kind=%d int=%d, want KindInt 5", dst[1].Kind, dst[1].Int)
			}
		})
	}
}
