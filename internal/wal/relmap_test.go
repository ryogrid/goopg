package wal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// B0.4 pins: RelMapFile encode/validate round-trip against the relmapper.c
// layout, and XLOG_RELMAP_UPDATE replay rewriting the target file.

func TestRelMapFileRoundTrip(t *testing.T) {
	maps := []RelMapping{{Oid: 1262, FileNumber: 1262}, {Oid: 1260, FileNumber: 9991}}
	image := EncodeRelMapFile(maps)
	if len(image) != RelMapFileSize {
		t.Fatalf("image size = %d, want %d", len(image), RelMapFileSize)
	}
	got, err := ValidateRelMapFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != maps[0] || got[1] != maps[1] {
		t.Fatalf("round-trip mappings = %+v, want %+v", got, maps)
	}
	// Corruption: a flipped byte must fail the CRC.
	image[10] ^= 0xFF
	if _, err := ValidateRelMapFile(image); err == nil {
		t.Fatal("corrupted image passed validation")
	}
}

func TestApplyRecordReplaysRelmapUpdate(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	image := EncodeRelMapFile([]RelMapping{{Oid: 1259, FileNumber: 4242}})
	framed, err := EncodeRelmapUpdatePG(16400, 1663, image)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 100)

	got, err := os.ReadFile(filepath.Join(dataDir, "base", "16400", "pg_filenode.map"))
	if err != nil {
		t.Fatalf("replay did not write the map: %v", err)
	}
	if string(got) != string(image) {
		t.Fatal("replayed pg_filenode.map differs from the record image")
	}

	// Shared map: dbid=0 targets global/.
	sharedImage := EncodeRelMapFile([]RelMapping{{Oid: 1262, FileNumber: 1262}})
	sharedFramed, err := EncodeRelmapUpdatePG(0, 1664, sharedImage)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, sharedFramed, 200)
	if _, err := os.Stat(filepath.Join(dataDir, "global", "pg_filenode.map")); err != nil {
		t.Fatalf("shared map not written: %v", err)
	}
}
