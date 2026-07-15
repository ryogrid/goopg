package wal

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// splitTestPage builds an 8 KiB page with a standard header, content outside the
// free-space hole, and zeros inside it, so its full-page image round-trips
// byte-for-byte (the hole is reconstructed as zeros).
func splitTestPage(marker byte) storage.Page {
	p := make(storage.Page, storage.BlockSize)
	for i := storage.SizeOfPageHeaderData; i < 100; i++ {
		p[i] = marker
	}
	for i := 7000; i < storage.BlockSize; i++ {
		p[i] = marker ^ 0xFF
	}
	h := storage.MustHeader(p)
	h.SetLower(100)
	h.SetUpper(7000)
	return p
}

// TestEncodeBtreeSplitPGFPIReplay drives a btree split (left overwritten, right
// a new block) through the PG-format emit + FPI-restore replay and asserts both
// pages are restored byte-for-byte (modulo pd_lsn, which replay stamps).
func TestEncodeBtreeSplitPGFPIReplay(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 55, Fork: storage.MainFork}

	// Left block 0 pre-exists (replay overwrites it); right block 1 is new (extend).
	blank := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(blank); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, blank); err != nil {
		t.Fatal(err)
	}

	left := splitTestPage(0xA1)
	right := splitTestPage(0xB2)

	framed, err := EncodeBtreeSplitPG(rel, 0, 1, left, right, storage.InvalidBlockNumber, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A block-bearing PG record must decode to the FPI path (nil native Payload).
	rec, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decodeRecordXLogDetailed(rec)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != xlogBtreeSplitL {
		t.Fatalf("rmid/info = %d/%#x, want RmgrBtree/SPLIT_L", dec.Header.Rmid, dec.Header.Info)
	}
	if len(dec.XLog.Blocks) != 2 {
		t.Fatalf("want 2 block refs, got %d", len(dec.XLog.Blocks))
	}
	for _, b := range dec.XLog.Blocks {
		if !b.HasImage || !b.ImageApply {
			t.Fatalf("block %d missing apply-image", b.ID)
		}
	}

	applyPGRecord(t, mgr, framed, 500)

	got0 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got0); err != nil {
		t.Fatal(err)
	}
	got1 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, got1); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(got0)[8:], []byte(left)[8:]) {
		t.Fatalf("left page not restored byte-for-byte")
	}
	if !bytes.Equal([]byte(got1)[8:], []byte(right)[8:]) {
		t.Fatalf("right page not restored byte-for-byte")
	}
	if storage.MustHeader(got0).LSN() != 500 || storage.MustHeader(got1).LSN() != 500 {
		t.Fatalf("pd_lsn not stamped to record EndLSN")
	}
}
