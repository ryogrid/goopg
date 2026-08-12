package wal

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21b part 2b: XLOG_BTREE_DEDUP (0x60) ---------------------------
//
// The leaf deduplication pass (_bt_dedup_pass, nbtdedup.c): before letting a
// full leaf split, PG merges runs of equal-key tuples into posting lists and
// reclaims the space. It is on by default for every opclass that allows it, so
// a real PG crash tail is full of these records — and until this slice they
// fell into RM_BTREE's `default:` arm, which since S16.3 REFUSES the start
// unless every mutated block carries a full-page image. PG logs an FPI only on
// a page's first touch after a checkpoint, so the refusal was reachable in
// ordinary operation.
//
// The record's shape is what makes this a page-REBUILD slice: it carries only
// {baseoff, nitems} intervals, no tuples at all. Everything the merged
// postings are made of is already on the page, so redo re-derives them —
// which is also why a wrong interval walk produces a page that still has the
// right item COUNT while holding the wrong TIDs.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21b.

// buildBtreeDedupPG assembles a real XLOG_BTREE_DEDUP record: main data is the
// 2-byte interval count, block 0's data run is the BTDedupInterval array
// (nbtdedup.c:_bt_dedup_pass's XLogRegisterBufData).
func buildBtreeDedupPG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber,
	intervals []btree.DedupInterval,
) []byte {
	t.Helper()
	mainData := make([]byte, sizeOfBtreeDedupData)
	binary.LittleEndian.PutUint16(mainData[0:2], uint16(len(intervals)))

	data := make([]byte, len(intervals)*btree.SizeOfDedupInterval)
	for i, iv := range intervals {
		off := i * btree.SizeOfDedupInterval
		binary.LittleEndian.PutUint16(data[off:off+2], iv.BaseOff)
		binary.LittleEndian.PutUint16(data[off+2:off+4], iv.NItems)
	}

	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: blk, Data: data}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrBtree, xlogBtreeDedup, 0, body)
}

// dedupPageTIDs reads back the heap TIDs each item on a leaf page stands for,
// one slice per item, so a test can assert the post-dedup page by content
// rather than by byte layout.
func dedupPageTIDs(t *testing.T, page storage.Page, firstOff uint16) [][]storage.ItemPointer {
	t.Helper()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]storage.ItemPointer
	for off := firstOff; off <= uint16(count); off++ {
		raw, err := storage.PageGetItemRaw(page, off)
		if err != nil {
			t.Fatal(err)
		}
		if !btree.BTreeTupleIsPosting(raw) {
			out = append(out, []storage.ItemPointer{btree.PGIndexTupleTID(raw)})
			continue
		}
		n := int(btree.BTreeTupleGetNPosting(raw))
		base := btree.BTreeTupleGetPostingOffset(raw)
		tids := make([]storage.ItemPointer, n)
		for i := range tids {
			tids[i] = btree.PGItemPointerAt(raw[base+i*btree.SizeOfItemPointerData:])
		}
		out = append(out, tids)
	}
	return out
}

func assertDedupTIDs(t *testing.T, got, want [][]storage.ItemPointer) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("page holds %d items, want %d (got %v)", len(got), len(want), got)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("item %d holds %d TIDs, want %d (%v vs %v)", i, len(got[i]), len(want[i]), got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("item %d TID %d = %v, want %v", i, j, got[i][j], want[i][j])
			}
		}
	}
}

// TestApplyRecordReplaysPGBtreeDedup is the core case: a rightmost leaf holding
// five plain tuples over two keys, deduplicated into one posting per key.
//
// The two intervals are NOT adjacent in a way that hides an off-by-one — the
// first merges three items at offset 1, the second merges two at offset 4 —
// and the TID order inside each posting is the page order, so an implementation
// that mis-walks the intervals produces the right item count with the wrong
// contents.
func TestApplyRecordReplaysPGBtreeDedup(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5210, Fork: storage.MainFork}
	const leafBlk = storage.BlockNumber(0)

	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 10)),
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 20)),
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 30)),
		btree.PGBTItemRaw([]byte("bbb"), tid(8, 10)),
		btree.PGBTItemRaw([]byte("bbb"), tid(8, 20)),
	))

	framed := buildBtreeDedupPG(t, rel, leafBlk, []btree.DedupInterval{
		{BaseOff: 1, NItems: 3},
		{BaseOff: 4, NItems: 2},
	})
	applyPGRecord(t, mgr, framed, 6100)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leafBlk, leaf); err != nil {
		t.Fatal(err)
	}
	assertDedupTIDs(t, dedupPageTIDs(t, leaf, 1), [][]storage.ItemPointer{
		{tid(7, 10), tid(7, 20), tid(7, 30)},
		{tid(8, 10), tid(8, 20)},
	})
	// The merged tuples must be real posting lists, not the base tuples with a
	// TID array bolted on: a reader keys off BT_IS_POSTING in t_tid.
	first, err := storage.PageGetItemRaw(leaf, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !btree.BTreeTupleIsPosting(first) {
		t.Error("merged item 1 is not flagged as a posting list")
	}
	if lsn := storage.MustHeader(leaf).LSN(); lsn != 6100 {
		t.Errorf("leaf pd_lsn = %d, want 6100", lsn)
	}
}

// TestApplyRecordReplaysPGBtreeDedupKeepsHighKeyAndMergesPostings covers the
// two shapes the simple case cannot: a NON-rightmost page, where offset 1 is
// the high key and P_FIRSTDATAKEY is 2 (mis-handling it would feed the pivot
// tuple into a posting), and a run whose base is ALREADY a posting list from an
// earlier dedup pass — upstream's `basetupsize = BTreeTupleGetPostingOffset`
// case, where the existing TID array is contributed to the merge instead of
// being copied into the new tuple's key material.
//
// It also pins the BTP_HAS_GARBAGE clear: dedup rewrites every item, so no
// LP_DEAD line pointer can survive, and upstream drops the hint.
func TestApplyRecordReplaysPGBtreeDedupKeepsHighKeyAndMergesPostings(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5211, Fork: storage.MainFork}
	const leafBlk = storage.BlockNumber(0)

	highKey := btree.PGBTItemRaw([]byte("zzz"), tid(0, 0))
	base := btree.PGBTItemRaw([]byte("aaa"), tid(7, 10))
	existing := btree.PGBTPostingRaw(base, []storage.ItemPointer{tid(7, 10), tid(7, 20)})

	page := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	// Next != P_NONE makes the page non-rightmost, so offset 1 is P_HIKEY.
	btree.WritePGOpaque(page, btree.PGBTPageOpaque{
		Prev: btree.PNone, Next: 9, Flags: btree.BTPLeaf | btree.BTPHasGarbage,
	})
	for _, raw := range [][]byte{
		highKey,
		existing,
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 30)),
		btree.PGBTItemRaw([]byte("ccc"), tid(9, 10)),
	} {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			t.Fatal(err)
		}
	}
	btreeSeedRel(t, mgr, rel, page)

	framed := buildBtreeDedupPG(t, rel, leafBlk, []btree.DedupInterval{{BaseOff: 2, NItems: 2}})
	applyPGRecord(t, mgr, framed, 6200)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leafBlk, leaf); err != nil {
		t.Fatal(err)
	}
	got, err := storage.PageGetItemRaw(leaf, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(highKey) {
		t.Errorf("offset 1 is no longer the high key (%x)", got)
	}
	// The merged posting holds THREE TIDs: the existing posting's two plus the
	// plain tuple's one. A merge that treated the base posting as opaque key
	// material would produce two.
	assertDedupTIDs(t, dedupPageTIDs(t, leaf, 2), [][]storage.ItemPointer{
		{tid(7, 10), tid(7, 20), tid(7, 30)},
		{tid(9, 10)},
	})
	if op := btree.ReadPGOpaque(leaf); op.Flags&btree.BTPHasGarbage != 0 {
		t.Error("BTP_HAS_GARBAGE survived the dedup rebuild")
	} else if op.Next != 9 {
		t.Errorf("btpo_next = %d, want 9 — the rebuild must not touch the sibling link", op.Next)
	}
}

// TestApplyRecordReplaysPGBtreeDedupFromImage is the checkpoint-adjacent
// shape: PG logged a full-page image, which already IS the deduplicated page.
// Redo must restore it and stop — re-running the interval walk on top of a
// restored image would merge already-merged postings a second time.
func TestApplyRecordReplaysPGBtreeDedupFromImage(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5212, Fork: storage.MainFork}
	const leafBlk = storage.BlockNumber(0)

	base := btree.PGBTItemRaw([]byte("aaa"), tid(7, 10))
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
		base,
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 20)),
	))
	after := btreeLeafPageWith(t, btree.PGBTPostingRaw(base, []storage.ItemPointer{tid(7, 10), tid(7, 20)}))

	mainData := make([]byte, sizeOfBtreeDedupData)
	binary.LittleEndian.PutUint16(mainData[0:2], 1)
	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: leafBlk, Image: &FullPageImage{Page: after, Apply: true},
	}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	applyPGRecord(t, mgr, framePGAssembled(RmgrBtree, xlogBtreeDedup, 0, body), 6300)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leafBlk, leaf); err != nil {
		t.Fatal(err)
	}
	assertDedupTIDs(t, dedupPageTIDs(t, leaf, 1), [][]storage.ItemPointer{
		{tid(7, 10), tid(7, 20)},
	})
}

// TestApplyRecordRefusesPGBtreeDedupBadIntervals pins the two malformed-record
// refusals, both of which would otherwise write a page that reads back cleanly
// while disagreeing with the primary:
//
//   - a baseoff that does not fall on a run boundary (here past the page's last
//     item) leaves intervals unmatched — the record describes a different page;
//   - a truncated block-0 data run relative to the record's own nintervals
//     would replay only the intervals that happened to fit.
func TestApplyRecordRefusesPGBtreeDedupBadIntervals(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5213, Fork: storage.MainFork}
	seed := func() {
		btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
			btree.PGBTItemRaw([]byte("aaa"), tid(7, 10)),
			btree.PGBTItemRaw([]byte("aaa"), tid(7, 20)),
		))
	}

	t.Run("unmatched-baseoff", func(t *testing.T) {
		seed()
		framed := buildBtreeDedupPG(t, rel, 0, []btree.DedupInterval{{BaseOff: 5, NItems: 2}})
		err := applyPGRecordErr(t, mgr, framed, 6400)
		if err == nil || !strings.Contains(err.Error(), "intervals matched a page offset") {
			t.Fatalf("err = %v, want a refusal naming the unmatched interval", err)
		}
		leaf := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(rel, 0, leaf); err != nil {
			t.Fatal(err)
		}
		assertDedupTIDs(t, dedupPageTIDs(t, leaf, 1), [][]storage.ItemPointer{
			{tid(7, 10)}, {tid(7, 20)},
		})
	})

	t.Run("truncated-interval-array", func(t *testing.T) {
		seed()
		// nintervals says 2, the data run carries one.
		mainData := make([]byte, sizeOfBtreeDedupData)
		binary.LittleEndian.PutUint16(mainData[0:2], 2)
		data := make([]byte, btree.SizeOfDedupInterval)
		binary.LittleEndian.PutUint16(data[0:2], 1)
		binary.LittleEndian.PutUint16(data[2:4], 2)
		body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: 0, Data: data}})
		if err != nil {
			t.Fatalf("assembleXLogRecord: %v", err)
		}
		err = applyPGRecordErr(t, mgr, framePGAssembled(RmgrBtree, xlogBtreeDedup, 0, body), 6500)
		if err == nil || !strings.Contains(err.Error(), "intervals (want >=") {
			t.Fatalf("err = %v, want a refusal naming the short interval array", err)
		}
	})

	t.Run("single-item-interval", func(t *testing.T) {
		seed()
		framed := buildBtreeDedupPG(t, rel, 0, []btree.DedupInterval{{BaseOff: 1, NItems: 1}})
		err := applyPGRecordErr(t, mgr, framed, 6600)
		if err == nil || !strings.Contains(err.Error(), "want >= 2") {
			t.Fatalf("err = %v, want a refusal naming the one-item run", err)
		}
	})
}
