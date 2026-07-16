package wal

import (
	"errors"
	"hash/crc32"
	"testing"
)

// TestXLogCRC32CIsCastagnoli pins the canonical Castagnoli test
// vector: CRC32C of "123456789" is 0xE3069283. Any drift in the
// polynomial (e.g. someone "fixing" it to crc32.IEEE) breaks
// pg_waldump compatibility immediately.
func TestXLogCRC32CIsCastagnoli(t *testing.T) {
	const expected uint32 = 0xE3069283
	got := XLogCRC32C([]byte("123456789"))
	if got != expected {
		t.Errorf("XLogCRC32C(\"123456789\") = 0x%08X, want 0x%08X (Castagnoli vector)", got, expected)
	}
}

// TestXLogRecordHeaderRoundTrip pins encode/decode is lossless for
// every field including the 8-byte Prev and the rmgr-area Info bits.
// Without this guard a future endianness flip or field reorder slides
// past undetected.
func TestXLogRecordHeaderRoundTrip(t *testing.T) {
	payload := []byte("hello world payload")
	h := XLogRecord{
		TotLen: uint32(SizeOfXLogRecord) + uint32(len(payload)),
		XID:    0x11223344,
		Prev:   0xAABBCCDDEEFF0011,
		Info:   XLRSpecialRelUpdate | 0x80, // framework bit + rmgr-area bit
		Rmid:   RmgrHeap,
	}
	buf := make([]byte, SizeOfXLogRecord)
	if err := EncodeXLogRecordHeader(buf, h, payload); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeXLogRecordHeader(buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// CRC was computed by Encode; the round-tripped header carries
	// it. Snapshot it onto the expected struct before comparing.
	h.CRC = got.CRC
	if got != h {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, h)
	}
	// VerifyXLogRecordCRC over the on-disk header + payload must
	// agree with the encoder's stored CRC.
	if err := VerifyXLogRecordCRC(buf, payload, got.CRC); err != nil {
		t.Errorf("VerifyXLogRecordCRC: %v", err)
	}
}

// TestVerifyXLogRecordCRCDetectsTamper: flipping a single bit in
// the payload after encoding must surface as ErrCorruptRecord. This
// is the per-record corruption guard recovery relies on.
func TestVerifyXLogRecordCRCDetectsTamper(t *testing.T) {
	payload := []byte("hello world")
	h := XLogRecord{
		TotLen: uint32(SizeOfXLogRecord + len(payload)),
		Rmid:   RmgrHeap,
	}
	buf := make([]byte, SizeOfXLogRecord)
	if err := EncodeXLogRecordHeader(buf, h, payload); err != nil {
		t.Fatal(err)
	}
	storedCRC := DecodeOnly(t, buf).CRC

	// Flip a payload bit.
	tampered := append([]byte(nil), payload...)
	tampered[5] ^= 0x01

	err := VerifyXLogRecordCRC(buf, tampered, storedCRC)
	if !errors.Is(err, ErrCorruptRecord) {
		t.Errorf("tamper detection err = %v, want ErrCorruptRecord", err)
	}
}

// DecodeOnly is a tiny helper that wraps DecodeXLogRecordHeader for
// tests and t.Fatal's on error. Saves repetitive boilerplate.
func DecodeOnly(t *testing.T, src []byte) XLogRecord {
	t.Helper()
	h, err := DecodeXLogRecordHeader(src)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return h
}

// TestEncodeRejectsTotLenMismatch pins the producer-side invariant:
// h.TotLen must match SizeOfXLogRecord + len(payload). Catches
// callers that forget to set TotLen, or set it to just the payload
// length.
func TestEncodeRejectsTotLenMismatch(t *testing.T) {
	buf := make([]byte, SizeOfXLogRecord)
	payload := []byte("xx")
	h := XLogRecord{TotLen: 100, Rmid: RmgrXLog} // wrong; should be 24+2
	if err := EncodeXLogRecordHeader(buf, h, payload); err == nil {
		t.Error("Encode accepted mismatched TotLen")
	}
}

// TestEncodeRejectsUndefinedFrameworkBits: framework area is the low
// 4 bits; only XLRSpecialRelUpdate (0x01) and XLRCheckConsistency
// (0x02) are defined. Bit 0x04 / 0x08 are reserved and should error.
func TestEncodeRejectsUndefinedFrameworkBits(t *testing.T) {
	buf := make([]byte, SizeOfXLogRecord)
	payload := []byte("p")
	h := XLogRecord{
		TotLen: uint32(SizeOfXLogRecord + len(payload)),
		Info:   0x04, // undefined framework bit
		Rmid:   RmgrHeap,
	}
	if err := EncodeXLogRecordHeader(buf, h, payload); err == nil {
		t.Error("Encode accepted undefined framework bit 0x04")
	}
}

// TestDecodeRejectsNonZeroPadding: upstream initialises the 2-byte
// padding to zero; non-zero is a corruption signal an external
// reader treats as fatal. Pin it.
func TestDecodeRejectsNonZeroPadding(t *testing.T) {
	buf := make([]byte, SizeOfXLogRecord)
	h := XLogRecord{TotLen: uint32(SizeOfXLogRecord), Rmid: RmgrHeap}
	if err := EncodeXLogRecordHeader(buf, h, nil); err != nil {
		t.Fatal(err)
	}
	buf[18] = 0xFF // plant nonzero padding
	if _, err := DecodeXLogRecordHeader(buf); !errors.Is(err, ErrInvalidRecordHeader) {
		t.Errorf("Decode nonzero-padding err = %v, want ErrInvalidRecordHeader", err)
	}
}

// TestDecodeRejectsUnknownRmgr: a rmid above MaxKnownRmgr is the
// "WAL emitted by a newer producer" branch — must surface as
// ErrInvalidRecordHeader so the legacy-format detector can route
// it cleanly.
func TestDecodeRejectsUnknownRmgr(t *testing.T) {
	buf := make([]byte, SizeOfXLogRecord)
	h := XLogRecord{TotLen: uint32(SizeOfXLogRecord), Rmid: 99}
	if err := EncodeXLogRecordHeader(buf, h, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeXLogRecordHeader(buf); !errors.Is(err, ErrInvalidRecordHeader) {
		t.Errorf("Decode unknown-rmgr err = %v, want ErrInvalidRecordHeader", err)
	}
}

// TestDecodeAcceptsGoopgCustomRmgrRange: rmids in PostgreSQL's
// reserved custom-rmgr range (128..255, RmgrGoopgCustomBase) must
// decode cleanly even though they exceed MaxKnownRmgr — this is the
// range goopg-private catalog/DDL records dispatch under (04-remove-
// canonical-and-pg-rmgr-dispatch.md §3.2/§5.4). A rmid just below the
// range (127) must still be rejected as unknown.
func TestDecodeAcceptsGoopgCustomRmgrRange(t *testing.T) {
	for _, rmid := range []Rmgr{RmgrGoopgCustomBase, RmgrGoopgCatalog, 255} {
		buf := make([]byte, SizeOfXLogRecord)
		h := XLogRecord{TotLen: uint32(SizeOfXLogRecord), Rmid: rmid}
		if err := EncodeXLogRecordHeader(buf, h, nil); err != nil {
			t.Fatalf("Encode rmid=%d: %v", rmid, err)
		}
		got, err := DecodeXLogRecordHeader(buf)
		if err != nil {
			t.Errorf("Decode rmid=%d: unexpected error %v", rmid, err)
		}
		if got.Rmid != rmid {
			t.Errorf("Decode rmid=%d: got Rmid=%d", rmid, got.Rmid)
		}
	}

	buf := make([]byte, SizeOfXLogRecord)
	h := XLogRecord{TotLen: uint32(SizeOfXLogRecord), Rmid: RmgrGoopgCustomBase - 1}
	if err := EncodeXLogRecordHeader(buf, h, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeXLogRecordHeader(buf); !errors.Is(err, ErrInvalidRecordHeader) {
		t.Errorf("Decode rmid=127 err = %v, want ErrInvalidRecordHeader", err)
	}
}

// TestDecodeTruncatedSrc: a too-short buffer must error rather than
// reading past slice bounds.
func TestDecodeTruncatedSrc(t *testing.T) {
	for _, n := range []int{0, 5, SizeOfXLogRecord - 1} {
		if _, err := DecodeXLogRecordHeader(make([]byte, n)); err == nil {
			t.Errorf("Decode buffer of %d bytes returned nil error", n)
		}
	}
}

// TestEncodeCRCMatchesDirectComputation: the encoder's stored CRC
// must equal a fresh CRC32C run over the same bytes (with xl_crc
// zeroed). This is the contract VerifyXLogRecordCRC depends on.
func TestEncodeCRCMatchesDirectComputation(t *testing.T) {
	payload := []byte("redo-payload")
	h := XLogRecord{
		TotLen: uint32(SizeOfXLogRecord + len(payload)),
		XID:    7,
		Prev:   1234,
		Rmid:   RmgrHeap,
	}
	buf := make([]byte, SizeOfXLogRecord)
	if err := EncodeXLogRecordHeader(buf, h, payload); err != nil {
		t.Fatal(err)
	}
	stored := DecodeOnly(t, buf).CRC

	// Independent computation: run Castagnoli over (payload ||
	// header bytes 0..19) — the upstream XLogInsertRecord order
	// from postgres/src/backend/access/transam/xlog.c (~line 5170).
	var scratch [SizeOfXLogRecord]byte
	copy(scratch[:], buf)
	scratch[20] = 0
	scratch[21] = 0
	scratch[22] = 0
	scratch[23] = 0
	tab := crc32.MakeTable(crc32.Castagnoli)
	want := crc32.Update(0, tab, payload)
	want = crc32.Update(want, tab, scratch[:20])
	if stored != want {
		t.Errorf("stored CRC=0x%08X, independent computation=0x%08X", stored, want)
	}
}
