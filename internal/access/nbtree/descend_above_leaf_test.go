package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestScanDescentStopsAboveLeaf pins M0122-0012 slice 76: a scan's descent
// (descendToLowerBound) returns the leaf its level-1 page's downlink names
// without visiting it, and its callers' own leaf-level steps — the P_IGNORE
// skip and the high-key move-right for lo — land on exactly the leaf the full
// descent returns. Every bound in and around the key range is tried, inclusive
// and exclusive, and every cursor delivers exactly the keys past its bound.
func TestScanDescentStopsAboveLeaf(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	const N = 3000 // several leaves under one internal root
	for i := 0; i < N; i++ {
		if err := bt.Insert(EncodeInt4(int32(2*i)), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}
	meta, err := bt.readMeta()
	if err != nil {
		t.Fatal(err)
	}
	if meta.Level < 1 {
		t.Fatalf("root level %d: the fixture never descends through an internal page", meta.Level)
	}
	// moveRight is the leaf-level step both callers apply to the block the
	// descent returns.
	moveRight := func(blk storage.BlockNumber, lo []byte, excl bool) storage.BlockNumber {
		for blk != storage.InvalidBlockNumber {
			slot, err := bt.pinR(blk)
			if err != nil {
				t.Fatal(err)
			}
			op := readOpaque(slot.Page())
			past := op.isUnlinked() || (lo != nil && lowerBoundPastPage(bt.format(), slot.Page(), lo, excl))
			if !op.IsLeaf() {
				t.Fatalf("block %d returned by the descent is not a leaf (level %d)", blk, op.Level)
			}
			bt.unpinR(slot)
			if !past {
				return blk
			}
			blk = op.Next
		}
		return blk
	}
	for k := int32(-1); k <= 2*N; k++ {
		lo := EncodeInt4(k)
		for _, excl := range []bool{false, true} {
			full, _, err := bt.descendMode(lo, false, excl, false, false)
			if err != nil {
				t.Fatal(err)
			}
			short, err := bt.descendToLowerBound(lo, excl)
			if err != nil {
				t.Fatal(err)
			}
			// With no concurrent split the downlink already names the
			// right leaf: a move-right here would be a descent landing
			// short (slice 41's exclusive-bound bug), costing leaf reads.
			if short != full {
				t.Fatalf("lo=%d excl=%v: descent returned leaf %d, the full descent's leaf is %d", k, excl, short, full)
			}
			if got := moveRight(short, lo, excl); got != full {
				t.Fatalf("lo=%d excl=%v: scan starts on leaf %d, the full descent's leaf is %d", k, excl, got, full)
			}
			if k%97 != 0 {
				continue
			}
			cur, err := bt.NewScanCursor(lo, nil, excl, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			keys, _, _, _ := drainCursor(t, cur)
			first := k + 1
			if !excl {
				first = k
			}
			if first < 0 {
				first = 0
			}
			first += first & 1 // the next stored (even) key
			if want := int(N - first/2); len(keys) != want || (want > 0 && keys[0] != first) {
				t.Fatalf("lo=%d excl=%v: cursor delivered %d keys from %v, want %d from %d", k, excl, len(keys), keys[:min(1, len(keys))], want, first)
			}
		}
	}
}

// TestScanDescentOnLeafRoot pins the single-page tree: a root that is itself
// a leaf is returned as is.
func TestScanDescentOnLeafRoot(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	for i := 0; i < 10; i++ {
		if err := bt.Insert(EncodeInt4(int32(i)), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := bt.NewScanCursor(EncodeInt4(3), nil, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keys, _, _, _ := drainCursor(t, cur); len(keys) != 6 || keys[0] != 4 {
		t.Fatalf("leaf-root cursor delivered %v, want 4..9", keys)
	}
}

// TestResetScanCursorMatchesNewCursor pins M0122-0012 slice 77: re-aiming one
// cursor (ResetScanCursor) — mid-walk, after a drain, inclusive and exclusive,
// with and without an upper bound — delivers exactly the keys inside the new
// bounds, nothing left over from its previous aim. The skip scan re-aims one
// cursor per descent.
func TestResetScanCursorMatchesNewCursor(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	const N = 3000
	for i := 0; i < N; i++ {
		if err := bt.Insert(EncodeInt4(int32(i)), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	reused := &ScanCursor{}
	for step, b := range []struct {
		lo, hi           int32
		loExcl, hasUpper bool
	}{{100, 0, false, false}, {2500, 2700, true, true}, {7, 9, false, true}, {2999, 0, true, false}, {0, 1500, false, true}} {
		var hi []byte
		if b.hasUpper {
			hi = EncodeInt4(b.hi)
		}
		// The keys past the bounds, computed independently: NewScanCursor
		// is itself ResetScanCursor on a new cursor, so it is no oracle.
		var want []int32
		for k := int32(0); k < N; k++ {
			if k < b.lo || (b.loExcl && k == b.lo) || (b.hasUpper && k > b.hi) {
				continue
			}
			want = append(want, k)
		}
		if err := bt.ResetScanCursor(reused, EncodeInt4(b.lo), hi, b.loExcl, false, nil); err != nil {
			t.Fatal(err)
		}
		if step%2 == 0 {
			// Re-aim mid-walk too: read one leaf, then re-aim again.
			if _, err := reused.Next(func([]byte, storage.ItemPointer, ScanPos) (bool, error) { return true, nil }); err != nil {
				t.Fatal(err)
			}
			if err := bt.ResetScanCursor(reused, EncodeInt4(b.lo), hi, b.loExcl, false, nil); err != nil {
				t.Fatal(err)
			}
		}
		got, _, _, _ := drainCursor(t, reused)
		if len(got) != len(want) || (len(want) > 0 && (got[0] != want[0] || got[len(got)-1] != want[len(want)-1])) {
			t.Fatalf("step %d: re-aimed cursor delivered %d keys, want %d", step, len(got), len(want))
		}
	}
}

// TestEmptyBoundDescendsLeftmost pins slice 77's minus-infinity shortcut: an
// empty or nil lower bound takes every page's first downlink and is past no
// page, so the descent lands on the leftmost leaf and the scan reads every key.
func TestEmptyBoundDescendsLeftmost(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	const N = 3000
	for i := 0; i < N; i++ {
		if err := bt.Insert(EncodeInt4(int32(i)), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	first, _, err := bt.descendMode(EncodeInt4(-1), false, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, lo := range [][]byte{nil, {}} {
		leaf, err := bt.descendToLowerBound(lo, false)
		if err != nil {
			t.Fatal(err)
		}
		if leaf != first {
			t.Fatalf("lo=%v: descent landed on leaf %d, the leftmost is %d", lo, leaf, first)
		}
		cur, err := bt.NewScanCursor(lo, nil, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if keys, _, _, _ := drainCursor(t, cur); len(keys) != N || keys[0] != 0 {
			t.Fatalf("lo=%v: cursor delivered %d keys from %v, want all %d", lo, len(keys), keys[:min(1, len(keys))], N)
		}
	}
}
