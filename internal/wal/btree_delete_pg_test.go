package wal

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21b part 3: XLOG_BTREE_DELETE (0x70) + REUSE_PAGE (0xD0) --------
//
// XLOG_BTREE_DELETE is the LP_DEAD "simple deletion" pass
// (_bt_delitems_delete, nbtpage.c): an index scan that lands on a dead heap
// tuple marks the entry, and the next insert short of room on that page deletes
// the marked ones instead of splitting. goopg has no such pass, so it never
// emits the opcode — and until this slice a real PG's record hit RM_BTREE's
// `default:` arm, which since S16.3 refuses a record whose blocks do not all
// carry a full-page image.
//
// The half that made it real work is `nupdated`: a posting-list tuple whose
// TIDs died one at a time is REWRITTEN rather than deleted (xl_btree_update),
// and replaying the deletions while dropping those rewrites would leave index
// entries pointing at heap slots VACUUM has already reused. That payload and
// that page work are shared byte for byte with xl_btree_vacuum, whose
// nupdated > 0 arm was an outright refusal until now — so the vacuum path is
// covered here too (sibling-path rule).
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21b.

// buildBtreeDeletePayload assembles block 0's data run, which xl_btree_delete
// and xl_btree_vacuum share (nbtxlog.h:197-237): deleted offsets, updated
// offsets, then one variable-length xl_btree_update per updated offset.
func buildBtreeDeletePayload(deleted []uint16, updates []btree.PostingUpdate) []byte {
	var data []byte
	put := func(v uint16) {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], v)
		data = append(data, b[:]...)
	}
	for _, off := range deleted {
		put(off)
	}
	for _, u := range updates {
		put(u.Offset)
	}
	for _, u := range updates {
		put(uint16(len(u.DeleteTIDs)))
		for _, t := range u.DeleteTIDs {
			put(t)
		}
	}
	return data
}

// buildBtreeDeletePG frames a real XLOG_BTREE_DELETE record. Main data is
// xl_btree_delete: snapshotConflictHorizon, ndeleted, nupdated, isCatalogRel —
// the first and last read by hot standby only, which goopg does not implement.
func buildBtreeDeletePG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber,
	deleted []uint16, updates []btree.PostingUpdate,
) []byte {
	t.Helper()
	return buildBtreeDeleteOpPG(t, xlogBtreeDelete, rel, blk, deleted, updates)
}

func buildBtreeDeleteOpPG(t *testing.T, info uint8, rel storage.RelFileNode, blk storage.BlockNumber,
	deleted []uint16, updates []btree.PostingUpdate,
) []byte {
	t.Helper()
	var mainData []byte
	if info == xlogBtreeVacuum {
		// xl_btree_vacuum is the two counts alone.
		mainData = make([]byte, sizeOfXLogBtreeVacuumData)
		binary.LittleEndian.PutUint16(mainData[0:2], uint16(len(deleted)))
		binary.LittleEndian.PutUint16(mainData[2:4], uint16(len(updates)))
	} else {
		mainData = make([]byte, sizeOfXLogBtreeDeleteData)
		binary.LittleEndian.PutUint32(mainData[0:4], 4242) // snapshotConflictHorizon
		binary.LittleEndian.PutUint16(mainData[4:6], uint16(len(deleted)))
		binary.LittleEndian.PutUint16(mainData[6:8], uint16(len(updates)))
	}
	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: blk, Data: buildBtreeDeletePayload(deleted, updates),
	}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrBtree, info, 0, body)
}

// TestApplyRecordReplaysPGBtreeDelete is the plain shape: three of five leaf
// entries were found dead and go away whole, and the page's garbage hint is
// cleared. The deleted offsets are non-contiguous so an implementation that
// treats them as a range rather than a set is caught.
func TestApplyRecordReplaysPGBtreeDelete(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5310, Fork: storage.MainFork}
	page := btreeLeafPageWith(t,
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 10)),
		btree.PGBTItemRaw([]byte("bbb"), tid(7, 20)),
		btree.PGBTItemRaw([]byte("ccc"), tid(7, 30)),
		btree.PGBTItemRaw([]byte("ddd"), tid(7, 40)),
		btree.PGBTItemRaw([]byte("eee"), tid(7, 50)),
	)
	op := btree.ReadPGOpaque(page)
	op.Flags |= btree.BTPHasGarbage
	btree.WritePGOpaque(page, op)
	btreeSeedRel(t, mgr, rel, page)

	applyPGRecord(t, mgr, buildBtreeDeletePG(t, rel, 0, []uint16{1, 3, 5}, nil), 7100)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, leaf); err != nil {
		t.Fatal(err)
	}
	assertDedupTIDs(t, dedupPageTIDs(t, leaf, 1), [][]storage.ItemPointer{
		{tid(7, 20)}, {tid(7, 40)},
	})
	if got := btree.ReadPGOpaque(leaf); got.Flags&btree.BTPHasGarbage != 0 {
		t.Error("BTP_HAS_GARBAGE survived the deletion")
	}
	if lsn := storage.MustHeader(leaf).LSN(); lsn != 7100 {
		t.Errorf("leaf pd_lsn = %d, want 7100", lsn)
	}
}

// TestApplyRecordReplaysPGBtreeDeleteWithPostingUpdates is the slice's real
// content: one record that both deletes whole items AND strips dead TIDs out of
// a surviving posting list.
//
// It pins the ORDER upstream applies the two in (btree_xlog_updates, then
// PageIndexMultiDelete). Both offset arrays are in the PRE-deletion coordinate
// space, so deleting first would shift the posting out from under offset 3 —
// here onto a plain tuple, which the update path refuses outright. Getting the
// order wrong therefore fails loudly instead of silently rewriting a neighbour.
func TestApplyRecordReplaysPGBtreeDeleteWithPostingUpdates(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5311, Fork: storage.MainFork}
	posting := btree.PGBTPostingRaw(
		btree.PGBTItemRaw([]byte("bbb"), tid(8, 10)),
		[]storage.ItemPointer{tid(8, 10), tid(8, 20), tid(8, 30), tid(8, 40)},
	)
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 10)),
		btree.PGBTItemRaw([]byte("aab"), tid(7, 20)),
		posting,
		btree.PGBTItemRaw([]byte("ccc"), tid(9, 10)),
	))

	// Drop item 2 whole, and strip the 2nd and 4th TID (0-based 1 and 3) out of
	// the posting at offset 3.
	framed := buildBtreeDeletePG(t, rel, 0, []uint16{2},
		[]btree.PostingUpdate{{Offset: 3, DeleteTIDs: []uint16{1, 3}}})
	applyPGRecord(t, mgr, framed, 7200)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, leaf); err != nil {
		t.Fatal(err)
	}
	assertDedupTIDs(t, dedupPageTIDs(t, leaf, 1), [][]storage.ItemPointer{
		{tid(7, 10)},
		{tid(8, 10), tid(8, 30)},
		{tid(9, 10)},
	})
	rewritten, err := storage.PageGetItemRaw(leaf, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !btree.BTreeTupleIsPosting(rewritten) {
		t.Fatal("the two-TID survivor is no longer flagged as a posting list")
	}
	// _bt_update_posting sizes the rewrite exactly as _bt_form_posting would,
	// so the bytes must equal a freshly formed posting over the survivors — a
	// rewrite that merely blanked the dead slots would read back with the right
	// TIDs and the wrong length.
	want := btree.PGBTPostingRaw(
		btree.PGBTItemRaw([]byte("bbb"), tid(8, 10)),
		[]storage.ItemPointer{tid(8, 10), tid(8, 30)},
	)
	if string(rewritten) != string(want) {
		t.Errorf("rewritten posting = %x, want %x", rewritten, want)
	}
}

// TestApplyRecordReplaysPGBtreeDeleteCollapsesPostingToPlainTuple covers
// _bt_update_posting's OTHER outcome: one surviving TID is not a one-entry
// posting list, it is a plain non-pivot tuple — INDEX_ALT_TID_MASK off, the
// survivor's heap TID back in t_tid, size back to the key material's length.
//
// A one-entry posting would be a tuple no PG ever writes: every posting reader
// (_bt_binsrch_posting, amcheck, goopg's own parsePostingRaw) treats
// BT_IS_POSTING as the discriminator, so leaving the bit set produces a page
// that only fails later, far from this record.
func TestApplyRecordReplaysPGBtreeDeleteCollapsesPostingToPlainTuple(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5312, Fork: storage.MainFork}
	base := btree.PGBTItemRaw([]byte("bbb"), tid(8, 10))
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
		btree.PGBTPostingRaw(base, []storage.ItemPointer{tid(8, 10), tid(8, 20)}),
		btree.PGBTItemRaw([]byte("ccc"), tid(9, 10)),
	))

	framed := buildBtreeDeletePG(t, rel, 0, nil,
		[]btree.PostingUpdate{{Offset: 1, DeleteTIDs: []uint16{0}}})
	applyPGRecord(t, mgr, framed, 7300)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, leaf); err != nil {
		t.Fatal(err)
	}
	got, err := storage.PageGetItemRaw(leaf, 1)
	if err != nil {
		t.Fatal(err)
	}
	if btree.BTreeTupleIsPosting(got) {
		t.Fatal("the single survivor is still flagged as a posting list")
	}
	if ptr := btree.PGIndexTupleTID(got); ptr != tid(8, 20) {
		t.Errorf("collapsed tuple t_tid = %v, want %v", ptr, tid(8, 20))
	}
	// The second item must be untouched: an nupdated-only record deletes
	// nothing, so no line pointer may move.
	if next, err := storage.PageGetItemRaw(leaf, 2); err != nil {
		t.Fatal(err)
	} else if ptr := btree.PGIndexTupleTID(next); ptr != tid(9, 10) {
		t.Errorf("neighbour t_tid = %v, want %v", ptr, tid(9, 10))
	}
}

// TestApplyRecordReplaysPGBtreeVacuumWithPostingUpdates is the sibling-path
// guard. xl_btree_vacuum carries the SAME payload and does the SAME page work
// (upstream's btree_xlog_vacuum and btree_xlog_delete both call
// btree_xlog_updates then PageIndexMultiDelete), and goopg refused its
// nupdated > 0 form outright until this slice. Fixing only the delete opcode
// would leave a real PG's VACUUM records still refusing the start.
func TestApplyRecordReplaysPGBtreeVacuumWithPostingUpdates(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5313, Fork: storage.MainFork}
	base := btree.PGBTItemRaw([]byte("bbb"), tid(8, 10))
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 10)),
		btree.PGBTPostingRaw(base, []storage.ItemPointer{tid(8, 10), tid(8, 20), tid(8, 30)}),
	))

	framed := buildBtreeDeleteOpPG(t, xlogBtreeVacuum, rel, 0, []uint16{1},
		[]btree.PostingUpdate{{Offset: 2, DeleteTIDs: []uint16{1}}})
	applyPGRecord(t, mgr, framed, 7400)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, leaf); err != nil {
		t.Fatal(err)
	}
	assertDedupTIDs(t, dedupPageTIDs(t, leaf, 1), [][]storage.ItemPointer{
		{tid(8, 10), tid(8, 30)},
	})
}

// TestApplyRecordReplaysPGBtreeDeleteFromImage is the checkpoint-adjacent
// shape: the image already IS the post-deletion page, so redo restores it and
// stops. Re-running the deletion on top of a restored image would delete a
// second set of items — the offsets no longer mean what the primary meant.
func TestApplyRecordReplaysPGBtreeDeleteFromImage(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5314, Fork: storage.MainFork}
	btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
		btree.PGBTItemRaw([]byte("aaa"), tid(7, 10)),
		btree.PGBTItemRaw([]byte("bbb"), tid(7, 20)),
	))
	after := btreeLeafPageWith(t, btree.PGBTItemRaw([]byte("bbb"), tid(7, 20)))

	mainData := make([]byte, sizeOfXLogBtreeDeleteData)
	binary.LittleEndian.PutUint16(mainData[4:6], 1)
	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: 0, Image: &FullPageImage{Page: after, Apply: true},
	}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	applyPGRecord(t, mgr, framePGAssembled(RmgrBtree, xlogBtreeDelete, 0, body), 7500)

	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, leaf); err != nil {
		t.Fatal(err)
	}
	assertDedupTIDs(t, dedupPageTIDs(t, leaf, 1), [][]storage.ItemPointer{{tid(7, 20)}})
}

// TestApplyRecordRefusesPGBtreeDeleteMalformed pins the refusals, each of which
// would otherwise write a page that reads back cleanly while disagreeing with
// what the primary wrote — the failure mode S16.3 exists to prevent.
func TestApplyRecordRefusesPGBtreeDeleteMalformed(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5315, Fork: storage.MainFork}
	base := btree.PGBTItemRaw([]byte("bbb"), tid(8, 10))
	seed := func() {
		btreeSeedRel(t, mgr, rel, btreeLeafPageWith(t,
			btree.PGBTItemRaw([]byte("aaa"), tid(7, 10)),
			btree.PGBTPostingRaw(base, []storage.ItemPointer{tid(8, 10), tid(8, 20)}),
		))
	}
	lsn := uint64(7600)
	refuse := func(t *testing.T, framed []byte, want string) {
		t.Helper()
		lsn += 100
		err := applyPGRecordErr(t, mgr, framed, lsn)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want a refusal containing %q", err, want)
		}
		// The page must be untouched: every check runs before the first write.
		leaf := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(rel, 0, leaf); err != nil {
			t.Fatal(err)
		}
		assertDedupTIDs(t, dedupPageTIDs(t, leaf, 1), [][]storage.ItemPointer{
			{tid(7, 10)}, {tid(8, 10), tid(8, 20)},
		})
	}

	t.Run("update-names-a-plain-tuple", func(t *testing.T) {
		seed()
		// Offset 1 is an ordinary tuple. Stripping "TID 0" out of it would
		// produce a tuple whose declared size no longer matches its bytes.
		refuse(t, buildBtreeDeletePG(t, rel, 0, nil,
			[]btree.PostingUpdate{{Offset: 1, DeleteTIDs: []uint16{0}}}),
			"not a posting list")
	})

	t.Run("update-deletes-every-tid", func(t *testing.T) {
		seed()
		// Upstream asserts nhtids > 0: a posting that loses all its TIDs is
		// deleted as a whole item instead, so this record describes nothing.
		refuse(t, buildBtreeDeletePG(t, rel, 0, nil,
			[]btree.PostingUpdate{{Offset: 2, DeleteTIDs: []uint16{0, 1}}}),
			"leaving none")
	})

	t.Run("tid-offset-past-the-posting", func(t *testing.T) {
		seed()
		refuse(t, buildBtreeDeletePG(t, rel, 0, nil,
			[]btree.PostingUpdate{{Offset: 2, DeleteTIDs: []uint16{5}}}),
			"outside the posting list")
	})

	t.Run("truncated-update-array", func(t *testing.T) {
		seed()
		// nupdated says 1 and the updated-offset array is there, but the
		// xl_btree_update itself was cut off. Trusting the buffer's length
		// instead of the record's own count would replay a deletion-only
		// record and silently keep the dead TIDs.
		mainData := make([]byte, sizeOfXLogBtreeDeleteData)
		binary.LittleEndian.PutUint16(mainData[6:8], 1)
		data := []byte{2, 0} // updated offset 2, no xl_btree_update behind it
		body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: 0, Data: data}})
		if err != nil {
			t.Fatalf("assembleXLogRecord: %v", err)
		}
		refuse(t, framePGAssembled(RmgrBtree, xlogBtreeDelete, 0, body), "ends inside xl_btree_update")
	})

	t.Run("deleted-offset-past-the-page", func(t *testing.T) {
		seed()
		refuse(t, buildBtreeDeletePG(t, rel, 0, []uint16{9}, nil), "outside data range")
	})
}

// TestApplyRecordAcceptsPGBtreeReusePageAsNoOp pins the 0xD0 arm.
//
// btree_xlog_reuse_page (nbtxlog.c:1006-1015) mutates NO page: its entire body
// is the hot-standby conflict resolve, which crash recovery does not perform.
// The record therefore registers no blocks at all — which is exactly why it
// cannot be left to RM_BTREE's `default:` arm, whose S16.3 precondition is that
// every block carries an applicable full-page image and which refuses a record
// with no blocks outright.
func TestApplyRecordAcceptsPGBtreeReusePageAsNoOp(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	// xl_btree_reuse_page: locator(12) + block(4) + FullTransactionId(8) +
	// isCatalogRel(1) = SizeOfBtreeReusePage 25 (nbtxlog.h:186-195).
	mainData := make([]byte, 25)
	binary.LittleEndian.PutUint32(mainData[0:4], 1)     // spcOid
	binary.LittleEndian.PutUint32(mainData[4:8], 1)     // dbOid
	binary.LittleEndian.PutUint32(mainData[8:12], 5316) // relNumber
	binary.LittleEndian.PutUint32(mainData[12:16], 3)   // block
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	applyPGRecord(t, mgr, framePGAssembled(RmgrBtree, xlogBtreeReusePage, 0, body), 7900)
}
