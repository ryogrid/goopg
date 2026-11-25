package xlog

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWALSegmentSizeNaming pins segment naming at a non-default
// wal_segment_size: 1 MB segments roll the log id every 4096 segments
// (XLogFileName), and highestLocalSegment reads the absolute segment number
// back from such a name rather than its low 8 hex digits. M0149-0022a.
func TestWALSegmentSizeNaming(t *testing.T) {
	t.Cleanup(func() { SetWALSegmentSize(DefaultSegmentSize) })
	SetWALSegmentSize(1 << 20)
	if got := WALSegmentSize(); got != 1<<20 {
		t.Fatalf("WALSegmentSize = %d", got)
	}
	if got, want := FormatSegmentNameTLI(0x100, 1), "000000010000000000000100"; got != want {
		t.Errorf("FormatSegmentNameTLI(0x100) = %q, want %q", got, want)
	}
	if got, want := FormatSegmentNameTLI(0x1001, 1), "000000010000000100000001"; got != want {
		t.Errorf("FormatSegmentNameTLI(0x1001) = %q, want %q", got, want)
	}
	dir := t.TempDir()
	for _, n := range []string{"000000010000000000000FFF", "000000010000000100000001"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := highestLocalSegment(dir); err != nil || got != 0x1001 {
		t.Errorf("highestLocalSegment = %#x, %v; want 0x1001", got, err)
	}
	SetWALSegmentSize(3 << 20) // not a power of two: ignored
	if got := WALSegmentSize(); got != 1<<20 {
		t.Errorf("invalid size adopted: %d", got)
	}
}
