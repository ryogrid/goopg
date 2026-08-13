package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (54th slice). `copy_binary.go` had NO arm for PG's unsigned
// identifier family — oid/regproc/xid/xid8 — nor for uuid, in EITHER direction.
// Three of the five were WRONG on the wire:
//
//	oid, regproc, xid : the default's KindInt escape shipped EIGHT big-endian
//	                    bytes where oidsend/regprocsend/xidsend are pq_sendint32
//	                    (postgres/src/backend/utils/adt/oid.c:71, regproc.c:208,
//	                    xid.c:67) — four.
//	uuid              : the default shipped the 36-character canonical TEXT
//	                    where uuid_send (uuid.c:192) is pq_sendbytes(…, 16).
//	xid8              : accidentally the right eight bytes (xid8send, xid.c:225)
//	                    — and the divergence surfaced on the HEAP side instead.
//
// The decode half was worse still: every one of them came back from the default
// as a raw-bytes STRING Datum, so a value that entered through binary COPY did
// not compare, index or render like the same value entering through INSERT.
// These tests fail against that HEAD.

var oidFamilyCols = []struct {
	name  string
	width int
}{
	{"oid", 4}, {"regproc", 4}, {"xid", 4}, {"xid8", 8},
}

// Upstream send widths. A binary COPY field of any other length is "incorrect
// binary data format" to a real client: CopyReadBinaryAttribute runs
// pq_getmsgend after each attribute (postgres/src/backend/commands/
// copyfromparse.c).
func TestCopyBinaryOidFamilySendShape(t *testing.T) {
	for _, tc := range oidFamilyCols {
		col := catalog.Type{Name: tc.name}
		b, err := datumToCopyBinary(col, NewIntDatum(2427))
		if err != nil {
			t.Fatalf("%s: datumToCopyBinary: %v", tc.name, err)
		}
		if len(b) != tc.width {
			t.Fatalf("%s_send payload = %d bytes, want %d", tc.name, len(b), tc.width)
		}
		var got uint64
		if tc.width == 4 {
			got = uint64(binary.BigEndian.Uint32(b))
		} else {
			got = binary.BigEndian.Uint64(b)
		}
		if got != 2427 {
			t.Fatalf("%s_send(2427) = %d", tc.name, got)
		}
	}
	u, err := datumToCopyBinary(catalog.Type{Name: "uuid"},
		NewStringDatum("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"))
	if err != nil {
		t.Fatalf("uuid_send: %v", err)
	}
	if len(u) != 16 {
		t.Fatalf("uuid_send payload = %d bytes, want 16 (pq_sendbytes UUID_LEN)", len(u))
	}
	if u[0] != 0xa0 || u[15] != 0x11 {
		t.Fatalf("uuid_send bytes = %x", u)
	}
}

// The decode twin must reproduce the Datum the HEAP decode produces for the
// same value — an oid must come back KindInt, not the raw-bytes string the
// default handed over (Hard-won Rule #2). Comparing against the heap decoder's
// own output is the pin.
func TestCopyBinaryOidFamilyRoundTripMatchesHeapDatum(t *testing.T) {
	type sample struct {
		typ string
		d   Datum
	}
	samples := []sample{
		{"oid", NewIntDatum(0)},
		{"oid", NewIntDatum(16384)},
		// The whole point of the family being UNSIGNED: 4294967295 is a legal
		// oid and must survive as a positive value, not as -1.
		{"oid", NewIntDatum(4294967295)},
		{"regproc", NewIntDatum(2427)},
		{"xid", NewIntDatum(3)},
		{"xid", NewIntDatum(4294967295)},
		{"xid8", NewIntDatum(1)},
		// Above 2^32 — the value the heap silently truncated before this slice.
		{"xid8", NewIntDatum(1234567890123)},
		{"uuid", NewStringDatum("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")},
		{"uuid", NewStringDatum("00000000-0000-0000-0000-000000000000")},
	}
	for _, s := range samples {
		col := catalog.Type{Name: s.typ}

		wire, err := datumToCopyBinary(col, s.d)
		if err != nil {
			t.Fatalf("%s %v: datumToCopyBinary: %v", s.typ, s.d.Format(), err)
		}
		back, err := copyBinaryToDatum(col, wire)
		if err != nil {
			t.Fatalf("%s %v: copyBinaryToDatum: %v", s.typ, s.d.Format(), err)
		}

		heap, err := encodeValuePG(col, s.d)
		if err != nil {
			t.Fatalf("%s %v: encodeValuePG: %v", s.typ, s.d.Format(), err)
		}
		heapBack, _, err := decodePhysicalPGValueMctx(col, heap, nil)
		if err != nil {
			t.Fatalf("%s %v: decodePhysicalPGValueMctx: %v", s.typ, s.d.Format(), err)
		}
		if back.Kind != heapBack.Kind || back.Format() != heapBack.Format() {
			t.Fatalf("%s %v: COPY decode = kind %d %q, heap decode = kind %d %q",
				s.typ, s.d.Format(), back.Kind, back.Format(), heapBack.Kind, heapBack.Format())
		}
		if back.Format() != s.d.Format() {
			t.Fatalf("%s: round-trip %q -> %q", s.typ, s.d.Format(), back.Format())
		}
	}
}

// Same VALUE and same WIDTH as the heap stores — only the byte order differs
// (heap little-endian, the COPY wire big-endian). uuid is not endian-swapped at
// all: pg_uuid_t is a byte array, so the two images are literally equal.
func TestCopyBinaryOidFamilyAgreesWithHeapEncode(t *testing.T) {
	for _, tc := range oidFamilyCols {
		col := catalog.Type{Name: tc.name}
		d := NewIntDatum(305419896) // 0x12345678
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
		if tc.width == 4 {
			if binary.BigEndian.Uint32(wire) != binary.LittleEndian.Uint32(heap) {
				t.Fatalf("%s: COPY %#x, heap %#x",
					tc.name, binary.BigEndian.Uint32(wire), binary.LittleEndian.Uint32(heap))
			}
		} else if binary.BigEndian.Uint64(wire) != binary.LittleEndian.Uint64(heap) {
			t.Fatalf("%s: COPY %#x, heap %#x",
				tc.name, binary.BigEndian.Uint64(wire), binary.LittleEndian.Uint64(heap))
		}
	}

	uuidCol := catalog.Type{Name: "uuid"}
	d := NewStringDatum("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")
	wire, err := datumToCopyBinary(uuidCol, d)
	if err != nil {
		t.Fatalf("uuid: datumToCopyBinary: %v", err)
	}
	heap, err := encodeValuePG(uuidCol, d)
	if err != nil {
		t.Fatalf("uuid: encodeValuePG: %v", err)
	}
	if string(wire) != string(heap) {
		t.Fatalf("uuid: COPY %x != heap %x (pg_uuid_t is a byte array, no swap)", wire, heap)
	}
}

// xid8 is pg_type OID 5069: typlen **8**, typalign 'd'. It shared the 4-byte
// `xid` arm in encodeValuePG and truncated a FullTransactionId to its low 32
// bits — silent data loss on the heap, and a 4-byte-short offset for every
// column after it when a hosted PG deforms the tuple with its own descriptor.
func TestHeapXid8IsEightBytesAtEightByteAlignment(t *testing.T) {
	col := catalog.Type{Name: "xid8"}
	const v = int64(1) << 40 // needs more than 32 bits
	heap, err := encodeValuePG(col, NewIntDatum(v))
	if err != nil {
		t.Fatalf("encodeValuePG(xid8): %v", err)
	}
	if len(heap) != 8 {
		t.Fatalf("heap xid8 width = %d bytes, want 8 (pg_type 5069 typlen)", len(heap))
	}
	back, n, err := decodePhysicalPGValueMctx(col, heap, nil)
	if err != nil {
		t.Fatalf("decodePhysicalPGValueMctx(xid8): %v", err)
	}
	if n != 8 || back.Kind != KindInt || back.Int != v {
		t.Fatalf("xid8 round-trip = kind %d %v after %d bytes, want %d after 8", back.Kind, back.Int, n, v)
	}
	if got := physicalPGTypeAlign(col); got != 8 {
		t.Fatalf("physicalPGTypeAlign(xid8) = %d, want 8 (typalign 'd')", got)
	}
	// `xid` (pg_type OID 28) is the 4-byte, 'i'-aligned sibling and must NOT
	// have moved with it.
	if got := physicalPGTypeAlign(catalog.Type{Name: "xid"}); got != 4 {
		t.Fatalf("physicalPGTypeAlign(xid) = %d, want 4 (typalign 'i')", got)
	}
}

// oid/regproc/xid are unsigned 32-bit and xid8 unsigned 64-bit. Upstream's
// uint32in_subr / uint64in_subr (postgres/src/backend/utils/adt/numutils.c,
// reached from oid.c:41) wrap a leading '-' through strtoul/strtoull and admit
// the result after signed OR unsigned extension, so a negative value whose
// magnitude fits the signed width is ACCEPTED and stores its two's-complement
// image ("-1040" → 4294966256). Only magnitude overflow — positive above the
// unsigned max, or negative below the signed min — is 22003. The check lives in
// the SHARED coercion so the heap and the COPY wire cannot disagree about which
// values exist.
//
// M-NIGHTLY AI-20260814-011711-002: the prior reading ("raise 22003 rather than
// wrapping") was wrong for a leading sign, and this test pinned it — the
// regress oid case (INSERT '-1040' expects 4294966256) caught the divergence.
func TestOidFamilyRangeWrapNegativesRejectMagnitudeOverflow(t *testing.T) {
	accepted := []struct {
		typ  string
		val  int64
		want uint64
	}{
		{"oid", -1, 4294967295},
		{"oid", -1040, 4294966256},
		{"regproc", -5, 4294967291},
		{"xid", -2147483648, 2147483648}, // MinInt32 wraps to 2^31
		{"xid8", -1, 18446744073709551615},
	}
	for _, tc := range accepted {
		col := catalog.Type{Name: tc.typ}
		if _, err := encodeValuePG(col, NewIntDatum(tc.val)); err != nil {
			t.Fatalf("encodeValuePG(%s, %d) rejected an in-range value: %v", tc.typ, tc.val, err)
		}
		if _, err := datumToCopyBinary(col, NewIntDatum(tc.val)); err != nil {
			t.Fatalf("datumToCopyBinary(%s, %d) rejected an in-range value: %v", tc.typ, tc.val, err)
		}
	}
	rejected := []struct {
		typ string
		val int64
	}{
		{"oid", 4294967296},  // 2^32, positive overflow
		{"oid", -2147483649}, // below MinInt32, negative overflow
		{"regproc", 4294967296},
		{"xid", 4294967296},
		{"xid", -2147483649},
	}
	for _, tc := range rejected {
		col := catalog.Type{Name: tc.typ}
		if _, err := encodeValuePG(col, NewIntDatum(tc.val)); err == nil {
			t.Fatalf("encodeValuePG(%s, %d) accepted an out-of-range value", tc.typ, tc.val)
		}
		if _, err := datumToCopyBinary(col, NewIntDatum(tc.val)); err == nil {
			t.Fatalf("datumToCopyBinary(%s, %d) accepted an out-of-range value", tc.typ, tc.val)
		}
	}
}

// oidrecv/xidrecv/xid8recv/uuid_recv all read a FIXED number of bytes and the
// binary COPY parser runs pq_getmsgend after every attribute, so a field of any
// other length is an error rather than a truncated or zero-extended value.
func TestCopyBinaryOidFamilyRecvLengthCheck(t *testing.T) {
	for _, tc := range append(oidFamilyCols, struct {
		name  string
		width int
	}{"uuid", 16}) {
		col := catalog.Type{Name: tc.name}
		for _, n := range []int{0, 1, 2, 3, 5, 7, 8, 12, 15, 16, 17} {
			if n == tc.width {
				continue
			}
			if _, err := copyBinaryToDatum(col, make([]byte, n)); err == nil {
				t.Fatalf("%s_recv accepted a %d-byte field (want %d)", tc.name, n, tc.width)
			}
		}
	}
}

// A uuid Datum that is not a valid UUID must be 22P02, the same code and the
// same isValidUUIDStr gate the heap encode arm applies — not a short or padded
// 16-byte field silently written to the wire.
func TestCopyBinaryUUIDRejectsInvalidText(t *testing.T) {
	col := catalog.Type{Name: "uuid"}
	for _, s := range []string{"", "not-a-uuid", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a1"} {
		if _, err := datumToCopyBinary(col, NewStringDatum(s)); err == nil {
			t.Fatalf("uuid_send accepted %q", s)
		}
	}
	// The no-hyphen spelling is accepted by uuid_in and normalised, exactly as
	// the heap arm does.
	b, err := datumToCopyBinary(col, NewStringDatum("a0eebc999c0b4ef8bb6d6bb9bd380a11"))
	if err != nil {
		t.Fatalf("uuid_send(no-hyphen): %v", err)
	}
	if len(b) != 16 {
		t.Fatalf("uuid_send(no-hyphen) = %d bytes", len(b))
	}
}
