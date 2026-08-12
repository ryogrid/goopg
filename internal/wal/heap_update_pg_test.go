package wal

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// B0.2 (doc 02a §3): non-HOT xl_heap_update replay pins. The old tuple gets
// xmax + a forward t_ctid WITHOUT HEAP_HOT_UPDATED; the new version lands at
// new_offnum with a self-pointing ctid and no HEAP_ONLY_TUPLE.

// TestApplyRecordReplaysPGHeapUpdateSamePage: old and new versions share the
// page — PG's single-block-ref form.
func TestApplyRecordReplaysPGHeapUpdateSamePage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 909, Fork: storage.MainFork}

	oldTup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("old"))
	oldTup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	oldBytes, err := oldTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, oldBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insertFramed, 100)

	const xmax = storage.TransactionID(99)
	newTup := storage.NewHeapTuple(xmax, storage.InvalidTransactionID, []byte("new"))
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapUpdatePG(rel, 0, 1, 0, 2, xmax, newBytes)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	oldAfter, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if oldAfter.Header.Xmax != xmax {
		t.Fatalf("old t_xmax = %d, want %d", oldAfter.Header.Xmax, xmax)
	}
	if oldAfter.Header.CTID != (storage.ItemPointer{Block: 0, Offset: 2}) {
		t.Fatalf("old t_ctid = %+v, want {0,2}", oldAfter.Header.CTID)
	}
	if oldAfter.Header.IsHotUpdated() {
		t.Fatalf("old tuple must NOT carry HEAP_HOT_UPDATED on a non-HOT update")
	}
	if oldAfter.Header.Infomask&storage.HeapXmaxInvalid != 0 {
		t.Fatalf("old tuple must have HEAP_XMAX_INVALID cleared")
	}
	newAfter, err := storage.PageGetHeapTuple(page, 2)
	if err != nil {
		t.Fatal(err)
	}
	if newAfter.Header.Xmin != xmax {
		t.Fatalf("new t_xmin = %d, want %d", newAfter.Header.Xmin, xmax)
	}
	if newAfter.Header.CTID != (storage.ItemPointer{Block: 0, Offset: 2}) {
		t.Fatalf("new t_ctid = %+v, want self {0,2}", newAfter.Header.CTID)
	}
	if newAfter.Header.IsHeapOnly() {
		t.Fatalf("new tuple must NOT carry HEAP_ONLY_TUPLE")
	}
	if string(newAfter.Data) != "new" {
		t.Fatalf("new tuple data = %q, want %q", newAfter.Data, "new")
	}
}

// TestApplyRecordReplaysPGHeapUpdateCrossPage: the versions live on different
// pages — block 0 carries the new page + tuple, block 1 references the old
// page; replay stamps each page under its own pd_lsn idempotency.
func TestApplyRecordReplaysPGHeapUpdateCrossPage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 910, Fork: storage.MainFork}

	// Old version on block 0; a filler insert on block 1 so the target page
	// exists (replay of heap-update does not extend relations).
	oldTup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("old"))
	oldTup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	oldBytes, err := oldTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, oldBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insertFramed, 100)

	filler := storage.NewHeapTuple(43, storage.InvalidTransactionID, []byte("fill"))
	filler.Header.CTID = storage.ItemPointer{Block: 1, Offset: 1}
	fillerBytes, err := filler.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	fillerFramed, err := EncodeHeapInsertPG(rel, 1, 1, fillerBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, fillerFramed, 150)

	const xmax = storage.TransactionID(77)
	newTup := storage.NewHeapTuple(xmax, storage.InvalidTransactionID, []byte("new2"))
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapUpdatePG(rel, 0, 1, 1, 2, xmax, newBytes)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	oldPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, oldPage); err != nil {
		t.Fatal(err)
	}
	oldAfter, err := storage.PageGetHeapTuple(oldPage, 1)
	if err != nil {
		t.Fatal(err)
	}
	if oldAfter.Header.Xmax != xmax {
		t.Fatalf("old t_xmax = %d, want %d", oldAfter.Header.Xmax, xmax)
	}
	if oldAfter.Header.CTID != (storage.ItemPointer{Block: 1, Offset: 2}) {
		t.Fatalf("old t_ctid = %+v, want {1,2} (cross-page forward link)", oldAfter.Header.CTID)
	}
	if oldAfter.Header.IsHotUpdated() {
		t.Fatalf("old tuple must NOT carry HEAP_HOT_UPDATED")
	}

	newPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, newPage); err != nil {
		t.Fatal(err)
	}
	newAfter, err := storage.PageGetHeapTuple(newPage, 2)
	if err != nil {
		t.Fatal(err)
	}
	if newAfter.Header.Xmin != xmax {
		t.Fatalf("new t_xmin = %d, want %d", newAfter.Header.Xmin, xmax)
	}
	if newAfter.Header.CTID != (storage.ItemPointer{Block: 1, Offset: 2}) {
		t.Fatalf("new t_ctid = %+v, want self {1,2}", newAfter.Header.CTID)
	}
	if string(newAfter.Data) != "new2" {
		t.Fatalf("new tuple data = %q, want %q", newAfter.Data, "new2")
	}
}

// TestApplyRecordReplaysPGHeapUpdateExtendsMissingNewPage pins M0131-S21d: the
// new-tuple page is acquired the way upstream's heap_xlog_update acquires it —
// XLogReadBufferExtended, which zero-extends the fork when the record names a
// block past its end (xlogutils.c:479-539) — instead of refusing the whole
// start with "block N does not exist" / "block N is uninitialised".
//
// This is not an exotic shape: a real-PG crash tail routinely references pages
// past the last flushed one, and an UPDATE that moves a row to a freshly
// extended page is exactly this record. Before the fix,
// TestE2E_GoopgCrashStartOnPGDataDir stopped here at replay record 43900.
func TestApplyRecordReplaysPGHeapUpdateExtendsMissingNewPage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 911, Fork: storage.MainFork}

	// Only block 0 exists; the update sends the new version to block 2, so
	// replay must create block 1 (an empty gap page) and block 2.
	oldTup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("old"))
	oldTup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	oldBytes, err := oldTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, oldBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insertFramed, 100)

	const xmax = storage.TransactionID(88)
	newTup := storage.NewHeapTuple(xmax, storage.InvalidTransactionID, []byte("moved"))
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapUpdatePG(rel, 0, 1, 2, 1, xmax, newBytes)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if nblocks != 3 {
		t.Fatalf("nblocks = %d, want 3 (gap page 1 + target page 2 extended)", nblocks)
	}
	gap := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, gap); err != nil {
		t.Fatal(err)
	}
	if !storage.IsNew(gap) {
		t.Fatalf("gap block 1 must stay empty; a later record fills it")
	}

	newPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 2, newPage); err != nil {
		t.Fatal(err)
	}
	newAfter, err := storage.PageGetHeapTuple(newPage, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(newAfter.Data) != "moved" {
		t.Fatalf("new tuple data = %q, want %q", newAfter.Data, "moved")
	}
	if newAfter.Header.CTID != (storage.ItemPointer{Block: 2, Offset: 1}) {
		t.Fatalf("new t_ctid = %+v, want self {2,1}", newAfter.Header.CTID)
	}

	oldPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, oldPage); err != nil {
		t.Fatal(err)
	}
	oldAfter, err := storage.PageGetHeapTuple(oldPage, 1)
	if err != nil {
		t.Fatal(err)
	}
	if oldAfter.Header.Xmax != xmax {
		t.Fatalf("old t_xmax = %d, want %d", oldAfter.Header.Xmax, xmax)
	}
	if oldAfter.Header.CTID != (storage.ItemPointer{Block: 2, Offset: 1}) {
		t.Fatalf("old t_ctid = %+v, want {2,1} (forward link to the extended page)", oldAfter.Header.CTID)
	}
}

// TestApplyRecordReplaysPGHeapUpdateSkipsMissingOldPage pins the other half of
// M0131-S21d's asymmetry: block 1 (the OLD version's page) is upstream's
// RBM_NORMAL read, so a page that is absent or all-zero yields BLK_NOTFOUND and
// the stamp is silently skipped — the relation is dropped or truncated later in
// the same stream. Only the new-tuple page may be extended into existence.
func TestApplyRecordReplaysPGHeapUpdateSkipsMissingOldPage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 912, Fork: storage.MainFork}

	seed := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("seed"))
	seed.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	seedBytes, err := seed.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, seedBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insertFramed, 100)

	const xmax = storage.TransactionID(66)
	newTup := storage.NewHeapTuple(xmax, storage.InvalidTransactionID, []byte("kept"))
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	// Old version claims block 9, which does not exist.
	framed, err := EncodeHeapUpdatePG(rel, 9, 1, 0, 2, xmax, newBytes)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	newAfter, err := storage.PageGetHeapTuple(page, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(newAfter.Data) != "kept" {
		t.Fatalf("new tuple data = %q, want %q — the new version must land even though the old page is gone", newAfter.Data, "kept")
	}
	if nblocks, err := mgr.NBlocks(rel); err != nil {
		t.Fatal(err)
	} else if nblocks != 1 {
		t.Fatalf("nblocks = %d, want 1 — a missing OLD page must not extend the fork", nblocks)
	}
}

// TestApplyRecordReplaysPGHeapUpdatePrefixSuffixFromOld pins M0131-S21g: a real
// PG's log_heap_update (heapam.c:8730-8800) does not log the bytes the new
// version shares with the old one. When the two tuples share a leading and/or
// trailing run inside the data area it logs `uint16 prefixlen` / `uint16
// suffixlen` in front of the xl_heap_header and drops those bytes, and
// heap_xlog_update (heapam_xlog.c:933-1005) splices them back in from the old
// tuple ON THE SAME PAGE (upstream asserts newblk == oldblk for both flags).
//
// goopg's own emit never sets either flag, so nothing in a goopg↔goopg stream
// exercises this — but a real PG's stream is full of it, and the damage was
// silent rather than loud: replay wrote the record's *middle* bytes as if they
// were the whole tuple. That is what left a crashed PG's `CREATE TABLE`
// half-recovered — pg_class's relhasindex flip compresses to a ~4-byte record,
// so the reload found the table's index and toast rows with a 4-byte husk where
// the table row should be, and `s28_items` did not exist
// (TestE2E_GoopgCrashStartOnPGDataDir).
func TestApplyRecordReplaysPGHeapUpdatePrefixSuffixFromOld(t *testing.T) {
	const (
		prefix = "PREFIX__"
		suffix = "__SUFFIX"
	)
	cases := []struct {
		name          string
		oldData       string
		newData       string
		prefixLen     int
		suffixLen     int
		flags         uint8
		wantBlockData int // bytes of tuple body actually logged
	}{
		{
			name:      "prefix and suffix",
			oldData:   prefix + "OLDMID" + suffix,
			newData:   prefix + "NEWMIDDLE" + suffix,
			prefixLen: len(prefix), suffixLen: len(suffix),
			flags:         xlhUpdatePrefixFromOld | xlhUpdateSuffixFromOld,
			wantBlockData: len("NEWMIDDLE"),
		},
		{
			name:      "prefix only",
			oldData:   prefix + "OLDTAIL",
			newData:   prefix + "NEWTAILLONGER",
			prefixLen: len(prefix), suffixLen: 0,
			flags:         xlhUpdatePrefixFromOld,
			wantBlockData: len("NEWTAILLONGER"),
		},
		{
			name:      "suffix only",
			oldData:   "OLDHEAD" + suffix,
			newData:   "NEWHEADLONGER" + suffix,
			prefixLen: 0, suffixLen: len(suffix),
			flags:         xlhUpdateSuffixFromOld,
			wantBlockData: len("NEWHEADLONGER"),
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
			defer mgr.Close()

			rel := storage.RelFileNode{DBOid: 1, RelOid: uint32(920 + i), Fork: storage.MainFork}

			oldTup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte(tc.oldData))
			oldTup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
			oldBytes, err := oldTup.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, oldBytes, false)
			if err != nil {
				t.Fatal(err)
			}
			applyPGRecord(t, mgr, insertFramed, 100)

			const xmax = storage.TransactionID(77)
			newTup := storage.NewHeapTuple(xmax, storage.InvalidTransactionID, []byte(tc.newData))
			newBytes, err := newTup.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}

			// Hand-build the COMPRESSED record: goopg's encoders deliberately
			// never emit this shape, so the pin has to speak PG's bytes
			// directly — lengths first, then xl_heap_header, then only the
			// bytes that differ.
			headerAndData := heapHeaderPlusData(newBytes)
			header := headerAndData[:sizeOfXLogHeapHeaderData]
			body := headerAndData[sizeOfXLogHeapHeaderData:]
			// hoffExtra (the null bitmap + its padding — here just PG's
			// MAXALIGN byte) is logged in full and precedes the prefix, exactly
			// as upstream writes it.
			hoffExtra := int(header[4]) - storage.SizeOfHeapTupleHeaderData
			logged := body[:hoffExtra]
			middle := body[hoffExtra+tc.prefixLen : len(body)-tc.suffixLen]
			if len(middle) != tc.wantBlockData {
				t.Fatalf("test setup: logged body = %d bytes, want %d", len(middle), tc.wantBlockData)
			}
			blockData := make([]byte, 0, 4+len(header)+len(logged)+len(middle))
			if tc.flags&xlhUpdatePrefixFromOld != 0 {
				blockData = binary.LittleEndian.AppendUint16(blockData, uint16(tc.prefixLen))
			}
			if tc.flags&xlhUpdateSuffixFromOld != 0 {
				blockData = binary.LittleEndian.AppendUint16(blockData, uint16(tc.suffixLen))
			}
			blockData = append(blockData, header...)
			blockData = append(blockData, logged...)
			blockData = append(blockData, middle...)

			mainData := make([]byte, sizeOfXLogHeapUpdateData)
			binary.LittleEndian.PutUint32(mainData[0:4], uint32(xmax)) // old_xmax
			binary.LittleEndian.PutUint16(mainData[4:6], 1)            // old_offnum
			mainData[7] = xlhUpdateContainsNewTuple | tc.flags
			binary.LittleEndian.PutUint16(mainData[12:14], 2) // new_offnum

			recBody, err := assembleXLogRecord(mainData, []BlockRef{{
				ID: 0, Rel: rel, Block: 0, Data: blockData,
			}})
			if err != nil {
				t.Fatal(err)
			}
			applyPGRecord(t, mgr, framePGAssembled(RmgrHeap, xlogHeapUpdate, uint32(xmax), recBody), 200)

			page := make(storage.Page, storage.BlockSize)
			if err := mgr.ReadBlock(rel, 0, page); err != nil {
				t.Fatal(err)
			}
			newAfter, err := storage.PageGetHeapTuple(page, 2)
			if err != nil {
				t.Fatalf("new version at slot 2: %v", err)
			}
			if string(newAfter.Data) != tc.newData {
				t.Fatalf("new tuple data = %q, want %q — the prefix/suffix taken from the old "+
					"version on the same page was not spliced back in", newAfter.Data, tc.newData)
			}
			oldAfter, err := storage.PageGetHeapTuple(page, 1)
			if err != nil {
				t.Fatal(err)
			}
			if string(oldAfter.Data) != tc.oldData {
				t.Fatalf("old tuple data = %q, want %q (the splice must not disturb its source)", oldAfter.Data, tc.oldData)
			}
			if oldAfter.Header.Xmax != xmax {
				t.Fatalf("old t_xmax = %d, want %d", oldAfter.Header.Xmax, xmax)
			}
		})
	}
}
