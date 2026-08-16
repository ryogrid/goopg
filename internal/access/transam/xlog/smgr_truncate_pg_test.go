package xlog

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21a-2 part 6: SMGR_TRUNCATE redo --------------------------------
//
// goopg emits its own truncate-to-zero as the native RecordKindSmgrTruncate
// (payload[0], routed through the native ApplyRecord switch), so this arm is
// reached only by a real-PG XLOG_SMGR_TRUNCATE — every TABLE/INDEX TRUNCATE
// and every VACUUM tail truncation. Unlike goopg's own record, upstream's
// carries a target block count (the main fork's SURVIVING prefix, not
// necessarily zero) and an independent flag per fork (storage.c:997-1094).
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21a-2 part 6.

// buildSmgrTruncatePG assembles a real xl_smgr_truncate record: BlockNumber
// blkno + RelFileLocator{spcOid,dbOid,relNumber} + int32 flags (20 bytes),
// no block references, on RM_SMGR with opcode 0x20.
func buildSmgrTruncatePG(t *testing.T, blkno uint32, rel storage.RelFileNode, flags uint32) []byte {
	t.Helper()
	mainData := make([]byte, 20)
	binary.LittleEndian.PutUint32(mainData[0:4], blkno)
	binary.LittleEndian.PutUint32(mainData[4:8], rel.TblOid)
	binary.LittleEndian.PutUint32(mainData[8:12], rel.DBOid)
	binary.LittleEndian.PutUint32(mainData[12:16], rel.RelOid)
	binary.LittleEndian.PutUint32(mainData[16:20], flags)
	body, err := assembleXLogRecord(mainData, nil)
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrStorage, xlogSmgrTruncate, 0, body)
}

// seedRelBlocks extends rel to n blocks of freshly-initialised pages.
func seedRelBlocks(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		page := make([]byte, storage.BlockSize)
		if err := storage.InitPage(storage.Page(page)); err != nil {
			t.Fatalf("InitPage: %v", err)
		}
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatalf("seed Extend: %v", err)
		}
	}
}

// TestApplyRecordReplaysPGSmgrTruncatePartialMainFork asserts SMGR_TRUNCATE_HEAP
// truncates the main fork to the record's blkno — a NON-zero surviving prefix,
// which goopg's own native truncate-to-zero record can never express.
func TestApplyRecordReplaysPGSmgrTruncatePartialMainFork(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 5, RelOid: 16400, Fork: storage.MainFork}
	seedRelBlocks(t, mgr, rel, 10)

	framed := buildSmgrTruncatePG(t, 3, rel, smgrTruncateHeap)
	applyPGRecord(t, mgr, framed, 100)

	n, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("NBlocks after truncate = %d, want 3", n)
	}

	// Idempotent second replay (already at/under blkno).
	applyPGRecord(t, mgr, framed, 200)
	if n, _ := mgr.NBlocks(rel); n != 3 {
		t.Fatalf("second replay not idempotent: NBlocks = %d", n)
	}
}

// TestApplyRecordReplaysPGSmgrTruncateVMAndFSMIndependently asserts the vm and
// fsm forks truncate to zero independently of the main fork and of each
// other, gated purely by their own flag bit.
func TestApplyRecordReplaysPGSmgrTruncateVMAndFSMIndependently(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	base := storage.RelFileNode{DBOid: 5, RelOid: 16401}
	mainRel := base
	mainRel.Fork = storage.MainFork
	vmRel := base
	vmRel.Fork = storage.VisibilityMapFork
	fsmRel := base
	fsmRel.Fork = storage.FSMFork

	seedRelBlocks(t, mgr, mainRel, 5)
	seedRelBlocks(t, mgr, vmRel, 2)
	seedRelBlocks(t, mgr, fsmRel, 2)

	// Only SMGR_TRUNCATE_VM: main and fsm untouched.
	framed := buildSmgrTruncatePG(t, 0, base, smgrTruncateVM)
	applyPGRecord(t, mgr, framed, 100)

	if n, _ := mgr.NBlocks(mainRel); n != 5 {
		t.Fatalf("main fork NBlocks = %d, want unchanged 5", n)
	}
	if n, _ := mgr.NBlocks(vmRel); n != 0 {
		t.Fatalf("vm fork NBlocks = %d, want 0", n)
	}
	if n, _ := mgr.NBlocks(fsmRel); n != 2 {
		t.Fatalf("fsm fork NBlocks = %d, want unchanged 2", n)
	}
}

// TestApplyRecordReplaysPGSmgrTruncateRecreatesDroppedRel asserts a truncate
// record whose main fork does not exist on disk (the relation was dropped
// later in the same WAL tail — smgr_redo's own rationale, storage.c:1010-1016)
// forcibly recreates it rather than erroring, matching upstream's "prefer to
// recreate the rel and replay the log as best we can until the drop is seen".
func TestApplyRecordReplaysPGSmgrTruncateRecreatesDroppedRel(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 5, RelOid: 16402, Fork: storage.MainFork}
	framed := buildSmgrTruncatePG(t, 0, rel, smgrTruncateHeap)
	applyPGRecord(t, mgr, framed, 100)

	// applySmgrCreate recreates the file with one init block; the
	// SMGR_TRUNCATE_HEAP truncate to blkno=0 then removes it again — matching
	// upstream, which always runs create-then-truncate in that order
	// (storage.c:1016-1041) even when the net effect is an empty file.
	n, err := mgr.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("recreated-then-truncated rel NBlocks = %d, want 0", n)
	}
}

// TestDecodeXLogSmgrTruncateTablespaceRemap asserts the default and global
// tablespace OIDs remap to goopg's TblOid=0 convention, mirroring
// decodeXLogSmgrCreate.
func TestDecodeXLogSmgrTruncateTablespaceRemap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		spcOid uint32
	}{
		{"default-tablespace", pgDefaultTableSpaceOID},
		{"global-tablespace", pgGlobalTableSpaceOID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mainData := make([]byte, 20)
			binary.LittleEndian.PutUint32(mainData[0:4], 7)
			binary.LittleEndian.PutUint32(mainData[4:8], tc.spcOid)
			binary.LittleEndian.PutUint32(mainData[8:12], 5)
			binary.LittleEndian.PutUint32(mainData[12:16], 16400)
			binary.LittleEndian.PutUint32(mainData[16:20], smgrTruncateHeap)
			rel, blkno, flags, err := decodeXLogSmgrTruncate(mainData)
			if err != nil {
				t.Fatal(err)
			}
			if rel.TblOid != 0 {
				t.Fatalf("TblOid = %d, want 0 (remapped)", rel.TblOid)
			}
			if blkno != 7 {
				t.Fatalf("blkno = %d, want 7", blkno)
			}
			if flags != smgrTruncateHeap {
				t.Fatalf("flags = %#x, want %#x", flags, smgrTruncateHeap)
			}
		})
	}
}
