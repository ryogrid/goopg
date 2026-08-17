package xlog

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// vacuumTestPage builds a non-rightmost leaf page carrying a high key plus the
// named data items. `garbage` sets BTP_HAS_GARBAGE, the hint upstream's
// btree_xlog_vacuum clears unconditionally.
//
// Items are PG pivot tuples for the same reason splitTestPages uses them: the
// vacuum record moves items by OFFSET NUMBER and never parses them as keys.
func vacuumTestPage(t *testing.T, keys []string, garbage bool) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(p); err != nil {
		t.Fatal(err)
	}
	flags := uint16(nbtree.BTPLeaf)
	if garbage {
		flags |= nbtree.BTPHasGarbage
	}
	nbtree.WritePGOpaque(p, nbtree.PGBTPageOpaque{Prev: 5, Next: 7, Level: 0, Flags: flags})
	// P_HIKEY: the page is not rightmost, so its first line pointer is the high
	// key and data starts at P_FIRSTKEY (2).
	if _, err := storage.PageAddItemRaw(p, nbtree.PGBTPivotRaw([]byte("hikey"), nbtree.PNone)); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		// The downlink is derived from the key, not from the position, so an
		// item is byte-identical whichever page it is built on — the surviving
		// items of a vacuumed page must match the ones the primary kept.
		if _, err := storage.PageAddItemRaw(p, nbtree.PGBTPivotRaw([]byte(k), storage.BlockNumber(100+int(k[0])))); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// TestEncodeBtreeVacuumPGIncremental pins M0130-S11.5c's record shape: PG's
// xl_btree_vacuum{ndeleted, nupdated} as MAIN DATA (the previous form carried
// none at all, so pg_waldump's btree_desc printed the two counts off the end of
// a zero-length area) plus the deleted offset numbers as block-0 data, with NO
// full-page image.
func TestEncodeBtreeVacuumPGIncremental(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 91, Fork: storage.MainFork}
	pre := vacuumTestPage(t, []string{"a", "b", "c", "d"}, true)
	// VACUUM keeps a and c: it drops offsets 3 ("b") and 5 ("d") and — like
	// upstream's redo — clears the garbage hint.
	post := vacuumTestPage(t, []string{"a", "c"}, false)
	deleted := []uint16{3, 5}

	framed, err := EncodeBtreeVacuumPG(rel, 0, pre, post, deleted)
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
	if len(dec.XLog.MainData) != sizeOfXLogBtreeVacuumData {
		t.Fatalf("main data len = %d, want %d (SizeOfBtreeVacuum)", len(dec.XLog.MainData), sizeOfXLogBtreeVacuumData)
	}
	if got := binary.LittleEndian.Uint16(dec.XLog.MainData[0:2]); got != 2 {
		t.Fatalf("ndeleted = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint16(dec.XLog.MainData[2:4]); got != 0 {
		t.Fatalf("nupdated = %d, want 0 (goopg never rewrites posting tuples in place)", got)
	}
	if len(dec.XLog.Blocks) != 1 {
		t.Fatalf("want 1 block ref, got %d", len(dec.XLog.Blocks))
	}
	b := dec.XLog.Blocks[0]
	if b.HasImage {
		t.Fatalf("block 0 carries a full-page image: the whole point of the incremental form is that it does not")
	}
	if want := []byte{3, 0, 5, 0}; !bytes.Equal(b.Data, want) {
		t.Fatalf("block 0 data = %v, want the offset array %v", b.Data, want)
	}
	// The record must be far smaller than the page it describes — the reason
	// the incremental form exists at all.
	if len(framed) >= storage.BlockSize/4 {
		t.Fatalf("incremental vacuum record is %d bytes, expected far below a page", len(framed))
	}
}

// TestReplayBtreeVacuumPGReproducesPage replays the incremental record against
// the pre-vacuum page on disk and asserts it reproduces what the primary wrote —
// items AT MATCHING OFFSETS and the garbage hint cleared — then asserts the
// replay is idempotent under an unchanged LSN.
func TestReplayBtreeVacuumPGReproducesPage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 92, Fork: storage.MainFork}
	pre := vacuumTestPage(t, []string{"a", "b", "c", "d"}, true)
	post := vacuumTestPage(t, []string{"a", "c"}, false)
	if _, err := mgr.Extend(rel, pre); err != nil {
		t.Fatal(err)
	}

	framed, err := EncodeBtreeVacuumPG(rel, 0, pre, post, []uint16{3, 5})
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 900)

	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if err := nbtree.CheckVacuumDelete(pre, got, []uint16{3, 5}); err != nil {
		t.Fatalf("replayed page is not the deletion result: %v", err)
	}
	if op := nbtree.ReadPGOpaque(got); op.Flags&nbtree.BTPHasGarbage != 0 {
		t.Fatalf("replay left BTP_HAS_GARBAGE set; upstream clears it")
	}
	if storage.MustHeader(got).LSN() != 900 {
		t.Fatalf("pd_lsn not stamped to record EndLSN")
	}

	// Same LSN again: the record must be a no-op rather than deleting two more
	// offsets off the already-vacuumed page.
	applyPGRecord(t, mgr, framed, 900)
	again := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(got), []byte(again)) {
		t.Fatalf("replay is not idempotent at an unchanged LSN")
	}
}

// TestEncodeBtreeVacuumPGFallsBackToImage pins the encoder's refusal to ship a
// record whose redo would build a page the primary never wrote: when the offsets
// do not reproduce the written page — goopg's posting-list rewrite and its
// BTDeleted|BTHalfDead stamp on a page that went empty are the two real cases —
// it logs a full-page image instead.
func TestEncodeBtreeVacuumPGFallsBackToImage(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 93, Fork: storage.MainFork}
	pre := vacuumTestPage(t, []string{"a", "b", "c", "d"}, true)
	// The written page is not "pre minus offsets 3 and 5": it also consolidated
	// the survivors into a differently-shaped item.
	post := vacuumTestPage(t, []string{"a+c"}, false)

	framed, err := EncodeBtreeVacuumPG(rel, 0, pre, post, []uint16{3, 5})
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
	b := dec.XLog.Blocks[0]
	if !b.HasImage || !b.ImageApply {
		t.Fatalf("mismatching deletion did not fall back to a full-page image")
	}
	if got := binary.LittleEndian.Uint16(dec.XLog.MainData[0:2]); got != 0 {
		t.Fatalf("image form ndeleted = %d, want 0 — redo takes BLK_RESTORED and must not also delete", got)
	}
}
