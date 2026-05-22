package wal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestBuildSegmentPadRecordMinSize pins the empty-body case: padLen == 24
// must produce a 24-byte record with no body and correct rmgr/info bytes,
// and the record must round-trip through the WAL decoder cleanly.
func TestBuildSegmentPadRecordMinSize(t *testing.T) {
	rec, err := buildSegmentPadRecord(SizeOfXLogRecord, 0x12345678)
	if err != nil {
		t.Fatalf("buildSegmentPadRecord(24): %v", err)
	}
	if len(rec) != SizeOfXLogRecord {
		t.Fatalf("len = %d, want %d", len(rec), SizeOfXLogRecord)
	}
	h, err := DecodeXLogRecordHeader(rec)
	if err != nil {
		t.Fatalf("DecodeXLogRecordHeader: %v", err)
	}
	if h.TotLen != uint32(SizeOfXLogRecord) {
		t.Errorf("TotLen = %d, want %d", h.TotLen, SizeOfXLogRecord)
	}
	if h.Rmid != RmgrXLog {
		t.Errorf("Rmid = %d, want RmgrXLog (0)", h.Rmid)
	}
	if h.Info != xlogInfoNoop {
		t.Errorf("Info = 0x%02x, want 0x%02x (XLOG_NOOP)", h.Info, xlogInfoNoop)
	}
	if h.Prev != 0x12345678 {
		t.Errorf("Prev = 0x%x, want 0x12345678", h.Prev)
	}
	if h.XID != 0 {
		t.Errorf("XID = %d, want 0", h.XID)
	}
	// CRC validation (over zero body + zeroed-CRC header bytes).
	if err := VerifyXLogRecordCRC(rec, nil, h.CRC); err != nil {
		t.Errorf("VerifyXLogRecordCRC: %v", err)
	}
}

// TestBuildSegmentPadRecordRoundTripSizes walks every encodable shape
// boundary (empty body, short chunk lo/mid/hi, long chunk lo/large) and
// asserts each pad record is byte-correct and parses via the full WAL
// decoder. This is the cross-section that pins the encoding choice for
// each branch in buildSegmentPadRecord's switch.
func TestBuildSegmentPadRecordRoundTripSizes(t *testing.T) {
	cases := []struct {
		name   string
		padLen int
	}{
		{"empty_body_24", SizeOfXLogRecord},
		{"short_chunk_min_26", SizeOfXLogRecord + 2},
		{"short_chunk_mid_100", 100},
		{"short_chunk_max_281", SizeOfXLogRecord + 2 + 255},
		{"long_chunk_min_282", SizeOfXLogRecord + 2 + 256},
		{"long_chunk_1024", 1024},
		{"long_chunk_64KiB", 64 * 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := buildSegmentPadRecord(tc.padLen, 0)
			if err != nil {
				t.Fatalf("buildSegmentPadRecord(%d): %v", tc.padLen, err)
			}
			if len(rec) != tc.padLen {
				t.Fatalf("len(rec) = %d, want padLen = %d", len(rec), tc.padLen)
			}
			h, err := DecodeXLogRecordHeader(rec[:SizeOfXLogRecord])
			if err != nil {
				t.Fatalf("DecodeXLogRecordHeader: %v", err)
			}
			if h.TotLen != uint32(tc.padLen) {
				t.Errorf("TotLen = %d, want %d", h.TotLen, tc.padLen)
			}
			if h.Rmid != RmgrXLog || h.Info != xlogInfoNoop {
				t.Errorf("rmid/info = %d/0x%02x, want RmgrXLog/0x20", h.Rmid, h.Info)
			}
			body := rec[SizeOfXLogRecord:]
			if err := VerifyXLogRecordCRC(rec[:SizeOfXLogRecord], body, h.CRC); err != nil {
				t.Errorf("VerifyXLogRecordCRC: %v", err)
			}
			// Full structured-decoder round-trip (parses block refs +
			// main data). NOOP records are not goopg-native, so we go
			// through decodeRecordXLogDetailed rather than
			// decodeRecordXLog (which rejects non-native records as
			// "carries structured xlog fragments").
			decoded, err := decodeRecordXLogDetailed(rec)
			if err != nil {
				t.Errorf("decodeRecordXLogDetailed: %v", err)
				return
			}
			if decoded.XLog == nil {
				t.Fatal("decoded.XLog == nil")
			}
			if len(decoded.XLog.Blocks) != 0 {
				t.Errorf("Blocks = %d, want 0", len(decoded.XLog.Blocks))
			}
			// MainData length is the body bytes after the chunk header.
			expectMain := tc.padLen - SizeOfXLogRecord
			switch {
			case expectMain == 0:
				expectMain = 0
			case expectMain <= 2+255:
				expectMain -= 2
			default:
				expectMain -= 5
			}
			if len(decoded.XLog.MainData) != expectMain {
				t.Errorf("MainData len = %d, want %d", len(decoded.XLog.MainData), expectMain)
				return
			}
			// All main-data bytes must be zero — they're padding.
			zero := make([]byte, expectMain)
			if !bytes.Equal(decoded.XLog.MainData, zero) {
				t.Errorf("MainData not all zero (len=%d)", expectMain)
			}
		})
	}
}

// TestBuildSegmentPadRecordRejectsTooSmall pins the lower-bound contract:
// padLen below the 24-byte minimum is unencodable (a sub-header pad has
// nowhere to put the XLogRecord). The call-site rewrite is responsible
// for not asking for these.
func TestBuildSegmentPadRecordRejectsTooSmall(t *testing.T) {
	for _, padLen := range []int{0, 1, 8, 16, 23} {
		_, err := buildSegmentPadRecord(padLen, 0)
		if err == nil {
			t.Errorf("padLen=%d: expected error, got nil", padLen)
			continue
		}
		if !strings.Contains(err.Error(), "below minimum") {
			t.Errorf("padLen=%d: error %q does not mention 'below minimum'", padLen, err)
		}
	}
}

// TestBuildSegmentPadRecordRejects1ByteBody pins the singular hole in the
// encoding scheme: padLen == 25 leaves a 1-byte body, which is smaller
// than even the 2-byte short main-data chunk header. The maxAlignXLog
// rule guarantees real reservations never produce padLen == 25, but the
// foundation rejects the value explicitly rather than silently producing
// a corrupt record.
func TestBuildSegmentPadRecordRejects1ByteBody(t *testing.T) {
	_, err := buildSegmentPadRecord(SizeOfXLogRecord+1, 0)
	if err == nil {
		t.Fatal("padLen=25: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "1-byte body") {
		t.Errorf("error %q does not mention '1-byte body'", err)
	}
}

// TestBuildSegmentPadRecordPrevPropagated asserts the prev pointer is
// stamped into the header so the segment tail's prev-chain stays
// continuous after a cross-segment crossing.
func TestBuildSegmentPadRecordPrevPropagated(t *testing.T) {
	for _, prev := range []uint64{0, 1, 0xDEAD_BEEF, 0xFFFF_FFFF_FFFF_FFFF} {
		rec, err := buildSegmentPadRecord(64, prev)
		if err != nil {
			t.Fatalf("buildSegmentPadRecord(64, 0x%x): %v", prev, err)
		}
		h, err := DecodeXLogRecordHeader(rec[:SizeOfXLogRecord])
		if err != nil {
			t.Fatalf("DecodeXLogRecordHeader: %v", err)
		}
		if h.Prev != prev {
			t.Errorf("Prev = 0x%x, want 0x%x", h.Prev, prev)
		}
	}
}

// TestBuildSegmentPadRecordBodyAllZeroAfterChunkHeader confirms that
// every body byte past the chunk-header prefix is zero. The
// onCrossSegment hook emits these bytes verbatim into the WAL buffer
// so any stray non-zero byte would corrupt a downstream consumer
// (e.g. CRC mismatch on re-decode, or a replica diff on byte
// comparison).
func TestBuildSegmentPadRecordBodyAllZeroAfterChunkHeader(t *testing.T) {
	cases := []struct {
		name      string
		padLen    int
		headerLen int
	}{
		{"short_chunk_100", 100, 2},
		{"long_chunk_1024", 1024, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := buildSegmentPadRecord(tc.padLen, 0)
			if err != nil {
				t.Fatalf("buildSegmentPadRecord(%d): %v", tc.padLen, err)
			}
			body := rec[SizeOfXLogRecord:]
			tail := body[tc.headerLen:]
			for i, b := range tail {
				if b != 0 {
					t.Fatalf("tail byte %d = 0x%02x, want 0 (padLen=%d, headerLen=%d)",
						i+tc.headerLen, b, tc.padLen, tc.headerLen)
				}
			}
		})
	}
}

// TestBuildSegmentPadRecordCRCDeterministic confirms the CRC is purely a
// function of the record bytes — two builds with the same (padLen, prev)
// produce byte-identical output (including CRC). Without determinism a
// replay byte-diff test could falsely fail across runs.
func TestBuildSegmentPadRecordCRCDeterministic(t *testing.T) {
	a, err := buildSegmentPadRecord(256, 0x1000)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	b, err := buildSegmentPadRecord(256, 0x1000)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two builds with identical inputs produced different bytes")
	}
}

// TestBuildSegmentPadRecordCRCDetectsCorruption pins that a single bit
// flip in the body invalidates the CRC. The record is otherwise
// well-formed; only the body has been mutated. Without this, the
// EncodeXLogRecordHeader CRC-over-body computation could silently drop
// out and we'd ship records whose body could be tampered with
// undetected.
func TestBuildSegmentPadRecordCRCDetectsCorruption(t *testing.T) {
	rec, err := buildSegmentPadRecord(64, 0)
	if err != nil {
		t.Fatalf("buildSegmentPadRecord: %v", err)
	}
	h, err := DecodeXLogRecordHeader(rec[:SizeOfXLogRecord])
	if err != nil {
		t.Fatalf("DecodeXLogRecordHeader: %v", err)
	}
	// Corrupt a body byte well past the chunk header.
	rec[SizeOfXLogRecord+10] ^= 0xFF
	body := rec[SizeOfXLogRecord:]
	if err := VerifyXLogRecordCRC(rec[:SizeOfXLogRecord], body, h.CRC); err == nil {
		t.Fatal("VerifyXLogRecordCRC: expected mismatch after body bit-flip, got nil")
	} else if !errors.Is(err, err) { // any error is fine
		t.Fatalf("VerifyXLogRecordCRC: unexpected error %v", err)
	}
}
