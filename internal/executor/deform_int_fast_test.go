package executor

import (
	"math"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// TestDeformIntFastArmMatchesDecoder pins decodeRowRangeInfo's by-value integer
// arm (M0122-0012) to the generic decodePhysicalPGValueLowered it bypasses. The
// arm is a second decoder for the same bytes, so the two must agree on every
// type spelling, every extreme value, NULLs, alignment padding and truncation.
func TestDeformIntFastArmMatchesDecoder(t *testing.T) {
	widths := map[string]int{
		"int2": 2, "smallint": 2, "smallserial": 2, "serial2": 2,
		"int4": 4, "integer": 4, "int": 4, "serial": 4, "serial4": 4,
		"int8": 8, "bigint": 8, "bigserial": 8, "serial8": 8,
	}
	values := map[int][]int64{
		2: {0, 1, -1, math.MinInt16, math.MaxInt16},
		4: {0, 1, -1, math.MinInt32, math.MaxInt32},
		8: {0, 1, -1, math.MinInt64, math.MaxInt64},
	}

	// The memo must name exactly the decoder's integer arms: each spelling
	// gets its width, and the generic decoder reads that many bytes into a
	// KindInt.
	for name, w := range widths {
		ty := catalog.Type{Name: name}
		if got := intWidthForLoweredName(ty, name); int(got) != w {
			t.Errorf("intWidthForLoweredName(%q) = %d, want %d", name, got, w)
		}
		if got := intWidthForLoweredName(catalog.Type{Name: name, IsArray: true}, name); got != 0 {
			t.Errorf("intWidthForLoweredName(%q[]) = %d, want 0", name, got)
		}
		d, n, err := decodePhysicalPGValueLowered(&ty, name, make([]byte, 8), nil, array.DefaultOutputStyle())
		if err != nil || n != w || d.Kind != KindInt {
			t.Errorf("generic decode of %q: kind=%v n=%d err=%v, want KindInt n=%d", name, d.Kind, n, err, w)
		}
	}
	for _, name := range []string{"oid", "date", "bool", "float8", "numeric", "text", "int4range"} {
		if got := intWidthForLoweredName(catalog.Type{Name: name}, name); got != 0 {
			t.Errorf("intWidthForLoweredName(%q) = %d, want 0", name, got)
		}
	}

	// Row-level agreement. A leading int2 then a varlena forces alignment
	// padding before the next integer; a NULL exercises the bitmap arm.
	for name, w := range widths {
		for _, v := range values[w] {
			cols := []catalog.Column{
				{Name: "p", Type: catalog.Type{Name: "int2"}},
				{Name: "x", Type: catalog.Type{Name: name}},
				{Name: "s", Type: catalog.Type{Name: "text"}},
				{Name: "y", Type: catalog.Type{Name: name}},
				{Name: "n", Type: catalog.Type{Name: name}},
				{Name: "z", Type: catalog.Type{Name: name}},
			}
			row := Row{NewIntDatum(7), NewIntDatum(v), NewStringDatum("abc"), NewIntDatum(-v - 1), NullDatum, NewIntDatum(v)}
			if w == 2 && v == math.MinInt16 {
				row[3] = NewIntDatum(math.MaxInt16)
			}
			if w == 4 && v == math.MinInt32 {
				row[3] = NewIntDatum(math.MaxInt32)
			}
			if w == 8 && v == math.MinInt64 {
				row[3] = NewIntDatum(math.MaxInt64)
			}
			body, err := EncodeRowPG(cols, row)
			if err != nil {
				t.Fatalf("%s %d: encode: %v", name, v, err)
			}
			bm := NullBitmapPG(row)
			fast := make(Row, len(cols))
			slow := make(Row, len(cols))
			info := resolveColTypeInfo(cols)
			offF, errF := decodeRowRangeInfo(fast, cols, info, body, bm, len(cols), nil, array.DefaultOutputStyle(), 0, len(cols), 0)
			offS, errS := decodeRowRangeInfo(slow, cols, nil, body, bm, len(cols), nil, array.DefaultOutputStyle(), 0, len(cols), 0)
			if errF != nil || errS != nil || offF != offS {
				t.Fatalf("%s %d: fast (off=%d err=%v) vs generic (off=%d err=%v)", name, v, offF, errF, offS, errS)
			}
			if !reflect.DeepEqual(fast, slow) {
				t.Fatalf("%s %d: fast %v != generic %v", name, v, fast, slow)
			}
			if fast[1].Int != v || fast[5].Int != v || !fast[4].IsNull() {
				t.Fatalf("%s %d: decoded %v", name, v, fast)
			}
		}
	}

	// Truncation: fewer bytes than the width must not be read by the fast
	// arm; both paths then report the same error.
	for name, w := range widths {
		cols := []catalog.Column{{Name: "x", Type: catalog.Type{Name: name}}}
		short := make([]byte, w-1)
		for i := range short {
			short[i] = 1
		}
		fast := make(Row, 1)
		slow := make(Row, 1)
		_, errF := decodeRowRangeInfo(fast, cols, resolveColTypeInfo(cols), short, nil, 1, nil, array.DefaultOutputStyle(), 0, 1, 0)
		_, errS := decodeRowRangeInfo(slow, cols, nil, short, nil, 1, nil, array.DefaultOutputStyle(), 0, 1, 0)
		if (errF == nil) != (errS == nil) || (errF != nil && errF.Error() != errS.Error()) {
			t.Errorf("%s truncated: fast err=%v, generic err=%v", name, errF, errS)
		}
		if errS == nil {
			t.Errorf("%s truncated: generic decode accepted %d bytes", name, w-1)
		}
	}
}
