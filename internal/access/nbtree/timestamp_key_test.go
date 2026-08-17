package nbtree

import (
	"bytes"
	"testing"
	"time"
)

// pgEpochForTest is 2000-01-01 00:00:00 UTC — the PostgreSQL epoch.
var pgEpochForTest = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func dateMicros(year, month, day int) int64 {
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return t.Sub(pgEpochForTest).Microseconds()
}

// TestEncodeTimestampChronologicalOrder verifies that chronologically
// ordered timestamps produce bytewise-increasing encoded keys — the
// fundamental B-tree ordering contract.
func TestEncodeTimestampChronologicalOrder(t *testing.T) {
	dates := []struct {
		y, m, d int
	}{
		{1992, 1, 1},
		{1994, 12, 31},
		{1995, 1, 1},
		{1995, 9, 15},
		{1997, 6, 30},
		{1998, 12, 31},
		{2000, 1, 1},  // epoch itself
		{2000, 1, 2},  // 1 day after epoch
		{2025, 5, 4},
	}
	encoded := make([][]byte, len(dates))
	for i, d := range dates {
		encoded[i] = EncodeTimestamp(dateMicros(d.y, d.m, d.d))
	}
	for i := 0; i < len(encoded)-1; i++ {
		if bytes.Compare(encoded[i], encoded[i+1]) >= 0 {
			t.Fatalf("chronological order violated at index %d: %v >= %v",
				i, encoded[i], encoded[i+1])
		}
	}
}

// TestEncodeTimestampEpochBoundary specifically verifies the epoch
// crossing: a pre-epoch timestamp (negative micros) must sort before
// a post-epoch timestamp (positive micros), and the epoch itself must
// fall between them.
func TestEncodeTimestampEpochBoundary(t *testing.T) {
	beforeEpoch := EncodeTimestamp(dateMicros(1999, 12, 31))
	epoch := EncodeTimestamp(0)
	afterEpoch := EncodeTimestamp(dateMicros(2000, 1, 2))

	if bytes.Compare(beforeEpoch, epoch) >= 0 {
		t.Fatal("pre-epoch should sort before epoch")
	}
	if bytes.Compare(epoch, afterEpoch) >= 0 {
		t.Fatal("epoch should sort before post-epoch")
	}
}

// TestEncodeTimestampFixedLength verifies the encoding is always 8
// bytes, matching EncodeInt8's fixed-length contract (important for
// composite key unambiguity without a terminator byte).
func TestEncodeTimestampFixedLength(t *testing.T) {
	for _, micros := range []int64{
		dateMicros(1992, 1, 1),
		0,
		dateMicros(2025, 5, 4),
		-1,
		1 << 62,
	} {
		enc := EncodeTimestamp(micros)
		if len(enc) != 8 {
			t.Fatalf("EncodeTimestamp(%d): want 8 bytes, got %d", micros, len(enc))
		}
	}
}

// TestEncodeTimestampEquality verifies that two timestamps representing
// the same moment produce identical keys.
func TestEncodeTimestampEquality(t *testing.T) {
	micros := dateMicros(1995, 1, 1)
	a := EncodeTimestamp(micros)
	b := EncodeTimestamp(micros)
	if !bytes.Equal(a, b) {
		t.Fatalf("same timestamp encodes differently: %v vs %v", a, b)
	}
}

// TestEncodeTimestampTpchShapes covers the exact date range used by
// TPC-H Q1/Q3/Q6: queries that filter lineitem rows by l_shipdate.
func TestEncodeTimestampTpchShapes(t *testing.T) {
	// Q6 range: [1994-01-01, 1995-01-01)
	lo := EncodeTimestamp(dateMicros(1994, 1, 1))
	hi := EncodeTimestamp(dateMicros(1994, 12, 31))
	if bytes.Compare(lo, hi) >= 0 {
		t.Fatal("Q6 range lo should < hi")
	}

	// Q1 filter: l_shipdate <= '1998-09-02'
	tpchCutoff := EncodeTimestamp(dateMicros(1998, 9, 2))
	earlier := EncodeTimestamp(dateMicros(1995, 1, 15))
	if bytes.Compare(earlier, tpchCutoff) >= 0 {
		t.Fatal("earlier date should sort before Q1 cutoff")
	}
}
