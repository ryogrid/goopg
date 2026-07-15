package wal

import (
	"bytes"
	"testing"
)

// TestPredictXLogRecordLenMatchesEncodeRecordXLog is the keystone test: it
// pins agreement between the pure predictor and the actual encoder across a
// matrix of payload shapes. The two share zero implementation surface so
// byte-for-byte agreement detects drift in either direction.
func TestPredictXLogRecordLenMatchesEncodeRecordXLog(t *testing.T) {
	// Payload-length boundary cases:
	//   - 0xFF / 0x100: short→long block-ID wrapping switchover
	//   - small odd lengths to exercise MAXALIGN padding (4-byte aligned
	//     records become 8-byte aligned via maxAlignXLog)
	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", []byte{}},
		{"one_byte", []byte{0x42}},
		{"three_bytes", []byte{1, 2, 3}},
		{"seven_bytes", []byte{1, 2, 3, 4, 5, 6, 7}},
		{"eight_bytes", []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		{"sixteen_bytes", bytes.Repeat([]byte{0xAB}, 16)},
		{"short_max_0xFF", bytes.Repeat([]byte{0xCD}, 0xFF)},
		{"long_min_0x100", bytes.Repeat([]byte{0xCD}, 0x100)},
		{"long_512", bytes.Repeat([]byte{0xCD}, 512)},
		{"long_8192", bytes.Repeat([]byte{0xCD}, 8192)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			predictedReal, predictedPadded := predictXLogRecordLen(tc.payload)
			encoded, actualReal, err := encodeRecordXLog(tc.payload, 0)
			if err != nil {
				t.Fatalf("encodeRecordXLog failed: %v", err)
			}
			if predictedReal != actualReal {
				t.Errorf("realRecLen: predicted=%d actual=%d", predictedReal, actualReal)
			}
			if predictedPadded != len(encoded) {
				t.Errorf("paddedLen: predicted=%d actual=%d", predictedPadded, len(encoded))
			}
		})
	}
}

// TestPredictXLogRecordLenPaddedIsMaxAlignOfReal pins the invariant that
// paddedLen == maxAlignXLog(realRecLen). encodeRecordXLog allocates
// `out := make([]byte, maxAlignXLog(realLen))`; any future change to the
// alignment rule must update both call sites atomically.
func TestPredictXLogRecordLenPaddedIsMaxAlignOfReal(t *testing.T) {
	for size := 0; size <= 64; size++ {
		payload := bytes.Repeat([]byte{0x55}, size)
		real, padded := predictXLogRecordLen(payload)
		want := maxAlignXLog(real)
		if padded != want {
			t.Errorf("payload size=%d: paddedLen=%d, want maxAlignXLog(%d)=%d",
				size, padded, real, want)
		}
	}
}

// TestPredictXLogRecordLenShortLongBoundary pins the exact byte where the
// wrapper switches from short (2-byte header) to long (5-byte header)
// block-ID prefixes. PG's upstream constants xlrBlockIDDataShort /
// xlrBlockIDDataLong define this boundary as len(payload) > 0xFF.
func TestPredictXLogRecordLenShortLongBoundary(t *testing.T) {
	short := bytes.Repeat([]byte{0x77}, 0xFF)
	long := bytes.Repeat([]byte{0x77}, 0x100)

	realShort, _ := predictXLogRecordLen(short)
	realLong, _ := predictXLogRecordLen(long)

	// short wraps to 2 + 0xFF = 257 bytes of main-data section.
	// long wraps to 5 + 0x100 = 261 bytes of main-data section.
	// Difference: 261 - 257 = 4 bytes.
	if delta := realLong - realShort; delta != 4 {
		t.Errorf("short→long transition: expected 4-byte delta, got %d "+
			"(short=%d, long=%d)", delta, realShort, realLong)
	}
}

// TestPredictXLogRecordLenNilPayloadReturnsZero pins the defensive nil
// short-circuit. encodeRecordXLog called with nil would still produce a
// 32+2=34 byte header+short chunk, but no real caller passes nil; the
// guard exists so a slice B caller bug surfaces as a structured
// errStripeAppendEmptyRecord from AppendBuiltEmitted instead of a
// silent zero-size reservation.
func TestPredictXLogRecordLenNilPayloadReturnsZero(t *testing.T) {
	real, padded := predictXLogRecordLen(nil)
	if real != 0 || padded != 0 {
		t.Errorf("nil payload: got (%d, %d), want (0, 0)", real, padded)
	}
}

// TestPredictXLogRecordLenIsPureNoSideEffects pins the contract that
// predictXLogRecordLen does not mutate its argument. The slice B call site
// holds the payload across the reservation→build closure boundary; a
// mutation would corrupt the encoded record.
func TestPredictXLogRecordLenIsPureNoSideEffects(t *testing.T) {
	payload := []byte{0xFE, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	snapshot := append([]byte(nil), payload...)
	predictXLogRecordLen(payload)
	if !bytes.Equal(payload, snapshot) {
		t.Errorf("payload mutated: got %v, want %v", payload, snapshot)
	}
}
