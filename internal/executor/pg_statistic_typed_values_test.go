package executor

import (
	"encoding/binary"
	"reflect"
	"strings"
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
		{"name", 19, []string{"pg_class", "a name, with comma"}, []string{"abc", "pg_proc", "zz"}},
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

// TestNameArrayElementImage pins the name[] element arm (M0149-0001j): each
// element is PG's 64-byte NUL-padded NameData at typalign 'c', so a 2-element
// array is 24 + 2 x 64 = 152 bytes with elemtype 19 (PG 18.3:
// pg_column_size(ARRAY['a','b']::name[]) = 152); input past 63 bytes is
// truncated as namein does; and a legacy blob written before the arm
// (elemtype 25, text bodies) still decodes on its text path.
func TestNameArrayElementImage(t *testing.T) {
	long := strings.Repeat("x", 70)
	typ := catalog.Type{Name: "name", IsArray: true}
	enc, err := encodeValuePG(typ, NewStringDatum("{a,"+long+"}"))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(enc) != 152 {
		t.Fatalf("name[] size = %d, want 152", len(enc))
	}
	if et := binary.LittleEndian.Uint32(enc[12:16]); et != 19 {
		t.Fatalf("elemtype = %d, want 19", et)
	}
	d, _, err := decodePhysicalPGValueMctx(typ, enc, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := d.StringValue(), "{a,"+long[:63]+"}"; got != want {
		t.Errorf("round-trip = %q, want %q", got, want)
	}
	legacy, err := encodeValuePG(catalog.Type{Name: "text", IsArray: true}, NewStringDatum("{a,b}"))
	if err != nil {
		t.Fatalf("legacy encode: %v", err)
	}
	d, _, err = decodePhysicalPGValueMctx(typ, legacy, nil)
	if err != nil || d.StringValue() != "{a,b}" {
		t.Errorf("legacy text-element name[] decode = %q, %v; want {a,b}", d.StringValue(), err)
	}
}
