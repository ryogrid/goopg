package wal

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeBtreeVacuumPGFPIReplay drives a btree vacuum (one leaf overwritten
// in place) through the PG-format emit + FPI-restore replay and asserts the page
// is restored byte-for-byte (modulo pd_lsn, which replay stamps).
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

	framed, err := EncodeBtreeVacuumPG(rel, 0, page)
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

// TestEncodeBtreeNewRootPGFPIReplay drives a btree new-root (new root block +
// in-place metapage) through the PG-format emit + FPI-restore replay and asserts
// both pages are restored byte-for-byte. The root block is new (extended); the
// metapage (block 0) is overwritten in place and rides at PG backup block-id 2.
func TestEncodeBtreeNewRootPGFPIReplay(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 72, Fork: storage.MainFork}

	// Block 0 (metapage) pre-exists; the new root at block 1 is extended.
	blank := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(blank); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, blank); err != nil {
		t.Fatal(err)
	}

	const rootBlk = storage.BlockNumber(1)
	const metaBlk = storage.BlockNumber(0)
	rootPage := splitTestPage(0xA0)
	metaPage := splitTestPage(0xA1)

	framed, err := EncodeBtreeNewRootPG(rel, rootBlk, rootPage, metaBlk, metaPage)
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
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != xlogBtreeNewRoot {
		t.Fatalf("rmid/info = %d/%#x, want RmgrBtree/NEWROOT", dec.Header.Rmid, dec.Header.Info)
	}
	if len(dec.XLog.Blocks) != 2 {
		t.Fatalf("want 2 block refs, got %d", len(dec.XLog.Blocks))
	}
	// PG block-ids: 0 = root (WILL_INIT), 2 = metapage.
	var haveRoot, haveMeta bool
	for _, b := range dec.XLog.Blocks {
		if !b.HasImage || !b.ImageApply {
			t.Fatalf("block %d missing apply-image", b.ID)
		}
		switch b.ID {
		case 0:
			haveRoot = true
		case 2:
			haveMeta = true
		}
	}
	if !haveRoot || !haveMeta {
		t.Fatalf("want block-ids 0 (root) and 2 (meta); got %+v", dec.XLog.Blocks)
	}

	applyPGRecord(t, mgr, framed, 700)

	gotRoot := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, rootBlk, gotRoot); err != nil {
		t.Fatal(err)
	}
	gotMeta := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, metaBlk, gotMeta); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(gotRoot)[8:], []byte(rootPage)[8:]) {
		t.Fatalf("new-root page not restored byte-for-byte")
	}
	if !bytes.Equal([]byte(gotMeta)[8:], []byte(metaPage)[8:]) {
		t.Fatalf("metapage not restored byte-for-byte")
	}
	if storage.MustHeader(gotRoot).LSN() != 700 || storage.MustHeader(gotMeta).LSN() != 700 {
		t.Fatalf("pd_lsn not stamped to record EndLSN on both pages")
	}
}
