package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// pageAllVisibleSet reports PD_ALL_VISIBLE on the stored heap page.
func pageAllVisibleSet(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, blk storage.BlockNumber) bool {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		t.Fatal(err)
	}
	return storage.MustHeader(page).Flags()&storage.PDAllVisible != 0
}

// allVisibleHeapPage seeds a one-tuple heap page at blk and marks it
// all-visible and all-frozen the way PG does (XLOG_HEAP2_VISIBLE: vm bits +
// PD_ALL_VISIBLE).
func allVisibleHeapPage(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, blk storage.BlockNumber, lsn uint64) {
	t.Helper()
	seedHeapTuplePG(t, mgr, rel, blk, 42, "row", lsn)
	applyPGRecord(t, mgr, buildHeapVisiblePG(t, rel, blk, 55, storage.VMAllVisible|storage.VMAllFrozen), lsn+50)
	if vmBitsAt(t, mgr, rel, blk) == 0 || !pageAllVisibleSet(t, mgr, rel, blk) {
		t.Fatalf("setup: block %d is not all-visible", blk)
	}
}

func pgTupleBytes(t *testing.T, xmin storage.TransactionID, blk storage.BlockNumber, slot uint16, data string) []byte {
	t.Helper()
	tup := storage.NewHeapTuple(xmin, storage.InvalidTransactionID, []byte(data))
	tup.Header.CTID = storage.ItemPointer{Block: blk, Offset: slot}
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestPGHeapRecordsClearVMBits pins heap redo's handling of the
// *_ALL_VISIBLE_CLEARED flags (heap_xlog_insert, heap_xlog_delete,
// heap_xlog_update): the page's visibility-map bits and PD_ALL_VISIBLE are
// cleared. Insert, delete and update replay used to ignore the flags, so a
// goopg standby of a PG primary kept a stale all-visible bit after PG had
// cleared it, and index-only scans skipped the heap for a changed page.
func TestPGHeapRecordsClearVMBits(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4431, Fork: storage.MainFork}

	check := func(t *testing.T, mgr *storage.Manager, blk storage.BlockNumber) {
		t.Helper()
		if got := vmBitsAt(t, mgr, rel, blk); got != 0 {
			t.Errorf("block %d vm bits = %#x after the clearing record, want 0", blk, got)
		}
		if pageAllVisibleSet(t, mgr, rel, blk) {
			t.Errorf("block %d PD_ALL_VISIBLE still set after the clearing record", blk)
		}
	}

	t.Run("insert", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		allVisibleHeapPage(t, mgr, rel, 0, 100)
		framed, err := EncodeHeapInsertPGVis(rel, 0, 2, pgTupleBytes(t, 60, 0, 2, "new"), false, true)
		if err != nil {
			t.Fatal(err)
		}
		applyPGRecord(t, mgr, framed, 300)
		check(t, mgr, 0)
	})

	t.Run("insert without the flag keeps the bits", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		allVisibleHeapPage(t, mgr, rel, 0, 100)
		framed, err := EncodeHeapInsertPG(rel, 0, 2, pgTupleBytes(t, 60, 0, 2, "new"), false)
		if err != nil {
			t.Fatal(err)
		}
		applyPGRecord(t, mgr, framed, 300)
		if vmBitsAt(t, mgr, rel, 0) == 0 {
			t.Errorf("an insert without ALL_VISIBLE_CLEARED cleared the vm bits")
		}
	})

	t.Run("delete", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		allVisibleHeapPage(t, mgr, rel, 0, 100)
		framed, err := EncodeHeapDeletePGVis(rel, 0, 1, 61, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		applyPGRecord(t, mgr, framed, 300)
		check(t, mgr, 0)
	})

	t.Run("hot update", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		allVisibleHeapPage(t, mgr, rel, 0, 100)
		framed, err := EncodeHeapHotUpdatePGVis(rel, 0, 1, 2, 62, pgTupleBytes(t, 62, 0, 2, "upd"), true)
		if err != nil {
			t.Fatal(err)
		}
		applyPGRecord(t, mgr, framed, 300)
		check(t, mgr, 0)
	})

	t.Run("cross-page update clears both pages", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		allVisibleHeapPage(t, mgr, rel, 0, 100)
		allVisibleHeapPage(t, mgr, rel, 1, 200)
		framed, err := EncodeHeapUpdatePGVis(rel, 0, 1, 1, 2, 63, pgTupleBytes(t, 63, 1, 2, "moved"), true, true)
		if err != nil {
			t.Fatal(err)
		}
		applyPGRecord(t, mgr, framed, 400)
		check(t, mgr, 0)
		check(t, mgr, 1)
	})

	// heap_xlog_insert clears the vm "even if the heap page is already
	// up-to-date": a page the record does not need to redo (its pd_lsn is
	// past the record) still loses its bits.
	t.Run("vm cleared when the heap page is skipped", func(t *testing.T) {
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		allVisibleHeapPage(t, mgr, rel, 0, 100)
		framed, err := EncodeHeapInsertPGVis(rel, 0, 2, pgTupleBytes(t, 60, 0, 2, "new"), false, true)
		if err != nil {
			t.Fatal(err)
		}
		applyPGRecord(t, mgr, framed, 120) // below the page's pd_lsn (150)
		if got := vmBitsAt(t, mgr, rel, 0); got != 0 {
			t.Errorf("vm bits = %#x after a clearing record whose heap page was skipped, want 0", got)
		}
	})
}

// TestHeapEncodersSetVMClearedFlags pins the encode side: each *PGVis
// encoder sets PG's flag bit in the record's main data, and only when asked.
func TestHeapEncodersSetVMClearedFlags(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4432, Fork: storage.MainFork}
	tuple := pgTupleBytes(t, 70, 0, 1, "x")
	mainFlags := func(t *testing.T, framed []byte, at int) uint8 {
		t.Helper()
		record, _, err := encodeRecordXLog(framed, 0)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := decodeRecordXLogDetailed(record)
		if err != nil {
			t.Fatal(err)
		}
		return dec.XLog.MainData[at]
	}
	ins, _ := EncodeHeapInsertPGVis(rel, 0, 1, tuple, false, true)
	insPlain, _ := EncodeHeapInsertPG(rel, 0, 1, tuple, false)
	del, _ := EncodeHeapDeletePGVis(rel, 0, 1, 71, nil, true)
	hot, _ := EncodeHeapHotUpdatePGVis(rel, 0, 1, 2, 72, tuple, true)
	upd, _ := EncodeHeapUpdatePGVis(rel, 0, 1, 3, 2, 73, tuple, true, true)
	same, _ := EncodeHeapUpdatePGVis(rel, 0, 1, 0, 2, 73, tuple, true, true)
	for _, c := range []struct {
		name       string
		framed     []byte
		at         int
		want, mask uint8
	}{
		{"insert", ins, 2, xlogHeapInsertAllVisibleCleared, xlogHeapInsertAllVisibleCleared},
		{"insert (no clear)", insPlain, 2, 0, xlogHeapInsertAllVisibleCleared},
		{"delete", del, 7, xlhDeleteAllVisibleCleared, xlhDeleteAllVisibleCleared},
		{"hot update", hot, 7, xlhUpdateOldAllVisibleCleared, xlhUpdateOldAllVisibleCleared | xlhUpdateNewAllVisibleCleared},
		{"cross-page update", upd, 7, xlhUpdateOldAllVisibleCleared | xlhUpdateNewAllVisibleCleared, xlhUpdateOldAllVisibleCleared | xlhUpdateNewAllVisibleCleared},
		{"same-page update", same, 7, xlhUpdateOldAllVisibleCleared, xlhUpdateOldAllVisibleCleared | xlhUpdateNewAllVisibleCleared},
	} {
		if got := mainFlags(t, c.framed, c.at) & c.mask; got != c.want {
			t.Errorf("%s: flags & %#x = %#x, want %#x", c.name, c.mask, got, c.want)
		}
	}
}
