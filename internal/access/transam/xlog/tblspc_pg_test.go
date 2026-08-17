package xlog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestTblspcCreateRecordRoundTripAndReplay exercises B4.1d end-to-end: the
// RM_TBLSPC create record must frame → record-encode → decode with the right
// (rmid, info, xid), route to the decoded replay path (nil native Payload),
// carry the {ts_id, ts_path} main-data verbatim, and its replay must
// (re)create pg_tblspc/<oid>.
func TestTblspcCreateRecordRoundTripAndReplay(t *testing.T) {
	const (
		tsOID    = 40123
		location = "" // in-place tablespace
		wantXID  = 77
	)
	framed, err := EncodeTblspcCreatePG(tsOID, location, wantXID)
	if err != nil {
		t.Fatalf("EncodeTblspcCreatePG: %v", err)
	}
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatalf("encodeRecordXLog: %v", err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatalf("decodeRecordXLogDetailed: %v", err)
	}
	if dec.Header.Rmid != RmgrTblspc || dec.Header.Info != xlogTblspcCreate {
		t.Fatalf("header rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrTblspc, xlogTblspcCreate)
	}
	if dec.Header.XID != wantXID {
		t.Fatalf("header xid = %d, want %d", dec.Header.XID, wantXID)
	}
	if dec.Payload != nil {
		t.Fatalf("RM_TBLSPC record must route to decoded replay (nil native Payload), got %d bytes", len(dec.Payload))
	}
	gotOID, gotPath, err := decodeXLogTblspcCreate(dec.XLog.MainData)
	if err != nil {
		t.Fatalf("decodeXLogTblspcCreate: %v", err)
	}
	if gotOID != tsOID || gotPath != location {
		t.Fatalf("decoded (oid=%d path=%q), want (oid=%d path=%q)", gotOID, gotPath, tsOID, location)
	}

	// Replay creates the directory.
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()
	applied, err := ApplyRecord(mgr, Record{StartLSN: 1, EndLSN: 100, XLog: dec.XLog})
	if err != nil {
		t.Fatalf("ApplyRecord(create): %v", err)
	}
	if !applied {
		t.Fatal("ApplyRecord(create) applied=false, want true")
	}
	dir := filepath.Join(dataDir, "pg_tblspc", "40123")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("pg_tblspc/40123 not created: stat err=%v", err)
	}
}

// TestTblspcDropRecordRoundTripAndReplay is the DROP counterpart: the record
// carries ts_id, routes to decoded replay, and its replay removes the dir.
func TestTblspcDropRecordRoundTripAndReplay(t *testing.T) {
	const (
		tsOID   = 40123
		wantXID = 78
	)
	framed, err := EncodeTblspcDropPG(tsOID, wantXID)
	if err != nil {
		t.Fatalf("EncodeTblspcDropPG: %v", err)
	}
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatalf("encodeRecordXLog: %v", err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatalf("decodeRecordXLogDetailed: %v", err)
	}
	if dec.Header.Rmid != RmgrTblspc || dec.Header.Info != xlogTblspcDrop {
		t.Fatalf("header rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrTblspc, xlogTblspcDrop)
	}
	gotOID, err := decodeXLogTblspcDrop(dec.XLog.MainData)
	if err != nil || gotOID != tsOID {
		t.Fatalf("decodeXLogTblspcDrop = (%d, %v), want (%d, nil)", gotOID, err, tsOID)
	}

	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "pg_tblspc", "40123")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()
	if _, err := ApplyRecord(mgr, Record{StartLSN: 1, EndLSN: 100, XLog: dec.XLog}); err != nil {
		t.Fatalf("ApplyRecord(drop): %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("pg_tblspc/40123 still present after drop replay (stat err=%v)", err)
	}
}
