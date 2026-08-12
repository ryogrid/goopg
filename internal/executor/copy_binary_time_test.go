package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (51st slice). `copy_binary.go` had NO `time`/`timetz` arm in
// EITHER direction: both types fell through to the raw-bytes default, so
// COPY … WITH (FORMAT binary) shipped Datum.Format()'s TEXT where upstream
// time_send/timetz_send ship an 8-byte TimeADT (plus an int32 zone), and the
// receiving half handed the same bytes back as a plain string Datum. These
// tests fail against that HEAD.
//
// The wire shape is upstream's (postgres/src/backend/utils/adt/date.c); the
// microseconds come from pgTimeMicros, the single hour-24 authority the heap
// encode and the btree key builders already share (Hard-won Rule #2).

func timeCol() catalog.Type   { return catalog.Type{Name: "time"} }
func timetzCol() catalog.Type { return catalog.Type{Name: "timetz"} }

func TestCopyBinaryTimeSendShape(t *testing.T) {
	ts, err := parseTimeString("12:34:56.789012")
	if err != nil {
		t.Fatalf("parseTimeString: %v", err)
	}
	b, err := datumToCopyBinary(timeCol(), NewTimeDatum(ts))
	if err != nil {
		t.Fatalf("datumToCopyBinary(time): %v", err)
	}
	if len(b) != 8 {
		t.Fatalf("time_send payload = %d bytes, want 8 (upstream pq_sendint64)", len(b))
	}
	want := int64(12)*3600*1_000_000 + int64(34)*60*1_000_000 + 56*1_000_000 + 789012
	if got := int64(binary.BigEndian.Uint64(b)); got != want {
		t.Fatalf("time_send micros = %d, want %d", got, want)
	}
}

// The hour-24 carrier is the value the 50th slice made storable; binary COPY
// must carry it too, not the 0 a naive Hour() extraction reports.
func TestCopyBinaryTimeHour24RoundTrip(t *testing.T) {
	d := timeHour24Carrier(t)
	b, err := datumToCopyBinary(timeCol(), d)
	if err != nil {
		t.Fatalf("datumToCopyBinary(24:00:00): %v", err)
	}
	if got := int64(binary.BigEndian.Uint64(b)); got != usecsPerDay {
		t.Fatalf("time_send(24:00:00) = %d, want %d (USECS_PER_DAY)", got, usecsPerDay)
	}
	back, err := copyBinaryToDatum(timeCol(), b)
	if err != nil {
		t.Fatalf("copyBinaryToDatum(24:00:00): %v", err)
	}
	if got := pgTimeMicros(back.TimeValue()); got != usecsPerDay {
		t.Fatalf("round-tripped micros = %d, want %d", got, usecsPerDay)
	}
}

// The binary payload must be byte-identical in VALUE to what the heap stores —
// same micros, only the byte order differs (heap is little-endian, the COPY
// wire is big-endian). A drift here is the encode↔encode sibling defect.
func TestCopyBinaryTimeAgreesWithHeapEncode(t *testing.T) {
	for _, lit := range []string{"00:00:00", "00:00:01", "23:59:59.999999", "24:00:00"} {
		ts, err := parseTimeString(lit)
		if err != nil {
			t.Fatalf("parseTimeString(%s): %v", lit, err)
		}
		d := NewTimeDatum(ts)
		wire, err := datumToCopyBinary(timeCol(), d)
		if err != nil {
			t.Fatalf("%s: datumToCopyBinary: %v", lit, err)
		}
		heap, err := encodeValuePG(timeCol(), d)
		if err != nil {
			t.Fatalf("%s: encodeValuePG: %v", lit, err)
		}
		got := int64(binary.BigEndian.Uint64(wire))
		want := int64(binary.LittleEndian.Uint64(heap))
		if got != want {
			t.Fatalf("%s: COPY binary micros = %d, heap micros = %d", lit, got, want)
		}
	}
}

func TestCopyBinaryTimeTZRoundTrip(t *testing.T) {
	// -07:00 (PDT) — a negative east-of-UTC offset, which is where the two
	// sign conventions (PG stores seconds WEST) would cancel a bug out if the
	// test only used UTC.
	ts, offsetSecs, err := parseTimeTZString("12:34:56-07", "")
	if err != nil {
		t.Fatalf("parseTimeTZString: %v", err)
	}
	if offsetSecs != -7*3600 {
		t.Fatalf("parsed offset = %d s, want %d", offsetSecs, -7*3600)
	}
	d := NewTimeTZDatum(ts, offsetSecs)
	b, err := datumToCopyBinary(timetzCol(), d)
	if err != nil {
		t.Fatalf("datumToCopyBinary(timetz): %v", err)
	}
	if len(b) != 12 {
		t.Fatalf("timetz_send payload = %d bytes, want 12 (int64 time + int32 zone)", len(b))
	}
	if got := int32(binary.BigEndian.Uint32(b[8:])); got != 7*3600 {
		t.Fatalf("timetz_send zone = %d, want %d (PG sign: positive = WEST)", got, 7*3600)
	}
	back, err := copyBinaryToDatum(timetzCol(), b)
	if err != nil {
		t.Fatalf("copyBinaryToDatum(timetz): %v", err)
	}
	if got := back.TimeTZOffsetSecs(); got != offsetSecs {
		t.Fatalf("round-tripped offset = %d s, want %d", got, offsetSecs)
	}
	if got, want := pgTimeMicros(back.TimeValue()), pgTimeMicros(ts); got != want {
		t.Fatalf("round-tripped micros = %d, want %d", got, want)
	}
	if back.TimeSub != TimeSubTimeTZ {
		t.Fatalf("round-tripped TimeSub = %v, want TimeSubTimeTZ (renders as timetz)", back.TimeSub)
	}
}

// time_recv / timetz_recv range-check what arrives on the wire; a foreign or
// corrupt stream must raise, not produce an out-of-range Datum.
func TestCopyBinaryTimeRecvRangeChecks(t *testing.T) {
	overflow := make([]byte, 8)
	binary.BigEndian.PutUint64(overflow, uint64(usecsPerDay+1))
	if _, err := copyBinaryToDatum(timeCol(), overflow); err == nil {
		t.Fatal("time_recv accepted USECS_PER_DAY+1, want 22008 time out of range")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22008" {
		t.Fatalf("time_recv error = %v, want *ExecError 22008", err)
	}

	// Exactly USECS_PER_DAY is IN range — upstream's bound is inclusive.
	atBound := make([]byte, 8)
	binary.BigEndian.PutUint64(atBound, uint64(usecsPerDay))
	if _, err := copyBinaryToDatum(timeCol(), atBound); err != nil {
		t.Fatalf("time_recv rejected USECS_PER_DAY (a real TimeADT): %v", err)
	}

	badZone := make([]byte, 12)
	binary.BigEndian.PutUint64(badZone[:8], 0)
	binary.BigEndian.PutUint32(badZone[8:], uint32(tzDispLimitSecs))
	if _, err := copyBinaryToDatum(timetzCol(), badZone); err == nil {
		t.Fatal("timetz_recv accepted zone == TZDISP_LIMIT, want 22009")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22009" {
		t.Fatalf("timetz_recv error = %v, want *ExecError 22009", err)
	}
}

// The spelled-out SQL names must reach the same arms — they are what
// operators_ddl.go's canonicaliser hands out for these types.
func TestCopyBinaryTimeSpelledOutTypeNames(t *testing.T) {
	ts, err := parseTimeString("01:02:03")
	if err != nil {
		t.Fatalf("parseTimeString: %v", err)
	}
	b, err := datumToCopyBinary(catalog.Type{Name: "time without time zone"}, NewTimeDatum(ts))
	if err != nil {
		t.Fatalf("time without time zone: %v", err)
	}
	if len(b) != 8 {
		t.Fatalf("time without time zone payload = %d bytes, want 8", len(b))
	}
	b, err = datumToCopyBinary(catalog.Type{Name: "time with time zone"}, NewTimeTZDatum(ts, 0))
	if err != nil {
		t.Fatalf("time with time zone: %v", err)
	}
	if len(b) != 12 {
		t.Fatalf("time with time zone payload = %d bytes, want 12", len(b))
	}
}
