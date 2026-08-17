package xlog

import (
	"fmt"
	"strings"
	"testing"
)

// M0131-S30.1b regression: the XLOG_NOOP pad that fills a cross-segment gap
// must be laid out with page headers, exactly like every other record.
//
// The gap `[curr, boundary)` that insertPosTracker.reserveEmittedAndPublish
// opens when a reservation would cross a segment boundary is a range of STREAM
// bytes. In page-header mode the stream is not all record bytes: every 8 KiB
// boundary inside that range holds a page header. emitSegmentPad used to build
// a `boundary-curr`-byte record and write it verbatim at `curr`, which
// (a) overwrote any page-header slot inside the gap with record bytes and
// (b) made the pad's own record length overrun the boundary by the same
// amount. Replay then read the pad's xl_info/xl_rmid pair — 0x20 (XLOG_NOOP)
// followed by 0x00 (RM_XLOG_ID) — where a page header belongs and stopped:
//
//	end of WAL reached during replay reason="invalid page header"
//	detail="wal: invalid page header: magic=0x0020 want 0xd118" lsn=117432305
//
// 117432304 is 16 bytes before the page boundary at 117432320, which is itself
// one page before the 112 MiB segment boundary — i.e. pad byte 16, the xl_info
// field, landed on the page-header slot. Every committed record behind that
// point was discarded (measured: 6762 of 500000 rows,
// `RUNS=3 bash analysis/crashprobe30.sh`).
//
// The fixture below reproduces that geometry exactly: the write cursor is
// parked `pageGapLead` bytes before the LAST page boundary of segment 0, then
// a record too large for the remaining 8 KiB + lead is appended, so the pad
// spans one interior page boundary.
//
// See docs/design/0131-0028.

// padPageGapSegSize gives segment 0 four pages, so a gap can span a page
// boundary without the fixture needing a 16 MiB segment.
const padPageGapSegSize = int64(4 * XLOGBlockSize)

// writeWALWithPageSpanningSegmentPad builds a two-segment WAL whose first
// segment carries a cross-segment pad starting `lead` bytes before the
// segment's last page boundary, and returns the directory plus every payload
// appended in order.
func writeWALWithPageSpanningSegmentPad(t *testing.T, lead int64) (walDir string, payloads []string) {
	t.Helper()
	walDir = t.TempDir()
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: padPageGapSegSize,
		Preallocate: true,
		WALBuffers:  1 << 20, // Path B — the stripe writer, the production path
	})
	if err != nil {
		t.Fatal(err)
	}

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

	// payloadFor(pos, want) yields a payload whose emitted size at `pos` is
	// exactly `want` bytes — the same prediction the writer itself uses, so
	// the cursor's landing position is known before each Append.
	payloadFor := func(pos uint64, want int) (string, bool) {
		for n := 1; n <= want; n++ {
			p := fmt.Sprintf("%0*d", n, n%10)
			_, padded := predictXLogRecordLen([]byte(p))
			total, _ := predictEmittedSize(padded, int64(pos), padPageGapSegSize)
			if total == want {
				return p, true
			}
		}
		return "", false
	}

	// Park the cursor `lead` bytes before segment 0's last page boundary.
	target := uint64(padPageGapSegSize - XLOGBlockSize - lead)
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

	// This record cannot fit the remaining `XLOGBlockSize + lead` bytes, so
	// the reservation re-lands at the boundary and pads the gap — and the gap
	// spans the page boundary at padPageGapSegSize-XLOGBlockSize.
	appendPayload("crosses-the-boundary-" + strings.Repeat("x", XLOGBlockSize))
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

// TestSegmentPadSpanningPageBoundaryReplays is the end-to-end guard: every
// committed payload must survive replay. Before the fix the walk stopped at
// the clobbered page header and the records behind it were lost.
func TestSegmentPadSpanningPageBoundaryReplays(t *testing.T) {
	// lead=16 is the production geometry (the pad's xl_info byte lands on the
	// page-header slot); 8 and 64 vary which pad byte does.
	for _, lead := range []int64{8, 16, 64} {
		t.Run(fmt.Sprintf("lead%d", lead), func(t *testing.T) {
			walDir, payloads := writeWALWithPageSpanningSegmentPad(t, lead)

			recs, err := readAllUncached(walDir, padPageGapSegSize)
			if err != nil {
				t.Fatalf("readAllUncached: %v", err)
			}
			got := make(map[string]bool, len(recs))
			for _, r := range recs {
				got[string(r.Payload)] = true
			}
			for i, p := range payloads {
				if !got[p] {
					t.Fatalf("payload %d/%d (%d bytes) missing from replay (%d records read) — "+
						"the WAL tail behind a page-spanning segment pad was discarded",
						i+1, len(payloads), len(p), len(recs))
				}
			}
		})
	}
}

// TestSegmentPadKeepsPageHeaderIntact pins the mechanism rather than the
// symptom: the page header inside the padded gap must still be a real page
// header claiming its own address.
func TestSegmentPadKeepsPageHeaderIntact(t *testing.T) {
	walDir, _ := writeWALWithPageSpanningSegmentPad(t, 16)

	stream, err := readStreamFrom(walDir, padPageGapSegSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	off := int(padPageGapSegSize - XLOGBlockSize) // the boundary inside the gap
	if len(stream) < off+SizeOfXLogShortPHD {
		t.Fatalf("stream is %d bytes, need at least %d", len(stream), off+SizeOfXLogShortPHD)
	}
	hdr, err := DecodeXLogPageHeader(stream[off : off+SizeOfXLogShortPHD])
	if err != nil {
		t.Fatalf("page header at %d does not decode: %v (bytes %x) — "+
			"the segment pad overwrote it", off, err, stream[off:off+8])
	}
	if hdr.Magic != XLOGPageMagic {
		t.Fatalf("page header at %d has magic=0x%04x, want 0x%04x", off, hdr.Magic, XLOGPageMagic)
	}
	if hdr.PageAddr != uint64(off) {
		t.Fatalf("page header at %d claims xlp_pageaddr=%d", off, hdr.PageAddr)
	}
	if hdr.Info&XLPFirstIsContRecord == 0 {
		t.Fatalf("page header at %d does not flag XLP_FIRST_IS_CONTRECORD; the pad "+
			"record continues onto this page", off)
	}
}

// TestPageHeaderBytesIn pins the arithmetic emitSegmentPad relies on: a
// boundary at `from` counts (the emitter writes a leading header there), one
// at `to` does not (the emitter stops after the record's last byte).
func TestPageHeaderBytesIn(t *testing.T) {
	const seg = int64(4 * XLOGBlockSize)
	cases := []struct {
		from, to int64
		want     int
	}{
		{0, 10, SizeOfXLogLongPHD},                              // leading long header at a segment boundary
		{XLOGBlockSize, XLOGBlockSize + 10, SizeOfXLogShortPHD}, // leading short header
		{100, 200, 0},           // wholly inside one page
		{100, XLOGBlockSize, 0}, // ends exactly on the boundary
		{XLOGBlockSize - 16, XLOGBlockSize + 1, SizeOfXLogShortPHD},
		{XLOGBlockSize - 16, 3 * XLOGBlockSize, 2 * SizeOfXLogShortPHD},
		{seg - 16, seg, 0},                     // the boundary at `to` is not counted
		{seg - 16, seg + 1, SizeOfXLogLongPHD}, // ...but crossing into it is, long-form
		{200, 100, 0},                          // degenerate
	}
	for _, c := range cases {
		if got := pageHeaderBytesIn(c.from, c.to, seg); got != c.want {
			t.Errorf("pageHeaderBytesIn(%d, %d) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
}
