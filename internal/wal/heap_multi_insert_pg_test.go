package wal

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21a-2: XLOG_HEAP2_MULTI_INSERT redo -----------------------------
//
// goopg never emits this opcode (its own COPY path emits per-tuple records), so
// before S21a-2 a PG crash tail containing any bulk load refused the start at
// the first MULTI_INSERT. These guards drive the real replay function through
// the same encode→decode→ApplyRecord path the other PG-format heap tests use,
// with the record bytes hand-built here because there is no goopg encoder for a
// record goopg does not produce.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21a-2.

// buildMultiInsertPG assembles a real xl_heap_multi_insert record.
//
// Main data is {uint8 flags; uint16 ntuples; OffsetNumber offsets[]} with
// ntuples at byte 2 (C alignment) and the offsets array present only when the
// page is NOT being reinitialised. Block 0's data is the SHORTALIGNed run of
// xl_multi_insert_tuple{datalen, t_infomask2, t_infomask, t_hoff} + tuple bytes
// past the fixed 23-byte header. Mirrors upstream log_heap_multi_insert
// (heapam.c:2560-2700).
func buildMultiInsertPG(t *testing.T, rel storage.RelFileNode, blk storage.BlockNumber, xid uint32,
	flags uint8, offsets []uint16, tuples [][]byte, initPage bool,
) []byte {
	t.Helper()
	mainData := make([]byte, sizeOfXLogHeapMultiInsertData)
	mainData[0] = flags
	binary.LittleEndian.PutUint16(mainData[2:4], uint16(len(tuples)))
	if !initPage {
		for _, off := range offsets {
			mainData = binary.LittleEndian.AppendUint16(mainData, off)
		}
	}

	body, err := assembleXLogRecord(mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: blk, Data: multiInsertBlockData(tuples), WillInit: initPage,
	}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	info := xlogHeap2MultiInsert
	if initPage {
		info |= xlogHeapInit
	}
	return framePGAssembled(RmgrHeap2, info, xid, body)
}

// multiInsertBlockData builds block 0's data run: the SHORTALIGNed sequence of
// xl_multi_insert_tuple headers, each followed by its tuple bytes.
func multiInsertBlockData(tuples [][]byte) []byte {
	var out []byte
	for _, tup := range tuples {
		if len(out)&1 != 0 {
			out = append(out, 0) // SHORTALIGN
		}
		data := tup[storage.SizeOfHeapTupleHeaderData:]
		hdr := make([]byte, sizeOfXLogMultiInsertTuple)
		binary.LittleEndian.PutUint16(hdr[0:2], uint16(len(data))) // datalen
		copy(hdr[2:6], tup[18:22])                                 // t_infomask2 + t_infomask
		hdr[6] = tup[22]                                           // t_hoff
		out = append(out, hdr...)
		out = append(out, data...)
	}
	return out
}

// multiInsertTuples builds n marshaled heap tuples with the given xmin.
func multiInsertTuples(t *testing.T, xid storage.TransactionID, vals ...string) [][]byte {
	t.Helper()
	out := make([][]byte, 0, len(vals))
	for _, v := range vals {
		tup := storage.NewHeapTuple(xid, storage.InvalidTransactionID, []byte(v))
		raw, err := tup.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary: %v", err)
		}
		out = append(out, raw)
	}
	return out
}

// applyPGRecordErr is applyPGRecord's non-fatal twin: the refusal guards below
// need the replay error rather than a t.Fatal on it.
func applyPGRecordErr(t *testing.T, mgr *storage.Manager, framed []byte, endLSN uint64) error {
	t.Helper()
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatalf("encodeRecordXLog: %v", err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatalf("decodeRecordXLogDetailed: %v", err)
	}
	_, err = ApplyRecord(mgr, Record{EndLSN: endLSN, XLog: dec.XLog, Payload: dec.Payload})
	return err
}

// TestApplyRecordReplaysPGHeapMultiInsertInitPage is the COPY-onto-a-fresh-page
// case: info == 0xD0 (MULTI_INSERT | XLOG_HEAP_INIT_PAGE), no offsets array,
// tuples land at FirstOffsetNumber+i. This is the shape S21a-1's mask fix was
// specifically about — under the old 0xF0 mask it matched no case at all.
func TestApplyRecordReplaysPGHeapMultiInsertInitPage(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 4021, Fork: storage.MainFork}
	const xid = storage.TransactionID(77)
	tuples := multiInsertTuples(t, xid, "alpha", "beta", "gamma")

	framed := buildMultiInsertPG(t, rel, 0, uint32(xid), 0, nil, tuples, true)
	if err := applyPGRecordErr(t, mgr, framed, 500); err != nil {
		t.Fatalf("ApplyRecord: %v", err)
	}

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	if got := storage.MustHeader(page).LSN(); got != 500 {
		t.Fatalf("pd_lsn = %d, want 500", got)
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		slot := uint16(1 + i)
		tup, err := storage.PageGetHeapTuple(page, slot)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		if string(tup.Data) != want {
			t.Fatalf("slot %d data = %q, want %q", slot, tup.Data, want)
		}
		if tup.Header.Xmin != xid {
			t.Fatalf("slot %d xmin = %d, want %d (xmin comes from xl_xid)", slot, tup.Header.Xmin, xid)
		}
		if tup.Header.Xmax != storage.InvalidTransactionID {
			t.Fatalf("slot %d xmax = %d, want invalid", slot, tup.Header.Xmax)
		}
		// A fresh insert self-points t_ctid, exactly as the single-tuple
		// xl_heap_insert redo (and the primary) do.
		if tup.Header.CTID.Block != 0 || tup.Header.CTID.Offset != slot {
			t.Fatalf("slot %d t_ctid = (%d,%d), want (0,%d)", slot,
				tup.Header.CTID.Block, tup.Header.CTID.Offset, slot)
		}
	}
}

// TestApplyRecordReplaysPGHeapMultiInsertOffsets is the non-init case: the page
// already holds tuples, so the record carries an explicit offsets array and the
// info byte has no 0x80 bit. Decoding the array is the half that goes silently
// wrong if the reader assumes the init-page layout — the tuples would land at
// 1..n and overwrite the rows already there.
func TestApplyRecordReplaysPGHeapMultiInsertOffsets(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 4022, Fork: storage.MainFork}

	// Seed one row through the ordinary PG heap-insert path.
	seed := multiInsertTuples(t, 10, "seed")[0]
	insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, seed, true)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insertFramed, 100)

	const xid = storage.TransactionID(11)
	tuples := multiInsertTuples(t, xid, "second", "third")
	framed := buildMultiInsertPG(t, rel, 0, uint32(xid), 0, []uint16{2, 3}, tuples, false)
	if err := applyPGRecordErr(t, mgr, framed, 200); err != nil {
		t.Fatalf("ApplyRecord: %v", err)
	}

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	for slot, want := range map[uint16]string{1: "seed", 2: "second", 3: "third"} {
		tup, err := storage.PageGetHeapTuple(page, slot)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		if string(tup.Data) != want {
			t.Fatalf("slot %d data = %q, want %q", slot, tup.Data, want)
		}
	}
}

// TestApplyRecordPGHeapMultiInsertIsIdempotent: replay must be re-runnable — a
// crash during recovery re-reads the same records. pd_lsn is the guard; without
// it the second pass would append the tuples a second time at slots 4..6.
func TestApplyRecordPGHeapMultiInsertIsIdempotent(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 4023, Fork: storage.MainFork}
	tuples := multiInsertTuples(t, 5, "one", "two")
	framed := buildMultiInsertPG(t, rel, 0, 5, 0, nil, tuples, true)

	if err := applyPGRecordErr(t, mgr, framed, 300); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := applyPGRecordErr(t, mgr, framed, 300); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("line pointers = %d, want 2 (replay applied the record twice)", count)
	}
}

// TestReplayZeroExtendsPastTheReplayGap pins the OTHER half of S21a-2: a record
// referencing a block past the fork's flushed length is NOT an error.
//
// The primary extends a relation in shared buffers and logs the insert; the
// extension itself is not WAL-logged, so a crash tail routinely references a
// block the file does not have yet. Upstream zero-extends up to it
// (XLogReadBufferExtended, xlogutils.c:479-539); goopg answered "replay gap
// block=N nblocks=M" and refused the whole start. Asserted on BOTH heap redo
// paths, since they now share redoHeapPageForBlock.
func TestReplayZeroExtendsPastTheReplayGap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(rel storage.RelFileNode, blk storage.BlockNumber) []byte
	}{
		{"multi-insert", func(rel storage.RelFileNode, blk storage.BlockNumber) []byte {
			return buildMultiInsertPG(t, rel, blk, 9, 0, nil, multiInsertTuples(t, 9, "far"), true)
		}},
		{"insert", func(rel storage.RelFileNode, blk storage.BlockNumber) []byte {
			framed, err := EncodeHeapInsertPG(rel, blk, 1, multiInsertTuples(t, 9, "far")[0], true)
			if err != nil {
				t.Fatal(err)
			}
			return framed
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
			defer mgr.Close()
			rel := storage.RelFileNode{DBOid: 1, RelOid: 4024, Fork: storage.MainFork}

			// One existing block, then a record for block 3 — blocks 1 and 2
			// are the gap.
			seed := multiInsertTuples(t, 8, "seed")[0]
			insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, seed, true)
			if err != nil {
				t.Fatal(err)
			}
			applyPGRecord(t, mgr, insertFramed, 100)

			if err := applyPGRecordErr(t, mgr, tc.build(rel, 3), 400); err != nil {
				t.Fatalf("ApplyRecord across the replay gap: %v", err)
			}
			nblocks, err := mgr.NBlocks(rel)
			if err != nil {
				t.Fatal(err)
			}
			if nblocks != 4 {
				t.Fatalf("nblocks = %d, want 4 (relation was not zero-extended to the referenced block)", nblocks)
			}
			page := make(storage.Page, storage.BlockSize)
			if err := mgr.ReadBlock(rel, 3, page); err != nil {
				t.Fatal(err)
			}
			tup, err := storage.PageGetHeapTuple(page, 1)
			if err != nil {
				t.Fatalf("target block: %v", err)
			}
			if string(tup.Data) != "far" {
				t.Fatalf("target block slot 1 = %q, want %q", tup.Data, "far")
			}
			// The gap pages must be readable and empty, not garbage.
			for _, blk := range []storage.BlockNumber{1, 2} {
				gap := make(storage.Page, storage.BlockSize)
				if err := mgr.ReadBlock(rel, blk, gap); err != nil {
					t.Fatalf("gap block %d: %v", blk, err)
				}
				if !storage.IsNew(gap) {
					t.Fatalf("gap block %d is not empty", blk)
				}
			}
		})
	}
}

// TestReplayPGHeapMultiInsertRejectsMalformedBlockData: upstream PANICs with
// "total tuple length mismatch" when the tuple walk does not consume block 0's
// data run exactly. A short walk means the record was mis-parsed and the
// unconsumed bytes are a tuple that would be silently dropped — data loss that
// reports success — so the decoder must refuse rather than apply a partial page.
func TestReplayPGHeapMultiInsertRejectsMalformedBlockData(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4025, Fork: storage.MainFork}
	tuples := multiInsertTuples(t, 3, "aa", "bb")

	// Both subtests carry two tuples' worth of block data and LIE about ntuples
	// in main data — the one field redo cannot cross-check against anything but
	// the run's own length.
	build := func(ntuples uint16) []byte {
		mainData := make([]byte, sizeOfXLogHeapMultiInsertData)
		binary.LittleEndian.PutUint16(mainData[2:4], ntuples)
		body, err := assembleXLogRecord(mainData, []BlockRef{{
			ID: 0, Rel: rel, Block: 0, Data: multiInsertBlockData(tuples), WillInit: true,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return framePGAssembled(RmgrHeap2, xlogHeap2MultiInsert|xlogHeapInit, 3, body)
	}

	t.Run("trailing-bytes", func(t *testing.T) {
		// Claim one tuple while the run carries two: the second is unconsumed
		// and would be silently dropped.
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		err := applyPGRecordErr(t, mgr, build(1), 600)
		if err == nil {
			t.Fatal("replay accepted a record whose tuple walk left bytes unconsumed")
		}
		if !strings.Contains(err.Error(), "total tuple length mismatch") {
			t.Fatalf("err = %v, want the total-tuple-length-mismatch refusal", err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		// Claim three tuples while the run carries two: the walk runs off the end.
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
		defer mgr.Close()
		if err := applyPGRecordErr(t, mgr, build(3), 700); err == nil {
			t.Fatal("replay accepted a record claiming more tuples than its block data holds")
		}
	})
}

// TestReplayPGHeapMultiInsertRefusesLinePointerReuse: upstream's
// PageAddItem(overwrite=true) can fill an already-allocated (dead) line pointer
// in place; goopg's PageInsertItemRawAt would SHIFT the array instead and
// displace the tuple already at that slot. The refusal is the honest answer
// until redo grows in-place reuse — a shifted page is silent corruption that
// only shows up as a wrong ctid much later. Ledger row: M0131-S21a-2.
func TestReplayPGHeapMultiInsertRefusesLinePointerReuse(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 4026, Fork: storage.MainFork}

	// Two rows on the page, then a record targeting slot 1 — already in use.
	// The seeding order matters (slot 2 cannot be inserted before slot 1
	// exists), so this is an ordered slice, not a map: as a map literal the
	// test failed on roughly half of all runs with "slot 2 out of range".
	for _, seed := range []struct {
		slot uint16
		val  string
	}{{1, "a"}, {2, "b"}} {
		slot, val := seed.slot, seed.val
		framed, err := EncodeHeapInsertPG(rel, 0, slot, multiInsertTuples(t, 4, val)[0], slot == 1)
		if err != nil {
			t.Fatal(err)
		}
		applyPGRecord(t, mgr, framed, uint64(100+slot))
	}
	framed := buildMultiInsertPG(t, rel, 0, 12, 0, []uint16{1}, multiInsertTuples(t, 12, "clash"), false)
	err := applyPGRecordErr(t, mgr, framed, 800)
	if err == nil {
		t.Fatal("replay accepted a multi-insert onto an already-allocated line pointer")
	}
	if !errors.Is(err, ErrUnsupportedRecord) {
		t.Fatalf("err = %v, want ErrUnsupportedRecord", err)
	}
}
