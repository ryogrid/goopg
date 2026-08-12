package wal

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// root-0032 regression: a crash in the middle of a checkpoint's retention pass
// leaves the OLDEST obsolete segments on disk with a hole above them, because
// removeOldSegments walks the obsolete segments newest-first. Before this fix
// both the replay side (readAllUncached, via firstAvailableSegment) and the
// writer side (detectWritePos) anchored on the globally smallest segment, so
// the leftovers became the whole stream: replay silently saw only the orphaned
// prefix and NewWriter refused to open the directory at all with
// "wal: gap at segment N" — leaving the cluster permanently unstartable.
// Reproduced end to end as a pgbench run SIGKILLed mid-retention: pg_wal held
// {1,2} plus 0x0C..0x30 and every subsequent `goopg start` failed.
//
// The fix defines the live stream as the longest contiguous run ending at the
// highest segment. This test pins both halves: the reader must return the live
// run's records (not the orphans), and the writer must open and append.
func TestSegmentGapFromInterruptedRetention(t *testing.T) {
	const segSize = int64(64 * 1024) // 8 pages — keeps the test fast
	dir := t.TempDir()

	cfg := Config{WALDir: dir, SegmentSize: segSize}
	w, err := NewWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 96)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	// Enough records to span ~6 segments.
	const nRecords = 3000
	for i := 0; i < nRecords; i++ {
		if _, _, err := w.Append(payload); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	segNos := listSegments(t, dir)
	if len(segNos) < 5 {
		t.Fatalf("need >= 5 segments to model a retention hole, got %d", len(segNos))
	}

	// Model the interrupted retention pass: the two newest obsolete segments
	// were already unlinked/recycled, the two oldest were not reached before
	// the crash. Delete the middle so a hole separates them from the live run.
	orphans := segNos[:2]
	holeStart, holeEnd := 2, len(segNos)-2
	for _, seg := range segNos[holeStart:holeEnd] {
		if err := os.Remove(filepath.Join(dir, formatSegmentName(seg))); err != nil {
			t.Fatalf("remove segment %d: %v", seg, err)
		}
	}
	liveStart := segNos[holeEnd]

	// Reader: the live run is the stream; the orphaned prefix is ignored.
	recs, err := readAllUncached(dir, segSize)
	if err != nil {
		t.Fatalf("readAllUncached over a retention hole: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no records read from the live segment run")
	}
	minLSN := uint64(liveStart) * uint64(segSize)
	for _, r := range recs {
		if r.StartLSN < minLSN {
			t.Fatalf("record at LSN %d predates the live run start %d — the orphaned prefix (segments %v) was replayed",
				r.StartLSN, minLSN, orphans)
		}
	}

	// Writer: opening the directory must succeed (pre-fix: "gap at segment N")
	// and the append must land after the live run's last record.
	w2, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("NewWriter over a retention hole: %v", err)
	}
	lastEnd := recs[len(recs)-1].EndLSN
	start, _, err := w2.Append(payload)
	if err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if start <= lastEnd {
		t.Fatalf("append landed at LSN %d, at or before the last recovered record end %d", start, lastEnd)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}

	recs2, err := readAllUncached(dir, segSize)
	if err != nil {
		t.Fatalf("readAllUncached after reopen: %v", err)
	}
	if len(recs2) != len(recs)+1 {
		t.Fatalf("record count after reopen: got %d want %d", len(recs2), len(recs)+1)
	}
}

// root-0032 regression, second half: when a record straddles a segment
// boundary the next segment's first page is flagged XLP_FIRST_IS_CONTRECORD
// and opens with that record's tail. detectWritePos used to decode those tail
// bytes as a record header, fail, and report the segment as holding nothing
// but its page header — so a reopened writer resumed at the top of the segment
// and appended OVER every record that followed the straddling one. The records
// left beyond the new, shorter write position are stale bytes that a later
// crash restart hits as "invalid record header: unknown rmid=N", which is fatal
// (the server refuses to start). Observed exactly that way in nightly run
// 20260725-011243's regress suite.
//
// The sweep over payload sizes exists so the boundary case actually happens at
// several different offsets before it.
//
// M0131-S30.6 changed what the writer produces here: BOTH append paths now
// re-land a crossing record at the segment boundary (and pad or zero-fill the
// gap), so no segment starts with a continuation record any more and the stream
// carries one XLOG_NOOP pad per padded crossing. The invariant this test exists
// for is unchanged and still asserted — a reopened writer must not resume on
// top of already-durable records — but it is now counted over the PAYLOAD
// records, with pads excluded.
func TestReopenAfterSegmentStraddlingRecord(t *testing.T) {
	const segSize = int64(64 * 1024)
	sawContRecord := false

	for _, payloadLen := range []int{40, 56, 72, 96, 120, 152, 200} {
		payloadLen := payloadLen
		t.Run("payload"+itoa(payloadLen), func(t *testing.T) {
			dir := t.TempDir()
			cfg := Config{WALDir: dir, SegmentSize: segSize}
			w, err := NewWriter(cfg)
			if err != nil {
				t.Fatal(err)
			}
			payload := make([]byte, payloadLen)
			for i := range payload {
				payload[i] = byte(i + 1)
			}
			// Fill three segments so the last one is entered mid-stream.
			n := int(3*segSize)/(payloadLen+xlogRecordHeaderSize) + 20
			for i := 0; i < n; i++ {
				if _, _, err := w.Append(payload); err != nil {
					t.Fatalf("append %d: %v", i, err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			segNos := listSegments(t, dir)
			last := segNos[len(segNos)-1]
			if segmentStartsWithContRecord(t, dir, last, segSize) {
				sawContRecord = true
			}

			beforeAll, err := readAllUncached(dir, segSize)
			if err != nil {
				t.Fatalf("readAllUncached: %v", err)
			}
			before := withoutSegmentPads(beforeAll)
			if len(before) != n {
				t.Fatalf("pre-reopen record count: got %d want %d (pads excluded; %d records total)",
					len(before), n, len(beforeAll))
			}

			// Reopen (the crash-restart path) and append one marker record.
			w2, err := NewWriter(cfg)
			if err != nil {
				t.Fatalf("NewWriter after close: %v", err)
			}
			marker := make([]byte, payloadLen)
			for i := range marker {
				marker[i] = 0xAB
			}
			if _, _, err := w2.Append(marker); err != nil {
				t.Fatalf("append marker: %v", err)
			}
			if err := w2.Close(); err != nil {
				t.Fatal(err)
			}

			afterAll, err := readAllUncached(dir, segSize)
			if err != nil {
				t.Fatalf("readAllUncached after reopen: %v", err)
			}
			after := withoutSegmentPads(afterAll)
			if len(after) != n+1 {
				t.Fatalf("post-reopen record count: got %d want %d — the reopened writer overwrote %d already-durable records",
					len(after), n+1, n+1-len(after))
			}
			if got := after[len(after)-1].Payload; len(got) == 0 || got[0] != 0xAB {
				t.Fatalf("last record is not the marker appended after reopen")
			}
		})
	}

	// M0131-S30.6: sawContRecord is now expected to stay false — neither append
	// path lets a record straddle a segment boundary. Kept as an observation so
	// a future writer change that reintroduces the shape is visible in the log
	// rather than silently unasserted.
	if sawContRecord {
		t.Log("a segment still starts with a continuation record — the pre-S30.6 straddling shape")
	}
}

// withoutSegmentPads drops the XLOG_NOOP pad records the writer emits over a
// segment-boundary gap, so record counts stay comparable to the number of
// payloads appended (M0131-S30.6).
func withoutSegmentPads(recs []Record) []Record {
	out := make([]Record, 0, len(recs))
	for _, r := range recs {
		if r.XLog != nil && r.XLog.Header.Rmid == RmgrXLog && r.XLog.Header.Info == xlogInfoNoop {
			continue
		}
		out = append(out, r)
	}
	return out
}

// segmentStartsWithContRecord reports whether segNo's first page header carries
// XLP_FIRST_IS_CONTRECORD (i.e. the segment opens with the tail of a record
// that started in the previous segment).
func segmentStartsWithContRecord(t *testing.T, dir string, segNo uint64, segSize int64) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, formatSegmentName(segNo)))
	if err != nil {
		t.Fatal(err)
	}
	hsize := pageHeaderSizeAt(int64(segNo)*segSize, segSize)
	if len(data) < hsize {
		return false
	}
	hdr, err := DecodeXLogPageHeader(data[:hsize])
	if err != nil {
		return false
	}
	return hdr.Info&XLPFirstIsContRecord != 0
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// TestLiveSegmentRunStart pins the run-selection rule itself, including the
// no-hole case (unchanged behaviour: the smallest segment) and a directory
// holding several holes (the newest run wins).
func TestLiveSegmentRunStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		segs []uint64
		want uint64
	}{
		{"contiguous from zero", []uint64{0, 1, 2, 3}, 0},
		{"contiguous after retention", []uint64{7, 8, 9}, 7},
		{"single segment", []uint64{42}, 42},
		{"one hole", []uint64{1, 2, 12, 13, 14}, 12},
		{"several holes", []uint64{1, 5, 6, 20, 21, 22}, 20},
		{"hole before final segment", []uint64{1, 2, 3, 9}, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveSegmentRunStart(tc.segs); got != tc.want {
				t.Fatalf("liveSegmentRunStart(%v) = %d, want %d", tc.segs, got, tc.want)
			}
		})
	}
}

func listSegments(t *testing.T, dir string) []uint64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var segNos []uint64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if segNo, ok := parseSegmentName(e.Name()); ok {
			segNos = append(segNos, segNo)
		}
	}
	sort.Slice(segNos, func(i, j int) bool { return segNos[i] < segNos[j] })
	return segNos
}
