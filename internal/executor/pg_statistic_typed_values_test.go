package executor

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestTypedStatValuesRoundTrip pins M0149-0001j: for every type in
// typedStatValueTypes, the stavalues goopg writes are a 1-D anyarray of the
// column's own element type (ndim 1, elemtype = the type's OID) — the form
// PG's update_attstats stores and a PG node's selfuncs can compare — and
// goopg's own reload (catalog.DecodePGStatisticPhysicalRow) gets back exactly
// the strings ANALYZE produced, so goopg's planning is unchanged across a
// restart. A type outside the set keeps the legacy text blob.
func TestTypedStatValuesRoundTrip(t *testing.T) {
	cases := []struct {
		typ  string
		oid  uint32
		mcv  []string
		hist []string
	}{
		{"int4", catalog.OIDInt4, []string{"3", "-7", "0"}, []string{"-100", "5", "2147483647"}},
		{"int8", catalog.OIDInt8, []string{"9000000000"}, []string{"1", "2", "3"}},
		{"int2", catalog.OIDInt2, []string{"12"}, []string{"-5", "7"}},
		{"numeric", 1700, []string{"1.50", "-0.001"}, []string{"0", "12345.6789", "99999999999999999999"}},
		{"float8", 701, []string{"1.5", "-2.25"}, []string{"0.1", "3.14159", "1e+100"}},
		{"text", catalog.OIDText, []string{"v1", "a longer value", ""}, []string{"a", "bb", "ccc"}},
		{"varchar", catalog.OIDVarChar, []string{"x", "yz"}, []string{"abc", "abcd", "abcde"}},
		{"date", 1082, []string{"2020-01-02"}, []string{"1998-01-01", "1998-06-15", "2020-12-31"}},
		{"bool", 16, []string{"t", "f"}, []string{"f", "t"}},
	}
	cols := pgStatisticColumnsPG18()
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			cs := catalog.ColumnStats{NDistinct: 10, Histogram: tc.hist}
			for _, v := range tc.mcv {
				cs.MCV = append(cs.MCV, catalog.MCVEntry{Value: v, Frequency: 0.1})
			}
			row := buildUserPGStatisticRowTyped(42, 1, catalog.Type{Name: tc.typ}, cs)
			for _, idx := range []int{26, 27} { // stavalues1, stavalues2
				blob := row[idx].BytesValue()
				if nd := binary.LittleEndian.Uint32(blob[4:8]); nd != 1 {
					t.Errorf("stavalues slot %d: ndim=%d, want 1", idx, nd)
				}
				if et := binary.LittleEndian.Uint32(blob[12:16]); et != tc.oid {
					t.Errorf("stavalues slot %d: elemtype=%d, want %d", idx, et, tc.oid)
				}
			}
			data, err := EncodeRowPG(cols, row)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, err := catalog.DecodePGStatisticPhysicalRow(data, NullBitmapPG(row))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(decoded.MCVValues, tc.mcv) {
				t.Errorf("MCV round-trip = %q, want %q", decoded.MCVValues, tc.mcv)
			}
			if !reflect.DeepEqual(decoded.HistBounds, tc.hist) {
				t.Errorf("histogram round-trip = %q, want %q", decoded.HistBounds, tc.hist)
			}
		})
	}
	// Outside the set (and the zero Type of the legacy wrapper): text blob.
	for _, typ := range []catalog.Type{{Name: "timestamptz"}, {}} {
		blob := pgStatValuesBytes(typ, []string{"x"})
		if et := binary.LittleEndian.Uint32(blob[12:16]); et != catalog.OIDText {
			t.Errorf("type %q: elemtype=%d, want legacy text %d", typ.Name, et, catalog.OIDText)
		}
	}
}
