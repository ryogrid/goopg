package xlog

import (
	"testing"
)

// TestSwitchSegmentReplayAndPosition pins XLOG_SWITCH: the record is written,
// the rest of the segment is zero, the next record lands on the next
// segment's first page with xl_prev = the switch record, both readers
// (readAllPageAware, detectWritePos) step over the zero tail, and a switch
// on an empty segment writes nothing. M0149-0076.
func TestSwitchSegmentReplayAndPosition(t *testing.T) {
	dir := t.TempDir()
	segSize := int64(4 * XLOGBlockSize)
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := framedAssemble(RmgrXLog, xlogXLogNoop, 0, []byte("abcdefgh"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(payload); err != nil {
		t.Fatal(err)
	}
	ret, err := w.SwitchSegment()
	if err != nil {
		t.Fatal(err)
	}
	if ret%uint64(segSize) == 0 || ret > uint64(segSize) {
		t.Fatalf("switch returned %d, want the end of the switch record inside segment 0", ret)
	}
	if got := w.WrittenLSN(); got != uint64(segSize) {
		t.Fatalf("after switch WrittenLSN = %d, want the segment boundary %d", got, segSize)
	}
	// A second switch on the empty segment writes nothing.
	if ret2, err := w.SwitchSegment(); err != nil || ret2 != uint64(segSize) {
		t.Fatalf("empty switch = %d, %v; want %d", ret2, err, segSize)
	}
	start, _, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if start <= uint64(segSize) || start > uint64(segSize)+uint64(XLOGBlockSize) {
		t.Fatalf("record after switch starts at %d, want the first page of segment 1", start)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := readAllUncached(dir, segSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("replay read %d records, want 3 (noop, switch, noop)", len(recs))
	}
	if recs[2].XLog == nil || recs[2].XLog.Header.Prev != recs[1].StartLSN-1 {
		t.Fatalf("record after switch has xl_prev %v, want the switch record %d", recs[2].XLog, recs[1].StartLSN-1)
	}
	w2, err := NewWriter(Config{WALDir: dir, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if got := w2.WrittenLSN(); got != recs[2].EndLSN {
		t.Fatalf("reopened writer at %d, want %d", got, recs[2].EndLSN)
	}
}
