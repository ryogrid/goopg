package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeClogTruncatePGRoutingAndDecode verifies the A9 xl_clog_truncate flip.
// PG's clog-truncate carries xl_xid=0, so routing to the decoded path relies on
// the native-size guard in nativeHeaderMatchesMainData (native RecordKindClogTruncate
// body = 5 bytes, PG body = 16). The test uses an oldestXid whose derived pageno's
// low byte equals RecordKindClogTruncate(33) — the exact collision the guard must
// resolve — and asserts the PG record still routes to the decoded path while a
// native 5-byte record still routes native.
func TestEncodeClogTruncatePGRoutingAndDecode(t *testing.T) {
	// pageno = oldestXid / clogXactsPerPage. Choose oldestXid so pageno == 33
	// (= RecordKindClogTruncate) → main-data[0] collides with the native kind.
	const oldestXid = storage.TransactionID(33 * clogXactsPerPage)
	if int(RecordKindClogTruncate) != 33 {
		t.Fatalf("test assumes RecordKindClogTruncate==33, got %d", RecordKindClogTruncate)
	}

	framed, err := EncodeClogTruncatePG(oldestXid, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decodeRecordXLogDetailed(rec)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Header.Rmid != RmgrCLOG || dec.Header.Info != xlogClogTruncate {
		t.Fatalf("rmid/info = %d/%#x, want RmgrCLOG/CLOG_TRUNCATE", dec.Header.Rmid, dec.Header.Info)
	}
	if dec.Header.XID != 0 {
		t.Fatalf("header xid = %d, want 0 (PG clog-truncate is xid-less)", dec.Header.XID)
	}
	if len(dec.XLog.MainData) != 16 {
		t.Fatalf("main-data len = %d, want 16", len(dec.XLog.MainData))
	}
	// The size guard must route this to the decoded path DESPITE main-data[0]
	// classifying to RmgrCLOG/CLOG_TRUNCATE (the collision) — because its length
	// (16) != the native RecordKindClogTruncate size (5).
	if dec.Payload != nil {
		t.Fatalf("PG clog-truncate routed NATIVE (Payload populated) — the size guard failed to disambiguate the pageno-byte collision")
	}
	pageno, gotOldest, gotDb, err := DecodeXLogClogTruncate(dec.XLog.MainData)
	if err != nil {
		t.Fatal(err)
	}
	if pageno != 33 || gotOldest != oldestXid || gotDb != 0 {
		t.Fatalf("decoded pageno=%d oldest=%d db=%d, want 33/%d/0", pageno, gotOldest, gotDb, oldestXid)
	}

	// A native RecordKindClogTruncate (5-byte body) must still route NATIVE so
	// the size guard doesn't regress the legacy fallback.
	nativeFramed, _, err := encodeRecordXLog(EncodeClogTruncate(oldestXid), 0)
	if err != nil {
		t.Fatal(err)
	}
	ndec, err := decodeRecordXLogDetailed(nativeFramed)
	if err != nil {
		t.Fatal(err)
	}
	if ndec.Payload == nil {
		t.Fatalf("native 5-byte clog-truncate routed DECODED — the size guard wrongly rejected a genuine native record")
	}
}
