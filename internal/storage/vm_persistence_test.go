package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVisibilityMapSaveLoadRoundTrip pins the M0080-0003
// persistence contract: a VM state written by Save() and read
// back by Load() must produce byte-identical bits per relation.
func TestVisibilityMapSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "global", "pg_vm_state.bin")

	src := NewVisibilityMap()
	relA := RelFileNode{DBOid: 1, RelOid: 16402, Fork: MainFork}
	relB := RelFileNode{DBOid: 1, RelOid: 16405, Fork: MainFork}
	src.SetAllVisible(relA, 0)
	src.SetAllVisible(relA, 3)
	src.SetAllVisible(relA, 5)
	src.SetAllVisible(relB, 0)

	if err := src.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("save file missing: %v", err)
	}

	dst := NewVisibilityMap()
	if err := dst.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
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
// behaviour: Load on a non-existent file returns nil and
// leaves the in-memory map empty. (M0080-0003.)
func TestVisibilityMapLoadMissingFileIsNoOp(t *testing.T) {
	vm := NewVisibilityMap()
	if err := vm.Load(filepath.Join(t.TempDir(), "does-not-exist.bin")); err != nil {
		t.Fatalf("Load of missing file should return nil, got %v", err)
	}
	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	if vm.AllVisible(rel, 0) {
		t.Error("fresh VM should not advertise any AllVisible bits")
	}
}

// TestVisibilityMapLoadRejectsBadMagic pins format guards.
// (M0080-0003.)
func TestVisibilityMapLoadRejectsBadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(path, []byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0, 0, 0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	vm := NewVisibilityMap()
	if err := vm.Load(path); err == nil {
		t.Error("Load must reject bad magic bytes")
	}
}

// TestVisibilityMapSaveDeterministicOrdering pins the
// "same state → byte-identical file" property by saving twice
// and comparing the outputs. (M0080-0003.)
func TestVisibilityMapSaveDeterministicOrdering(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.bin")
	pathB := filepath.Join(dir, "b.bin")

	src := NewVisibilityMap()
	// Use different DBOids / RelOids to exercise sort.
	for _, rel := range []RelFileNode{
		{DBOid: 1, RelOid: 16405, Fork: MainFork},
		{DBOid: 1, RelOid: 16402, Fork: MainFork},
		{DBOid: 5, RelOid: 99, Fork: MainFork},
	} {
		src.SetAllVisible(rel, 1)
	}
	if err := src.Save(pathA); err != nil {
		t.Fatal(err)
	}
	if err := src.Save(pathB); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(pathA)
	b, _ := os.ReadFile(pathB)
	if string(a) != string(b) {
		t.Error("two saves of the same VM state must be byte-identical")
	}
}
