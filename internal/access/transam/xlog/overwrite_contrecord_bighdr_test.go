package xlog

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestOverwriteContrecordForOversizedHeader is 039_end_of_wal's multi-page
// shape: a record header with a valid prev-link but xl_tot_len = 2 GB at the
// end of the WAL, and nothing after it. PG assembles it, finds the
// continuation missing and writes XLOG_OVERWRITE_CONTRECORD on the next
// page; the WAL writer must then resume after that record, not at the
// dropped header. M0149-0017d.
func TestOverwriteContrecordForOversizedHeader(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range []int{100, 300} {
		payload := EncodeHeapVacuum(storage.RelFileNode{DBOid: 5, RelOid: uint32(16400 + i)}, 0, make([]uint16, n/2))
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	end := w.WrittenLSN()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadAll(walDir, DefaultSegmentSize)
	if err != nil || len(recs) < 2 {
		t.Fatal(err)
	}
	last := recs[len(recs)-1]
	seg := filepath.Join(walDir, FormatSegmentNameTLI(0, 1))
	data, _ := os.ReadFile(seg)
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], 2*1024*1024*1024)
	binary.LittleEndian.PutUint64(hdr[8:], last.StartLSN-1)
	copy(data[end:], hdr)
	_ = os.WriteFile(seg, data, 0o600)

	mc, ok, err := FindMissingContrecord(walDir, DefaultSegmentSize, recs[0].StartLSN-1)
	if err != nil || !ok || mc.AbortedRecPtr != end {
		t.Fatalf("FindMissingContrecord = %+v %v %v; want aborted at %#x", mc, ok, err, end)
	}
	recEnd, err := WriteOverwriteContrecord(walDir, DefaultSegmentSize, mc, 1)
	if err != nil {
		t.Fatal(err)
	}
	w2, err := NewWriter(Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if got := w2.WrittenLSN(); got != maxAlignXLogU(recEnd) {
		t.Fatalf("writer resumes at %#x, want %#x", got, maxAlignXLogU(recEnd))
	}
}
