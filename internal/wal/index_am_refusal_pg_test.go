package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0131-S25 — the index-AM boundary.
//
// rmids 12 (hash), 13 (gin), 14 (gist), 16 (spgist) and 17 (brin) are the five
// PG resource managers goopg has no redo for and — unlike the four S23 closed —
// cannot fake. This file guards the two things that CAN be right about a
// refusal:
//
//   - it names the access method, opcode, LSN and relation, instead of
//     "unsupported xlog record rmid=13"; and
//   - the pre-flight scan reports EVERY distinct AM in the stream in one error,
//     before a single page is written.
//
// Design: docs/design/0131-0015-*.md §S25. Rationale for refusing rather than
// attempting an FPI-only replay is in index_am_refusal.go's header comment
// (GiST's NSN is not in the image; REGBUF_WILL_INIT blocks carry no image).

// decodeTestIndexAMRecord builds a structurally valid PG record under rmid/info
// and returns it in the shape the reader hands to replay: XLog populated,
// LSNs set. ApplyRecord only routes to the decoded dispatcher when r.XLog is
// non-nil, so a hand-built record without it would silently take the native
// payload[0] path and prove nothing.
func decodeTestIndexAMRecord(t *testing.T, rmid Rmgr, info byte, start, end uint64) Record {
	t.Helper()
	raw := encodeTestPGRecordRmid(t, rmid, info, []byte{9})
	decoded, err := decodeRecordXLogDetailed(raw)
	if err != nil {
		t.Fatalf("rmid=%d: decode err = %v, want nil", rmid, err)
	}
	return Record{StartLSN: start, EndLSN: end, XLog: decoded.XLog}
}

// TestIndexAMRefusalNamesAccessMethodAndLSN is the headline guard: the refusal
// message must be actionable. Every one of the five says the access method by
// the name a user typed in `USING <am>`, its rmid, its opcode and the LSN span
// — and stays fail-closed (error, applied=false, ErrUnsupportedRecord).
func TestIndexAMRefusalNamesAccessMethodAndLSN(t *testing.T) {
	cases := []struct {
		rmid Rmgr
		info byte
		am   string
	}{
		{RmgrHash, 0x20, "hash"},     // XLOG_HASH_INSERT
		{RmgrGin, 0x20, "gin"},       // XLOG_GIN_INSERT
		{RmgrGist, 0x30, "gist"},     // XLOG_GIST_PAGE_SPLIT
		{RmgrSPGist, 0x10, "spgist"}, // SPGIST_ADD_LEAF
		{RmgrBrin, 0x20, "brin"},     // XLOG_BRIN_UPDATE
	}
	for _, tc := range cases {
		r := decodeTestIndexAMRecord(t, tc.rmid, tc.info, 4200, 4300)
		// The arm refuses before touching storage, so a nil manager keeps this
		// a pure unit test.
		applied, err := replayDecodedXLogRecord(nil, r)
		if err == nil {
			t.Fatalf("%s: replay err = nil (applied=%v), want refusal", tc.am, applied)
		}
		if applied {
			t.Fatalf("%s: applied = true alongside error", tc.am)
		}
		if !errors.Is(err, ErrUnsupportedRecord) {
			t.Fatalf("%s: err = %v, want ErrUnsupportedRecord", tc.am, err)
		}
		msg := err.Error()
		for _, want := range []string{
			tc.am,
			"rmid=" + strconv.Itoa(int(tc.rmid)),
			fmt.Sprintf("opcode=0x%02x", tc.info),
			"lsn[4200,4300]",
			"REINDEX",
		} {
			if !strings.Contains(msg, want) {
				t.Fatalf("%s: err = %q, want it to contain %q", tc.am, msg, want)
			}
		}
	}
}

// TestIndexAMRefusalMasksBrinInitPageFlag covers the one per-AM detail that is
// easy to get wrong: XLOG_BRIN_INIT_PAGE (0x80, brin_xlog.h:43) is a FLAG ORed
// onto the opcode, masked off with XLOG_BRIN_OPMASK (0x70, brin_xlog.h:38)
// before brin_redo's switch. Reporting 0xa0 would name an opcode that does not
// exist in brin_xlog.h and send the reader hunting for it.
func TestIndexAMRefusalMasksBrinInitPageFlag(t *testing.T) {
	// XLOG_BRIN_UPDATE (0x20) | XLOG_BRIN_INIT_PAGE (0x80).
	r := decodeTestIndexAMRecord(t, RmgrBrin, 0xA0, 10, 20)
	_, err := replayDecodedXLogRecord(nil, r)
	if err == nil {
		t.Fatal("replay err = nil, want refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "opcode=0x20") {
		t.Fatalf("err = %q, want the OPMASK-ed opcode 0x20", msg)
	}
	if strings.Contains(msg, "opcode=0xa0") {
		t.Fatalf("err = %q, reports the init-page flag as part of the opcode", msg)
	}
	if !strings.Contains(msg, "init_page") {
		t.Fatalf("err = %q, want the init-page flag reported separately", msg)
	}
}

// TestPreflightIndexAMReportsEveryAMAtOnce: one failed start must teach the
// whole boundary. A stream carrying two GIN records and one BRIN record names
// both AMs, counts them, and gives the first LSN of each — rather than stopping
// at the first GIN record and saying nothing about BRIN.
func TestPreflightIndexAMReportsEveryAMAtOnce(t *testing.T) {
	records := []Record{
		decodeTestIndexAMRecord(t, RmgrGin, 0x20, 100, 200),
		decodeTestIndexAMRecord(t, RmgrBrin, 0x20, 300, 400),
		decodeTestIndexAMRecord(t, RmgrGin, 0x30, 500, 600),
	}
	err := preflightIndexAMRecords(records)
	if err == nil {
		t.Fatal("preflight err = nil, want a refusal naming gin and brin")
	}
	if !errors.Is(err, ErrUnsupportedRecord) {
		t.Fatalf("err = %v, want ErrUnsupportedRecord", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"gin (rmid=13, 2 record(s), first at lsn[100,200]",
		"brin (rmid=17, 1 record(s), first at lsn[300,400]",
		"nothing has been replayed",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("err = %q, want it to contain %q", msg, want)
		}
	}
	// rmid order, so the message reads the same way every run.
	if strings.Index(msg, "gin (rmid=13") > strings.Index(msg, "brin (rmid=17") {
		t.Fatalf("err = %q, want AMs reported in rmid order", msg)
	}
}

// TestPreflightIndexAMPassesCleanStream is the counterweight: the scan must not
// refuse a stream that merely contains PG rmgrs goopg DOES handle. A false
// positive here would refuse every real-PG crash tail.
func TestPreflightIndexAMPassesCleanStream(t *testing.T) {
	heapRaw, _, _ := encodeTestPGHeapInsertRecord(t)
	heapDecoded, err := decodeRecordXLogDetailed(heapRaw)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		{StartLSN: 1, EndLSN: 2, XLog: heapDecoded.XLog},
		decodeTestIndexAMRecord(t, RmgrLogicalMessage, xlogLogicalMessage, 3, 4),
		decodeTestIndexAMRecord(t, RmgrBtree, xlogBtreeInsertLeaf, 5, 6),
		{StartLSN: 7, EndLSN: 8, Payload: []byte{RecordKindPageImage}}, // XLog == nil
	}
	if err := preflightIndexAMRecords(records); err != nil {
		t.Fatalf("preflight err = %v, want nil for a stream with no index-AM record", err)
	}
	if err := preflightIndexAMRecords(nil); err != nil {
		t.Fatalf("preflight(nil) err = %v, want nil", err)
	}
}

// TestPreflightIndexAMRefusesBeforeApplyingAnything is the reason the scan
// exists rather than relying on the per-record arm. The per-record refusal also
// stops the start, but only AFTER applying every record before the offending
// one — leaving a data directory a real PG would then have to reconcile with a
// pg_control that was never advanced.
//
// The test proves both halves in the same run: the prefix record alone DOES
// create a relation file (so the assertion below is not vacuous), and the same
// prefix followed by a GIN record creates nothing.
func TestPreflightIndexAMRefusesBeforeApplyingAnything(t *testing.T) {
	heapRaw, _, _ := encodeTestPGHeapInsertRecord(t)
	heapDecoded, err := decodeRecordXLogDetailed(heapRaw)
	if err != nil {
		t.Fatal(err)
	}
	heapRec := Record{StartLSN: 1, EndLSN: 2, XLog: heapDecoded.XLog}

	// Half 1: the prefix on its own reaches storage.
	aloneDir := t.TempDir()
	aloneMgr := storage.NewManager(storage.ManagerConfig{DataDir: aloneDir})
	defer aloneMgr.Close()
	if _, err := ReplayRecords(aloneMgr, []Record{heapRec}); err != nil {
		t.Fatalf("prefix-only replay err = %v, want nil", err)
	}
	if n := countRegularFilesTest(t, aloneDir); n == 0 {
		t.Fatal("prefix-only replay wrote no file — the guard below would be vacuous")
	}

	// Half 2: the same prefix behind an index-AM record writes nothing.
	guardDir := t.TempDir()
	guardMgr := storage.NewManager(storage.ManagerConfig{DataDir: guardDir})
	defer guardMgr.Close()
	stats, err := ReplayRecords(guardMgr, []Record{
		heapRec,
		decodeTestIndexAMRecord(t, RmgrGin, 0x20, 3, 4),
	})
	if err == nil {
		t.Fatal("replay err = nil, want the pre-flight refusal")
	}
	if !strings.Contains(err.Error(), "gin") {
		t.Fatalf("err = %v, want the gin pre-flight refusal", err)
	}
	if stats.Applied != 0 {
		t.Fatalf("stats.Applied = %d, want 0 (pre-flight must run before the apply loop)", stats.Applied)
	}
	if n := countRegularFilesTest(t, guardDir); n != 0 {
		t.Fatalf("%d file(s) written under the data dir; the pre-flight refusal must mutate nothing", n)
	}
}

func countRegularFilesTest(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	if err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
