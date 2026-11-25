package transam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestSubtransExtendForXIDZeroesPageAtBoundary is ExtendSUBTRANS: only the
// first XID of a page zeroes (and so creates) that page; XIDs inside a page
// leave the file alone, and a page that starts a new segment creates the
// segment file. 009_twophase checks that pg_subtrans/ changes after a run of
// transactions with no subtransactions. M0149-0016c.
func TestSubtransExtendForXIDZeroesPageAtBoundary(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSubtransSLRU(dir)
	if err != nil {
		t.Fatal(err)
	}
	size := func(name string) int64 {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return -1
		}
		return fi.Size()
	}
	if err := s.ExtendForXID(FirstNormalTransactionID); err != nil {
		t.Fatal(err)
	}
	if got := size("0000"); got != storage.BlockSize {
		t.Fatalf("after xid 3: 0000 is %d bytes, want one page", got)
	}
	for _, x := range []storage.TransactionID{4, 2047, 2049} {
		if err := s.ExtendForXID(x); err != nil {
			t.Fatal(err)
		}
	}
	if got := size("0000"); got != storage.BlockSize {
		t.Fatalf("mid-page xids grew 0000 to %d bytes", got)
	}
	if err := s.SetParent(2049, 2048); err != nil {
		t.Fatal(err)
	}
	// xid 4096 starts page 2: zeroed, and page 1's parent link survives.
	if err := s.ExtendForXID(4096); err != nil {
		t.Fatal(err)
	}
	if got := size("0000"); got != 3*storage.BlockSize {
		t.Fatalf("after xid 4096: 0000 is %d bytes, want 3 pages", got)
	}
	if p, err := s.GetParent(2049); err != nil || p != 2048 {
		t.Fatalf("parent of 2049 = %d, %v; want 2048", p, err)
	}
	if err := s.ExtendForXID(subtransXactsPerSegment); err != nil {
		t.Fatal(err)
	}
	if got := size("0001"); got != storage.BlockSize {
		t.Fatalf("segment 0001 is %d bytes, want one page", got)
	}
}
