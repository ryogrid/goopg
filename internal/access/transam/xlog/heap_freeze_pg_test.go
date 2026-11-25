package xlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeHeapFreezePGRoundTripAndReplay checks the freeze half of the
// composite xl_heap_prune: the record decodes back to the same frozen slots (via
// the freeze plan's trailing offset array), and replay rewrites the tuple's xmin
// to FrozenTransactionId.
func TestEncodeHeapFreezePGRoundTripAndReplay(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 908, Fork: storage.MainFork}

	// Decode round-trip.
	frozen := []uint16{1, 3, 5}
	framed, err := EncodeHeapFreezePG(rel, 2, freezeTestPage(t, 5, nil), frozen)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Header.Rmid != RmgrHeap2 || dec.Header.Info != xlogHeap2PruneVacuumClean {
		t.Fatalf("rmid/info = %d/%#x, want RmgrHeap2/VACUUM_CLEANUP", dec.Header.Rmid, dec.Header.Info)
	}
	block, ok := xlogBlockRefByID(dec.XLog, 0)
	if !ok {
		t.Fatalf("decoded record missing block 0")
	}
	gotR, _, gotU, gotF, err := decodeXLogHeapPrune(dec.XLog.MainData, block.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotR) != 0 || len(gotU) != 0 {
		t.Fatalf("unexpected redirect/unused: %v / %v", gotR, gotU)
	}
	if !reflect.DeepEqual(gotF, frozen) {
		t.Fatalf("frozen slots = %v, want %v", gotF, frozen)
	}

	// Real replay: insert a tuple, then freeze it.
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	tup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("v"))
	tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	tupBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insFramed, err := EncodeHeapInsertPG(rel, 0, 1, tupBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insFramed, 100)

	frzFramed, err := EncodeHeapFreezePG(rel, 0, freezeTestPage(t, 1, nil), []uint16{1})
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, frzFramed, 200)

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	frozenTup, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if frozenTup.Header.Xmin != storage.FrozenTransactionID {
		t.Fatalf("frozen t_xmin = %d, want FrozenTransactionID (%d)", frozenTup.Header.Xmin, storage.FrozenTransactionID)
	}
}

// freezeTestPage builds a heap page of n one-column tuples whose
// t_infomask2 is 1 (natts) and t_infomask HEAP_XMAX_INVALID, except that
// infomask overrides the infomask of the slots it names.
func freezeTestPage(t *testing.T, n int, infomask map[uint16]uint16) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		tup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("v"))
		tup.Header.Infomask2 = 1
		tup.Header.Infomask = 0x0800 // HEAP_XMAX_INVALID
		if m, ok := infomask[uint16(i)]; ok {
			tup.Header.Infomask = m
		}
		if _, err := storage.PageAddHeapTuple(page, tup); err != nil {
			t.Fatal(err)
		}
	}
	return page
}

// TestEncodeHeapFreezePGPlanLayout pins the block-0 bytes against PG's C
// layout: xlhp_freeze_plans' nplans + 2 pad bytes, then one 12-byte
// xlhp_freeze_plan whose ntuples sits at offset 10 (a pad byte follows
// frzflags), then the offsets. The unpadded 11-byte plan goopg used to write
// made PG 18.3 pg_waldump read `ntuples: 256` for a one-slot plan.
//
// The plan carries the frozen tuples' own header fields (M0149-0002): PG's
// redo stores them into each tuple, so the zeros goopg used to write left
// natts 0 and a PG server read every column as NULL.
func TestEncodeHeapFreezePGPlanLayout(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 908, Fork: storage.MainFork}
	frozen := []uint16{1, 3, 5}
	framed, err := EncodeHeapFreezePG(rel, 2, freezeTestPage(t, 5, nil), frozen)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatal(err)
	}
	block, ok := xlogBlockRefByID(dec.XLog, 0)
	if !ok {
		t.Fatal("decoded record missing block 0")
	}
	want := []byte{
		1, 0, 0, 0, // nplans = 1, pad
		0, 0, 0, 0, // xmax
		1, 0, 0, 0x08, // t_infomask2 = 1, t_infomask = HEAP_XMAX_INVALID
		0, 0, // frzflags, pad
		3, 0, // ntuples
		1, 0, 3, 0, 5, 0, // frz_offsets
	}
	if !reflect.DeepEqual(block.Data, want) {
		t.Fatalf("block 0 data = %v, want PG layout %v", block.Data, want)
	}

	// Tuples with different headers get one plan each, sorted by
	// (xmax, infomask2, infomask) as heap_log_freeze_cmp does, with the
	// offsets in plan order.
	page := freezeTestPage(t, 3, map[uint16]uint16{2: 0x0900}) // + HEAP_XMIN_COMMITTED
	framed, err = EncodeHeapFreezePG(rel, 2, page, []uint16{3, 2, 1})
	if err != nil {
		t.Fatal(err)
	}
	if record, _, err = encodeRecordXLog(framed, 0); err != nil {
		t.Fatal(err)
	}
	if dec, err = decodeRecordXLogDetailed(record); err != nil {
		t.Fatal(err)
	}
	block, _ = xlogBlockRefByID(dec.XLog, 0)
	want = []byte{
		2, 0, 0, 0, // nplans = 2, pad
		0, 0, 0, 0, 1, 0, 0, 0x08, 0, 0, 2, 0, // xmax 0, natts 1, XMAX_INVALID: 2 tuples
		0, 0, 0, 0, 1, 0, 0, 0x09, 0, 0, 1, 0, // xmax 0, natts 1, XMAX_INVALID|XMIN_COMMITTED: 1 tuple
		1, 0, 3, 0, 2, 0, // frz_offsets
	}
	if !reflect.DeepEqual(block.Data, want) {
		t.Fatalf("two-plan block 0 data = %v, want %v", block.Data, want)
	}
}

// TestDecodeXLogHeapPruneLegacyFreezePlan checks that WAL written before the
// padding fix — one unpadded 11-byte plan, recognised by its odd length —
// still decodes to the same frozen slots on replay.
func TestDecodeXLogHeapPruneLegacyFreezePlan(t *testing.T) {
	legacy := []byte{
		1, 0, 0, 0, // nplans = 1, pad
		0, 0, 0, 0, 0, 0, 0, 0, // xmax, t_infomask2, t_infomask
		0,    // frzflags (no pad)
		2, 0, // ntuples
		4, 0, 7, 0, // frz_offsets
	}
	mainData := []byte{0, xlhpHasFreezePlans | xlhpCleanupLock}
	_, _, _, gotF, err := decodeXLogHeapPrune(mainData, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotF, []uint16{4, 7}) {
		t.Fatalf("legacy frozen slots = %v, want [4 7]", gotF)
	}
}

// TestPGWaldumpReadsFreezePlan runs PG 18.3's own pg_waldump over a goopg
// freeze record: heapdesc.c casts the block data to xlhp_freeze_plan[], so
// the plan must decode as one plan of three offsets. With the unpadded
// 11-byte plan it printed `ntuples: 256` (the pad slot's high byte) for
// this one three-offset plan.
func TestPGWaldumpReadsFreezePlan(t *testing.T) {
	waldump := findPGWaldump(t)

	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: DefaultSegmentSize,
		Preallocate: true,
		PageHeaders: true,
		SystemID:    0xABCDEF0123456789,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 1001, Fork: storage.MainFork}
	frz, err := EncodeHeapFreezePG(rel, 0, freezeTestPage(t, 5, nil), []uint16{1, 3, 5})
	if err != nil {
		t.Fatal(err)
	}
	var firstStart, lastStart, end uint64
	for i, rec := range [][]byte{EncodeCheckpoint(), frz, EncodeXactCommit(storage.TransactionID(42))} {
		start, nextEnd, err := w.Append(rec)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstStart = start
		}
		lastStart, end = start, nextEnd
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	startSeg := firstSegmentName(t, walDir)
	if len(startSeg) == 24 && startSeg[:8] == "00000000" {
		raw, err := os.ReadFile(filepath.Join(walDir, startSeg))
		if err != nil {
			t.Fatal(err)
		}
		startSeg = "000000010000000000000000"
		if err := os.WriteFile(filepath.Join(walDir, startSeg), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// See TestPGWaldumpParsesEmittedWAL for why STARTSEG and -e are explicit.
	cmd := exec.Command(waldump, filepath.Join(walDir, startSeg), "-t", "1",
		"-s", lsnToRecPtr(firstStart), "-e", lsnToRecPtr(lastStart), "-r", "Heap2")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pg_waldump failed: %v\n%s", err, out)
	}
	const want = "nplans: 1, nredirected: 0, ndead: 0, nunused: 0, plans: [{ xmax: 0, infomask: 2048, infomask2: 1, ntuples: 3, offsets: [1, 3, 5] }]"
	if !strings.Contains(string(out), want) {
		t.Fatalf("pg_waldump output lacks %q:\n%s", want, out)
	}
}
