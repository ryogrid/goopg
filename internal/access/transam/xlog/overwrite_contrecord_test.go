package xlog

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestOverwriteContrecordForTornContinuation: a record spanning pages whose
// next page never reached disk is detected as xlogreader.c's assembled-record
// failure (abortedRecPtr = the record, missingContrecPtr = that page), and
// WriteOverwriteContrecord puts XLOG_OVERWRITE_CONTRECORD first on that page
// under XLP_FIRST_IS_OVERWRITE_CONTRECORD, with xl_prev at the last complete
// record; a WAL writer then resumes right after it. M0149-0017a.
func TestOverwriteContrecordForTornContinuation(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range []int{100, 300, 20000} {
		payload := EncodeHeapVacuum(storage.RelFileNode{DBOid: 5, RelOid: uint32(16400 + i)}, 0, make([]uint16, n/2))
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadAll(walDir, DefaultSegmentSize)
	if err != nil || len(recs) < 3 {
		t.Fatalf("ReadAll: %d records, %v", len(recs), err)
	}
	big, prev := recs[len(recs)-1], recs[len(recs)-2]
	bigStart := big.StartLSN - 1
	nextPage := bigStart - bigStart%XLOGBlockSize + XLOGBlockSize
	if big.EndLSN <= nextPage {
		t.Fatalf("test record does not span pages (%#x..%#x)", bigStart, big.EndLSN)
	}
	// The continuation page and everything after it never reached disk.
	seg := filepath.Join(walDir, FormatSegmentNameTLI(0, 1))
	data, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	for i := nextPage; i < uint64(len(data)); i++ {
		data[i] = 0
	}
	if err := os.WriteFile(seg, data, 0o600); err != nil {
		t.Fatal(err)
	}

	mc, ok, err := FindMissingContrecord(walDir, DefaultSegmentSize, recs[0].StartLSN-1)
	if err != nil || !ok {
		t.Fatalf("FindMissingContrecord = %+v, %v, %v; want a missing contrecord", mc, ok, err)
	}
	if mc.AbortedRecPtr != bigStart || mc.MissingContrecPtr != nextPage || mc.PrevRecPtr != prev.StartLSN-1 {
		t.Fatalf("got %+v, want aborted %#x missing %#x prev %#x", mc, bigStart, nextPage, prev.StartLSN-1)
	}

	end, err := WriteOverwriteContrecord(walDir, DefaultSegmentSize, mc, 12345)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(seg)
	hdr, err := DecodeXLogPageHeader(data[nextPage : nextPage+SizeOfXLogShortPHD])
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Info&XLPFirstIsOverwriteContRecord == 0 || hdr.Info&XLPFirstIsContRecord != 0 || hdr.PageAddr != nextPage {
		t.Fatalf("page header %+v: want XLP_FIRST_IS_OVERWRITE_CONTRECORD at %#x", hdr, nextPage)
	}
	r := data[nextPage+SizeOfXLogShortPHD:]
	totLen := binary.LittleEndian.Uint32(r[0:4])
	if got := binary.LittleEndian.Uint64(r[8:16]); got != mc.PrevRecPtr {
		t.Fatalf("xl_prev %#x, want %#x", got, mc.PrevRecPtr)
	}
	if r[16] != xlogXLogOverwriteContrecord || Rmgr(r[17]) != RmgrXLog {
		t.Fatalf("record info %#x rmid %d, want XLOG_OVERWRITE_CONTRECORD", r[16], r[17])
	}
	crc := binary.LittleEndian.Uint32(r[20:24])
	if err := VerifyXLogRecordCRC(r[:SizeOfXLogRecord], r[SizeOfXLogRecord:totLen], crc); err != nil {
		t.Fatalf("CRC: %v", err)
	}
	// xl_overwrite_contrecord: overwritten_lsn, overwrite_time (after the
	// 2-byte short main-data block header).
	main := r[SizeOfXLogRecord+2 : totLen]
	if got := binary.LittleEndian.Uint64(main[0:8]); got != bigStart {
		t.Fatalf("overwritten_lsn %#x, want %#x", got, bigStart)
	}
	if got := int64(binary.LittleEndian.Uint64(main[8:16])); got != 12345 {
		t.Fatalf("overwrite_time %d, want 12345", got)
	}

	// PG 18.3's own reader, when the oracle install is present.
	if waldump, _ := filepath.Abs("../../../../postgres/local_install/bin/pg_waldump"); fileExists(waldump) {
		lsn := fmt.Sprintf("%X/%X", uint32(nextPage>>32), uint32(nextPage))
		cmd := exec.Command(waldump, "-p", walDir, "-s", lsn, "-n", "1")
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+filepath.Join(filepath.Dir(waldump), "../lib"))
		out, _ := cmd.CombinedOutput()
		want := fmt.Sprintf("OVERWRITE_CONTRECORD lsn %X/%X", uint32(bigStart>>32), uint32(bigStart))
		t.Logf("pg_waldump: %s", strings.TrimSpace(string(out)))
		if !strings.Contains(string(out), want) {
			t.Fatalf("pg_waldump does not show %q:\n%s", want, out)
		}
	}

	// The replay reader drops the partial record at the flagged page and
	// hands its start to the overwrite record, which then verifies.
	recs2, err := ReadAll(walDir, DefaultSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	ow := recs2[len(recs2)-1]
	if ow.StartLSN-1 != nextPage+SizeOfXLogShortPHD || ow.OverwrittenRecPtr != bigStart {
		t.Fatalf("last record start %#x overwritten %#x; want the overwrite record at %#x for %#x",
			ow.StartLSN-1, ow.OverwrittenRecPtr, nextPage+SizeOfXLogShortPHD, bigStart)
	}
	if err := verifyOverwriteContrecord(ow); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := verifyOverwriteContrecord(Record{XLog: ow.XLog}); err == nil {
		t.Fatalf("verify accepted an overwrite record reached without a dropped partial record")
	}

	w2, err := NewWriter(Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if got := w2.WrittenLSN(); got != maxAlignXLogU(end) {
		t.Fatalf("writer resumes at %#x, want %#x (after the overwrite record)", got, end)
	}
}

func maxAlignXLogU(x uint64) uint64 { return (x + 7) &^ 7 }

// TestRemoveSegmentsAfterGap: a segment missing after the redo segment ends
// the WAL; segments past the hole are removed, those before it kept.
func TestRemoveSegmentsAfterGap(t *testing.T) {
	dir := t.TempDir()
	const seg = 16 << 20
	for _, n := range []uint64{1, 2, 4, 5} {
		if err := os.WriteFile(filepath.Join(dir, FormatSegmentNameTLI(n, 1)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := RemoveSegmentsAfterGap(dir, seg, 1*seg+40)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %v, want segments 4 and 5", removed)
	}
	for _, n := range []uint64{1, 2} {
		if _, err := os.Stat(filepath.Join(dir, FormatSegmentNameTLI(n, 1))); err != nil {
			t.Fatalf("segment %d removed: %v", n, err)
		}
	}
	if removed, _ := RemoveSegmentsAfterGap(dir, seg, 1*seg); len(removed) != 0 {
		t.Fatalf("contiguous run: removed %v", removed)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
