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
	// goopg now emits ordinary-PG-shaped headers: an XactCommit record
	// classifies as RM_XACT / XLOG_XACT_COMMIT (not the old RM_XLOG/0xF0
	// skip-tag). The XID stays 0 in the header — the native body carries
	// its own xid, which goopg recovery reads directly.
	payload := EncodeXactCommit(4242)
	enc, _, err := encodeRecordXLog(payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := DecodeXLogRecordHeader(enc[:xlogRecordHeaderSize])
	if err != nil {
		t.Fatal(err)
	}
	if h.Rmid != RmgrXact {
		t.Fatalf("rmid = %d, want %d (RmgrXact)", h.Rmid, RmgrXact)
	}
	if h.Info != xlogXactCommit {
		t.Fatalf("info = 0x%02x, want 0x%02x (XLOG_XACT_COMMIT)", h.Info, xlogXactCommit)
	}
	if h.XID != 0 {
		t.Fatalf("xid = %d, want 0 (native body carries the xid)", h.XID)
	}
}
