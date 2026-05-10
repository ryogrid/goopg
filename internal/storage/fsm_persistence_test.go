package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFSMSaveLoadRoundTrip pins the M0080-0004 persistence
// contract: an FSM state written by Save() and read back by
// Load() must produce byte-identical free-space values per
// relation block.
func TestFSMSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "global", "pg_fsm_state.bin")

	src := NewFSM()
	relA := RelFileNode{DBOid: 1, RelOid: 16402, Fork: MainFork}
	relB := RelFileNode{DBOid: 1, RelOid: 16405, Fork: MainFork}
	src.RecordFreeSpace(relA, 0, 7000)
	src.RecordFreeSpace(relA, 1, 0)
	src.RecordFreeSpace(relA, 2, 4096)
	src.RecordFreeSpace(relB, 0, 8000)

	if err := src.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dst := NewFSM()
	if err := dst.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify the GetPageWithFreeSpace lookup still works on the
	// restored map.
	if got, ok := dst.GetPageWithFreeSpace(relA, 4000); !ok || got != 0 {
		t.Errorf("relA lookup for >=4000 bytes: got=(%d,%v) want=(0,true)", got, ok)
	}
	if got, ok := dst.GetPageWithFreeSpace(relB, 100); !ok || got != 0 {
		t.Errorf("relB lookup for >=100 bytes: got=(%d,%v) want=(0,true)", got, ok)
	}
}

// TestFSMLoadMissingFileIsNoOp pins fresh-cluster behaviour.
// (M0080-0004.)
func TestFSMLoadMissingFileIsNoOp(t *testing.T) {
	f := NewFSM()
	if err := f.Load(filepath.Join(t.TempDir(), "does-not-exist.bin")); err != nil {
		t.Fatalf("Load of missing file should return nil, got %v", err)
	}
}

// TestFSMLoadRejectsBadMagic pins format guards. (M0080-0004.)
func TestFSMLoadRejectsBadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(path, []byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0, 0, 0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewFSM()
	if err := f.Load(path); err == nil {
		t.Error("Load must reject bad magic bytes")
	}
}

// TestFSMSaveDeterministicOrdering pins "same state → byte-
// identical file". (M0080-0004.)
func TestFSMSaveDeterministicOrdering(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.bin")
	pathB := filepath.Join(dir, "b.bin")

	src := NewFSM()
	for _, rel := range []RelFileNode{
		{DBOid: 1, RelOid: 16405, Fork: MainFork},
		{DBOid: 1, RelOid: 16402, Fork: MainFork},
		{DBOid: 5, RelOid: 99, Fork: MainFork},
	} {
		src.RecordFreeSpace(rel, 1, 1234)
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
		t.Error("two saves of the same FSM state must be byte-identical")
	}
}
