package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (62nd slice). A `time(N)`/`timetz(N)` column's precision was
// applied at OUTPUT by TRUNCATING the fractional seconds (three hand-maintained
// copies: the cast path, copyTimeOfDayMicros, appendTimeText). Upstream rounds
// half-away-from-zero at INPUT via AdjustTimeForTypmod (date.c:1710), so
// `'23:59:59.999999'::time(2)` is 24:00:00 on PG 18.3 and was 23:59:59.99 on
// goopg. These tests pin the rounding at every input site and the verbatim
// render.

func timeCol2() catalog.Type   { return catalog.Type{Name: "time", Args: []int64{2}} }
func timetzCol2() catalog.Type { return catalog.Type{Name: "timetz", Args: []int64{2}} }

func TestRoundTimeDatumToPrecision(t *testing.T) {
	// The carry the output truncation could not express: 59.999999 rounds up
	// through the second into 24:00:00 (usecsPerDay).
	ts, err := parseTimeString("23:59:59.999999")
	if err != nil {
		t.Fatalf("parseTimeString: %v", err)
	}
	d := roundTimeDatumToPrecision(NewTimeDatum(ts), 2)
	if got := pgTimeMicros(d.TimeValue()); got != usecsPerDay {
		t.Fatalf("round(23:59:59.999999, 2) = %d, want %d (24:00:00)", got, usecsPerDay)
	}

	// A non-carrying round to the same precision.
	ts, _ = parseTimeString("12:34:56.789012")
	d = roundTimeDatumToPrecision(NewTimeDatum(ts), 2)
	if got := pgTimeMicros(d.TimeValue()); got != 12*3600000000+34*60000000+56*1000000+790000 {
		t.Fatalf("round(12:34:56.789012, 2) = %d, want 12:34:56.79", got)
	}

	// No precision → no-op.
	ts, _ = parseTimeString("12:34:56.789012")
	if got := pgTimeMicros(roundTimeDatumToPrecision(NewTimeDatum(ts), -1).TimeValue()); got != pgTimeMicros(ts) {
		t.Fatalf("round(..., -1) changed the value: %d", got)
	}
}

func TestRoundTimeDatumToPrecisionPreservesTimetzOffset(t *testing.T) {
	ts, offsetSecs, err := parseTimeTZString("12:34:56.789012+05:30", "")
	if err != nil {
		t.Fatalf("parseTimeTZString: %v", err)
	}
	d := roundTimeDatumToPrecision(NewTimeTZDatum(ts, offsetSecs), 2)
	if d.TimeSub != TimeSubTimeTZ {
		t.Fatalf("round dropped the timetz subtype: %v", d.TimeSub)
	}
	if got := d.TimeTZOffsetSecs(); got != offsetSecs {
		t.Fatalf("round moved the offset: %d → %d", offsetSecs, got)
	}
	if got := pgTimeMicros(d.TimeValue()); got != 12*3600000000+34*60000000+56*1000000+790000 {
		t.Fatalf("round(12:34:56.789012, 2) = %d, want 12:34:56.79", got)
	}
}

// COPY TEXT: a `time(2)` column's precision must round the value the same way
// INSERT does (Hard-won Rule #2 — the two readers of one type must agree).
func TestCopyTextToDatumAppliesTimeTypmod(t *testing.T) {
	d, err := copyTextToDatum(timeCol2(), []byte("23:59:59.999999"), "")
	if err != nil {
		t.Fatalf("copyTextToDatum: %v", err)
	}
	if got := pgTimeMicros(d.TimeValue()); got != usecsPerDay {
		t.Fatalf("COPY text time(2) of 23:59:59.999999 = %d, want %d (24:00:00)", got, usecsPerDay)
	}
}

// COPY BINARY: the recv twin must round identically.
func TestCopyBinaryToDatumAppliesTimeTypmod(t *testing.T) {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, uint64(23*3600000000+59*60000000+59999999))
	d, err := copyBinaryToDatum(timeCol2(), payload)
	if err != nil {
		t.Fatalf("copyBinaryToDatum: %v", err)
	}
	if got := pgTimeMicros(d.TimeValue()); got != usecsPerDay {
		t.Fatalf("COPY binary time(2) of 23:59:59.999999 = %d, want %d (24:00:00)", got, usecsPerDay)
	}
}

// COPY TEXT of a `timetz(2)` column rounds the wall clock and keeps the zone.
func TestCopyTextToDatumAppliesTimetzTypmod(t *testing.T) {
	d, err := copyTextToDatum(timetzCol2(), []byte("12:34:56.789012+05:30"), "")
	if err != nil {
		t.Fatalf("copyTextToDatum: %v", err)
	}
	if d.TimeSub != TimeSubTimeTZ {
		t.Fatalf("copyTextToDatum lost the timetz subtype: %v", d.TimeSub)
	}
	if got := pgTimeMicros(d.TimeValue()); got != 12*3600000000+34*60000000+56*1000000+790000 {
		t.Fatalf("COPY text timetz(2) of 12:34:56.789012 = %d, want 12:34:56.79", got)
	}
}

// INSERT: coerceRowForConstraintChecks is the single coercion point every new
// row passes through; a `time(2)` column's value must be rounded there.
func TestCoerceRowForConstraintChecksAppliesTimeTypmod(t *testing.T) {
	cols := []catalog.Column{{Name: "t", Type: timeCol2()}}
	row := Row{NewStringDatum("23:59:59.999999")}
	if err := coerceRowForConstraintChecks(cols, row, func(int) bool { return true }, nil, 0); err != nil {
		t.Fatalf("coerceRowForConstraintChecks: %v", err)
	}
	if row[0].Kind != KindTime {
		t.Fatalf("row[0] kind = %d, want KindTime (coerced)", row[0].Kind)
	}
	if got := pgTimeMicros(row[0].TimeValue()); got != usecsPerDay {
		t.Fatalf("INSERT time(2) of 23:59:59.999999 = %d, want %d (24:00:00)", got, usecsPerDay)
	}
}

// The storage choke point applies the typmod too, so a value that bypasses
// input coercion — a DEFAULT or generated column, skipped by
// coerceRowForConstraintChecks's !insertMissing filter — is still rounded
// before it reaches the heap. Rounding is idempotent, so a value already
// rounded at input is untouched.
func TestEncodeValuePGAppliesTimeTypmod(t *testing.T) {
	b, err := encodeValuePG(timeCol2(), NewStringDatum("23:59:59.999999"))
	if err != nil {
		t.Fatalf("encodeValuePG: %v", err)
	}
	if got := int64(binary.LittleEndian.Uint64(b)); got != usecsPerDay {
		t.Fatalf("encodeValuePG time(2) of 23:59:59.999999 = %d, want %d (24:00:00)", got, usecsPerDay)
	}
}
