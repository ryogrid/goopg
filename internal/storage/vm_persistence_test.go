package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVisibilityMapSaveLoadRoundTrip pins the M0130-S1
// persistence contract: VM state written via VMSaveForks and read
// back by VMLoadForks must produce equivalent bits per relation.
func TestVisibilityMapSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()

	src := NewVisibilityMap()
	relA := RelFileNode{DBOid: 1, RelOid: 16402, Fork: MainFork}
	relB := RelFileNode{DBOid: 1, RelOid: 16405, Fork: MainFork}
	src.SetAllVisible(relA, 0)
	src.SetAllVisible(relA, 3)
	src.SetAllVisible(relA, 5)
	src.SetAllVisible(relB, 0)

	if err := src.VMSaveForks(dir, nil); err != nil {
		t.Fatalf("VMSaveForks: %v", err)
	}

	dst := NewVisibilityMap()
	if err := dst.VMLoadForks(dir); err != nil {
		t.Fatalf("VMLoadForks: %v", err)
	}

	// Per-block verification.
	for blk := BlockNumber(0); blk <= 5; blk++ {
		want := src.AllVisible(relA, blk)
		got := dst.AllVisible(relA, blk)
		if got != want {
			t.Errorf("relA block %d: got=%v want=%v", blk, got, want)
		}
	}
	if !dst.AllVisible(relB, 0) {
		t.Error("relB block 0 lost across save/load")
	}
}

// TestVisibilityMapLoadMissingFileIsNoOp pins fresh-cluster
// behaviour: VMLoadForks on an empty directory returns nil and
// leaves the in-memory map empty. (M0130-S1.)
func TestVisibilityMapLoadMissingFileIsNoOp(t *testing.T) {
	vm := NewVisibilityMap()
	if err := vm.VMLoadForks(t.TempDir()); err != nil {
		t.Fatalf("VMLoadForks of empty dir should return nil, got %v", err)
	}
	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	if vm.AllVisible(rel, 0) {
		t.Error("fresh VM should not advertise any AllVisible bits")
	}
}

// TestVisibilityMapLoadRejectsCorruptFork pins format guards.
// A fork file that is not a multiple of BlockSize must be rejected.
// (M0130-S1.)
func TestVisibilityMapLoadRejectsCorruptFork(t *testing.T) {
	dir := t.TempDir()
	// Write a corrupt _vm file that is not a multiple of BlockSize.
	dbDir := filepath.Join(dir, "base", "1")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	forkPath := filepath.Join(dbDir, "99999_vm")
	if err := os.WriteFile(forkPath, []byte{0xDE, 0xAD, 0xBE, 0xEF}, 0o600); err != nil {
		t.Fatal(err)
	}
	vm := NewVisibilityMap()
	if err := vm.VMLoadForks(dir); err == nil {
		t.Error("VMLoadForks must reject corrupt fork file")
	}
}

// TestVisibilityMapSaveDeterministicOrdering pins the
// "same state → byte-identical fork files" property by saving
// twice and comparing the outputs. (M0130-S1.)
func TestVisibilityMapSaveDeterministicOrdering(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	src := NewVisibilityMap()
	for _, rel := range []RelFileNode{
		{DBOid: 1, RelOid: 16405, Fork: MainFork},
		{DBOid: 1, RelOid: 16402, Fork: MainFork},
		{DBOid: 5, RelOid: 99, Fork: MainFork},
	} {
		src.SetAllVisible(rel, 1)
	}
	if err := src.VMSaveForks(dirA, nil); err != nil {
		t.Fatal(err)
	}
	if err := src.VMSaveForks(dirB, nil); err != nil {
		t.Fatal(err)
	}

	// Compare the fork files — same state should produce identical files.
	for _, rel := range []RelFileNode{
		{DBOid: 1, RelOid: 16405, Fork: VisibilityMapFork},
		{DBOid: 1, RelOid: 16402, Fork: VisibilityMapFork},
		{DBOid: 5, RelOid: 99, Fork: VisibilityMapFork},
	} {
		pathA := RelForkPath(dirA, rel)
		pathB := RelForkPath(dirB, rel)
		a, errA := os.ReadFile(pathA)
		b, errB := os.ReadFile(pathB)
		if errA != nil || errB != nil {
			t.Fatalf("read fork %d/%d: errA=%v errB=%v", rel.DBOid, rel.RelOid, errA, errB)
		}
		if string(a) != string(b) {
			t.Errorf("VM fork %d/%d: two saves must be byte-identical", rel.DBOid, rel.RelOid)
		}
	}
}
