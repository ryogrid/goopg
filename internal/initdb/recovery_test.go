package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/storage"
)

// TestCrashRecoveryReplaysWALAfterUncleanShutdown is the M0002
// "Crash-recovery test" from the milestone Definition of Done.
// It exercises the FPI + atomic-split WAL records end-to-end:
//
//  1. open a fresh data dir, build a B-tree, insert enough keys to
//     force at least one split. Each mutation emits either an FPI
//     (first dirty per epoch) or a BtreeSplit record.
//  2. force the WAL durable, then close the Manager + WAL writer
//     WITHOUT flushing the buffer pool. That drops every dirty
//     in-memory page on the floor — exactly what SIGKILL does
//     mid-workload before bgwriter / checkpointer flush.
//  3. reopen the data dir via initdb.Open, which now invokes
//     wal.ReplayFromDirWithMgr before constructing the new pool.
//  4. assert every inserted key is searchable through the
//     reopened btree.
//
// Without recovery, step 4 would fail because the data files
// hold only the partial pre-crash state. With recovery, the WAL
// replay reconstructs the post-split images and the right pages
// that smgr.Extend never made fully durable.
func TestCrashRecoveryReplaysWALAfterUncleanShutdown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9200, Fork: storage.MainFork}

	// Phase 1: build a populated tree.
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open phase 1: %v", err)
	}
	bt, err := nbtree.Create(rt.Pool, rel)
	if err != nil {
		t.Fatalf("nbtree.Create: %v", err)
	}
	const N = 800
	for i := 0; i < N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(nbtree.EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}

	// Phase 2: simulate SIGKILL. Force WAL durable, close the
	// WAL writer + Manager directly, then drop every reference
	// without going through Pool.Close (which would FlushAll
	// the dirty pages we want to lose).
	if err := rt.WAL.FlushUpTo(rt.WAL.WrittenLSN()); err != nil {
		t.Fatalf("FlushUpTo: %v", err)
	}
	if err := rt.WAL.Close(); err != nil {
		t.Fatalf("WAL.Close: %v", err)
	}
	if err := rt.StorageMgr.Close(); err != nil {
		t.Fatalf("StorageMgr.Close: %v", err)
	}
	rt = nil

	// Phase 3: reopen. initdb.Open's first action is replay.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open phase 3 (post-crash): %v", err)
	}
	defer rt2.Close()

	// Phase 4: every inserted key must be searchable through a
	// fresh btree handle.
	bt2, err := nbtree.Open(rt2.Pool, rel)
	if err != nil {
		t.Fatalf("nbtree.Open after recovery: %v", err)
	}
	for i := 0; i < N; i++ {
		ptr, ok, err := bt2.Search(nbtree.EncodeInt4(int32(i)))
		if err != nil {
			t.Fatalf("Search(%d) after recovery: %v", i, err)
		}
		if !ok {
			t.Fatalf("key %d missing after recovery — WAL replay did not reconstruct it", i)
		}
		if int32(ptr.Block) != int32(i+1) {
			t.Errorf("Search(%d): block=%d want %d", i, ptr.Block, i+1)
		}
	}
}

// TestCrashRecoveryReplaysChecksummedClusterCleanly is the recovery-validation
// gate for the M0102-0010 `--data-checksums` default-ON flip. The flip is
// deferred until a checksummed cluster is proven to (a) replay its WAL — full
// page images included — after an unclean shutdown and (b) stream to a PG
// standby cleanly. This test closes half (a): it runs the same SIGKILL /
// WAL-replay sequence as TestCrashRecoveryReplaysWALAfterUncleanShutdown, but
// on a cluster initialized with DataChecksums=true, and then asserts that every
// recovered page carries a *valid* pd_checksum.
//
// Why this is the load-bearing check: with checksums on, Open() reads
// data_checksum_version=1 from pg_control and builds a checksum-enabled Manager
// (initdb/open.go), so recovery's FPI restore path
// (wal/recovery.go restoreDecodedXLogBlockImage → writeBlockOrExtend →
// Manager.WriteBlock → checksummedForWrite) must recompute pd_checksum for each
// replayed block. If it did not — e.g. if an FPI were written verbatim with a
// stale checksum, or replayed pages bypassed the checksum write seam — the
// Phase-4 btree reads would fail with *ChecksumError (reads verify on a
// checksum-enabled Manager), and the Phase-5 on-disk walk would find an invalid
// page. A green run is the architectural proof that FPI replay is
// checksum-correct.
func TestCrashRecoveryReplaysChecksummedClusterCleanly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, DataChecksums: true}); err != nil {
		t.Fatalf("Init with DataChecksums=true: %v", err)
	}
	if ctrl, err := control.ReadControlFile(dir); err != nil {
		t.Fatalf("ReadControlFile: %v", err)
	} else if ctrl.DataChecksumVersion != 1 {
		t.Fatalf("data_checksum_version = %d, want 1 (checksums not enabled)", ctrl.DataChecksumVersion)
	}

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9200, Fork: storage.MainFork}

	// Phase 1: build a multi-page tree on the checksum-enabled Manager. Every
	// WriteBlock here flows through checksummedForWrite, so the runtime pages
	// are checksummed as they are written; the splits also stage the FPI /
	// BtreeSplit WAL records that recovery will later replay.
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open phase 1: %v", err)
	}
	bt, err := nbtree.Create(rt.Pool, rel)
	if err != nil {
		t.Fatalf("nbtree.Create: %v", err)
	}
	const N = 800
	for i := 0; i < N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(nbtree.EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}

	// Phase 2: simulate SIGKILL — force WAL durable, then drop the Manager and
	// WAL writer WITHOUT flushing the dirty buffer pool (Pool.Close would
	// FlushAll, hiding the recovery work).
	if err := rt.WAL.FlushUpTo(rt.WAL.WrittenLSN()); err != nil {
		t.Fatalf("FlushUpTo: %v", err)
	}
	if err := rt.WAL.Close(); err != nil {
		t.Fatalf("WAL.Close: %v", err)
	}
	if err := rt.StorageMgr.Close(); err != nil {
		t.Fatalf("StorageMgr.Close: %v", err)
	}
	rt = nil

	// Phase 3: reopen. Open replays the WAL through a checksum-enabled Manager
	// (it reads data_checksum_version=1 from pg_control).
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open phase 3 (post-crash): %v", err)
	}
	defer rt2.Close()

	// Phase 4: every key must be searchable. These reads go through the
	// checksum-enabled Manager, so a replayed page with a bad pd_checksum would
	// surface here as a *ChecksumError rather than a wrong answer.
	bt2, err := nbtree.Open(rt2.Pool, rel)
	if err != nil {
		t.Fatalf("nbtree.Open after recovery: %v", err)
	}
	for i := 0; i < N; i++ {
		ptr, ok, err := bt2.Search(nbtree.EncodeInt4(int32(i)))
		if err != nil {
			t.Fatalf("Search(%d) after recovery: %v", i, err)
		}
		if !ok {
			t.Fatalf("key %d missing after recovery — WAL replay did not reconstruct it", i)
		}
		if int32(ptr.Block) != int32(i+1) {
			t.Errorf("Search(%d): block=%d want %d", i, ptr.Block, i+1)
		}
	}

	// Phase 5: comprehensive on-disk proof. Flush the recovered pages, then
	// walk every relation file (the catalog files stamped at init plus the
	// btree file rebuilt by replay) and verify each populated block carries a
	// valid pd_checksum under the same off/BlockSize convention the smgr uses.
	if err := rt2.Pool.FlushAll(); err != nil {
		t.Fatalf("FlushAll before on-disk verify: %v", err)
	}
	btreePath := filepath.Join(dir, "base", "1", "9200")
	if fi, err := os.Stat(btreePath); err != nil {
		t.Fatalf("stat recovered btree file %q: %v", btreePath, err)
	} else if fi.Size() <= storage.BlockSize {
		t.Fatalf("recovered btree file is %d bytes (<= one page); expected splits to span multiple pages", fi.Size())
	}

	relFiles := collectRelationFiles(t, dir)
	sawBtree := false
	checked, populated := 0, 0
	for _, path := range relFiles {
		if path == btreePath {
			sawBtree = true
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		for i := 0; i < len(raw)/storage.BlockSize; i++ {
			block := raw[i*storage.BlockSize : (i+1)*storage.BlockSize]
			checked++
			if !storage.VerifyPage(block, storage.BlockNumber(i)) {
				t.Errorf("post-recovery page verification failed: block %d of %q", i, path)
			}
			if !storage.IsNew(block) {
				populated++
				if storage.MustHeader(block).Checksum() == 0 {
					t.Errorf("post-recovery populated block %d of %q has zero pd_checksum", i, path)
				}
			}
		}
	}
	if !sawBtree {
		t.Fatalf("recovered btree file %q not among scanned relation files", btreePath)
	}
	if checked == 0 || populated == 0 {
		t.Fatalf("scanned %d pages (%d populated); expected populated pages post-recovery", checked, populated)
	}
}
