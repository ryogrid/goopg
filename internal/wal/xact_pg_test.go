package wal

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeXactCommitPGRoundTrip drives commit / commit-with-invals / abort
// records through the real encode + decode path and asserts: the xid lands in
// the header (xl_xid, not the body), the opcode is right, the record routes to
// the decoded path (nil native Payload, since header.XID != classifyXLogRecord's
// hardcoded 0), and the HAS_INVALS signal is detectable.
func TestEncodeXactCommitPGRoundTrip(t *testing.T) {
	const xid = storage.TransactionID(0x0BADF00D)

	t.Run("commit", func(t *testing.T) {
		framed, err := EncodeXactCommitPG(xid, false)
		if err != nil {
			t.Fatal(err)
		}
		dec := decodeXactRecord(t, framed)
		if dec.Header.Rmid != RmgrXact || dec.Header.Info&xlogXactOpMask != xlogXactCommit {
			t.Fatalf("rmid/info = %d/%#x, want RmgrXact/commit", dec.Header.Rmid, dec.Header.Info)
		}
		if dec.Header.XID != uint32(xid) {
			t.Fatalf("xl_xid = %#x, want %#x", dec.Header.XID, uint32(xid))
		}
		if dec.Payload != nil {
			t.Fatalf("xact commit must route to the decoded path (nil native Payload)")
		}
		if xactCommitCarriesInvals(dec.Header.Info, dec.XLog.MainData) {
			t.Fatalf("plain commit must not signal invals")
		}
	})

	t.Run("commit_with_invals", func(t *testing.T) {
		framed, err := EncodeXactCommitPG(xid, true)
		if err != nil {
			t.Fatal(err)
		}
		dec := decodeXactRecord(t, framed)
		if dec.Header.Info&xlogXactHasInfo == 0 {
			t.Fatalf("inval commit must set XLOG_XACT_HAS_INFO")
		}
		if !xactCommitCarriesInvals(dec.Header.Info, dec.XLog.MainData) {
			t.Fatalf("inval commit must signal HAS_INVALS")
		}
	})

	t.Run("abort", func(t *testing.T) {
		framed, err := EncodeXactAbortPG(xid)
		if err != nil {
			t.Fatal(err)
		}
		dec := decodeXactRecord(t, framed)
		if dec.Header.Info&xlogXactOpMask != xlogXactAbort {
			t.Fatalf("info = %#x, want abort opcode", dec.Header.Info)
		}
		if dec.Header.XID != uint32(xid) {
			t.Fatalf("xl_xid = %#x, want %#x", dec.Header.XID, uint32(xid))
		}
		if dec.Payload != nil {
			t.Fatalf("xact abort must route to the decoded path (nil native Payload)")
		}
	})
}

func decodeXactRecord(t *testing.T, framed []byte) decodedXLogRecord {
	t.Helper()
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatalf("encodeRecordXLog: %v", err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatalf("decodeRecordXLogDetailed: %v", err)
	}
	return dec
}
