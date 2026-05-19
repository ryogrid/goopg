package initdb_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestLoadOrCreateTimelineID_Default returns 1 on first call and
// persists the file so the same datadir round-trips on a second
// open.
func TestLoadOrCreateTimelineID_Default(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tli, err := initdb.LoadOrCreateTimelineID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateTimelineID: %v", err)
	}
	if tli != 1 {
		t.Fatalf("expected default TLI=1, got %d", tli)
	}
	again, err := initdb.LoadOrCreateTimelineID(dir)
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if again != 1 {
		t.Fatalf("expected persisted TLI=1, got %d", again)
	}
}

// TestWriteThenLoadTimelineID exercises the bump path that Promote
// uses: write a new TLI, re-open, observe the bump.
func TestWriteThenLoadTimelineID(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := initdb.WriteTimelineID(dir, 7); err != nil {
		t.Fatalf("WriteTimelineID: %v", err)
	}
	got, err := initdb.LoadOrCreateTimelineID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateTimelineID: %v", err)
	}
	if got != 7 {
		t.Fatalf("expected TLI=7, got %d", got)
	}
}
