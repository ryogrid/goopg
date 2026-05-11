package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// TestRuntimeCloseTriggersFinalCheckpoint is the M0089-0002
// durability gate. It verifies that Runtime.Close synchronously
// flushes + fsyncs all dirty heap pages before releasing file
// handles, so on-disk state at process exit fully reflects every
// successful in-memory write.
//
// Pre-fix scenario (the pgbench bug at scale 100):
//   - PinNew → Manager.Extend writes an empty init page to disk
//     and increments nblocks.
//   - The caller mutates the in-memory page (e.g. via the btree
//     insert path), marking the slot dirty.
//   - Pool.Close calls FlushAll which pwrites the dirty slot.
//   - But there is NO fsync. The bytes live in the OS page cache.
//   - Worse: heavy-concurrency eviction earlier in the workload
//     can already have flushed an OLDER snapshot of the page,
//     and the in-memory copy holding the FINAL content is the
//     one Pool.Close sees — but with a clean Pool.Close + Manager
//     Close + reopen on a busy box, the page cache may serve the
//     stale older flush (or, in the FSM-persisted case, the FSM
//     references a block whose file hasn't been extended at all).
//
// Post-fix: Runtime.Close calls Checkpointer.CheckpointNow() at
// its very top, which (via M0089-0001) flushes every dirty slot
// AND fsyncs every data file before any close happens. The
// file on disk is durable at process exit.
//
// The test forces the failure mode by inspecting the data file
// directly after Close — bypassing the buffer pool — and
// asserting the inserted btree entry is on disk.
func TestRuntimeCloseTriggersFinalCheckpoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rel := storage.RelFileNode{DBOid: 1, RelOid: 9700, Fork: storage.MainFork}

	// Phase 1: open, create a btree, insert ONE entry, then call
	// Close without any explicit checkpoint / save. Close must
	// flush + fsync the dirty page (with the inserted entry).
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open phase 1: %v", err)
	}
	bt, err := btree.Create(rt.Pool, rel)
	if err != nil {
		t.Fatalf("btree.Create: %v", err)
	}
	const wantBlock = storage.BlockNumber(42)
	ptr := storage.ItemPointer{Block: wantBlock, Offset: 7}
	if err := bt.Insert(btree.EncodeInt4(int32(1234)), ptr); err != nil {
		t.Fatalf("btree.Insert: %v", err)
	}

	// Snapshot the on-disk file size BEFORE Close to confirm the
	// extends happened (so the post-Close state is the durable
	// version of what's on disk now, not "the file was empty all
	// along"). Note: Extend's pwrite immediately grew the file —
	// the gap is the dirty content, not the file length.
	relPath := filepath.Join(dir, "base", "1", "9700")
	preCloseStat, err := os.Stat(relPath)
	if err != nil {
		t.Fatalf("stat %s before close: %v", relPath, err)
	}
	if preCloseStat.Size() == 0 {
		t.Fatalf("expected non-zero rel size before close, got 0 (extend did not run)")
	}

	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rt = nil

	// Phase 2: reopen the data dir. The btree entry must be
	// readable on the post-restart pool — exercising the durable
	// on-disk state, not in-memory carry-over.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open phase 2 (post-Close): %v", err)
	}
	defer rt2.Close()

	bt2, err := btree.Open(rt2.Pool, rel)
	if err != nil {
		t.Fatalf("btree.Open after restart: %v", err)
	}
	gotPtr, ok, err := bt2.Search(btree.EncodeInt4(int32(1234)))
	if err != nil {
		t.Fatalf("Search after restart: %v", err)
	}
	if !ok {
		t.Fatalf("inserted key missing after restart — final-checkpoint did not run")
	}
	if gotPtr.Block != wantBlock || gotPtr.Offset != 7 {
		t.Errorf("Search: got (%d, %d), want (%d, 7)", gotPtr.Block, gotPtr.Offset, wantBlock)
	}
}
