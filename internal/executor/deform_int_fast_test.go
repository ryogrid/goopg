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

// TestDeformNumericFastArmMatchesDecoder pins decodeRowRangeInfo's in-place
// numeric arm (M0122-0012) to the generic decoder's "numeric" arm. Values that
// fit an int64 mantissa take the new arm. Wide mantissas, legacy decimal-text
// payloads and the special spellings must fall through and decode exactly as
// before.
func TestDeformNumericFastArmMatchesDecoder(t *testing.T) {
	for _, name := range []string{"numeric", "decimal"} {
		if !numericFastForLoweredName(catalog.Type{Name: name}, name) {
			t.Errorf("numericFastForLoweredName(%q) = false", name)
		}
		if numericFastForLoweredName(catalog.Type{Name: name, IsArray: true}, name) {
			t.Errorf("numericFastForLoweredName(%q[]) = true", name)
		}
	}
	for _, name := range []string{"int4", "float8", "text", "money"} {
		if numericFastForLoweredName(catalog.Type{Name: name}, name) {
			t.Errorf("numericFastForLoweredName(%q) = true", name)
		}
	}

	// shortVarlena wraps a payload in a 1-byte varlena header, the form a
	// pre-flip cluster stored its numeric text in.
	shortVarlena := func(p string) []byte {
		return append([]byte{byte((len(p)+1)<<1 | 1)}, p...)
	}
	texts := []string{"0", "1", "-1", "12.50", "-0.0001", "99999.99", "9223372036854775807",
		"-9223372036854775808", "123456789012345678901234.5", "0.000000000000000000001"}
	for _, typ := range []catalog.Type{{Name: "numeric"}, {Name: "numeric", Args: []int64{7, 2}}, {Name: "decimal"}} {
		cols := []catalog.Column{
			{Name: "k", Type: catalog.Type{Name: "int4"}},
			{Name: "a", Type: typ},
			{Name: "n", Type: typ},
			{Name: "b", Type: typ},
		}
		for _, text := range texts {
			m, s, err := parseNumeric(text)
			if err != nil {
				t.Fatalf("parseNumeric(%q): %v", text, err)
			}
			row := Row{NewIntDatum(1), newNumeric(m, int(s)), NullDatum, newNumeric(m, int(s))}
			body, err := EncodeRowPG(cols, row)
			if err != nil {
				t.Fatalf("%s %q: encode: %v", typ.Name, text, err)
			}
			checkDeformAgrees(t, typ.Name+" "+text, cols, body, NullBitmapPG(row))
		}
		// Legacy text payloads and special spellings, as an older cluster
		// stores them: int4 then a short varlena, no NULLs.
		for _, legacy := range []string{"12.50", "-3", "NaN", "Infinity", "-Infinity", "123456789012345678901234.5"} {
			body := []byte{1, 0, 0, 0}
			body = append(body, shortVarlena(legacy)...)
			lcols := cols[:2]
			checkDeformAgrees(t, typ.Name+" legacy "+legacy, lcols, body, nil)
		}
	}
	// A malformed varlena (length past the data) must report the generic
	// arm's error, not be read by the fast arm.
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "numeric"}}}
	bad := []byte{0x7f, 1, 2}
	fast := make(Row, 1)
	slow := make(Row, 1)
	_, errF := decodeRowRangeInfo(fast, cols, resolveColTypeInfo(cols), bad, nil, 1, nil, array.DefaultOutputStyle(), 0, 1, 0)
	_, errS := decodeRowRangeInfo(slow, cols, nil, bad, nil, 1, nil, array.DefaultOutputStyle(), 0, 1, 0)
	if errS == nil || errF == nil || errF.Error() != errS.Error() {
		t.Errorf("malformed varlena: fast err=%v, generic err=%v", errF, errS)
	}
}

// checkDeformAgrees decodes one body through the resolved-info path (which
// takes the in-place arms) and the info=nil path (generic decoder only), and
// fails unless the rows, end offsets and errors agree.
func checkDeformAgrees(t *testing.T, label string, cols []catalog.Column, body, bm []byte) {
	t.Helper()
	fast := make(Row, len(cols))
	slow := make(Row, len(cols))
	offF, errF := decodeRowRangeInfo(fast, cols, resolveColTypeInfo(cols), body, bm, len(cols), nil, array.DefaultOutputStyle(), 0, len(cols), 0)
	offS, errS := decodeRowRangeInfo(slow, cols, nil, body, bm, len(cols), nil, array.DefaultOutputStyle(), 0, len(cols), 0)
	if (errF == nil) != (errS == nil) || (errF != nil && errF.Error() != errS.Error()) || offF != offS {
		t.Fatalf("%s: fast (off=%d err=%v) vs generic (off=%d err=%v)", label, offF, errF, offS, errS)
	}
	for i := range cols {
		f, g := fast[i], slow[i]
		// A big-mantissa numeric lives in an arena and its Int is a handle,
		// which differs between two decodes; compare the value it renders.
		if f.Kind == KindNumeric && g.Kind == KindNumeric && (f.Flags|g.Flags)&flagBigNumeric != 0 {
			if f.Flags != g.Flags || numericText(f) != numericText(g) {
				t.Fatalf("%s col %d: fast %s (flags %d) != generic %s (flags %d)", label, i, numericText(f), f.Flags, numericText(g), g.Flags)
			}
			continue
		}
		if !reflect.DeepEqual(f, g) {
			t.Fatalf("%s col %d: fast %#v != generic %#v", label, i, f, g)
		}
	}
}
