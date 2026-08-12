package executor

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (55th slice). `copy_binary.go` had NO arm for `interval` in either
// direction, so both halves fell through to the default:
//
//	encode: a KindInterval Datum matches none of the default's Kind cases, so it
//	        shipped Datum.Format()'s TEXT ("02:00:00", "1 mon") under FORMAT
//	        binary. Upstream interval_send
//	        (postgres/src/backend/utils/adt/timestamp.c:1022) is
//	        pq_sendint64(time) + pq_sendint32(day) + pq_sendint32(month) — a
//	        fixed 16 bytes — so the stream is one no real PG client can read
//	        (CopyReadBinaryAttribute's pq_getmsgend, copyfromparse.c).
//	decode: the 16 bytes came back as a raw-bytes STRING Datum, which put an
//	        interval column right back in the lexicographic world the heap
//	        codec's own "interval" arm was written to escape — '2 hours' sorting
//	        after '10 days', `i = interval '30 days'` missing the '1 mon' PG
//	        calls equal.
//
// These tests fail against that HEAD.

// The wire shape upstream sends: 16 bytes, {time int64, day int32, month int32},
// big-endian, in THAT field order (which is not goopg's usual months-first
// spelling — getting it backwards is the obvious way to be silently wrong).
func TestCopyBinaryIntervalSendShape(t *testing.T) {
	col := catalog.Type{Name: "interval"}
	// 1 month, 2 days, 3 hours.
	d := NewIntervalDatumFull(1, 2, 3*3600*1_000_000)
	b, err := datumToCopyBinary(col, d)
	if err != nil {
		t.Fatalf("datumToCopyBinary: %v", err)
	}
	if len(b) != 16 {
		t.Fatalf("interval_send payload = %d bytes, want 16", len(b))
	}
	if got := int64(binary.BigEndian.Uint64(b[:8])); got != 3*3600*1_000_000 {
		t.Fatalf("interval_send time = %d micros, want %d", got, 3*3600*1_000_000)
	}
	if got := int32(binary.BigEndian.Uint32(b[8:12])); got != 2 {
		t.Fatalf("interval_send day = %d, want 2", got)
	}
	if got := int32(binary.BigEndian.Uint32(b[12:16])); got != 1 {
		t.Fatalf("interval_send month = %d, want 1", got)
	}
}

// Hard-won Rule #2: the decode twin must reproduce the Datum the HEAP decode
// produces for the same value. Comparing against the heap decoder's own output
// is the pin — a string Datum holding 16 raw bytes formats as garbage and fails
// here, where a length-only check would pass.
func TestCopyBinaryIntervalRoundTripMatchesHeapDatum(t *testing.T) {
	col := catalog.Type{Name: "interval"}
	samples := []Datum{
		NewIntervalDatumFull(0, 0, 0),
		NewIntervalDatumFull(1, 0, 0),                 // '1 mon'
		NewIntervalDatumFull(0, 0, 2*3600*1_000_000),  // '02:00:00'
		NewIntervalDatumFull(0, 30, 0),                // '30 days'
		NewIntervalDatumFull(-13, -5, -1),             // every field negative
		NewIntervalDatumFull(14, 3, 45*60*1_000_000),  // mixed, > 1 year
		NewIntervalDatumFull(0, 0, 123456),            // sub-second
		NewIntervalInfinity(true),                     // INTERVAL_NOEND
		NewIntervalInfinity(false),                    // INTERVAL_NOBEGIN
	}
	for _, d := range samples {
		wire, err := datumToCopyBinary(col, d)
		if err != nil {
			t.Fatalf("%q: datumToCopyBinary: %v", d.Format(), err)
		}
		back, err := copyBinaryToDatum(col, wire)
		if err != nil {
			t.Fatalf("%q: copyBinaryToDatum: %v", d.Format(), err)
		}

		heap, err := encodeValuePG(col, d)
		if err != nil {
			t.Fatalf("%q: encodeValuePG: %v", d.Format(), err)
		}
		heapBack, _, err := decodePhysicalPGValueMctx(col, heap, nil)
		if err != nil {
			t.Fatalf("%q: decodePhysicalPGValueMctx: %v", d.Format(), err)
		}
		if back.Kind != heapBack.Kind || back.Format() != heapBack.Format() {
			t.Fatalf("%q: COPY decode = kind %d %q, heap decode = kind %d %q",
				d.Format(), back.Kind, back.Format(), heapBack.Kind, heapBack.Format())
		}
		if back.Kind != KindInterval {
			t.Fatalf("%q: COPY decode kind = %d, want KindInterval", d.Format(), back.Kind)
		}
		if back.IntervalMonthsValue() != d.IntervalMonthsValue() ||
			back.IntervalDaysValue() != d.IntervalDaysValue() ||
			back.IntervalMicrosValue() != d.IntervalMicrosValue() {
			t.Fatalf("interval round-trip {%d,%d,%d} -> {%d,%d,%d}",
				d.IntervalMonthsValue(), d.IntervalDaysValue(), d.IntervalMicrosValue(),
				back.IntervalMonthsValue(), back.IntervalDaysValue(), back.IntervalMicrosValue())
		}
	}
}

// Same VALUE and same WIDTH as the heap stores — only the byte order differs
// (heap little-endian at the same {time,day,month} offsets, the COPY wire
// big-endian). This is the pin the 53rd and 54th slices found their adjacent
// heap defects with (the float spelling bug; the halved xid8), so it runs for
// every new arm.
func TestCopyBinaryIntervalAgreesWithHeapEncode(t *testing.T) {
	col := catalog.Type{Name: "interval"}
	for _, d := range []Datum{
		NewIntervalDatumFull(0x1234, 0x5678, 0x0123456789ab),
		NewIntervalDatumFull(-1, -2, -3),
		NewIntervalInfinity(true),
		NewIntervalInfinity(false),
	} {
		wire, err := datumToCopyBinary(col, d)
		if err != nil {
			t.Fatalf("datumToCopyBinary: %v", err)
		}
		heap, err := encodeValuePG(col, d)
		if err != nil {
			t.Fatalf("encodeValuePG: %v", err)
		}
		if len(heap) != 16 || len(wire) != 16 {
			t.Fatalf("heap width %d vs COPY width %d, want 16 (pg_type 1186 typlen)",
				len(heap), len(wire))
		}
		if binary.BigEndian.Uint64(wire[:8]) != binary.LittleEndian.Uint64(heap[:8]) {
			t.Fatalf("time: COPY %#x, heap %#x",
				binary.BigEndian.Uint64(wire[:8]), binary.LittleEndian.Uint64(heap[:8]))
		}
		if binary.BigEndian.Uint32(wire[8:12]) != binary.LittleEndian.Uint32(heap[8:12]) {
			t.Fatalf("day: COPY %#x, heap %#x",
				binary.BigEndian.Uint32(wire[8:12]), binary.LittleEndian.Uint32(heap[8:12]))
		}
		if binary.BigEndian.Uint32(wire[12:16]) != binary.LittleEndian.Uint32(heap[12:16]) {
			t.Fatalf("month: COPY %#x, heap %#x",
				binary.BigEndian.Uint32(wire[12:16]), binary.LittleEndian.Uint32(heap[12:16]))
		}
	}
	// physicalPGTypeAlign must agree with pg_type 1186's typalign 'd' — the
	// field the halved-xid8 finding (54th slice) reached through this same pin.
	if got := physicalPGTypeAlign(col); got != 8 {
		t.Fatalf("physicalPGTypeAlign(interval) = %d, want 8 (typalign 'd')", got)
	}
}

// interval_recv reads a FIXED 16 bytes and the binary COPY parser runs
// pq_getmsgend after every attribute, so a field of any other length is an
// error rather than a truncated or zero-extended value.
func TestCopyBinaryIntervalRecvLengthCheck(t *testing.T) {
	col := catalog.Type{Name: "interval"}
	for _, n := range []int{0, 1, 4, 8, 12, 15, 17, 24} {
		if _, err := copyBinaryToDatum(col, make([]byte, n)); err == nil {
			t.Fatalf("interval_recv accepted a %d-byte field (want 16)", n)
		}
	}
}

// The KindString entry point (a bare quoted literal, `unknown` upstream, which
// reaches an interval column through interval_in) must produce the same wire
// bytes as the equivalent KindInterval Datum, and invalid text must be 22007 —
// the same code and the same tokenizer the heap encode arm applies. Both halves
// now share pgIntervalFieldsFromDatum, which is what makes that guaranteed
// rather than coincidental.
func TestCopyBinaryIntervalAcceptsUnknownLiteralAndRejectsGarbage(t *testing.T) {
	col := catalog.Type{Name: "interval"}
	fromText, err := datumToCopyBinary(col, NewStringDatum("1 mon 2 days 03:00:00"))
	if err != nil {
		t.Fatalf("datumToCopyBinary(text): %v", err)
	}
	fromDatum, err := datumToCopyBinary(col, NewIntervalDatumFull(1, 2, 3*3600*1_000_000))
	if err != nil {
		t.Fatalf("datumToCopyBinary(interval): %v", err)
	}
	if string(fromText) != string(fromDatum) {
		t.Fatalf("text entry %x != interval entry %x", fromText, fromDatum)
	}
	for _, s := range []string{"not an interval", "1 zorkmid"} {
		_, err := datumToCopyBinary(col, NewStringDatum(s))
		if err == nil {
			t.Fatalf("interval_send accepted %q", s)
		}
		if ee, ok := err.(*ExecError); !ok || ee.Code != "22007" {
			t.Fatalf("interval_send(%q) err = %v, want 22007", s, err)
		}
	}
}

// A full binary COPY row through the public writer/parser, not just the
// per-attribute helpers: the framing must carry the 16-byte length and the row
// must come back as an interval, since that is the path a real client drives.
func TestCopyBinaryIntervalRowFraming(t *testing.T) {
	cols := []catalog.Column{{Name: "i", Type: catalog.Type{Name: "interval"}}}
	in := Row{NewIntervalDatumFull(3, 4, math.MaxInt32)}

	buf, err := AppendCopyBinaryRow(nil, in, cols)
	if err != nil {
		t.Fatalf("AppendCopyBinaryRow: %v", err)
	}
	if got := int32(binary.BigEndian.Uint32(buf[2:6])); got != 16 {
		t.Fatalf("field length header = %d, want 16", got)
	}
	buf = AppendCopyBinaryTrailer(buf)

	rows, trailer, _, err := ParseCopyBinaryRows(buf, cols)
	if err != nil {
		t.Fatalf("ParseCopyBinaryRows: %v", err)
	}
	if !trailer || len(rows) != 1 {
		t.Fatalf("parsed %d rows, trailer=%v", len(rows), trailer)
	}
	if rows[0][0].Kind != KindInterval || rows[0][0].Format() != in[0].Format() {
		t.Fatalf("round-trip = kind %d %q, want kind %d %q",
			rows[0][0].Kind, rows[0][0].Format(), in[0].Kind, in[0].Format())
	}
}
