package transam

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadTwoPhaseFilePG18CRC: the CRC-32C goopg checks is PG's — a
// pg_twophase file PG 18.3 wrote reads back clean, and a flipped byte fails.
func TestReadTwoPhaseFilePG18CRC(t *testing.T) {
	src := filepath.Join("xlog", "testdata", "pg18_twophase_00000000000002F1")
	if _, err := ReadTwoPhaseFile(src); err != nil {
		t.Fatalf("PG 18.3 file: %v", err)
	}
	b, _ := os.ReadFile(src)
	b[80] ^= 1
	bad := filepath.Join(t.TempDir(), "bad")
	_ = os.WriteFile(bad, b, 0o600)
	if _, err := ReadTwoPhaseFile(bad); err == nil {
		t.Fatal("corrupted file passed the CRC check")
	}
}

// TestCheckPointTwoPhaseWritesOnlyBeforeRedo: a checkpoint writes the state
// file of a transaction prepared before its redo point and leaves a later one
// to the WAL; finishing removes the file.
func TestCheckPointTwoPhaseWritesOnlyBeforeRedo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), TwoPhaseDirName)
	m := NewManager()
	m.SetTwoPhaseDir(dir)
	m.RegisterPrepared(PreparedXact{XID: 800, GID: "a", State: []byte("state-a"), PrepareEndLSN: 100})
	m.RegisterPrepared(PreparedXact{XID: 801, GID: "b", State: []byte("state-b"), PrepareEndLSN: 300})
	if err := m.CheckPointTwoPhase(200); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTwoPhaseFile(filepath.Join(dir, TwoPhaseFileName(800)))
	if err != nil || string(got) != "state-a" {
		t.Fatalf("file for 800: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, TwoPhaseFileName(801))); err == nil {
		t.Fatal("801 was prepared after redo but got a file")
	}
	if !m.IsPreparedXID(800) || !m.TakeTwoPhase(800) || m.IsPreparedXID(800) {
		t.Fatal("registry bookkeeping")
	}
	m.RemoveTwoPhaseFile(800)
	if _, err := os.Stat(filepath.Join(dir, TwoPhaseFileName(800))); err == nil {
		t.Fatal("file survived RemoveTwoPhaseFile")
	}
}
