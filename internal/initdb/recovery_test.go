package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
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
	bt, err := btree.Create(rt.Pool, rel)
	if err != nil {
		t.Fatalf("btree.Create: %v", err)
	}
	const N = 800
	for i := 0; i < N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(btree.EncodeInt4(int32(i)), ptr); err != nil {
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
	bt2, err := btree.Open(rt2.Pool, rel)
	if err != nil {
		t.Fatalf("btree.Open after recovery: %v", err)
	}
	for i := 0; i < N; i++ {
		ptr, ok, err := bt2.Search(btree.EncodeInt4(int32(i)))
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
