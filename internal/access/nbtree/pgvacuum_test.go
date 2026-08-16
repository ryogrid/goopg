package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// vacPage builds a non-rightmost leaf (high key at P_HIKEY, data from
// P_FIRSTKEY) carrying one pivot item per key.
func vacPage(t *testing.T, keys []string, garbage bool) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(p); err != nil {
		t.Fatal(err)
	}
	flags := BTPLeaf
	if garbage {
		flags |= BTPHasGarbage
	}
	WritePGOpaque(p, PGBTPageOpaque{Prev: 5, Next: 7, Level: 0, Flags: flags})
	if _, err := storage.PageAddItemRaw(p, PGBTPivotRaw([]byte("hikey"), PNone)); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if _, err := storage.PageAddItemRaw(p, PGBTPivotRaw([]byte(k), storage.BlockNumber(100+int(k[0])))); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// TestReplayVacuumDelete pins upstream PageIndexMultiDelete's contract as
// btree_xlog_vacuum uses it: the named PHYSICAL offsets go away, the survivors
// keep their order and slide down, the high key is untouched, and the garbage
// hint is cleared.
func TestReplayVacuumDelete(t *testing.T) {
	page := vacPage(t, []string{"a", "b", "c", "d"}, true)
	if err := ReplayVacuumDelete(page, []uint16{3, 5}); err != nil {
		t.Fatal(err)
	}
	if err := checkSamePage(t, page, vacPage(t, []string{"a", "c"}, false)); err != nil {
		t.Fatal(err)
	}
	if _, hasHK, err := PGHighKeyRaw(page); err != nil || !hasHK {
		t.Fatalf("high key lost: hasHK=%v err=%v", hasHK, err)
	}
}

// TestReplayVacuumDeleteRejectsBadOffsets: PageIndexMultiDelete requires an
// ascending, in-range offset array. A record that violates either would delete
// the wrong items (or none) rather than fail, so the checks are explicit.
func TestReplayVacuumDeleteRejectsBadOffsets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deleted []uint16
	}{
		{"descending", []uint16{5, 3}},
		{"duplicate", []uint16{3, 3}},
		{"the high key", []uint16{1, 3}},
		{"past the last item", []uint16{3, 99}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := vacPage(t, []string{"a", "b", "c", "d"}, true)
			if err := ReplayVacuumDelete(page, tc.deleted); err == nil {
				t.Fatalf("accepted %v", tc.deleted)
			}
		})
	}
}

// TestCheckVacuumDelete is the encoder's gate: it must accept a deletion that
// reproduces the written page and reject one that does not — the latter being
// how goopg's posting-list rewrite and its page-went-empty flag stamp get routed
// to a full-page image instead of a divergent incremental record.
func TestCheckVacuumDelete(t *testing.T) {
	pre := vacPage(t, []string{"a", "b", "c", "d"}, true)
	if err := CheckVacuumDelete(pre, vacPage(t, []string{"a", "c"}, false), []uint16{3, 5}); err != nil {
		t.Fatalf("rejected the exact deletion result: %v", err)
	}
	if err := CheckVacuumDelete(pre, vacPage(t, []string{"a", "d"}, false), []uint16{3, 5}); err == nil {
		t.Fatalf("accepted a page holding items the deletion did not leave")
	}
	if err := CheckVacuumDelete(pre, vacPage(t, []string{"a", "c"}, true), []uint16{3, 5}); err == nil {
		t.Fatalf("accepted a page whose opaque differs (redo clears BTP_HAS_GARBAGE)")
	}
	// The page VACUUM emptied also carries BTDeleted|BTHalfDead, which no redo
	// sets — the case that must NOT be logged incrementally.
	emptied := vacPage(t, nil, false)
	op := ReadPGOpaque(emptied)
	op.Flags |= BTPDeleted | BTPHalfDead
	WritePGOpaque(emptied, op)
	if err := CheckVacuumDelete(pre, emptied, []uint16{2, 3, 4, 5}); err == nil {
		t.Fatalf("accepted the page-went-empty rewrite, whose flags no vacuum redo produces")
	}
}

func checkSamePage(t *testing.T, got, want storage.Page) error {
	t.Helper()
	return CheckVacuumDelete(got, want, nil)
}
