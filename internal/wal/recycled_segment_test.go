package wal

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// M0131-S19 regression suite: goopg must stop trusting a WAL page that does
// not claim the address it is stored at.
//
// Why this class of bug exists at all: goopg zero-fills a recycled segment,
// so "all-zero page header" has always been a sufficient end-of-WAL sentinel
// for goopg's OWN directories. PostgreSQL does not zero-fill —
// `InstallXLogFileSegment` (postgres/src/backend/access/transam/xlog.c:3559)
// is a bare `durable_rename`, so a real PG's pg_wal holds full-size "future"
// segments still packed with CRC-valid records from a previous write cycle,
// and the tail of the *current* segment past the write position is likewise
// old data rather than zeros. Upstream's only defence is the xlp_pageaddr
// comparison in `XLogReaderValidatePageHeader`
// (postgres/src/backend/access/transam/xlogreader.c:1319-1337). goopg decoded
// and wrote xlp_pageaddr but never compared it, so both the reader and the
// restart-position scanner walked straight into that stale data.

// recycledSegSize is a multiple of XLOGBlockSize so that segments really do
// contain page headers at the offsets the walkers expect. (The older
// detect-tests use segSize=1024, which is smaller than a page and therefore
// never exercises a page boundary at all.)
const recycledSegSize = int64(4 * XLOGBlockSize)

// writeRecycledWAL builds a one-segment WAL directory holding `n` real
// records and returns the directory plus the true end LSN of the last record.
func writeRecycledWAL(t *testing.T, payloads []string) (walDir string, lastEnd uint64) {
	t.Helper()
	walDir = t.TempDir()
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: recycledSegSize, Preallocate: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		_, end, err := w.Append([]byte(p))
		if err != nil {
			t.Fatal(err)
		}
		lastEnd = end
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	w.stateRef.eagerWG.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return walDir, lastEnd
}

// TestDetectWritePos_IgnoresPGRecycledFutureSegment is the load-bearing half
// (Hard-won Rule #2's sibling path): a PG-style recycled segment — full size,
// NOT zero-filled, packed with decodable records whose page headers carry a
// previous cycle's addresses.
//
// Before M0131-S19 the phantom-drop loop in detectWritePos only skipped a
// trailing segment that scanned as *empty*, so a recycled segment (which
// scans as very much non-empty) broke the loop on its first iteration. The
// genuinely-active segment below it was then classified "non-final, therefore
// fully used" and writePos landed a whole segment past the true end of the
// stream — inside stale bytes, which the next append would then interleave
// with.
func TestDetectWritePos_IgnoresPGRecycledFutureSegment(t *testing.T) {
	walDir, lastEnd := writeRecycledWAL(t, []string{"alpha", "beta", "gamma"})

	// Recycle segment 0 into position 1 exactly the way PG does: rename the
	// bytes into place, untouched. (goopg's own eager lookahead would have
	// left segment 1 zero-filled; overwrite whatever is there.)
	seg0, err := os.ReadFile(filepath.Join(walDir, formatSegmentName(0)))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(seg0)) != recycledSegSize {
		t.Fatalf("segment 0 is %d bytes, expected a preallocated %d", len(seg0), recycledSegSize)
	}
	if err := os.WriteFile(filepath.Join(walDir, formatSegmentName(1)), seg0, 0o644); err != nil {
		t.Fatal(err)
	}

	writePos, _, err := detectWritePos(walDir, recycledSegSize, false)
	if err != nil {
		t.Fatalf("detectWritePos: %v", err)
	}
	if writePos != int64(lastEnd) {
		t.Fatalf("writePos = %d, want %d — segment 1 is a PG-style recycled segment "+
			"(stale pageaddr) and must not be mistaken for live WAL", writePos, lastEnd)
	}
}

// TestReadAll_StopsAtStalePageAddr is the reader half. It is conditional in
// the field — it only bites when the live stream happens to end exactly on a
// page boundary, so that the walker reaches a stale page HEADER instead of
// stale mid-page bytes (which fail record decoding anyway). The test creates
// that condition directly: page 1's header keeps its valid magic and its
// records, but claims an address from an earlier cycle.
func TestReadAll_StopsAtStalePageAddr(t *testing.T) {
	// Enough payload to spill well past the first 8 KiB page.
	payloads := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		payloads = append(payloads, "the quick brown fox jumps over the lazy dog")
	}
	walDir, _ := writeRecycledWAL(t, payloads)

	baseline, err := ReadAll(walDir, recycledSegSize)
	if err != nil {
		t.Fatalf("baseline ReadAll: %v", err)
	}
	var beyondPage0 int
	for _, r := range baseline {
		if r.StartLSN > uint64(XLOGBlockSize) {
			beyondPage0++
		}
	}
	if beyondPage0 == 0 {
		t.Fatalf("test setup is vacuous: all %d records fit in page 0, so no second page header exists to falsify", len(baseline))
	}

	// Falsify page 1's xlp_pageaddr in place (bytes 8..16 of the short
	// header at offset XLOGBlockSize). Everything else about the page —
	// magic, info bits, the records behind it — stays valid, which is
	// exactly what a recycled page looks like.
	segPath := filepath.Join(walDir, formatSegmentName(0))
	seg, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatal(err)
	}
	hdrOff := int(XLOGBlockSize)
	if _, derr := DecodeXLogPageHeader(seg[hdrOff : hdrOff+SizeOfXLogShortPHD]); derr != nil {
		t.Fatalf("expected a valid page header at offset %d: %v", hdrOff, derr)
	}
	binary.LittleEndian.PutUint64(seg[hdrOff+8:hdrOff+16], 0)
	if err := os.WriteFile(segPath, seg, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadAll(walDir, recycledSegSize)
	if err != nil {
		t.Fatalf("ReadAll after falsifying page 1: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected page 0's records to survive; the stale page is only page 1")
	}
	if len(got) >= len(baseline) {
		t.Fatalf("ReadAll returned %d records, baseline was %d — the stale page 1 was replayed as live WAL", len(got), len(baseline))
	}
	// Every surviving record must lie inside page 0 — including the record
	// that straddles the boundary, which checkSpan rejects because the page
	// header it swallows is the stale one.
	for _, r := range got {
		if r.StartLSN > uint64(XLOGBlockSize) {
			t.Fatalf("record at LSN %d starts beyond the stale page boundary %d", r.StartLSN, XLOGBlockSize)
		}
	}
}

// TestXLogPageValidatorMatchesUpstreamChecks pins each individual rule the
// validator ports, so a future edit that loosens one is caught by name rather
// than by an integration test that happens to notice.
func TestXLogPageValidatorMatchesUpstreamChecks(t *testing.T) {
	const segSize = int64(4 * XLOGBlockSize)

	shortHdr := func(pageAddr uint64, tli uint32) []byte {
		buf := make([]byte, SizeOfXLogShortPHD)
		if err := EncodeXLogPageHeader(buf, XLogPageHeader{Magic: XLOGPageMagic, TLI: tli, PageAddr: pageAddr}); err != nil {
			t.Fatal(err)
		}
		return buf
	}
	longHdr := func(pageAddr uint64, tli uint32, segSz uint32, blcksz uint32) []byte {
		buf := make([]byte, SizeOfXLogLongPHD)
		if err := EncodeXLogLongPageHeader(buf, XLogLongPageHeader{
			Std:        XLogPageHeader{Magic: XLOGPageMagic, TLI: tli, PageAddr: pageAddr},
			SysID:      0xDEADBEEF,
			SegSize:    segSz,
			XLogBlcksz: blcksz,
		}); err != nil {
			t.Fatal(err)
		}
		return buf
	}

	t.Run("accepts a well-formed sequence", func(t *testing.T) {
		v := xlogPageValidator{segSize: segSize}
		if err := v.check(longHdr(0, 1, uint32(segSize), uint32(XLOGBlockSize)), 0); err != nil {
			t.Fatalf("segment-start page rejected: %v", err)
		}
		if err := v.check(shortHdr(uint64(XLOGBlockSize), 1), uint64(XLOGBlockSize)); err != nil {
			t.Fatalf("mid-segment page rejected: %v", err)
		}
		// A later timeline is fine — only going BACKWARDS is an error.
		if err := v.check(shortHdr(uint64(2*XLOGBlockSize), 2), uint64(2*XLOGBlockSize)); err != nil {
			t.Fatalf("forward timeline switch rejected: %v", err)
		}
	})

	t.Run("rejects a stale pageaddr", func(t *testing.T) {
		v := xlogPageValidator{segSize: segSize}
		if err := v.check(shortHdr(0, 1), uint64(XLOGBlockSize)); err == nil {
			t.Fatal("a page claiming address 0 while stored at 8192 must be rejected")
		}
	})

	t.Run("rejects a backwards timeline", func(t *testing.T) {
		v := xlogPageValidator{segSize: segSize}
		if err := v.check(shortHdr(uint64(XLOGBlockSize), 3), uint64(XLOGBlockSize)); err != nil {
			t.Fatal(err)
		}
		if err := v.check(shortHdr(uint64(2*XLOGBlockSize), 2), uint64(2*XLOGBlockSize)); err == nil {
			t.Fatal("TLI 2 after TLI 3 must be rejected as out-of-sequence")
		}
	})

	t.Run("rejects header-form mismatches", func(t *testing.T) {
		v := xlogPageValidator{segSize: segSize}
		if err := v.check(shortHdr(0, 1), 0); err == nil {
			t.Fatal("a segment's first page without XLP_LONG_HEADER must be rejected")
		}
		v = xlogPageValidator{segSize: segSize}
		if err := v.check(longHdr(uint64(XLOGBlockSize), 1, uint32(segSize), uint32(XLOGBlockSize)), uint64(XLOGBlockSize)); err == nil {
			t.Fatal("a long header on a non-first page must be rejected")
		}
	})

	t.Run("rejects foreign geometry", func(t *testing.T) {
		v := xlogPageValidator{segSize: segSize}
		if err := v.check(longHdr(0, 1, uint32(segSize)*2, uint32(XLOGBlockSize)), 0); err == nil {
			t.Fatal("xlp_seg_size from a differently-configured cluster must be rejected")
		}
		v = xlogPageValidator{segSize: segSize}
		if err := v.check(longHdr(0, 1, uint32(segSize), uint32(XLOGBlockSize)/2), 0); err == nil {
			t.Fatal("xlp_xlog_blcksz from a differently-configured cluster must be rejected")
		}
	})
}
