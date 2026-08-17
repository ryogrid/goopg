package xlog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// M0131-S30.2 regression guards.
//
// Until this landed, EVERY stop of the replay walk was reported as a clean
// end-of-WAL: a WARN in the log and a successfully started cluster. That is
// how both row-loss defects of the S30 series presented (S30.1: 4416 rows,
// S30.1b: 6762 rows) — the truncation was in the startup log all along and
// nothing acted on it. A stop with durable WAL still behind it must refuse the
// start instead.

// earlyEndSegSize gives each segment four pages, so a fixture can hold several
// pages of records without a 16 MiB file.
const earlyEndSegSize = int64(4 * XLOGBlockSize)

// writeMultiPageWAL appends small records until the stream covers at least
// `pages` pages, and returns the WAL directory and the payloads in order.
func writeMultiPageWAL(t *testing.T, pages int) (walDir string, payloads []string) {
	t.Helper()
	walDir = t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: earlyEndSegSize,
		Preallocate: true,
		WALBuffers:  1 << 20, // Path B — the stripe writer, the production path
	})
	if err != nil {
		t.Fatal(err)
	}
	var lastEnd uint64
	for i := 0; lastEnd < uint64(int64(pages)*XLOGBlockSize); i++ {
		p := fmt.Sprintf("early-end-record-%04d", i)
		_, end, aerr := w.Append([]byte(p))
		if aerr != nil {
			t.Fatalf("append %d: %v", i, aerr)
		}
		payloads = append(payloads, p)
		lastEnd = end
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

// segmentPath returns the first (lowest-numbered) segment file in walDir.
func segmentPath(t *testing.T, walDir string) string {
	t.Helper()
	segNo, err := firstAvailableSegment(walDir)
	if err != nil || segNo < 0 {
		t.Fatalf("firstAvailableSegment(%s) = %d, %v", walDir, segNo, err)
	}
	names, err := filepath.Glob(filepath.Join(walDir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatalf("no segments under %s", walDir)
	}
	// firstAvailableSegment already established the ordering; the sorted glob's
	// first entry is that segment.
	first := names[0]
	for _, n := range names {
		if filepath.Base(n) < filepath.Base(first) {
			first = n
		}
	}
	return first
}

// corruptByteAt flips one byte of the segment file at the given stream offset,
// which breaks the containing record's CRC without changing any framing.
func corruptByteAt(t *testing.T, path string, off int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], off); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	if _, err := f.WriteAt(b[:], off); err != nil {
		t.Fatal(err)
	}
}

// TestReadAllRefusesEarlyEndOfWAL: a record that fails to validate in the
// MIDDLE of the stream, with intact pages behind it, is not an end of WAL —
// ReadAll must return ErrEarlyEndOfWAL rather than the prefix plus a WARN.
func TestReadAllRefusesEarlyEndOfWAL(t *testing.T) {
	walDir, payloads := writeMultiPageWAL(t, 3)
	recs, err := ReadAll(walDir, earlyEndSegSize)
	if err != nil {
		t.Fatalf("baseline ReadAll: %v", err)
	}
	if len(recs) != len(payloads) {
		t.Fatalf("baseline read %d records, wrote %d", len(recs), len(payloads))
	}

	// Corrupt a record body on the FIRST page, so pages 1 and 2 — each of
	// which starts a record of its own — remain durable behind the stop.
	var victim *Record
	for i := range recs {
		if recs[i].StartLSN-1 < XLOGBlockSize && recs[i].EndLSN < XLOGBlockSize {
			victim = &recs[i]
		}
	}
	if victim == nil {
		t.Fatal("no record lies entirely within the first page")
	}
	// StartLSN is 1-based; the payload begins after the 24-byte header.
	corruptByteAt(t, segmentPath(t, walDir), int64(victim.StartLSN-1)+int64(xlogRecordHeaderSize)+1)

	_, err = ReadAll(walDir, earlyEndSegSize)
	if !errors.Is(err, ErrEarlyEndOfWAL) {
		t.Fatalf("ReadAll after mid-stream corruption = %v, want ErrEarlyEndOfWAL", err)
	}

	// The operator escape hatch downgrades the refusal to a WARN, so a cluster
	// whose only option is "start and accept the loss" is not bricked.
	t.Setenv(allowEarlyEndOfWALEnv, "1")
	recs2, err := ReadAll(walDir, earlyEndSegSize)
	if err != nil {
		t.Fatalf("ReadAll with %s=1: %v", allowEarlyEndOfWALEnv, err)
	}
	if len(recs2) >= len(recs) {
		t.Fatalf("override read %d records, want fewer than the intact %d", len(recs2), len(recs))
	}
}

// TestReadAllAcceptsTrueEndOfWAL: the ordinary crash tail — a torn record with
// nothing durable behind it — must still be a clean end of WAL. Without this
// the refusal above would make every crash an unstartable cluster.
func TestReadAllAcceptsTrueEndOfWAL(t *testing.T) {
	walDir, _ := writeMultiPageWAL(t, 3)
	recs, err := ReadAll(walDir, earlyEndSegSize)
	if err != nil {
		t.Fatalf("baseline ReadAll: %v", err)
	}

	// Tear the last PAGE-INTERNAL record and zero everything after it: exactly
	// what a SIGKILL mid-append leaves on a preallocated segment. Page-internal
	// matters because a record straddling a page boundary has 24 header bytes of
	// the next page inside its byte range, and xlp_rem_len is not covered by the
	// record CRC — flipping a byte there is a no-op, not a torn record.
	victim := -1
	for i := range recs {
		if (recs[i].StartLSN-1)/XLOGBlockSize == (recs[i].EndLSN-1)/XLOGBlockSize {
			victim = i
		}
	}
	if victim < 0 {
		t.Fatal("no page-internal record in the fixture")
	}
	path := segmentPath(t, walDir)
	corruptByteAt(t, path, int64(recs[victim].StartLSN-1)+int64(xlogRecordHeaderSize)+1)
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	zeros := make([]byte, earlyEndSegSize-int64(recs[victim].EndLSN))
	if _, err := f.WriteAt(zeros, int64(recs[victim].EndLSN)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := ReadAll(walDir, earlyEndSegSize)
	if err != nil {
		t.Fatalf("ReadAll on a torn tail = %v, want a clean end of WAL", err)
	}
	if len(got) != victim {
		t.Fatalf("read %d records, want %d (everything before the torn record)", len(got), victim)
	}
}

// TestDurableWALAfterIgnoresZeroTail pins the classifier itself: a stream whose
// remainder is the preallocated zero fill carries no durable WAL.
func TestDurableWALAfterIgnoresZeroTail(t *testing.T) {
	stream := make([]byte, 4*XLOGBlockSize)
	if lsn, ok := durableWALAfter(stream, XLOGBlockSize/2, earlyEndSegSize, 0); ok {
		t.Fatalf("durableWALAfter over an all-zero stream = %d, true; want false", lsn)
	}
}
