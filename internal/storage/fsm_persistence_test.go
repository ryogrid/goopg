package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFSMSaveLoadRoundTrip pins the M0130-S1 persistence
// contract: FSM state written via FSMSaveForks and read back by
// FSMLoadForks must produce equivalent free-space values per
// relation block.
func TestFSMSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()

	src := NewFSM()
	relA := RelFileNode{DBOid: 1, RelOid: 16402, Fork: MainFork}
	relB := RelFileNode{DBOid: 1, RelOid: 16405, Fork: MainFork}
	src.RecordFreeSpace(relA, 0, 7000)
	src.RecordFreeSpace(relA, 1, 0)
	src.RecordFreeSpace(relA, 2, 4096)
	src.RecordFreeSpace(relB, 0, 8000)

	if err := src.FSMSaveForks(dir, nil); err != nil {
		t.Fatalf("FSMSaveForks: %v", err)
	}

	dst := NewFSM()
	if err := dst.FSMLoadForks(dir); err != nil {
		t.Fatalf("FSMLoadForks: %v", err)
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
// (M0130-S1.)
func TestFSMLoadMissingFileIsNoOp(t *testing.T) {
	f := NewFSM()
	if err := f.FSMLoadForks(t.TempDir()); err != nil {
		t.Fatalf("FSMLoadForks of empty dir should return nil, got %v", err)
	}
}

// TestFSMLoadRejectsCorruptFork pins format guards. A fork file
// that is not a multiple of BlockSize must be rejected. (M0130-S1.)
func TestFSMLoadRejectsCorruptFork(t *testing.T) {
	dir := t.TempDir()
	// Write a corrupt _fsm file that is not a multiple of BlockSize.
	dbDir := filepath.Join(dir, "base", "1")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	forkPath := filepath.Join(dbDir, "99999_fsm")
	if err := os.WriteFile(forkPath, []byte{0xDE, 0xAD, 0xBE, 0xEF}, 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewFSM()
	if err := f.FSMLoadForks(dir); err == nil {
		t.Error("FSMLoadForks must reject corrupt fork file")
	}
}

// TestFSMSaveDeterministicOrdering pins "same state → byte-
// identical fork files". (M0130-S1.)
func TestFSMSaveDeterministicOrdering(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	src := NewFSM()
	for _, rel := range []RelFileNode{
		{DBOid: 1, RelOid: 16405, Fork: MainFork},
		{DBOid: 1, RelOid: 16402, Fork: MainFork},
		{DBOid: 5, RelOid: 99, Fork: MainFork},
	} {
		src.RecordFreeSpace(rel, 1, 1234)
	}
	if err := src.FSMSaveForks(dirA, nil); err != nil {
		t.Fatal(err)
	}
	if err := src.FSMSaveForks(dirB, nil); err != nil {
		t.Fatal(err)
	}

	// Compare the fork files — same state should produce identical files.
	for _, rel := range []RelFileNode{
		{DBOid: 1, RelOid: 16405, Fork: FSMFork},
		{DBOid: 1, RelOid: 16402, Fork: FSMFork},
		{DBOid: 5, RelOid: 99, Fork: FSMFork},
	} {
		pathA := RelForkPath(dirA, rel)
		pathB := RelForkPath(dirB, rel)
		a, errA := os.ReadFile(pathA)
		b, errB := os.ReadFile(pathB)
		if errA != nil || errB != nil {
			t.Fatalf("read fork %d/%d: errA=%v errB=%v", rel.DBOid, rel.RelOid, errA, errB)
		}
		if string(a) != string(b) {
			t.Errorf("FSM fork %d/%d: two saves must be byte-identical", rel.DBOid, rel.RelOid)
		}
	}
}
