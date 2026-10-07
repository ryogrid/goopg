package storage

import (
	"os"
	"testing"
)

// TestVisibilityMapWALHookLogsEveryChangeOnce pins M0146-0063: the runtime map
// writes one change record for every set and clear that changes a block's bits,
// and none for a no-op — ClearBlock runs on every heap insert, nearly always
// on a page whose bits are already clear.
func TestVisibilityMapWALHookLogsEveryChangeOnce(t *testing.T) {
	type rec struct {
		rel   RelFileNode
		blk   BlockNumber
		flags uint8
	}
	var got []rec
	vm := NewVisibilityMap()
	vm.SetWALHook(func(rel RelFileNode, blk BlockNumber, flags uint8) error {
		got = append(got, rec{rel, blk, flags})
		return nil
	})
	rel := RelFileNode{DBOid: 5, RelOid: 16400, Fork: MainFork}

	vm.ClearBlock(rel, 0)     // no bits: no record
	vm.SetAllVisible(rel, 0)  // set visible
	vm.SetAllVisible(rel, 0)  // no change
	vm.SetAllFrozen(rel, 1)   // set visible + frozen
	vm.ClearAllFrozen(rel, 0) // not frozen: no record
	vm.ClearAllFrozen(rel, 1) // frozen only
	vm.ClearBlock(rel, 1)     // clear both
	vm.ClearBlock(rel, 1)     // no change
	vm.DropRelation(rel)      // drop
	vm.DropRelation(rel)      // already forgotten

	want := []rec{
		{rel, 0, VMWALSetAllVisible},
		{rel, 1, VMWALSetAllVisible | VMWALSetAllFrozen},
		{rel, 1, VMWALClearFrozenOnly},
		{rel, 1, 0},
		{rel, 0, VMWALDropRelation},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestVMSaveRemovesDroppedRelationsFork pins the save half of a drop: a
// TRUNCATE keeps the relfilenode, so the fork of a relation the map forgot must
// go at the next save, or the next load hands its new rows the old bits.
func TestVMSaveRemovesDroppedRelationsFork(t *testing.T) {
	dir := t.TempDir()
	vm := NewVisibilityMap()
	rel := RelFileNode{DBOid: 5, RelOid: 16401, Fork: MainFork}
	vm.SetAllVisible(rel, 0)
	if err := vm.VMSaveForks(dir, nil); err != nil {
		t.Fatal(err)
	}
	path := RelForkPath(dir, RelFileNode{DBOid: 5, RelOid: 16401, Fork: VisibilityMapFork})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: fork not written: %v", err)
	}
	vm.DropRelation(rel)
	if err := vm.VMSaveForks(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the dropped relation's fork survived the save (err=%v)", err)
	}
}
