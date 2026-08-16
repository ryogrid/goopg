package transam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestSubtransSLRUTruncateBefore writes parent links spanning two SLRU
// segments (subtransXactsPerSegment == 65536), truncates at a cutoff inside
// the second segment, and asserts that:
//
//	(a) the segment file entirely below the cutoff page is unlinked;
//	(b) the segment straddling/above the cutoff page is retained, with its
//	    entries byte-for-byte intact;
//	(c) GetParent for a truncated-away XID returns InvalidTransactionID, the
//	    documented post-truncation contract (M0122-0009).
func TestSubtransSLRUTruncateBefore(t *testing.T) {
	dir := t.TempDir()
	slru, err := OpenSubtransSLRU(dir)
	if err != nil {
		t.Fatalf("OpenSubtransSLRU: %v", err)
	}

	const seg1 = storage.TransactionID(subtransXactsPerSegment) // 65536: first XID of segment 0001

	// Links in segment 0 (will be truncated away).
	below := map[storage.TransactionID]storage.TransactionID{
		FirstNormalTransactionID: 2,
		50000:                    40000,
		seg1 - 1:                 seg1 - 2, // last XID of segment 0
	}
	// Links in segment 1, straddling the cutoff page so both the dropped
	// prefix and the kept tail of the same segment are exercised.
	above := map[storage.TransactionID]storage.TransactionID{
		seg1:       seg1 - 1,  // first XID of segment 1 (below cutoff page)
		seg1 + 100: seg1 + 50, // still page 0 of seg 1 (below cutoff page)
		seg1 + storage.TransactionID(subtransXactsPerPage) + 7:   seg1 + 6,       // page 1 of seg 1 (>= cutoff)
		seg1 + storage.TransactionID(subtransXactsPerPage)*3 + 9: seg1 + 100 + 9, // deeper into seg 1
	}
	for sub, par := range below {
		if err := slru.SetParent(sub, par); err != nil {
			t.Fatalf("SetParent(%d,%d): %v", sub, par, err)
		}
	}
	for sub, par := range above {
		if err := slru.SetParent(sub, par); err != nil {
			t.Fatalf("SetParent(%d,%d): %v", sub, par, err)
		}
	}

	// Sanity: both segment files exist before truncation.
	for _, seg := range []string{"0000", "0001"} {
		if _, err := os.Stat(filepath.Join(dir, seg)); err != nil {
			t.Fatalf("pre-truncate: expected SLRU segment %s: %v", seg, err)
		}
	}

	// Cutoff = an XID inside segment 1's page 1 (strictly after seg1's first
	// page). This drops all of segment 0 (unlinked below) plus seg 1's page 0
	// worth of entries become logically stale, while the FILE itself (segment
	// 1) is retained since its last page is >= cutoff page.
	cutoff := seg1 + storage.TransactionID(subtransXactsPerPage)
	if err := slru.TruncateBefore(cutoff); err != nil {
		t.Fatalf("TruncateBefore(%d): %v", cutoff, err)
	}

	if _, err := os.Stat(filepath.Join(dir, "0000")); !os.IsNotExist(err) {
		t.Fatalf("segment 0000 still present after truncation below it: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "0001")); err != nil {
		t.Fatalf("segment 0001 (straddles/above cutoff) unexpectedly removed: %v", err)
	}

	// Truncated-away XIDs (segment 0) now read back as unset.
	for sub := range below {
		got, err := slru.GetParent(sub)
		if err != nil {
			t.Fatalf("GetParent(%d) after truncate: %v", sub, err)
		}
		if got != storage.InvalidTransactionID {
			t.Errorf("GetParent(%d) after truncate = %d, want InvalidTransactionID (segment removed)", sub, got)
		}
	}
	// Retained segment's entries survive byte-for-byte, regardless of whether
	// they are logically below or above the cutoff page (file-granularity
	// truncation only drops whole segments).
	for sub, wantPar := range above {
		got, err := slru.GetParent(sub)
		if err != nil {
			t.Fatalf("GetParent(%d) after truncate: %v", sub, err)
		}
		if got != wantPar {
			t.Errorf("GetParent(%d) after truncate = %d, want %d (retained segment)", sub, got, wantPar)
		}
	}

	// Idempotent: calling again with the same or an older cutoff is a no-op.
	if err := slru.TruncateBefore(cutoff); err != nil {
		t.Fatalf("TruncateBefore(%d) repeat: %v", cutoff, err)
	}
	if err := slru.TruncateBefore(seg1); err != nil {
		t.Fatalf("TruncateBefore(%d) older cutoff: %v", seg1, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "0001")); err != nil {
		t.Fatalf("segment 0001 removed by a no-op older truncate: %v", err)
	}
}

// TestSubxactMapTruncate exercises SubxactMap.Truncate end-to-end: it prunes
// in-memory parent/abort entries below the horizon, leaves entries at/above
// the horizon untouched, and (when persistence is enabled) truncates the
// on-disk SLRU mirror to match.
func TestSubxactMapTruncate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_subtrans")
	m := NewSubxactMap()
	if err := m.EnablePersistence(dir); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}

	const horizon = storage.TransactionID(subtransXactsPerSegment + subtransXactsPerPage*2)

	old1 := storage.TransactionID(FirstNormalTransactionID)
	old2 := storage.TransactionID(50000)
	m.Register(old1, old1+1)
	m.Register(old2, old2-1)
	m.MarkAborted(old2)

	keep := horizon + 500
	m.Register(keep, keep-1)
	m.MarkAborted(keep)

	if err := m.Truncate(horizon); err != nil {
		t.Fatalf("Truncate(%d): %v", horizon, err)
	}

	// Pruned: below-horizon entries no longer resolve as subxacts, in memory
	// or via the SLRU (fresh reader).
	for _, sub := range []storage.TransactionID{old1, old2} {
		if m.IsSubxact(sub) {
			t.Errorf("IsSubxact(%d) after Truncate(%d) = true, want false (pruned)", sub, horizon)
		}
		if m.IsAborted(sub) {
			t.Errorf("IsAborted(%d) after Truncate(%d) = true, want false (pruned)", sub, horizon)
		}
	}
	fresh, err := OpenSubtransSLRU(dir)
	if err != nil {
		t.Fatalf("OpenSubtransSLRU: %v", err)
	}
	for _, sub := range []storage.TransactionID{old1, old2} {
		got, err := fresh.GetParent(sub)
		if err != nil {
			t.Fatalf("GetParent(%d): %v", sub, err)
		}
		if got != storage.InvalidTransactionID {
			t.Errorf("on-disk GetParent(%d) after Truncate = %d, want InvalidTransactionID", sub, got)
		}
	}

	// Retained: at/above-horizon entry keeps its parent link and abort status.
	if !m.IsSubxact(keep) {
		t.Errorf("IsSubxact(%d) after Truncate(%d) = false, want true (retained)", keep, horizon)
	}
	if got := m.Parent(keep); got != keep-1 {
		t.Errorf("Parent(%d) after Truncate = %d, want %d", keep, got, keep-1)
	}
	if !m.IsAborted(keep) {
		t.Errorf("IsAborted(%d) after Truncate = false, want true (retained)", keep)
	}
}

// TestSubxactMapTruncateNoPersistence exercises Truncate on a pure in-memory
// map (persistence never enabled): it must still prune the maps and return no
// error, rather than panicking on a nil slru.
func TestSubxactMapTruncateNoPersistence(t *testing.T) {
	m := NewSubxactMap()
	m.Register(10, 5)
	m.Register(1000, 999)

	if err := m.Truncate(500); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if m.IsSubxact(10) {
		t.Error("IsSubxact(10) after Truncate(500) = true, want false (pruned)")
	}
	if !m.IsSubxact(1000) {
		t.Error("IsSubxact(1000) after Truncate(500) = false, want true (retained)")
	}
}
