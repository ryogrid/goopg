package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// The M0119-0006 array-element slice: interval[], uuid[] and numeric[] stop
// storing their elements as text and store the same PG physical image their
// scalar columns already store. Before this, arrayElemTypeInfo had no arm for
// any of the three, so all three fell to the unknown-element fallback — an
// ArrayType whose elemtype field said 25 (text) while pg_attribute.atttypid
// said _interval / _uuid / _numeric, holding text bodies under a type whose
// typlen is 16 (interval, uuid) or whose input function is numeric_in.
//
// Every `want` below is captured from the PG 18.3 reference cluster
// (bench/tpch, port 65432), not derived from goopg's own behaviour.

// TestArrayCodecPGTypeElementRoundTrip checks the text → blob → text cycle for
// the three flipped element types. The interval cases carry the real content of
// the flip: '2 hours' comes back as interval_out's 02:00:00, and '1 mon' stays
// '1 mon' but quoted by array_out because it contains a space — the pre-flip
// text path echoed both back verbatim and unquoted.
func TestArrayCodecPGTypeElementRoundTrip(t *testing.T) {
	cases := []struct {
		elem string
		in   string
		want string // PG 18.3 output for the same ARRAY[...]::<elem>[]
	}{
		// SELECT ARRAY['1 mon','30 days','2 hours']::interval[]
		//   → {"1 mon","30 days",02:00:00}
		{"interval", "{1 mon,30 days,2 hours}", `{"1 mon","30 days",02:00:00}`},
		{"interval", `{"1 mon","2 hours"}`, `{"1 mon",02:00:00}`},
		{"interval", "{00:00:00}", "{00:00:00}"},
		{"interval", "{}", "{}"},
		// SELECT ARRAY['A0EEBC99-...','000...0']::uuid[] → lowercased canonical.
		{"uuid", "{A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11,00000000-0000-0000-0000-000000000001}",
			"{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11,00000000-0000-0000-0000-000000000001}"},
		{"uuid", "{}", "{}"},
		// SELECT ARRAY['1.50','0','-2.5e3']::numeric[] → {1.50,0,-2500}
		{"numeric", "{1.50,0,-2.5e3}", "{1.50,0,-2500}"},
		{"numeric", "{-0.00001,123456789012345678901234567890}",
			"{-0.00001,123456789012345678901234567890}"},
		{"numeric", "{}", "{}"},
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

// TestArrayCodecPGTypeOnDiskLayout pins the blob shape against PG 18.3's own
// pg_column_size for the identical arrays. This is the assertion the round-trip
// test cannot make: a self-consistent encoder/decoder pair would round-trip
// text elements just as happily, and it is the ON-DISK image that a PG standby,
// pg_amcheck's heap tier or a logical subscriber reads.
//
//	select pg_column_size(ARRAY['1 mon','2 hours']::interval[])       → 56
//	select pg_column_size(ARRAY[<uuid>,<uuid>]::uuid[])               → 56
//	select pg_column_size(ARRAY['1.50','-2500']::numeric[])           → 44
//
// 56 = 24-byte header + two 16-byte fields (interval at align 8, uuid at align
// 1); 44 = 24 + a 10-byte numeric varlena padded to 12 + an 8-byte one at
// align 4.
func TestArrayCodecPGTypeOnDiskLayout(t *testing.T) {
	cases := []struct {
		elem    string
		in      string
		wantOID uint32
		wantLen int
	}{
		{"interval", "{1 mon,2 hours}", 1186, 56},
		{"uuid", "{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11,00000000-0000-0000-0000-000000000001}", 2950, 56},
		{"numeric", "{1.50,-2500}", 1700, 44},
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

// TestArrayCodecPGTypeLegacyTextBlob is the read-compat half: the flip has no
// on-disk migration, so every cluster that predates it holds interval/uuid/
// numeric arrays in the pre-flip shape — elemtype 25 with 4-byte-varlena text
// bodies at align 4. The discrimination is exact, not heuristic: the blob
// states its own element type, and elemtype 25 under one of these columns can
// only have come from the pre-flip encoder.
func TestArrayCodecPGTypeLegacyTextBlob(t *testing.T) {
	// legacyBlob rebuilds exactly what the pre-flip encoder emitted.
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
		{"interval", []string{"1 mon", "2 hours"}, `{"1 mon","2 hours"}`},
		{"uuid", []string{"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"}, "{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}"},
		{"numeric", []string{"1.50", "-2500"}, "{1.50,-2500}"},
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

// TestArrayCodecPGTypeInvalidElement checks that a bad element is rejected at
// encode time with the scalar arm's SQLSTATE rather than silently stored. The
// pre-flip text path accepted anything at all.
func TestArrayCodecPGTypeInvalidElement(t *testing.T) {
	cases := []struct{ elem, in, code string }{
		{"uuid", "{not-a-uuid}", "22P02"},
		{"interval", "{not an interval}", "22007"},
		{"numeric", "{12x3}", "22P02"},
	}
	for _, tc := range cases {
		typ := catalog.Type{Name: tc.elem, IsArray: true}
		_, err := encodeValuePG(typ, NewStringDatum(tc.in))
		if err == nil {
			t.Fatalf("%s %q: encode accepted an invalid element", tc.elem, tc.in)
		}
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != tc.code {
			t.Errorf("%s %q: err = %v, want ExecError %s", tc.elem, tc.in, err, tc.code)
		}
	}
}
