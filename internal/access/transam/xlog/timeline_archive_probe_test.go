package xlog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestFindNewestTimelineArchive pins findNewestTimeLine under archive
// recovery (M0149-0018c): history files restorable from the archive count,
// a pg_wal copy counts when the archive lacks one, the probe stops at the
// first gap. Each probe restores into RECOVERYHISTORY, which the next probe
// replaces and promotion removes.
func TestFindNewestTimelineArchive(t *testing.T) {
	wal := t.TempDir()
	arch := t.TempDir()
	for _, n := range []string{"00000002.history", "00000003.history"} {
		if err := os.WriteFile(filepath.Join(arch, n), []byte("1\t0/3000000\tno recovery target specified\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wal, "00000004.history"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := `cp "` + arch + `/%f" "%p"`
	if got := FindNewestTimelineArchive(context.Background(), wal, cmd, "000000010000000000000001", 1); got != 4 {
		t.Fatalf("newest = %d, want 4 (2, 3 archived; 4 in pg_wal; 5 absent)", got)
	}
	if got := FindNewestTimelineArchive(context.Background(), wal, "", "000000010000000000000001", 1); got != 1 {
		t.Fatalf("without restore_command newest = %d, want 1 (00000002.history not in pg_wal)", got)
	}
}
