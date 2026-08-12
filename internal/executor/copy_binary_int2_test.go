package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (52nd slice). `copy_binary.go` had NO `int2` arm in EITHER
// direction: a smallint column fell through to the default, whose KindInt
// escape emits EIGHT big-endian bytes where upstream int2send
// (postgres/src/backend/utils/adt/int.c:98, pq_sendint16) emits exactly two,
// and whose decode handed the payload back as a string Datum. These tests fail
// against that HEAD.

func int2Col() catalog.Type { return catalog.Type{Name: "int2"} }

func TestCopyBinaryInt2SendShape(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 32767, -32768, 1234} {
		b, err := datumToCopyBinary(int2Col(), Datum{Kind: KindInt, Int: v})
		if err != nil {
			t.Fatalf("datumToCopyBinary(int2, %d): %v", v, err)
		}
		if len(b) != 2 {
			t.Fatalf("int2_send(%d) payload = %d bytes, want 2 (upstream pq_sendint16)", v, len(b))
		}
		if got := int64(int16(binary.BigEndian.Uint16(b))); got != v {
			t.Fatalf("int2_send(%d) decoded = %d", v, got)
		}
	}
}

func TestCopyBinaryInt2RoundTrip(t *testing.T) {
	for _, v := range []int64{0, -1, 32767, -32768, -12345} {
		b, err := datumToCopyBinary(int2Col(), Datum{Kind: KindInt, Int: v})
		if err != nil {
			t.Fatalf("datumToCopyBinary(%d): %v", v, err)
		}
		back, err := copyBinaryToDatum(int2Col(), b)
		if err != nil {
			t.Fatalf("copyBinaryToDatum(%d): %v", v, err)
		}
		// The decode twin must produce an INT Datum, not the string the
		// raw-bytes default used to hand back (Hard-won Rule #2).
		if back.Kind != KindInt {
			t.Fatalf("round-tripped %d as kind %d, want KindInt", v, back.Kind)
		}
		if back.Int != v {
			t.Fatalf("round-tripped %d as %d", v, back.Int)
		}
	}
}

// Same VALUE as the heap stores — only the byte order differs (heap is
// little-endian, the COPY wire big-endian). The encode↔encode sibling pin.
func TestCopyBinaryInt2AgreesWithHeapEncode(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 32767, -32768} {
		d := Datum{Kind: KindInt, Int: v}
		wire, err := datumToCopyBinary(int2Col(), d)
		if err != nil {
			t.Fatalf("%d: datumToCopyBinary: %v", v, err)
		}
		heap, err := encodeValuePG(int2Col(), d)
		if err != nil {
			t.Fatalf("%d: encodeValuePG: %v", v, err)
		}
		if len(heap) != len(wire) {
			t.Fatalf("%d: heap width %d vs COPY width %d", v, len(heap), len(wire))
		}
		got := int16(binary.BigEndian.Uint16(wire))
		want := int16(binary.LittleEndian.Uint16(heap))
		if got != want {
			t.Fatalf("%d: COPY binary value = %d, heap value = %d", v, got, want)
		}
	}
}

// int2recv is pq_getmsgint(buf, sizeof(int16)) and the binary COPY parser runs
// pq_getmsgend after every attribute, so a field that is not exactly two bytes
// is "incorrect binary data format" upstream rather than a truncated value.
func TestCopyBinaryInt2RecvLengthCheck(t *testing.T) {
	for _, n := range []int{0, 1, 3, 4, 8} {
		if _, err := copyBinaryToDatum(int2Col(), make([]byte, n)); err == nil {
			t.Fatalf("int2_recv accepted a %d-byte field, want an error", n)
		}
	}
}

// PG cannot hold an out-of-range int2 Datum; if goopg reaches the encoder with
// one it is goopg-side corruption, and 22003 (the heap arm's own code) is the
// PG-compatible answer rather than a silent 16-bit wrap.
func TestCopyBinaryInt2SendRangeCheck(t *testing.T) {
	for _, v := range []int64{32768, -32769, 100000} {
		_, err := datumToCopyBinary(int2Col(), Datum{Kind: KindInt, Int: v})
		if err == nil {
			t.Fatalf("int2_send accepted %d, want 22003", v)
		}
		if ee, ok := err.(*ExecError); !ok || ee.Code != "22003" {
			t.Fatalf("int2_send(%d) error = %v, want *ExecError 22003", v, err)
		}
	}
}

// The spelled-out SQL name must reach the same arms — it is what the DDL
// canonicaliser hands out for this type.
func TestCopyBinaryInt2SpelledOutTypeName(t *testing.T) {
	b, err := datumToCopyBinary(catalog.Type{Name: "smallint"}, Datum{Kind: KindInt, Int: 7})
	if err != nil {
		t.Fatalf("smallint: %v", err)
	}
	if len(b) != 2 {
		t.Fatalf("smallint payload = %d bytes, want 2", len(b))
	}
	back, err := copyBinaryToDatum(catalog.Type{Name: "smallint"}, b)
	if err != nil {
		t.Fatalf("smallint decode: %v", err)
	}
	if back.Kind != KindInt || back.Int != 7 {
		t.Fatalf("smallint round-trip = %+v, want KindInt 7", back)
	}
}
