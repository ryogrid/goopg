package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNativeHeapVisibleRedoKeepsTheForkCurrent pins M0146-0063: goopg's own
// RecordKindHeapVisible, written by the runtime visibility map's WAL hook for
// every set and clear, is applied to the relation's _vm fork by redo. Startup
// loads the fork after replay, so the map a crash start trusts is the one the
// crashed server had. Before this its ApplyRecord arm was a no-op.
func TestNativeHeapVisibleRedoKeepsTheForkCurrent(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	heap := storage.RelFileNode{DBOid: 5, RelOid: 16500}
	vmPath := storage.RelForkPath(dir, storage.RelFileNode{DBOid: 5, RelOid: 16500, Fork: storage.VisibilityMapFork})

	lsn := uint64(1000)
	apply := func(blk storage.BlockNumber, flags uint8) {
		t.Helper()
		lsn += 100
		r := Record{EndLSN: lsn, Payload: EncodeHeapVisible(HeapVisiblePayload{Rel: heap, HeapBlk: blk, Flags: flags})}
		if _, err := ApplyRecord(mgr, r); err != nil {
			t.Fatalf("ApplyRecord(blk %d, flags %#x): %v", blk, flags, err)
		}
	}
	bits := func() []uint8 {
		t.Helper()
		masks, err := storage.ReadVMFork(vmPath)
		if err != nil {
			t.Fatalf("ReadVMFork: %v", err)
		}
		return masks
	}
	at := func(m []uint8, blk int) uint8 {
		if blk < len(m) {
			return m[blk]
		}
		return 0
	}

	// A clear before any set creates no fork.
	apply(3, 0)
	if m := bits(); m != nil {
		t.Fatalf("a clear on an absent fork created one: %v", m[:4])
	}

	apply(0, HeapVisibleSetAllVisible)
	apply(1, HeapVisibleSetAllVisible|HeapVisibleSetAllFrozen)
	apply(2, HeapVisibleSetAllVisible|HeapVisibleSetAllFrozen)
	m := bits()
	if at(m, 0) != storage.VMAllVisible || at(m, 1) != storage.VMAllVisible|storage.VMAllFrozen || at(m, 2) != storage.VMAllVisible|storage.VMAllFrozen {
		t.Fatalf("after sets: blocks 0..2 = %v, want [1 3 3]", m[:3])
	}

	apply(0, 0)                          // a heap write on block 0
	apply(1, HeapVisibleClearFrozenOnly) // a row lock on block 1
	m = bits()
	if at(m, 0) != 0 || at(m, 1) != storage.VMAllVisible || at(m, 2) != storage.VMAllVisible|storage.VMAllFrozen {
		t.Fatalf("after clears: blocks 0..2 = %v, want [0 1 3]", m[:3])
	}

	// TRUNCATE keeps the relfilenode: the drop must empty the fork, or the
	// relation's new rows would inherit block 2's bits.
	apply(0, HeapVisibleDropRelation)
	if m := bits(); m != nil {
		t.Fatalf("after drop: fork still holds bits %v", m[:3])
	}
}
