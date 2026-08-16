package xlog

import (
	"os"
	"path/filepath"
	"testing"
)

// root-0035 regression: a reader that is handed segmentSize 0 must recover the
// cluster's real wal_segment_size from the stream, not assume
// DefaultSegmentSize.
//
// Why this matters is not cosmetic. readAllUncached anchors the stream at
// baseOffset = firstSegNo*segmentSize and reports every record's StartLSN /
// EndLSN relative to it. Assume 16 MiB for a 1 MiB cluster and the LSNs come
// out 16× too large — larger than any pd_lsn on the heap pages. Replay's
// idempotency check (page LSN >= record EndLSN ⇒ already applied) then never
// fires, so startup redo re-applies inserts the running server had already
// applied and the cluster dies with
//
//	wal replay: ...: xlog heap-insert apply: storage: not enough free space in page
//
// which is exactly how TestRestartAfterRetention (internal/server) failed. The
// startup path is the 0-passing caller: initdb.Open →
// wal.ReplayFromDirWithMgr(mgr, pg_wal, 0), and ~30 catalog-recovery modules
// call wal.ReadAll(walDir, 0) behind the recovery cache.
//
// Upstream solves the same problem the same way: a stand-alone WAL reader with
// no cluster context takes WalSegSz from the first file's
// longhdr->xlp_seg_size (postgres/src/bin/pg_waldump/pg_waldump.c,
// search_directory) and validates it with IsValidWalSegSize.
func TestReadAllDerivesSegmentSizeFromStream(t *testing.T) {
	// 1 MiB is the smallest size upstream's IsValidWalSegSize accepts, and
	// it differs from DefaultSegmentSize (16 MiB) — so a reader that falls
	// back to the default is off by 16× and this test catches it.
	const segSize = int64(1 << 20)
	dir := t.TempDir()

	w, err := NewWriter(Config{WALDir: dir, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 512)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	// ~1.5 MiB of payload: enough to spill past the first segment so the
	// baseOffset multiplication below is actually exercised.
	const nRecords = 3000
	type lsnPair struct{ start, end uint64 }
	want := make([]lsnPair, 0, nRecords)
	for i := 0; i < nRecords; i++ {
		start, end, err := w.Append(payload)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		want = append(want, lsnPair{start, end})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	segNos := listSegments(t, dir)
	if len(segNos) < 2 {
		t.Fatalf("need >= 2 segments to exercise a non-zero baseOffset, got %d", len(segNos))
	}

	if got := detectSegmentSizeAt(dir, segNos[0]); got != segSize {
		t.Fatalf("detectSegmentSizeAt = %d, want %d (xlp_seg_size of the long page header)", got, segSize)
	}

	// Drop the leading segments so firstSegNo > 0, the state WAL retention
	// leaves behind and the one where an assumed segment size corrupts
	// baseOffset. Keep the last two so a whole stream still remains.
	dropped := segNos[:len(segNos)-2]
	for _, seg := range dropped {
		if err := os.Remove(filepath.Join(dir, formatSegmentName(seg))); err != nil {
			t.Fatalf("remove segment %d: %v", seg, err)
		}
	}
	liveStart := segNos[len(segNos)-2]

	// The whole point: segmentSize 0 must behave exactly like the explicit,
	// correct size.
	derived, err := readAllUncached(dir, 0)
	if err != nil {
		t.Fatalf("readAllUncached(dir, 0): %v", err)
	}
	explicit, err := readAllUncached(dir, segSize)
	if err != nil {
		t.Fatalf("readAllUncached(dir, %d): %v", segSize, err)
	}
	if len(derived) == 0 {
		t.Fatal("no records decoded from the retained segments")
	}
	if len(derived) != len(explicit) {
		t.Fatalf("segmentSize 0 decoded %d records, explicit %d decoded %d", len(derived), segSize, len(explicit))
	}
	for i := range derived {
		if derived[i].StartLSN != explicit[i].StartLSN || derived[i].EndLSN != explicit[i].EndLSN {
			t.Fatalf("record %d: derived lsn[%d,%d] != explicit lsn[%d,%d]",
				i, derived[i].StartLSN, derived[i].EndLSN, explicit[i].StartLSN, explicit[i].EndLSN)
		}
	}

	// And the LSNs must be the ABSOLUTE positions the writer handed out —
	// the values stamped into pd_lsn — not merely self-consistent ones.
	// Match on the writer's record list by StartLSN.
	byStart := make(map[uint64]lsnPair, len(want))
	for _, p := range want {
		byStart[p.start] = p
	}
	minLSN := uint64(liveStart) * uint64(segSize)
	matched := 0
	for _, r := range derived {
		if r.StartLSN < minLSN {
			t.Fatalf("record StartLSN %d precedes the retained range start %d — baseOffset is wrong", r.StartLSN, minLSN)
		}
		if r.XLog != nil && r.XLog.Header.Rmid == RmgrXLog && r.XLog.Header.Info == xlogInfoNoop {
			// Segment-boundary XLOG_NOOP pad — emitted by the writer itself at
			// a crossing (M0131-S30.6), so it has no entry in the appended
			// record list.
			continue
		}
		p, ok := byStart[r.StartLSN]
		if !ok {
			t.Fatalf("decoded StartLSN %d was never assigned by the writer (baseOffset drift)", r.StartLSN)
		}
		if p.end != r.EndLSN {
			t.Fatalf("record at %d: decoded EndLSN %d, writer assigned %d", r.StartLSN, r.EndLSN, p.end)
		}
		matched++
	}
	if matched == 0 {
		t.Fatal("no decoded record matched a writer-assigned LSN")
	}
}

// TestIsValidWalSegSize pins the bounds detectSegmentSizeAt trusts, mirroring
// upstream's IsValidWalSegSize (postgres/src/include/access/xlog_internal.h):
// a power of two in [1 MB, 1 GB]. A garbage or zeroed xlp_seg_size must be
// rejected so the caller falls back to DefaultSegmentSize instead of anchoring
// the stream at an absurd offset.
func TestIsValidWalSegSize(t *testing.T) {
	valid := []int64{1 << 20, 1 << 21, 16 << 20, 1 << 30}
	for _, s := range valid {
		if !IsValidWalSegSize(s) {
			t.Errorf("IsValidWalSegSize(%d) = false, want true", s)
		}
	}
	invalid := []int64{0, -1, 1 << 16, 3 << 20, (1 << 30) + 1, 1 << 31}
	for _, s := range invalid {
		if IsValidWalSegSize(s) {
			t.Errorf("IsValidWalSegSize(%d) = true, want false", s)
		}
	}
}
