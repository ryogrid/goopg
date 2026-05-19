package wal

import "testing"

func TestEncodeDecodeRecordXLogRoundTripShortMainData(t *testing.T) {
	payload := []byte("hello")
	enc, _, err := encodeRecordXLog(payload, 77)
	if err != nil {
		t.Fatal(err)
	}
	got, n, err := decodeRecordXLog(enc)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(enc) {
		t.Fatalf("decoded bytes = %d, want %d", n, len(enc))
	}
	if string(got) != string(payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}

	h, err := DecodeXLogRecordHeader(enc[:xlogRecordHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	if h.Prev != 77 {
		t.Fatalf("xl_prev = %d, want 77", h.Prev)
	}
}

func TestEncodeDecodeRecordXLogRoundTripLongMainData(t *testing.T) {
	payload := make([]byte, 700)
	for i := range payload {
		payload[i] = byte(i & 0xFF)
	}
	enc, _, err := encodeRecordXLog(payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, n, err := decodeRecordXLog(enc)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(enc) {
		t.Fatalf("decoded bytes = %d, want %d", n, len(enc))
	}
	if len(got) != len(payload) {
		t.Fatalf("payload len = %d, want %d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("payload[%d] = %d, want %d", i, got[i], payload[i])
		}
	}
}

func TestDecodeRecordXLogRejectsBadMainDataTag(t *testing.T) {
	enc, _, err := encodeRecordXLog([]byte("x"), 0)
	if err != nil {
		t.Fatal(err)
	}
	enc[xlogRecordHeaderSize] = 0x01 // unsupported chunk tag
	_, _, err = decodeRecordXLog(enc)
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestEncodeRecordXLogClassifiesXactCommitXID(t *testing.T) {
	// Goopg-internal records (non-canonical) are routed through RmgrXLog
	// with xlogInfoDefault so PG's xlog_redo skips them safely (M0105-0007).
	// XID is 0 in the header for non-canonical records; canonical records
	// (0xFE prefix) carry the full XID for logical-replication consumers.
	payload := EncodeXactCommit(4242)
	enc, _, err := encodeRecordXLog(payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := DecodeXLogRecordHeader(enc[:xlogRecordHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	if h.Rmid != RmgrXLog {
		t.Fatalf("rmid = %d, want %d (RmgrXLog)", h.Rmid, RmgrXLog)
	}
	if h.XID != 0 {
		t.Fatalf("xid = %d, want 0 (non-canonical records carry no XID)", h.XID)
	}
}
