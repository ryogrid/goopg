package xlog

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestSwitchTimelineResumesAtEndOfLog pins Writer.SwitchTimeline, the writer
// half of a promotion's end of recovery (XLogInitNewTimeline,
// RemoveNonParentXlogFiles):
//   - a partial record the walreceiver appended past the last complete one is
//     dropped, and the first new record starts at the end of log, its
//     xl_prev naming the last complete record;
//   - the segment holding the switch point is copied to the new timeline's
//     name, and an old-timeline segment after it is removed;
//   - every record, before and after the switch, reads back in order.
func TestSwitchTimelineResumesAtEndOfLog(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      filepath.Clean(dir),
		SegmentSize: DefaultSegmentSize,
		WALBuffers:  4 * 1024 * 1024,
		PageHeaders: true,
		SystemID:    1,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	var lastStart, lastEnd uint64
	for _, p := range []string{"before-1", "before-2", "before-3"} {
		if lastStart, lastEnd, err = w.Append([]byte(p)); err != nil {
			t.Fatalf("Append(%s): %v", p, err)
		}
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	// The walreceiver's tail: the first half of a record framed after
	// before-3, as a primary cut mid-record leaves it.
	rec, realLen, err := encodeRecordXLog([]byte("partial-record-from-the-primary-that-never-finished"), lastStart-1)
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := emitWithPageHeaders(rec, realLen, int64(lastEnd), DefaultSegmentSize, 1, 1)
	if _, _, err := w.AppendRaw(stream[:len(stream)/2]); err != nil {
		t.Fatalf("AppendRaw: %v", err)
	}
	if w.WrittenLSN() <= lastEnd {
		t.Fatalf("partial tail not appended: written %X, last record end %X", w.WrittenLSN(), lastEnd)
	}
	// A preallocated old-timeline segment past the switch point.
	stale := filepath.Join(dir, FormatSegmentNameTLI(7, 1))
	if err := os.WriteFile(stale, make([]byte, 8192), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := w.SwitchTimeline(2, lastEnd, lastStart-1); err != nil {
		t.Fatalf("SwitchTimeline: %v", err)
	}
	if got := w.TimelineID(); got != 2 {
		t.Errorf("TimelineID after switch = %d, want 2", got)
	}
	if got := w.WrittenLSN(); got != lastEnd {
		t.Errorf("WrittenLSN after switch = %X, want the end of log %X (partial tail dropped)", got, lastEnd)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("old-timeline segment after the switch point survived: %v", err)
	}
	seg := lastEnd / uint64(DefaultSegmentSize)
	// The old timeline keeps its last segment; the new one gets a full,
	// zero-filled copy (XLogFileCopy).
	if _, err := os.Stat(filepath.Join(dir, FormatSegmentNameTLI(seg, 1))); err != nil {
		t.Errorf("segment %d on timeline 1: %v, want it kept", seg, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, FormatSegmentNameTLI(seg, 2))); err != nil || fi.Size() != DefaultSegmentSize {
		t.Errorf("segment %d on timeline 2: %v, want a full segment", seg, err)
	}

	afterStart, afterEnd, err := w.Append([]byte("after-1"))
	if err != nil {
		t.Fatalf("Append after switch: %v", err)
	}
	if afterStart != lastEnd+1 {
		t.Errorf("first record after the switch starts at %X, want %X (right after the last complete record)", afterStart, lastEnd+1)
	}
	if err := w.FlushUpTo(afterEnd); err != nil {
		t.Fatal(err)
	}
	newSeg, err := os.ReadFile(filepath.Join(dir, FormatSegmentNameTLI(seg, 2)))
	if err != nil {
		t.Fatal(err)
	}
	off := (afterStart - 1) % uint64(DefaultSegmentSize)
	hdr, err := DecodeXLogRecordHeader(newSeg[off : off+SizeOfXLogRecord])
	if err != nil {
		t.Fatalf("decode the first new record's header: %v", err)
	}
	if hdr.Prev != lastStart-1 {
		t.Errorf("first new record xl_prev = %X, want %X (the last replayed record)", hdr.Prev, lastStart-1)
	}

	recs, err := readAllUncached(dir, 0)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, string(r.Payload))
	}
	want := []string{"before-1", "before-2", "before-3", "after-1"}
	if len(got) < len(want) {
		t.Fatalf("read back %q, want a stream ending in %q", got, want)
	}
	tail := got[len(got)-len(want):]
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("read back %q, want a stream ending in %q", got, want)
		}
	}
}

// TestEndOfRecoveryRecordRoundTrips pins EncodeEndOfRecoveryPG against its
// decoder: xl_end_of_recovery's 24 bytes (end_time, ThisTimeLineID,
// PrevTimeLineID, wal_level) framed as RM_XLOG XLOG_END_OF_RECOVERY.
func TestEndOfRecoveryRecordRoundTrips(t *testing.T) {
	payload, err := EncodeEndOfRecoveryPG(814_000_000_000_000, 2, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	rec, realLen, err := encodeRecordXLog(payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decodeRecordXLogDetailed(rec[:maxAlignXLog(realLen)])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.XLog == nil || dec.XLog.Header.Rmid != RmgrXLog || dec.XLog.Header.Info&0xF0 != xlogXLogEndOfRecovery {
		t.Fatalf("decoded header %+v, want RM_XLOG XLOG_END_OF_RECOVERY", dec.XLog)
	}
	if len(dec.XLog.MainData) != sizeOfXlEndOfRecovery {
		t.Errorf("main data %d bytes, want sizeof(xl_end_of_recovery) %d", len(dec.XLog.MainData), sizeOfXlEndOfRecovery)
	}
	endTime, thisTLI, prevTLI, walLevel, err := DecodeEndOfRecovery(dec.XLog.MainData)
	if err != nil || endTime != 814_000_000_000_000 || thisTLI != 2 || prevTLI != 1 || walLevel != 1 {
		t.Errorf("decoded (%d, %d, %d, %d, %v), want (814000000000000, 2, 1, 1)", endTime, thisTLI, prevTLI, walLevel, err)
	}
}

// TestReopenSegmentFilesWritesTheReplacedSegment pins Writer.ReopenSegmentFiles:
// once a segment the writer has open is replaced by rename — archive recovery
// putting the archived copy in place (KeepFileRestoredFromArchive) — the next
// write must land in the file now under that name. Without dropping the
// cached descriptor it lands in the orphaned inode and is gone from pg_wal.
func TestReopenSegmentFilesWritesTheReplacedSegment(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: DefaultSegmentSize, SystemID: 1, TimelineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	_, end, err := w.Append([]byte("before-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(dir, FormatSegmentNameTLI(0, 1))
	orig, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	// The "archived copy": same bytes, renamed over the open segment.
	tmp := filepath.Join(dir, RecoveryXLogName)
	if err := os.WriteFile(tmp, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, seg); err != nil {
		t.Fatal(err)
	}
	if err := w.ReopenSegmentFiles(); err != nil {
		t.Fatal(err)
	}
	payload := []byte("written-after-recovery")
	_, end2, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end2); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got[:end2], payload) {
		t.Fatal("the record written after the segment was replaced is not in pg_wal's file")
	}
}
