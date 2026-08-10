package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 array-element slice, part 2: date[] / time[] / timestamp[] /
// timestamptz[] / timetz[] / bytea[] stop storing their elements as TEXT and
// store the same PG physical image their scalar columns already store.
//
// Before this, arrayElemTypeInfo had no arm for any of the six, so all six fell
// to the unknown-element fallback — an ArrayType whose elemtype field says 25
// (text) while pg_attribute.atttypid for the same column says
// _date / _time / _timestamp / _timestamptz / _timetz / _bytea. goopg read its
// own text straight back, so nothing inside the engine looked wrong; the blob
// and the catalog simply disagreed about one column, which is what a
// descriptor-trusting reader (a PG 18.3 standby, pg_amcheck's heap tier, the
// pgoutput decoder) deforms wrongly.
//
// Every `want` below is captured from the PG 18.3 reference cluster (bench/tpch,
// port 65432) with TimeZone=UTC — goopg's own zone — not derived from goopg.

// TestArrayCodecDateTimeElementRoundTrip is the user-visible half: the element
// text is now upstream's type OUTPUT function applied to the stored image, so a
// non-canonical input spelling is normalised exactly as PG normalises it. The
// pre-flip text path echoed whatever the user typed, which is how '2020-1-2'
// and '1:2:3' used to survive into the output.
func TestArrayCodecDateTimeElementRoundTrip(t *testing.T) {
	cases := []struct {
		elem string
		in   string
		want string // PG 18.3 output for the same ARRAY[...]::<elem>[]
	}{
		{"date", "{2020-01-01,2021-06-15}", "{2020-01-01,2021-06-15}"},
		{"date", "{2020-1-2}", "{2020-01-02}"}, // normalised, not echoed
		{"date", "{infinity,-infinity}", "{infinity,-infinity}"},
		{"date", "{}", "{}"},
		{"time", "{01:02:03,04:05:06.789}", "{01:02:03,04:05:06.789}"},
		{"time", "{1:2:3,12:34:56.100000}", "{01:02:03,12:34:56.1}"},
		// array_out quotes a timestamp: its text contains a space.
		{"timestamp", "{2020-01-01 10:00:00}", `{"2020-01-01 10:00:00"}`},
		{"timestamp", `{"2021-06-15 23:59:59.5"}`, `{"2021-06-15 23:59:59.5"}`},
		{"timestamp", "{infinity}", "{infinity}"},
		// A timestamptz is an absolute instant: +02 comes back as UTC.
		{"timestamptz", `{"2020-01-01 10:00:00+02"}`, `{"2020-01-01 08:00:00+00"}`},
		{"timetz", "{01:02:03+05:00,12:00:00-03:30}", "{01:02:03+05,12:00:00-03:30}"},
		// bytea elements store the RAW bytes and render through byteaout's hex
		// form; the backslash is what makes array_out quote them.
		{"bytea", `{"\\x0102","\\xff"}`, `{"\\x0102","\\xff"}`},
		{"bytea", `{"\\x"}`, `{"\\x"}`},
	}
	for _, tc := range cases {
		typ := catalog.Type{Name: tc.elem, IsArray: true}
		blob, err := encodeValuePG(typ, NewStringDatum(tc.in))
		if err != nil {
			t.Fatalf("%s encode %q: %v", tc.elem, tc.in, err)
		}
		got, n, err := decodePhysicalPGValueMctx(typ, blob, nil)
		if err != nil {
			t.Fatalf("%s decode %q: %v", tc.elem, tc.in, err)
		}
		if n != len(blob) {
			t.Errorf("%s %q: decoded %d bytes, blob is %d", tc.elem, tc.in, n, len(blob))
		}
		if got.Kind != KindString || got.StringValue() != tc.want {
			t.Errorf("%s %q: round-trip = %q, want %q", tc.elem, tc.in, got.StringValue(), tc.want)
		}
	}
}

// TestArrayCodecDateTimeOnDiskLayout is the assertion the round-trip cannot
// make: a self-consistent encoder/decoder pair round-trips text elements just as
// happily. These are PG 18.3's own pg_column_size values for the identical
// arrays, and the elemtype OIDs pg_type assigns:
//
//	pg_column_size(ARRAY['2020-01-01','2021-06-15']::date[])            → 32
//	pg_column_size(ARRAY['01:02:03','04:05:06.789']::time[])            → 40
//	pg_column_size(ARRAY[<two timestamps>]::timestamp[])                → 40
//	pg_column_size(ARRAY[<two timestamptz>]::timestamptz[])             → 40
//	pg_column_size(ARRAY['01:02:03+05','02:00:00+00']::timetz[])        → 56
//	pg_column_size(ARRAY['\x01','\x0102030405']::bytea[])               → 44
//
// The timetz and bytea rows also pin upstream's trailing alignment:
// construct_md_array re-aligns the running length after EVERY element including
// the last, so 24 + 12 + pad4 + 12 = 52 is padded to 56, and 24 + (4+1) padded
// to 8 + (4+5) = 41 is padded to 44.
func TestArrayCodecDateTimeOnDiskLayout(t *testing.T) {
	cases := []struct {
		elem    string
		in      string
		wantOID uint32
		wantLen int
	}{
		{"date", "{2020-01-01,2021-06-15}", 1082, 32},
		{"time", "{01:02:03,04:05:06.789}", 1083, 40},
		{"timestamp", `{"2020-01-01 10:00:00","2021-06-15 23:59:59.5"}`, 1114, 40},
		{"timestamptz", `{"2020-01-01 10:00:00+00","2021-06-15 23:59:59.5+02"}`, 1184, 40},
		{"timetz", "{01:02:03+05,02:00:00+00}", 1266, 56},
		{"bytea", `{"\\x01","\\x0102030405"}`, 17, 44},
	}
	for _, tc := range cases {
		typ := catalog.Type{Name: tc.elem, IsArray: true}
		blob, err := encodeValuePG(typ, NewStringDatum(tc.in))
		if err != nil {
			t.Fatalf("%s encode: %v", tc.elem, err)
		}
		if len(blob) != tc.wantLen {
			t.Errorf("%s: blob is %d bytes, PG 18.3 pg_column_size is %d", tc.elem, len(blob), tc.wantLen)
		}
		if got := int(binary.LittleEndian.Uint32(blob[0:4]) >> 2); got != len(blob) {
			t.Errorf("%s: varlena header says %d, blob is %d", tc.elem, got, len(blob))
		}
		if got := binary.LittleEndian.Uint32(blob[12:16]); got != tc.wantOID {
			t.Errorf("%s: ArrayType elemtype = %d, want %d", tc.elem, got, tc.wantOID)
		}
	}
}

// TestArrayCodecDateTimeElementMatchesScalarColumn is the sibling-agreement gate
// (Hard-won Rule #2). The element encoder DELEGATES to the scalar arm rather
// than re-deriving the image, and this pins that: the bytes an element occupies
// must be byte-identical to the ones the same value takes in a scalar column of
// the element type, at the offset the element table predicts.
func TestArrayCodecDateTimeElementMatchesScalarColumn(t *testing.T) {
	cases := []struct{ elem, value string }{
		{"date", "2020-01-01"},
		{"date", "infinity"},
		{"time", "04:05:06.789"},
		{"timestamp", "2020-01-01 10:00:00"},
		{"timestamptz", "2020-01-01 10:00:00+02"},
		{"timetz", "01:02:03+05"},
	}
	for _, tc := range cases {
		scalar, err := encodeValuePG(catalog.Type{Name: tc.elem}, NewStringDatum(tc.value))
		if err != nil {
			t.Fatalf("%s scalar encode %q: %v", tc.elem, tc.value, err)
		}
		_, size, _, _, ok := arrayElemTypeInfo(tc.elem)
		if !ok {
			t.Fatalf("%s: element table has no arm", tc.elem)
		}
		if len(scalar) != size {
			t.Errorf("%s: scalar image is %d bytes, element table says %d", tc.elem, len(scalar), size)
		}
		blob, err := encodeValuePG(catalog.Type{Name: tc.elem, IsArray: true},
			NewStringDatum("{"+tc.value+"}"))
		if err != nil {
			t.Fatalf("%s array encode %q: %v", tc.elem, tc.value, err)
		}
		elem := blob[arrayHeaderSize : arrayHeaderSize+size]
		if string(elem) != string(scalar) {
			t.Errorf("%s %q: element bytes %x != scalar column bytes %x", tc.elem, tc.value, elem, scalar)
		}
	}
}

// TestArrayCodecDateTimeLegacyTextBlob is the read-compat half: the flip has no
// on-disk migration, so every cluster predating it holds these arrays in the
// pre-flip shape (elemtype 25, 4-byte-varlena text bodies at align 4). The
// discrimination is exact rather than heuristic — the blob states its own
// element type, and elemtype 25 under a date[]/bytea[] column can only have come
// from the pre-flip encoder.
func TestArrayCodecDateTimeLegacyTextBlob(t *testing.T) {
	legacyBlob := func(elems []string) []byte {
		var body []byte
		for _, e := range elems {
			if pad := alignPad(len(body), 4); pad > 0 {
				body = append(body, make([]byte, pad)...)
			}
			body = append(body, array4ByteVarlena(e)...)
		}
		buf := make([]byte, arrayHeaderSize+len(body))
		binary.LittleEndian.PutUint32(buf[0:4], uint32(len(buf))<<2)
		binary.LittleEndian.PutUint32(buf[4:8], 1)
		binary.LittleEndian.PutUint32(buf[8:12], 0)
		binary.LittleEndian.PutUint32(buf[12:16], 25) // text — the pre-flip fallback
		binary.LittleEndian.PutUint32(buf[16:20], uint32(len(elems)))
		binary.LittleEndian.PutUint32(buf[20:24], 1)
		copy(buf[arrayHeaderSize:], body)
		return buf
	}
	cases := []struct {
		elem  string
		elems []string
		want  string
	}{
		{"date", []string{"2020-01-01", "2021-06-15"}, "{2020-01-01,2021-06-15}"},
		{"time", []string{"01:02:03"}, "{01:02:03}"},
		{"timestamp", []string{"2020-01-01 10:00:00"}, `{"2020-01-01 10:00:00"}`},
		{"timetz", []string{"01:02:03+05"}, "{01:02:03+05}"},
		{"bytea", []string{`\x0102`}, `{"\\x0102"}`},
	}
	for _, tc := range cases {
		typ := catalog.Type{Name: tc.elem, IsArray: true}
		blob := legacyBlob(tc.elems)
		got, n, err := decodePhysicalPGValueMctx(typ, blob, nil)
		if err != nil {
			t.Fatalf("%s legacy decode: %v", tc.elem, err)
		}
		if n != len(blob) {
			t.Errorf("%s legacy: decoded %d bytes, blob is %d", tc.elem, n, len(blob))
		}
		if got.StringValue() != tc.want {
			t.Errorf("%s legacy: got %q, want %q", tc.elem, got.StringValue(), tc.want)
		}
	}
}

// TestArrayCodecDateTimeInvalidElement: a bad element is now rejected at encode
// time with the scalar arm's SQLSTATE instead of being stored as text. The
// pre-flip path accepted literally anything.
func TestArrayCodecDateTimeInvalidElement(t *testing.T) {
	cases := []struct{ elem, in string }{
		{"date", "{not-a-date}"},
		{"time", "{25:99:99xyz}"},
		{"timestamp", "{not a timestamp}"},
		{"timetz", "{nope}"},
	}
	for _, tc := range cases {
		typ := catalog.Type{Name: tc.elem, IsArray: true}
		if _, err := encodeValuePG(typ, NewStringDatum(tc.in)); err == nil {
			t.Errorf("%s %q: encode accepted an invalid element", tc.elem, tc.in)
		}
	}
}
