package xlog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeWALSegments appends payloads until the writer has filled at least
// `segs` segments of tailGapSegSize, closes it, and returns the payloads.
func writeWALSegments(t *testing.T, walDir string, segs int) []string {
	t.Helper()
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: tailGapSegSize, Preallocate: true, WALBuffers: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var payloads []string
	for i := 0; ; i++ {
		p := fmt.Sprintf("payload-%04d-%0200d", i, i)
		_, end, aerr := w.Append([]byte(p))
		if aerr != nil {
			t.Fatalf("append: %v", aerr)
		}
		payloads = append(payloads, p)
		if end > uint64(segs-1)*uint64(tailGapSegSize)+uint64(tailGapSegSize)/2 {
			break
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return payloads
}

// TestReadStreamStopsAtPreallocatedTail pins M-NIGHTLY
// tpcds/stage-startup-20261007: the reader must not read pg_wal's
// preallocated (zero-filled) or recycled (stale) tail. Upstream ends the read
// at the first page whose header is not valid for its own address
// (XLogReaderValidatePageHeader). goopg read every file until ENOENT — 1.1 GB
// of zeros per start on the TPC-DS SF1 cluster (1 live segment, 70
// preallocated).
func TestReadStreamStopsAtPreallocatedTail(t *testing.T) {
	walDir := t.TempDir()
	payloads := writeWALSegments(t, walDir, 2)

	live, bounded := lastLiveSegment(walDir, tailGapSegSize, 0)
	if !bounded || live != 1 {
		t.Fatalf("lastLiveSegment = (%d, %v), want (1, true)", live, bounded)
	}
	// Add a zero-filled tail and a recycled segment holding stale WAL (a copy
	// of segment 0, whose xlp_pageaddr is not its own address).
	seg0, err := os.ReadFile(filepath.Join(walDir, FormatSegmentName(0)))
	if err != nil {
		t.Fatal(err)
	}
	for segNo := uint64(2); segNo <= 6; segNo++ {
		body := make([]byte, tailGapSegSize)
		if segNo == 6 {
			body = seg0
		}
		if err := os.WriteFile(filepath.Join(walDir, FormatSegmentName(segNo)), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	live, bounded = lastLiveSegment(walDir, tailGapSegSize, 0)
	if !bounded || live != 1 {
		t.Fatalf("with a zero/recycled tail: lastLiveSegment = (%d, %v), want (1, true)", live, bounded)
	}
	stream, err := readStreamFrom(walDir, tailGapSegSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := 2 * int(tailGapSegSize); len(stream) != want {
		t.Errorf("readStreamFrom read %d bytes, want %d (the two live segments only)", len(stream), want)
	}
	assertAllPayloadsReplayed(t, walDir, payloads)
}

// TestReadStreamReadsThroughAMidStreamHole pins that the trim drops only the
// TRAILING tail: an invalid segment followed by a live one is still read, so
// durableWALAfter's hole detection sees the records behind it.
func TestReadStreamReadsThroughAMidStreamHole(t *testing.T) {
	walDir := t.TempDir()
	writeWALSegments(t, walDir, 3)
	if err := os.WriteFile(filepath.Join(walDir, FormatSegmentName(1)), make([]byte, tailGapSegSize), 0o600); err != nil {
		t.Fatal(err)
	}
	live, bounded := lastLiveSegment(walDir, tailGapSegSize, 0)
	if !bounded || live != 2 {
		t.Fatalf("lastLiveSegment = (%d, %v), want (2, true): a live segment follows the hole", live, bounded)
	}
	stream, err := readStreamFrom(walDir, tailGapSegSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stream) < 3*int(tailGapSegSize) {
		t.Errorf("readStreamFrom read %d bytes, want at least the 3 segments through the live one", len(stream))
	}
}
