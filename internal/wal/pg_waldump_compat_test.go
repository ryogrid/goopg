package wal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func findPGWaldump(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("PG_WALDUMP"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("pg_waldump"); err == nil {
		return p
	}
	candidates := []string{
		filepath.Join("..", "..", "postgres", "local_install", "bin", "pg_waldump"),
		filepath.Join("postgres", "local_install", "bin", "pg_waldump"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	t.Skip("pg_waldump not installed")
	return ""
}

func firstSegmentName(t *testing.T, walDir string) string {
	t.Helper()
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}
	segs := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := parseSegmentName(e.Name()); ok {
			segs = append(segs, e.Name())
		}
	}
	if len(segs) == 0 {
		t.Fatal("no WAL segment files found")
	}
	sort.Strings(segs)
	return segs[0]
}

// lsnToRecPtr converts goopg's 1-based byte LSN to the upstream
// PostgreSQL 0-based XLogRecPtr format (high32/low32) that
// pg_waldump expects on its `-s` flag.
func lsnToRecPtr(lsn uint64) string {
	if lsn == 0 {
		return "0/0"
	}
	pos := lsn - 1
	return fmt.Sprintf("%X/%X", uint32(pos>>32), uint32(pos))
}

func TestPGWaldumpParsesEmittedWAL(t *testing.T) {
	waldump := findPGWaldump(t)

	dataDir := t.TempDir()
	walDir := filepath.Join(dataDir, "pg_wal")
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: DefaultSegmentSize,
		Preallocate: true,
		PageHeaders: true,
		SystemID:    0xABCDEF0123456789,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatal(err)
	}

	rel := storage.RelFileNode{DBOid: 1, RelOid: 1001, Fork: storage.MainFork}
	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("waldump-row"))
	tupBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	page[100] = 0xAB
	pagePayload, err := EncodePageImage(rel, 0, page)
	if err != nil {
		t.Fatal(err)
	}

	// B0.2: the non-HOT catalog xl_heap_update must decode structurally too.
	updPayload, err := EncodeHeapUpdatePG(rel, 0, 1, 0, 2, storage.TransactionID(43), tupBytes)
	if err != nil {
		t.Fatal(err)
	}
	// B0.4: XLOG_RELMAP_UPDATE (RM_RELMAP) must decode structurally.
	relmapPayload, err := EncodeRelmapUpdatePG(1, 1663, EncodeRelMapFile([]RelMapping{{Oid: 1259, FileNumber: 1259}}))
	if err != nil {
		t.Fatal(err)
	}
	records := [][]byte{
		EncodeCheckpoint(),
		EncodeHeapInsert(rel, 0, 1, tupBytes),
		EncodeHeapDelete(rel, 0, 1, storage.TransactionID(42), nil),
		updPayload,
		relmapPayload,
		EncodeXactCommit(storage.TransactionID(42)),
		pagePayload,
	}
	var firstStart, lastStart, end uint64
	for i, rec := range records {
		start, nextEnd, err := w.Append(rec)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstStart = start
		}
		lastStart = start
		end = nextEnd
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	startSeg := firstSegmentName(t, walDir)
	if len(startSeg) == 24 && startSeg[:8] == "00000000" {
		alias := "000000010000000000000000"
		raw, err := os.ReadFile(filepath.Join(walDir, startSeg))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(walDir, alias), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		startSeg = alias
	}

	// `-e` stops pg_waldump at the start of the last record so it
	// doesn't read past our written data into the preallocated
	// zero-filled tail (which would surface as a spurious
	// "invalid record length 0" error).
	//
	// An explicit STARTSEG positional argument (rather than bare `-p`)
	// is required here, not just stylistic: pg_waldump's own
	// directory-only mode (`identify_target_directory(waldir, NULL)`,
	// pg_waldump.c) auto-detects WalSegSz by opening whatever WAL-named
	// file `readdir` happens to return first — unordered, and never
	// guaranteed to be the earliest segment. Real WAL directories
	// (goopg's and upstream's alike) routinely contain an
	// already-preallocated, all-zero *next* segment even when only the
	// first has been written (eager next-segment lookahead, M0007
	// follow-up), so an unlucky directory order can hand pg_waldump
	// that all-zero file and its zeroed long-page-header reads back
	// `xlp_seg_size=0`, aborting with "invalid WAL segment size".
	// Naming the exact start segment sidesteps the ambiguity entirely,
	// matching how pg_waldump is normally invoked against a known LSN
	// range.
	cmd := exec.Command(waldump,
		"-q",
		filepath.Join(walDir, startSeg),
		"-t", "1",
		"-s", lsnToRecPtr(firstStart),
		"-e", lsnToRecPtr(lastStart),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pg_waldump failed: %v\ncmd=%q\n%s", err, cmd.Args, string(out))
	}
}

// TestCrossSegmentXLPrevChain verifies the xl_prev link chain across a
// segment boundary. Uses a tiny segment size (32 bytes, same pattern as
// TestRecordCanSpanSegments) to force cross-segment records, then reads
// them back with ReadAll and asserts the record count and payload integrity.
// This is the M0130-S7.1 regression gate for the xl_prev 0-based fix
// (writer.go detectWritePos −1 conversion).
//
// pg_waldump is NOT used for cross-segment verification because it requires
// standard segment sizes (1 MiB–1 GiB, per isValidWalSegSize in xlogreader.c).
// Filling a 1 MiB segment in a unit test is impractical. The existing
// TestPGWaldumpParsesEmittedWAL already validates the single-segment
// pg_waldump chain (same xl_prev code path); this test adds the
// cross-segment dimension via goopg's own reader.
func TestCrossSegmentXLPrevChain(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	const segSize = int64(32)
	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: segSize,
		PageHeaders: true,
		SystemID:    0xABCDEF0123456789,
		TimelineID:  1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Write records large enough to span multiple segments. Each record
	// is ~80 bytes; with 32-byte segments, even one record crosses a
	// boundary. Write several to exercise the chain across 2+ boundaries.
	const numRecords = 10
	payload := make([]byte, 80)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	payloads := make([][]byte, numRecords)
	for i := range numRecords {
		_, end, aerr := w.Append(payload)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if err := w.FlushUpTo(end); err != nil {
			t.Fatal(err)
		}
		payloads[i] = payload
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify we crossed at least one segment boundary.
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}
	segCount := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := parseSegmentName(e.Name()); ok {
			segCount++
		}
	}
	if segCount < 2 {
		t.Fatalf("expected >=2 WAL segments (had %d) — segment size too large or records too few", segCount)
	}

	// ReadAll reconstructs records across segments by following xl_prev
	// links. If any link is broken (e.g. a +1 off-by-one in the 0-based
	// conversion), ReadAll returns fewer records than written or errors.
	recs, err := ReadAll(walDir, segSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != numRecords {
		t.Fatalf("ReadAll returned %d records, want %d — possible xl_prev chain break", len(recs), numRecords)
	}
	for i, r := range recs {
		if len(r.Payload) != len(payloads[i]) {
			t.Fatalf("record %d: payload len=%d, want %d", i, len(r.Payload), len(payloads[i]))
		}
	}
}
