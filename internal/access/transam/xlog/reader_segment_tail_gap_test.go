package xlog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// M0131-S30.1 regression: a segment tail too small to hold a record header
// must not be decoded as a record — and a record that legitimately starts
// there must still be read.
//
// The stripe writer (Path B: Config.WALBuffers > 0, the production path)
// never lets a record straddle a segment boundary. When a reservation would
// cross one, insertPosTracker.reserveEmittedAndPublish re-lands the record at
// the boundary and pads `[curr, boundary)` with an XLOG_NOOP — unless that gap
// is smaller than one 24-byte record header, in which case no pad fits and the
// bytes are left untouched (reserve_emitted.go, `gapLen < xlogMinimumRecordSize`).
//
// readAllPageAware used to walk straight into such a gap. extractRecordBytes
// skips page headers, so it completed the 24-byte "header" from the FIRST
// record of the next segment — producing garbage, an end-of-WAL verdict, and
// the silent loss of every record behind it. That is the measured
// crash-recovery row loss of `analysis/crashprobe30.sh`: `padding bytes
// nonzero (0xff 0x07)` at lsn=134217721, 8 bytes short of a segment boundary,
// where 0x07ff is the high half of the following record's xl_prev.
//
// state.appendPGCompat's Path A (Config.WALBuffers == 0, oversized records,
// drain) has NO boundary re-land and does emit straddling records, so the skip
// must be decided by the CRC and not by the offset — TestReadAllKeeps... below
// pins that half.
//
// See docs/design/0131-0022.

// tailGapSegSize is a multiple of XLOGBlockSize so the fixture really contains
// page headers, and small enough that a handful of appends reaches the second
// segment.
const tailGapSegSize = int64(4 * XLOGBlockSize)

// writeWALWithShortSegmentTailGap builds a two-segment WAL directory whose
// first segment ends `gapLen` bytes (gapLen < 24) before the boundary, and
// returns the directory plus every payload appended, in order.
//
// walBuffers selects the writer path: > 0 is the stripe path (Path B), which
// re-lands the crossing record at the boundary and leaves the gap unwritten;
// 0 is the direct-write path (Path A), which straddles the boundary instead.
//
// The gap is engineered by choosing each payload's size from the same
// prediction functions the writer uses (predictXLogRecordLen +
// predictEmittedSize), so the write cursor's position is known exactly before
// each Append.
func writeWALWithShortSegmentTailGap(t *testing.T, gapLen, walBuffers int64) (walDir string, payloads []string) {
	t.Helper()
	if gapLen <= 0 || gapLen >= int64(xlogRecordHeaderSize) {
		t.Fatalf("gapLen=%d must be in (0, %d)", gapLen, xlogRecordHeaderSize)
	}
	walDir = t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: tailGapSegSize,
		Preallocate: true,
		WALBuffers:  walBuffers,
	})
	if err != nil {
		t.Fatal(err)
	}

	target := uint64(tailGapSegSize) - uint64(gapLen) // where curr must land
	var lastEnd uint64
	appendPayload := func(p string) uint64 {
		_, end, aerr := w.Append([]byte(p))
		if aerr != nil {
			t.Fatalf("append %q: %v", p, aerr)
		}
		payloads = append(payloads, p)
		lastEnd = end
		return end
	}

	// payloadFor(n) yields a payload whose emitted size at the current cursor
	// is exactly n bytes, or ok=false when no payload length produces it.
	payloadFor := func(pos uint64, want int) (string, bool) {
		for n := 1; n <= want; n++ {
			p := fmt.Sprintf("%0*d", n, n%10)
			_, padded := predictXLogRecordLen([]byte(p))
			total, _ := predictEmittedSize(padded, int64(pos), tailGapSegSize)
			if total == want {
				return p, true
			}
		}
		return "", false
	}

	cur := lastEnd
	for cur < target {
		remain := int(target - cur)
		step := remain
		if step > 512 {
			step = 512
		}
		p, ok := payloadFor(cur, step)
		if !ok {
			t.Fatalf("no payload produces an emitted size of %d bytes at pos=%d", step, cur)
		}
		cur = appendPayload(p)
	}
	if cur != target {
		t.Fatalf("cursor landed at %d, want %d", cur, target)
	}

	// This one cannot fit in the remaining gapLen bytes.
	appendPayload("crosses-the-boundary")
	// ...and a few more so there is real committed work behind the gap.
	for i := 0; i < 3; i++ {
		appendPayload(fmt.Sprintf("after-boundary-%d", i))
	}

	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	w.stateRef.eagerWG.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return walDir, payloads
}

// assertAllPayloadsReplayed reads walDir back and fails when any payload is
// missing — the loss signature of the S30.1 defect.
func assertAllPayloadsReplayed(t *testing.T, walDir string, payloads []string) {
	t.Helper()
	recs, err := readAllUncached(walDir, tailGapSegSize)
	if err != nil {
		t.Fatalf("readAllUncached: %v", err)
	}
	got := make(map[string]bool, len(recs))
	for _, r := range recs {
		got[string(r.Payload)] = true
	}
	for i, p := range payloads {
		if !got[p] {
			t.Fatalf("payload %d/%d %q missing from replay (%d records read) — "+
				"the WAL tail behind a sub-header segment-tail gap was discarded",
				i+1, len(payloads), p, len(recs))
		}
	}
}

// TestReadAllSkipsShortSegmentTailGap is the Path-B (stripe writer) case: the
// gap bytes belong to no record and replay must step over them.
func TestReadAllSkipsShortSegmentTailGap(t *testing.T) {
	for _, gapLen := range []int64{8, 16} {
		t.Run(fmt.Sprintf("gap%d", gapLen), func(t *testing.T) {
			walDir, payloads := writeWALWithShortSegmentTailGap(t, gapLen, 1<<20)
			assertAllPayloadsReplayed(t, walDir, payloads)
		})
	}
}

// TestReadAllSkipsShortSegmentTailGapWithStaleBytes is the recycled-segment
// variant: goopg recycles segments by rename, so an unusable tail is not
// necessarily zero — it can hold a previous write cycle's bytes (the measured
// production failure had 0xff there). Those must not be decoded either.
func TestReadAllSkipsShortSegmentTailGapWithStaleBytes(t *testing.T) {
	const gapLen = int64(8)
	walDir, payloads := writeWALWithShortSegmentTailGap(t, gapLen, 1<<20)

	seg0 := filepath.Join(walDir, formatSegmentName(0))
	b, err := os.ReadFile(seg0)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(b)) != tailGapSegSize {
		t.Fatalf("segment 0 is %d bytes, want %d", len(b), tailGapSegSize)
	}
	for i := int64(len(b)) - gapLen; i < int64(len(b)); i++ {
		b[i] = 0xFF
	}
	if err := os.WriteFile(seg0, b, 0o600); err != nil {
		t.Fatal(err)
	}

	assertAllPayloadsReplayed(t, walDir, payloads)
}

// TestReadAllKeepsRecordStraddlingSegmentBoundary is the reader-robustness
// half of the S30.1 fix and the reason the segment-tail skip is CRC-conditional
// rather than positional: a record that legitimately BEGINS in a segment's last
// 8 bytes and continues after the next segment's long page header must still be
// read.
//
// Since M0131-S30.6 no goopg append path produces that shape — Path A now
// re-lands a crossing record at the boundary exactly as the stripe path does —
// so the fixture is built by hand, emitting at the cursor with no re-land the
// way Path A used to. Streams written by older goopg versions have this shape
// on disk, so the reader must keep handling it.
func TestReadAllKeepsRecordStraddlingSegmentBoundary(t *testing.T) {
	walDir, payloads := writeWALWithStraddlingRecord(t, 8)

	// Sanity: this fixture must really straddle, otherwise it guards nothing.
	stream, err := readStreamFrom(walDir, tailGapSegSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	straddleOff := int(tailGapSegSize) - 8
	if !recordStartsAt(stream, straddleOff, int64(straddleOff), tailGapSegSize) {
		t.Fatalf("fixture does not straddle: no CRC-valid record at offset %d", straddleOff)
	}

	assertAllPayloadsReplayed(t, walDir, payloads)
}

// writeWALWithStraddlingRecord builds a two-segment WAL whose first segment
// ends `lead` bytes (lead < 24) before the boundary and whose next record
// STARTS in those bytes and continues into the following segment. The records
// up to the boundary are written by a real Writer; the straddling record and
// the ones behind it are emitted by hand at the cursor (encodeRecordXLog +
// emitWithPageHeaders, prev threaded manually), which is exactly what
// state.appendPGCompat's Path A did before M0131-S30.6.
func writeWALWithStraddlingRecord(t *testing.T, lead int64) (walDir string, payloads []string) {
	t.Helper()
	if lead <= 0 || lead >= int64(xlogRecordHeaderSize) {
		t.Fatalf("lead=%d must be in (0, %d)", lead, xlogRecordHeaderSize)
	}
	walDir = t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: tailGapSegSize,
		Preallocate: true,
		WALBuffers:  1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	sysID, tli := w.stateRef.sysID, w.stateRef.tli

	target := uint64(tailGapSegSize) - uint64(lead)
	var cur, lastStart, lastEnd uint64
	for cur < target {
		remain := int(target - cur)
		step := remain
		if step > 512 {
			step = 512
		}
		p, ok := payloadOfEmittedSize(cur, step, tailGapSegSize)
		if !ok {
			t.Fatalf("no payload produces an emitted size of %d bytes at pos=%d", step, cur)
		}
		start, end, aerr := w.Append([]byte(p))
		if aerr != nil {
			t.Fatalf("append %q: %v", p, aerr)
		}
		payloads = append(payloads, p)
		lastStart, lastEnd, cur = start, end, end
	}
	if cur != target {
		t.Fatalf("cursor landed at %d, want %d", cur, target)
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	w.stateRef.eagerWG.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Hand-emit from the cursor with no boundary re-land. Append returns a
	// 1-based start, so the prev pointer (0-based record-CONTENT start) of the
	// last writer record is start-1.
	pos := int64(target)
	prev := lastStart - 1
	for _, p := range []string{"straddles-the-boundary", "after-boundary-0", "after-boundary-1"} {
		record, realRecLen, eerr := encodeRecordXLog([]byte(p), prev)
		if eerr != nil {
			t.Fatalf("encodeRecordXLog(%q): %v", p, eerr)
		}
		out, leading := emitWithPageHeaders(record, realRecLen, pos, tailGapSegSize, sysID, tli)
		writeStreamBytes(t, walDir, tailGapSegSize, pos, out)
		payloads = append(payloads, p)
		prev = uint64(pos) + uint64(leading)
		pos += int64(len(out))
	}
	return walDir, payloads
}

// writeStreamBytes lands `b` at absolute stream position `pos`, splitting the
// write across segment files and creating (zero-filled) any segment that does
// not exist yet.
func writeStreamBytes(t *testing.T, walDir string, segSize, pos int64, b []byte) {
	t.Helper()
	for len(b) > 0 {
		segNo := uint64(pos / segSize)
		off := pos % segSize
		n := int64(len(b))
		if off+n > segSize {
			n = segSize - off
		}
		name := filepath.Join(walDir, formatSegmentName(segNo))
		if _, err := os.Stat(name); os.IsNotExist(err) {
			if werr := os.WriteFile(name, make([]byte, segSize), 0o600); werr != nil {
				t.Fatal(werr)
			}
		}
		f, err := os.OpenFile(name, os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt(b[:n], off); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		b = b[n:]
		pos += n
	}
}
