package wal

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeBtreeVacuumPGFPIReplay drives a btree vacuum through the record's
// IMAGE form — the one the encoder falls back to when the rewrite is not a plain
// deletion (no pre-page, no deleted offsets) — and asserts the page is restored
// byte-for-byte (modulo pd_lsn, which replay stamps). The incremental form is
// covered by TestEncodeBtreeVacuumPGIncremental.
func TestEncodeBtreeVacuumPGFPIReplay(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 71, Fork: storage.MainFork}

	// Block 0 pre-exists (replay overwrites it in place).
	blank := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(blank); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, blank); err != nil {
		t.Fatal(err)
	}

	page := splitTestPage(0xC0)

	framed, err := EncodeBtreeVacuumPG(rel, 0, nil, page, nil)
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
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != xlogBtreeVacuum {
		t.Fatalf("rmid/info = %d/%#x, want RmgrBtree/VACUUM", dec.Header.Rmid, dec.Header.Info)
	}
	if len(dec.XLog.Blocks) != 1 {
		t.Fatalf("want 1 block ref, got %d", len(dec.XLog.Blocks))
	}
	if b := dec.XLog.Blocks[0]; !b.HasImage || !b.ImageApply {
		t.Fatalf("block 0 missing apply-image")
	}

	applyPGRecord(t, mgr, framed, 500)

	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(got)[8:], []byte(page)[8:]) {
		t.Fatalf("vacuum page not restored byte-for-byte")
	}
	if storage.MustHeader(got).LSN() != 500 {
		t.Fatalf("pd_lsn not stamped to record EndLSN")
	}
}
