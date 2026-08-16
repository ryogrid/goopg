package xlog

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21b part 2: XLOG_BTREE_INSERT_POST (0x50) ----------------------
//
// The leaf insert whose heap TID fell INSIDE an existing posting list, so the
// primary split that posting to make room (nbtinsert.c `_bt_insertonpg`,
// postingoff > 0). goopg never emits it — its own duplicate-key inserts APPEND
// to a posting — so before this slice it fell into RM_BTREE's `default:` arm
// and, since S16.3, REFUSED the start unless every mutated block carried a
// full-page image. Deduplication is on by default in every real PG index whose
// opclass allows it, so this opcode is ordinary traffic in a real crash tail,
// and PG logs an FPI only on a page's first touch after a checkpoint.
//
// The interesting property is that the record does NOT carry the item that
// ends up on the page: block 0's data is {uint16 postingoff, orignewitem}, and
// redo must re-run `_bt_swap_posting` to learn the final item — the split
// evicts the old posting's rightmost heap TID into the new item and puts the
// new item's original TID into the gap. Replaying `orignewitem` verbatim would
// leave BOTH the posting and the new item holding the wrong TID.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21b.

// btreeLeafPageWith builds a leaf page carrying the given raw items in order.
func btreeLeafPageWith(t *testing.T, items ...[]byte) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	nbtree.WritePGOpaque(page, nbtree.PGBTPageOpaque{Prev: nbtree.PNone, Next: nbtree.PNone, Flags: nbtree.BTPLeaf})
	for _, raw := range items {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			t.Fatal(err)
		}
	}
	return page
}

// buildBtreeInsertPostPG assembles a real XLOG_BTREE_INSERT_POST record: main
// data is the 2-byte offnum, block 0's data run is the 2-byte posting offset
// followed by orignewitem (nbtinsert.c:1316-1330).
func buildBtreeInsertPostPG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber,
	offnum, postingoff uint16, orignewitem []byte,
) []byte {
	t.Helper()
	mainData := make([]byte, sizeOfXLogBtreeInsertData)
	binary.LittleEndian.PutUint16(mainData[0:2], offnum)

	data := make([]byte, 2+len(orignewitem))
	binary.LittleEndian.PutUint16(data[0:2], postingoff)
	copy(data[2:], orignewitem)

	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk, Data: data}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrBtree, xlogBtreeInsertPost, 0, body)
}

func tid(block storage.BlockNumber, off uint16) storage.ItemPointer {
	return storage.ItemPointer{Block: block, Offset: off}
}

// TestApplyRecordReplaysPGBtreeInsertPost is the whole slice in one assertion
// set: a 4-TID posting split at index 2 by a new item whose TID sorts between
// the posting's second and third TIDs.
//
// After redo the posting must still hold FOUR TIDs — {10,20,NEW,30}, having
// dropped its old rightmost 40 — and the item added at offnum must carry 40,
// NOT the TID the record logged. Any implementation that merely inserts
// orignewitem passes a "the page has one more item" check and fails here.
func TestApplyRecordReplaysPGBtreeInsertPost(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5201, Fork: storage.MainFork}
	const leafBlk = storage.BlockNumber(0)

	base := nbtree.PGBTItemRaw([]byte("dup"), tid(7, 10))
	oposting := nbtree.PGBTPostingRaw(base, []storage.ItemPointer{
		tid(7, 10), tid(7, 20), tid(7, 30), tid(7, 40),
	})
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t, oposting))

	// The new item's own TID is (7,25) — it belongs at array index 2, between
	// 20 and 30, which is exactly the postingoff _bt_binsrch_posting returned
	// on the primary.
	orignewitem := nbtree.PGBTItemRaw([]byte("dup"), tid(7, 25))
	framed := buildBtreeInsertPostPG(t, rel, leafBlk, 2, 2, orignewitem)
	applyPGRecord(t, mgr, framed, 5200)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leafBlk, leaf); err != nil {
		t.Fatal(err)
	}
	if n, err := storage.PageLinePointerCount(leaf); err != nil || n != 2 {
		t.Fatalf("leaf has %d items (err %v), want 2", n, err)
	}

	gotPosting, err := storage.PageGetItemRaw(leaf, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotPosting) != len(oposting) {
		t.Fatalf("rewritten posting is %d bytes, want %d (the split must not resize it)", len(gotPosting), len(oposting))
	}
	if got := nbtree.BTreeTupleGetNPosting(gotPosting); got != 4 {
		t.Fatalf("rewritten posting holds %d TIDs, want 4", got)
	}
	wantTIDs := []storage.ItemPointer{tid(7, 10), tid(7, 20), tid(7, 25), tid(7, 30)}
	off := nbtree.BTreeTupleGetPostingOffset(gotPosting)
	for i, want := range wantTIDs {
		got := nbtree.PGItemPointerAt(gotPosting[off+i*nbtree.SizeOfItemPointerData:])
		if got != want {
			t.Errorf("posting TID %d = %v, want %v", i, got, want)
		}
	}

	gotNew, err := storage.PageGetItemRaw(leaf, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := nbtree.PGIndexTupleTID(gotNew); got != tid(7, 40) {
		t.Errorf("new item's t_tid = %v, want (7,40) — the max TID evicted from the posting", got)
	}
	if len(gotNew) != len(orignewitem) {
		t.Errorf("new item is %d bytes, want %d", len(gotNew), len(orignewitem))
	}
	if lsn := storage.MustHeader(leaf).LSN(); lsn != 5200 {
		t.Errorf("leaf pd_lsn = %d, want 5200", lsn)
	}
}

// TestApplyRecordReplaysPGBtreeInsertPostFromImage is the checkpoint-adjacent
// shape: the same opcode, but the page's first touch after a checkpoint, so PG
// logged a full-page image. Redo must restore the image and must NOT then also
// re-run the split on top of it — the image already encodes the post-mutation
// page.
func TestApplyRecordReplaysPGBtreeInsertPostFromImage(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5202, Fork: storage.MainFork}
	const leafBlk = storage.BlockNumber(0)

	base := nbtree.PGBTItemRaw([]byte("dup"), tid(7, 10))
	before := btreeLeafPageWith(t, nbtree.PGBTPostingRaw(base, []storage.ItemPointer{tid(7, 10), tid(7, 20)}))
	btreeSeedRel(t, mgr, rel, before)

	// The image is the page as it looked AFTER the primary applied the split.
	after := btreeLeafPageWith(t,
		nbtree.PGBTPostingRaw(base, []storage.ItemPointer{tid(7, 10), tid(7, 15)}),
		nbtree.PGBTItemRaw([]byte("dup"), tid(7, 20)),
	)

	mainData := make([]byte, sizeOfXLogBtreeInsertData)
	binary.LittleEndian.PutUint16(mainData[0:2], 2)
	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: leafBlk, Image: &FullPageImage{Page: after, Apply: true},
	}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	applyPGRecord(t, mgr, framePGAssembled(RmgrBtree, xlogBtreeInsertPost, 0, body), 5300)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leafBlk, leaf); err != nil {
		t.Fatal(err)
	}
	if n, err := storage.PageLinePointerCount(leaf); err != nil || n != 2 {
		t.Fatalf("leaf has %d items (err %v), want 2 (the restored image)", n, err)
	}
	gotNew, err := storage.PageGetItemRaw(leaf, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := nbtree.PGIndexTupleTID(gotNew); got != tid(7, 20) {
		t.Errorf("new item's t_tid = %v, want (7,20) from the image", got)
	}
}

// TestApplyRecordRefusesPGBtreeInsertPostBadPostingOff pins upstream's own
// sanity check (_bt_swap_posting's `postingoff > 0 && postingoff < nhtids`
// elog(ERROR)). postingoff == 0 is what a corrupt opclass produces, and
// honouring it would shift the whole TID array and write a posting whose TIDs
// are no longer ascending — an index that scans wrong rather than one that
// fails to start.
func TestApplyRecordRefusesPGBtreeInsertPostBadPostingOff(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5203, Fork: storage.MainFork}
	base := nbtree.PGBTItemRaw([]byte("dup"), tid(7, 10))
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
		nbtree.PGBTPostingRaw(base, []storage.ItemPointer{tid(7, 10), tid(7, 20)})))

	for _, tc := range []struct {
		name       string
		postingoff uint16
	}{
		{"zero", 0},
		{"at-nhtids", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			framed := buildBtreeInsertPostPG(t, rel, 0, 2, tc.postingoff,
				nbtree.PGBTItemRaw([]byte("dup"), tid(7, 15)))
			err := applyPGRecordErr(t, mgr, framed, 5400)
			if err == nil || !strings.Contains(err.Error(), "cannot be split at offset") {
				t.Fatalf("err = %v, want a refusal naming the illegal split offset", err)
			}
		})
	}
}

// TestApplyRecordRefusesPGBtreeInsertPostOnNonPosting is the other corruption
// direction: the item to the left of offnum is a plain tuple, not a posting
// list. Upstream reads it as a posting unconditionally (its Assert is
// compiled out in production), so goopg refusing is strictly safer — but the
// point of the guard is that the refusal happens BEFORE the page is touched.
func TestApplyRecordRefusesPGBtreeInsertPostOnNonPosting(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5204, Fork: storage.MainFork}
	plain := nbtree.PGBTItemRaw([]byte("dup"), tid(7, 10))
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t, plain))

	framed := buildBtreeInsertPostPG(t, rel, 0, 2, 1, nbtree.PGBTItemRaw([]byte("dup"), tid(7, 15)))
	if err := applyPGRecordErr(t, mgr, framed, 5500); err == nil {
		t.Fatal("replay accepted a posting split of a non-posting item")
	}

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, leaf); err != nil {
		t.Fatal(err)
	}
	if n, err := storage.PageLinePointerCount(leaf); err != nil || n != 1 {
		t.Fatalf("leaf has %d items (err %v), want 1 — the refusal must not have mutated the page", n, err)
	}
}

// TestApplyRecordRefusesPGBtreeInsertPostAtFirstOffset guards the offnum
// arithmetic: the posting being split is OffsetNumberPrev(offnum), so an
// offnum of 1 (P_HIKEY's slot) or 0 has no predecessor at all. Computing
// offnum-1 unchecked would underflow to 65535 and read a wild slot.
func TestApplyRecordRefusesPGBtreeInsertPostAtFirstOffset(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5205, Fork: storage.MainFork}
	base := nbtree.PGBTItemRaw([]byte("dup"), tid(7, 10))
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
		nbtree.PGBTPostingRaw(base, []storage.ItemPointer{tid(7, 10), tid(7, 20)})))

	framed := buildBtreeInsertPostPG(t, rel, 0, 1, 1, nbtree.PGBTItemRaw([]byte("dup"), tid(7, 15)))
	err := applyPGRecordErr(t, mgr, framed, 5600)
	if err == nil || !strings.Contains(err.Error(), "no predecessor item") {
		t.Fatalf("err = %v, want a refusal naming the missing predecessor", err)
	}
}
