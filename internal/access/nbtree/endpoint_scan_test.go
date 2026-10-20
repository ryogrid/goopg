package nbtree

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestScanFromEndVisitsEntriesInReverseKeyOrder pins ScanFromEnd, the backward
// read get_actual_variable_endpoint needs for a column's maximum (M0146-0233):
// over a multi-level tree its first entry is the largest key and the walk
// returns every entry in exactly reverse key order across the leaf links.
func TestScanFromEndVisitsEntriesInReverseKeyOrder(t *testing.T) {
	const rows = 20000
	desc := int4Desc()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 256})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9218, Fork: storage.MainFork}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	tidOf := func(g int) storage.ItemPointer {
		return storage.ItemPointer{Block: storage.BlockNumber((g - 1) / 100), Offset: uint16((g-1)%100 + 1)}
	}
	// Insert in a scrambled order so splits are not all rightmost.
	for i := 0; i < rows; i++ {
		g := (i*7919)%rows + 1
		v := make([]byte, 4)
		binary.LittleEndian.PutUint32(v, uint32(g))
		if err := bt.Insert(tup(t, desc.Attrs, [][]byte{v}, tidOf(g)), tidOf(g)); err != nil {
			t.Fatalf("Insert(%d): %v", g, err)
		}
	}
	want := rows
	if err := bt.ScanFromEnd(func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		if ptr != tidOf(want) {
			t.Fatalf("entry %d from the end has TID %v, want %v (key %d)", rows-want, ptr, tidOf(want), want)
		}
		want--
		return true, nil
	}); err != nil {
		t.Fatalf("ScanFromEnd: %v", err)
	}
	if want != 0 {
		t.Fatalf("ScanFromEnd stopped with %d entries unvisited", want)
	}
	// Stopping after the first entry reads only the largest key.
	n := 0
	_ = bt.ScanFromEnd(func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		n++
		if ptr != tidOf(rows) {
			t.Fatalf("first entry from the end is %v, want %v", ptr, tidOf(rows))
		}
		return false, nil
	})
	if n != 1 {
		t.Fatalf("fn ran %d times after declining", n)
	}
}
