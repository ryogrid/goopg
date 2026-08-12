package executor

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (53rd slice). `copy_binary.go` had NO `float4`/`float8` arm in
// EITHER direction. goopg has no float Datum Kind — a float column's Datum is
// the KindNumeric/KindString that floatTextDatum builds from PGFloatOut's text
// — so the encode default shipped that TEXT under FORMAT binary (a stream no
// real PG client can read: CopyReadBinaryAttribute expects exactly 4/8 bytes),
// and the decode default handed the raw bytes back as a string Datum. Upstream
// is float4send/float8send = pq_sendfloat4/8, the IEEE-754 image big-endian
// (postgres/src/backend/utils/adt/float.c:350,567;
// postgres/src/backend/libpq/pqformat.c:252). These tests fail against that
// HEAD.

func float4Col() catalog.Type { return catalog.Type{Name: "float4"} }
func float8Col() catalog.Type { return catalog.Type{Name: "float8"} }

var floatSamples = []float64{0, 1, -1, 0.5, -0.25, 3.5, 1e10, -1e-10, 12345.75}

func TestCopyBinaryFloatSendShape(t *testing.T) {
	for _, v := range floatSamples {
		d4 := floatTextDatum(PGFloatOut(float64(float32(v)), 32))
		b4, err := datumToCopyBinary(float4Col(), d4)
		if err != nil {
			t.Fatalf("datumToCopyBinary(float4, %v): %v", v, err)
		}
		if len(b4) != 4 {
			t.Fatalf("float4_send(%v) payload = %d bytes, want 4 (upstream pq_sendfloat4)", v, len(b4))
		}
		if got := math.Float32frombits(binary.BigEndian.Uint32(b4)); got != float32(v) {
			t.Fatalf("float4_send(%v) decoded = %v", v, got)
		}

		d8 := floatTextDatum(PGFloatOut(v, 64))
		b8, err := datumToCopyBinary(float8Col(), d8)
		if err != nil {
			t.Fatalf("datumToCopyBinary(float8, %v): %v", v, err)
		}
		if len(b8) != 8 {
			t.Fatalf("float8_send(%v) payload = %d bytes, want 8 (upstream pq_sendfloat8)", v, len(b8))
		}
		if got := math.Float64frombits(binary.BigEndian.Uint64(b8)); got != v {
			t.Fatalf("float8_send(%v) decoded = %v", v, got)
		}
	}
}

// The decode twin must reproduce the Datum shape the HEAP decode produces for
// the same value — not the raw-bytes string the default used to hand back
// (Hard-won Rule #2). Comparing against the heap decoder's own output is the pin.
func TestCopyBinaryFloatRoundTripMatchesHeapDatum(t *testing.T) {
	for _, v := range floatSamples {
		for _, tc := range []struct {
			col  catalog.Type
			bits int
		}{
			{float4Col(), 32}, {catalog.Type{Name: "real"}, 32},
			{float8Col(), 64}, {catalog.Type{Name: "double precision"}, 64},
			{catalog.Type{Name: "double"}, 64},
			// `float` was ENCODE-only in the heap codec until this slice: it
			// stored 8 fixed bytes and then failed to read them back
			// ("truncated 4-byte varlena"). Covering every declared spelling
			// here is what catches that class of encode/decode drift.
			{catalog.Type{Name: "float"}, 64},
		} {
			want := v
			if tc.bits == 32 {
				want = float64(float32(v))
			}
			d := floatTextDatum(PGFloatOut(want, tc.bits))

			wire, err := datumToCopyBinary(tc.col, d)
			if err != nil {
				t.Fatalf("%s %v: datumToCopyBinary: %v", tc.col.Name, v, err)
			}
			back, err := copyBinaryToDatum(tc.col, wire)
			if err != nil {
				t.Fatalf("%s %v: copyBinaryToDatum: %v", tc.col.Name, v, err)
			}

			heap, err := encodeValuePG(tc.col, d)
			if err != nil {
				t.Fatalf("%s %v: encodeValuePG: %v", tc.col.Name, v, err)
			}
			heapBack, _, err := decodePhysicalPGValueMctx(tc.col, heap, nil)
			if err != nil {
				t.Fatalf("%s %v: decodePhysicalPGValueMctx: %v", tc.col.Name, v, err)
			}
			if back.Kind != heapBack.Kind || back.Format() != heapBack.Format() {
				t.Fatalf("%s %v: COPY decode = kind %d %q, heap decode = kind %d %q",
					tc.col.Name, v, back.Kind, back.Format(), heapBack.Kind, heapBack.Format())
			}
		}
	}
}

// Same VALUE as the heap stores — only the byte order differs (heap little-
// endian, the COPY wire big-endian). The encode↔encode sibling pin.
func TestCopyBinaryFloatAgreesWithHeapEncode(t *testing.T) {
	for _, v := range floatSamples {
		d4 := floatTextDatum(PGFloatOut(float64(float32(v)), 32))
		wire4, err := datumToCopyBinary(float4Col(), d4)
		if err != nil {
			t.Fatalf("%v: datumToCopyBinary float4: %v", v, err)
		}
		heap4, err := encodeValuePG(float4Col(), d4)
		if err != nil {
			t.Fatalf("%v: encodeValuePG float4: %v", v, err)
		}
		if len(heap4) != len(wire4) {
			t.Fatalf("%v: float4 heap width %d vs COPY width %d", v, len(heap4), len(wire4))
		}
		if binary.BigEndian.Uint32(wire4) != binary.LittleEndian.Uint32(heap4) {
			t.Fatalf("%v: float4 COPY bits %#x, heap bits %#x",
				v, binary.BigEndian.Uint32(wire4), binary.LittleEndian.Uint32(heap4))
		}

		d8 := floatTextDatum(PGFloatOut(v, 64))
		wire8, err := datumToCopyBinary(float8Col(), d8)
		if err != nil {
			t.Fatalf("%v: datumToCopyBinary float8: %v", v, err)
		}
		heap8, err := encodeValuePG(float8Col(), d8)
		if err != nil {
			t.Fatalf("%v: encodeValuePG float8: %v", v, err)
		}
		if len(heap8) != len(wire8) {
			t.Fatalf("%v: float8 heap width %d vs COPY width %d", v, len(heap8), len(wire8))
		}
		if binary.BigEndian.Uint64(wire8) != binary.LittleEndian.Uint64(heap8) {
			t.Fatalf("%v: float8 COPY bits %#x, heap bits %#x",
				v, binary.BigEndian.Uint64(wire8), binary.LittleEndian.Uint64(heap8))
		}
	}
}

// NaN / ±Infinity are ordinary IEEE bit patterns to float4send/float8send —
// no text escape hatch — and PGFloatOut round-trips them through the Datum.
func TestCopyBinaryFloatSpecialValues(t *testing.T) {
	for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		d := floatTextDatum(PGFloatOut(v, 64))
		wire, err := datumToCopyBinary(float8Col(), d)
		if err != nil {
			t.Fatalf("float8_send(%v): %v", v, err)
		}
		bits := binary.BigEndian.Uint64(wire)
		got := math.Float64frombits(bits)
		if math.IsNaN(v) {
			if !math.IsNaN(got) {
				t.Fatalf("float8_send(NaN) = %v", got)
			}
			// PG's get_float8_nan() is the canonical quiet NaN; Go's
			// strconv.ParseFloat("NaN") sets the low payload bit, which made
			// goopg's binary COPY stream differ from PG 18.3's in exactly this
			// bit (measured 2026-08-13). Byte identity is the bar, not
			// math.IsNaN — see pgFloatFromDatum.
			if bits != 0x7ff8000000000000 {
				t.Fatalf("float8_send(NaN) bits = %#016x, want PG's %#016x", bits, uint64(0x7ff8000000000000))
			}
			// The heap twin must carry the same canonical pattern.
			heap, err := encodeValuePG(float8Col(), d)
			if err != nil {
				t.Fatalf("encodeValuePG(NaN): %v", err)
			}
			if hb := binary.LittleEndian.Uint64(heap); hb != 0x7ff8000000000000 {
				t.Fatalf("heap NaN bits = %#016x, want %#016x", hb, uint64(0x7ff8000000000000))
			}
			// float4 narrowing must land on PG's 0x7fc00000.
			w4, err := datumToCopyBinary(float4Col(), floatTextDatum(PGFloatOut(v, 32)))
			if err != nil {
				t.Fatalf("float4_send(NaN): %v", err)
			}
			if b4 := binary.BigEndian.Uint32(w4); b4 != 0x7fc00000 {
				t.Fatalf("float4_send(NaN) bits = %#08x, want %#08x", b4, uint32(0x7fc00000))
			}
			continue
		}
		if got != v {
			t.Fatalf("float8_send(%v) = %v", v, got)
		}
	}
}

// float4recv/float8recv are pq_getmsgfloat4/8 and the binary COPY parser runs
// pq_getmsgend after every attribute, so a field of any other length is
// "incorrect binary data format" upstream rather than a truncated value.
func TestCopyBinaryFloatRecvLengthCheck(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 5, 8} {
		if _, err := copyBinaryToDatum(float4Col(), make([]byte, n)); err == nil {
			t.Fatalf("float4_recv accepted a %d-byte field, want an error", n)
		}
	}
	for _, n := range []int{0, 4, 7, 9, 16} {
		if _, err := copyBinaryToDatum(float8Col(), make([]byte, n)); err == nil {
			t.Fatalf("float8_recv accepted a %d-byte field, want an error", n)
		}
	}
}

// The spelled-out SQL names must reach the same arms — goopg stores a column's
// DECLARED type name verbatim, so `real`/`double precision`/`float` are what
// the dispatch actually sees for those DDL spellings.
func TestCopyBinaryFloatSpelledOutTypeNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
	}{
		{"real", 4}, {"float4", 4},
		{"double precision", 8}, {"double", 8}, {"float", 8}, {"float8", 8},
	} {
		col := catalog.Type{Name: tc.name}
		d := floatTextDatum(PGFloatOut(2.5, 64))
		b, err := datumToCopyBinary(col, d)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(b) != tc.width {
			t.Fatalf("%s payload = %d bytes, want %d", tc.name, len(b), tc.width)
		}
		back, err := copyBinaryToDatum(col, b)
		if err != nil {
			t.Fatalf("%s decode: %v", tc.name, err)
		}
		if back.Format() != "2.5" {
			t.Fatalf("%s round-trip = %q, want \"2.5\"", tc.name, back.Format())
		}
	}
}
