package xlog

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestFindNextRecord checks XLogFindNextRecord's page walk on WAL written by
// the real writer, with records that span pages: on every page the first
// record found is the one that starts right after the page header, or right
// after the tail of a record continued from an earlier page — skipping whole
// pages that tail fills — and past the end of the WAL there is none.
func TestFindNextRecord(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range []int{100, 20000, 300, 9000, 50, 30000, 10} {
		payload := EncodeHeapVacuum(storage.RelFileNode{DBOid: 5, RelOid: uint32(16400 + i)}, 0, make([]uint16, n/2))
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadAll(walDir, DefaultSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	segSize := DefaultSegmentSize
	// want(P): where XLogFindNextRecord lands for page P.
	var want func(page uint64) uint64
	want = func(page uint64) uint64 {
		hsize := uint64(pageHeaderSizeAt(int64(page), segSize))
		for _, r := range recs {
			if start := r.StartLSN - 1; start < page && r.EndLSN > page {
				if r.EndLSN < page+XLOGBlockSize {
					return r.EndLSN
				}
				return want(page + XLOGBlockSize)
			}
		}
		return page + hsize
	}
	first := (recs[0].StartLSN - 1) / XLOGBlockSize * XLOGBlockSize
	last := recs[len(recs)-1].EndLSN
	spanned := 0
	for page := first; page < last; page += XLOGBlockSize {
		got, ok, err := FindNextRecord(walDir, 1, segSize, page)
		if err != nil || !ok {
			t.Fatalf("page %X: ok %v, err %v", page, ok, err)
		}
		if w := want(page); got != w {
			t.Errorf("page %X: found %X, want %X", page, got, w)
		}
		if got > page+uint64(SizeOfXLogLongPHD) {
			spanned++
		}
	}
	if spanned < 5 {
		t.Fatalf("only %d pages start with a continued record; the test WAL does not exercise the skip", spanned)
	}
	end := (last + XLOGBlockSize) / XLOGBlockSize * XLOGBlockSize
	if _, ok, err := FindNextRecord(walDir, 1, segSize, end+XLOGBlockSize); ok || err != nil {
		t.Errorf("past the end of WAL: ok %v, err %v; want no record", ok, err)
	}
	if _, ok, err := FindNextRecord(walDir, 2, segSize, first); ok || err != nil {
		t.Errorf("a timeline with no segment: ok %v, err %v; want no record", ok, err)
	}
	if _, _, err := FindNextRecord(walDir, 1, segSize, first+8); err == nil {
		t.Error("an unaligned LSN must be refused")
	}
}
