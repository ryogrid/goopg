package executor

import (
	"encoding/binary"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (61st slice). `smallserial`/`serial2`/`serial4`/`serial8` (and the
// already-listed `serial`/`bigserial`) are the sequence-backed spellings of the
// fixed-width integer types, but `codec.go`'s heap arms listed only `serial` and
// `bigserial`, so a `smallserial`/`serial2`/`serial4`/`serial8` column fell
// through to the varlena default and stored its value as TEXT where PG stores
// the 2/4/8-byte int image (feedback_pg_faithful_binary_over_text, inverted).
// `copy_binary.go` was missing the whole serial family. These tests pin that the
// heap, the alignment table and both binary-COPY directions now agree with the
// canonical integer spelling.

var serialFamilyCols = []struct {
	name      string
	canonical string // underlying integer spelling the serial name expands to
	width     int
	value     int64
}{
	{"smallserial", "int2", 2, 1234},
	{"serial2", "int2", 2, -1234},
	{"serial", "int4", 4, 305419896}, // 0x12345678
	{"serial4", "int4", 4, -123456789},
	{"bigserial", "int8", 8, 1234567890123},
	{"serial8", "int8", 8, -1234567890123},
}

// The heap stores the fixed-width image, not varlena text.
func TestSerialSpellingsEncodeFixedWidthInt(t *testing.T) {
	for _, tc := range serialFamilyCols {
		got, err := encodeValuePG(catalog.Type{Name: tc.name}, NewIntDatum(tc.value))
		if err != nil {
			t.Fatalf("%s: encodeValuePG: %v", tc.name, err)
		}
		want, err := encodeValuePG(catalog.Type{Name: tc.canonical}, NewIntDatum(tc.value))
		if err != nil {
			t.Fatalf("%s: encodeValuePG(%s): %v", tc.name, tc.canonical, err)
		}
		if len(got) != tc.width {
			t.Fatalf("%s: encoded width %d, want %d (varlena text would differ)", tc.name, len(got), tc.width)
		}
		if string(got) != string(want) {
			t.Fatalf("%s: image %x, want %x (canonical %s)", tc.name, got, want, tc.canonical)
		}
	}
}

// A bare quoted literal / COPY TEXT FROM path hands a KindString datum to the
// codec; the int arms coerce it (coerceStringToInt64) so the stored image is
// identical to the typed one. This is what makes COPY TEXT FROM of a serial
// column correct without a separate copy_text.go decode arm.
func TestSerialSpellingsEncodeKindStringCoerces(t *testing.T) {
	for _, tc := range serialFamilyCols {
		text := strconv.FormatInt(tc.value, 10)
		got, err := encodeValuePG(catalog.Type{Name: tc.name}, NewStringDatum(text))
		if err != nil {
			t.Fatalf("%s: encodeValuePG(string): %v", tc.name, err)
		}
		want, err := encodeValuePG(catalog.Type{Name: tc.name}, NewIntDatum(tc.value))
		if err != nil {
			t.Fatalf("%s: encodeValuePG(int): %v", tc.name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s: string %q encoded %x, want %x", tc.name, text, got, want)
		}
	}
}

// The alignment table (physicalPGTypeAlign) must agree with the stored width, or
// a hosted PG deforms the tuple. int2 aligns 2, int4 4, int8 8.
func TestSerialSpellingsAlign(t *testing.T) {
	for _, tc := range serialFamilyCols {
		if got := physicalPGTypeAlign(catalog.Type{Name: tc.name}); got != tc.width {
			t.Fatalf("%s: align %d, want %d", tc.name, got, tc.width)
		}
	}
}

// pgPhysicalTypeIsVarlena must agree with encodeValuePG's fixed-width arms: a
// serial column is NOT varlena. If the two disagree, HEAP_HASVARWIDTH is set
// wrong and the PG18 nocachegetattr attcacheoff walker trips (codec.go comment
// above pgPhysicalTypeIsVarlena).
func TestSerialSpellingsNotVarlena(t *testing.T) {
	for _, tc := range serialFamilyCols {
		if pgPhysicalTypeIsVarlena(catalog.Type{Name: tc.name}) {
			t.Fatalf("%s: pgPhysicalTypeIsVarlena = true, want false (fixed-width int)", tc.name)
		}
	}
}

// Heap round-trip: encode then decode, kind and value survive.
func TestSerialSpellingsHeapRoundTrip(t *testing.T) {
	for _, tc := range serialFamilyCols {
		enc, err := encodeValuePG(catalog.Type{Name: tc.name}, NewIntDatum(tc.value))
		if err != nil {
			t.Fatalf("%s: encodeValuePG: %v", tc.name, err)
		}
		back, n, err := decodePhysicalPGValueMctx(catalog.Type{Name: tc.name}, enc, nil)
		if err != nil {
			t.Fatalf("%s: decodePhysicalPGValueMctx: %v", tc.name, err)
		}
		if n != tc.width {
			t.Fatalf("%s: decoded width %d, want %d", tc.name, n, tc.width)
		}
		if back.Kind != KindInt || back.Int != tc.value {
			t.Fatalf("%s: round-trip %d -> kind %d %d", tc.name, tc.value, back.Kind, back.Int)
		}
	}
}

// The binary-COPY wire image agrees with the heap image in width and value —
// only the byte order differs (heap little-endian, COPY wire big-endian).
func TestSerialSpellingsAgreeWithBinaryCopy(t *testing.T) {
	for _, tc := range serialFamilyCols {
		col := catalog.Type{Name: tc.name}
		d := NewIntDatum(tc.value)
		wire, err := datumToCopyBinary(col, d)
		if err != nil {
			t.Fatalf("%s: datumToCopyBinary: %v", tc.name, err)
		}
		heap, err := encodeValuePG(col, d)
		if err != nil {
			t.Fatalf("%s: encodeValuePG: %v", tc.name, err)
		}
		if len(heap) != len(wire) || len(heap) != tc.width {
			t.Fatalf("%s: heap width %d vs COPY width %d, want %d", tc.name, len(heap), len(wire), tc.width)
		}
		var wireVal, heapVal int64
		switch tc.width {
		case 2:
			wireVal = int64(int16(binary.BigEndian.Uint16(wire)))
			heapVal = int64(int16(binary.LittleEndian.Uint16(heap)))
		case 4:
			wireVal = int64(int32(binary.BigEndian.Uint32(wire)))
			heapVal = int64(int32(binary.LittleEndian.Uint32(heap)))
		case 8:
			wireVal = int64(binary.BigEndian.Uint64(wire))
			heapVal = int64(binary.LittleEndian.Uint64(heap))
		}
		if wireVal != heapVal || heapVal != tc.value {
			t.Fatalf("%s: COPY binary value %d, heap value %d, want %d", tc.name, wireVal, heapVal, tc.value)
		}
	}
}

// Binary COPY round-trip decodes back to the int datum, not a string.
func TestSerialSpellingsBinaryCopyRoundTrip(t *testing.T) {
	for _, tc := range serialFamilyCols {
		col := catalog.Type{Name: tc.name}
		wire, err := datumToCopyBinary(col, NewIntDatum(tc.value))
		if err != nil {
			t.Fatalf("%s: datumToCopyBinary: %v", tc.name, err)
		}
		back, err := copyBinaryToDatum(col, wire)
		if err != nil {
			t.Fatalf("%s: copyBinaryToDatum: %v", tc.name, err)
		}
		if back.Kind != KindInt || back.Int != tc.value {
			t.Fatalf("%s: round-trip %d -> kind %d %d", tc.name, tc.value, back.Kind, back.Int)
		}
	}
}
