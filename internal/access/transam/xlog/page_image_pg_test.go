package xlog

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodePageImagePGFPIReplay drives a standalone first-touch FPI through the
// PG-format emit (RM_XLOG / XLOG_FPI) + FPI-restore replay and asserts the page
// is restored byte-for-byte (modulo pd_lsn, which replay stamps to the record
// EndLSN — matching what maybeEmitFPI stamps on the primary).
func TestEncodePageImagePGFPIReplay(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 91, Fork: storage.MainFork}

	// Block 0 pre-exists (replay overwrites it in place).
	blank := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(blank); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, blank); err != nil {
		t.Fatal(err)
	}

	page := splitTestPage(0xB0)

	framed, err := EncodePageImagePG(rel, 0, page)
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
	if dec.Header.Rmid != RmgrXLog || dec.Header.Info != xlogXLogFPI {
		t.Fatalf("rmid/info = %d/%#x, want RmgrXLog/XLOG_FPI", dec.Header.Rmid, dec.Header.Info)
	}
	if len(dec.XLog.Blocks) != 1 {
		t.Fatalf("want 1 block ref, got %d", len(dec.XLog.Blocks))
	}
	if b := dec.XLog.Blocks[0]; !b.HasImage || !b.ImageApply {
		t.Fatalf("block 0 missing apply-image")
	}

	applyPGRecord(t, mgr, framed, 900)

	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(got)[8:], []byte(page)[8:]) {
		t.Fatalf("FPI page not restored byte-for-byte")
	}
	if storage.MustHeader(got).LSN() != 900 {
		t.Fatalf("pd_lsn not stamped to record EndLSN")
	}
}
