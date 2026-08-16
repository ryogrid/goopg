package xlog

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDiscoverCheckpointInNewestSegment verifies that when the
// checkpoint record is in the latest (newest) segment,
// DiscoverLastCheckpointLSN returns its EndLSN.
func TestDiscoverCheckpointInNewestSegment(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	// Write some data records, then a checkpoint.
	if _, _, err := w.Append([]byte("data1")); err != nil {
		t.Fatal(err)
	}
	_, ckptEnd, err := w.Append(EncodeCheckpoint())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(ckptEnd); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	lsn, err := DiscoverLastCheckpointLSN(walDir, 128)
	if err != nil {
		t.Fatalf("DiscoverLastCheckpointLSN: %v", err)
	}
	if lsn != ckptEnd {
		t.Fatalf("LSN = %d, want %d (checkpoint end)", lsn, ckptEnd)
	}
}

// TestDiscoverCheckpointInOlderSegment verifies that when more records
// follow the checkpoint (without a second checkpoint), the function
// still returns the last checkpoint's EndLSN.
func TestDiscoverCheckpointInOlderSegment(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	_, ckptEnd, err := w.Append(EncodeCheckpoint())
	if err != nil {
		t.Fatal(err)
	}
	// Write more records AFTER the checkpoint (simulates post-checkpoint writes).
	if _, _, err := w.Append([]byte("post1")); err != nil {
		t.Fatal(err)
	}
	end2, _, err := w.Append([]byte("post2"))
	if err != nil {
		t.Fatal(err)
	}
	_ = end2
	if err := w.FlushUpTo(ckptEnd); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	lsn, err := DiscoverLastCheckpointLSN(walDir, 128)
	if err != nil {
		t.Fatalf("DiscoverLastCheckpointLSN: %v", err)
	}
	if lsn != ckptEnd {
		t.Fatalf("LSN = %d, want %d (checkpoint end)", lsn, ckptEnd)
	}
}

// TestDiscoverCheckpointNoMarker verifies that when no checkpoint
// record exists in the WAL, DiscoverLastCheckpointLSN returns an error.
func TestDiscoverCheckpointNoMarker(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	_, end, err := w.Append([]byte("only-data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = DiscoverLastCheckpointLSN(walDir, 128)
	if err == nil {
		t.Fatal("expected error for WAL without checkpoint, got nil")
	}
}

// TestDiscoverCheckpointFreshCluster verifies that an empty WAL
// directory (fresh cluster) returns (0, nil) — not an error.
func TestDiscoverCheckpointFreshCluster(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	if err := os.MkdirAll(walDir, 0o755); err != nil {
		t.Fatal(err)
	}

	lsn, err := DiscoverLastCheckpointLSN(walDir, 128)
	if err != nil {
		t.Fatalf("fresh cluster: unexpected error: %v", err)
	}
	if lsn != 0 {
		t.Fatalf("fresh cluster: LSN = %d, want 0", lsn)
	}
}

// TestDiscoverCheckpointAfterRetention simulates the M0045 scenario:
// WAL retention has removed segment 0, and only higher-numbered
// segments remain. DiscoverLastCheckpointLSN must still find the
// checkpoint in the retained segments.
func TestDiscoverCheckpointAfterRetention(t *testing.T) {
	// A9: after retention the stream is read starting at the first
	// remaining segment, so segment starts must fall on WAL page
	// boundaries — the segment size has to be a multiple of
	// XLOGBlockSize (as in production: 16 MiB / 8 KiB). One page per
	// segment keeps the test small. WALBuffers routes appends through
	// the stripe path, which relocates records at segment boundaries
	// (production behaviour) instead of emitting cross-segment
	// contrecords — so removing segment 0 leaves whole records behind.
	const segSize = int64(XLOGBlockSize)
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: segSize, WALBuffers: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}

	// Write records until segment 0 is full and the stream has
	// entered segment 1.
	for {
		_, end, err := w.Append(make([]byte, 600))
		if err != nil {
			t.Fatal(err)
		}
		if end > uint64(segSize) {
			break
		}
	}
	// Checkpoint is written after filling segment 0 and beyond.
	_, ckptEnd, err := w.Append(EncodeCheckpoint())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(ckptEnd); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify multiple segments were created (otherwise the test setup
	// is wrong and segment 0 = only segment).
	seg1 := filepath.Join(walDir, formatSegmentName(1))
	if _, err := os.Stat(seg1); os.IsNotExist(err) {
		t.Skip("segment 1 not created with this segSize; test setup needs adjustment")
	}

	// Simulate WAL retention: remove segment 0.
	seg0 := filepath.Join(walDir, formatSegmentName(0))
	if err := os.Remove(seg0); err != nil {
		t.Fatalf("remove segment 0: %v", err)
	}

	// DiscoverLastCheckpointLSN must find the checkpoint in the
	// retained segments (1+) after segment 0 is removed.
	lsn, err := DiscoverLastCheckpointLSN(walDir, segSize)
	if err != nil {
		t.Fatalf("after retention: DiscoverLastCheckpointLSN: %v", err)
	}
	if lsn == 0 {
		t.Fatal("after retention: expected non-zero checkpoint LSN")
	}
	if lsn != ckptEnd {
		t.Fatalf("after retention: LSN = %d, want %d", lsn, ckptEnd)
	}
}
