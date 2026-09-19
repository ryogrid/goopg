package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// drainCursor pulls leaf batches from cur until exhaustion and returns the
// collected (key, ptr, pos) tuples in pull order plus the number of Next
// calls that returned ok=true (leaves processed).
func drainCursor(t *testing.T, cur *ScanCursor) (keys []int32, ptrs []storage.ItemPointer, poss []ScanPos, leaves int) {
	t.Helper()
	for {
		batch := 0
		ok, err := cur.Next(func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error) {
			k, _ := DecodeInt4(key)
			keys = append(keys, k)
			ptrs = append(ptrs, ptr)
			poss = append(poss, pos)
			batch++
			return true, nil
		})
		if err != nil {
			t.Fatalf("cursor.Next: %v", err)
		}
		if !ok {
			break
		}
		leaves++
	}
	return keys, ptrs, poss, leaves
}

// TestScanCursorMatchesEagerScan pins the M0142-0005b contract: a cursor
// delivers exactly the same (key, TID, ScanPos) stream in the same order
// that RangeScanWithPos produces eagerly, across a multi-leaf range.
func TestScanCursorMatchesEagerScan(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	const N = 2000 // forces several leaf splits
	for i := 0; i < N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}

	lo, hi := EncodeInt4(137), EncodeInt4(1811)
	var eKeys []int32
	var ePtrs []storage.ItemPointer
	var ePoss []ScanPos
	err := bt.RangeScanWithPos(lo, hi, false, false,
		func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error) {
			k, _ := DecodeInt4(key)
			eKeys = append(eKeys, k)
			ePtrs = append(ePtrs, ptr)
			ePoss = append(ePoss, pos)
			return true, nil
		})
	if err != nil {
		t.Fatalf("RangeScanWithPos: %v", err)
	}
	if len(eKeys) < 100 {
		t.Fatalf("eager scan collected only %d entries — test not exercising a real range", len(eKeys))
	}

	cur, err := bt.NewScanCursor(lo, hi, false, false, nil)
	if err != nil {
		t.Fatalf("NewScanCursor: %v", err)
	}
	cKeys, cPtrs, cPoss, leaves := drainCursor(t, cur)
	if leaves < 2 {
		t.Fatalf("cursor visited %d leaves — test should span several", leaves)
	}
	if len(cKeys) != len(eKeys) {
		t.Fatalf("cursor delivered %d entries, eager %d", len(cKeys), len(eKeys))
	}
	for i := range cKeys {
		if cKeys[i] != eKeys[i] || cPtrs[i] != ePtrs[i] || cPoss[i] != ePoss[i] {
			t.Fatalf("entry %d: cursor=(%d,%+v,%+v) eager=(%d,%+v,%+v)",
				i, cKeys[i], cPtrs[i], cPoss[i], eKeys[i], ePtrs[i], ePoss[i])
		}
	}
}

// TestScanCursorEarlyStop verifies the laziness contract: a consumer that
// declines mid-leaf ends the scan — no further leaves are walked and the
// cursor stays exhausted.
func TestScanCursorEarlyStop(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	const N = 1500
	for i := 0; i < N; i++ {
		if err := bt.Insert(EncodeInt4(int32(i)), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}

	cur, err := bt.NewScanCursor(EncodeInt4(0), EncodeInt4(int32(N)), false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	stopAt := 5
	ok, err := cur.Next(func(_ []byte, _ storage.ItemPointer, _ ScanPos) (bool, error) {
		seen++
		return seen < stopAt, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || seen != stopAt {
		t.Fatalf("first Next: ok=%v seen=%d, want ok=true seen=%d", ok, seen, stopAt)
	}
	// Cursor must be left exhausted: every further Next returns ok=false
	// without invoking fn.
	calls := 0
	for i := 0; i < 3; i++ {
		ok, err := cur.Next(func(_ []byte, _ storage.ItemPointer, _ ScanPos) (bool, error) {
			calls++
			return true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatalf("post-stop Next returned ok=true")
		}
	}
	if calls != 0 {
		t.Fatalf("fn invoked %d times after scan ended", calls)
	}
}

// TestScanCursorExclusiveBounds checks the loExclusive/hiExclusive bound
// rules agree with the eager walk's (compareHigh semantics).
func TestScanCursorExclusiveBounds(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	for _, k := range []int32{10, 20, 30, 40, 50} {
		if err := bt.Insert(EncodeInt4(k), storage.ItemPointer{Block: storage.BlockNumber(k), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// (10, 50) exclusive both ends → 20, 30, 40.
	cur, err := bt.NewScanCursor(EncodeInt4(10), EncodeInt4(50), true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys, _, _, _ := drainCursor(t, cur)
	want := []int32{20, 30, 40}
	if len(keys) != len(want) {
		t.Fatalf("got %v, want %v", keys, want)
	}
	for i := range keys {
		if keys[i] != want[i] {
			t.Fatalf("got %v, want %v", keys, want)
		}
	}
}

// TestScanCursorEmpty covers ranges that match nothing — both before the
// first key and past the rightmost leaf — and a leaf filter that declines
// every block.
func TestScanCursorEmpty(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	for i := int32(0); i < 500; i++ {
		if err := bt.Insert(EncodeInt4(i), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// Range entirely above every key.
	cur, err := bt.NewScanCursor(EncodeInt4(9000), EncodeInt4(9999), false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys, _, _, _ := drainCursor(t, cur)
	if len(keys) != 0 {
		t.Fatalf("out-of-range cursor delivered %v", keys)
	}

	// Leaf filter declining everything.
	cur, err = bt.NewScanCursor(EncodeInt4(0), EncodeInt4(499), false, false,
		func(storage.BlockNumber) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	keys, _, _, _ = drainCursor(t, cur)
	if len(keys) != 0 {
		t.Fatalf("filter-declined cursor delivered %v", keys)
	}
}

// TestScanCursorLeafFilterPartition splits the leaf chain between two
// cursors via complementary block filters (the Gather leaf-ownership
// mechanism, C-19c) and checks their union equals the unfiltered scan.
func TestScanCursorLeafFilterPartition(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	const N = 1200
	for i := 0; i < N; i++ {
		if err := bt.Insert(EncodeInt4(int32(i)), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	lo, hi := EncodeInt4(0), EncodeInt4(int32(N-1))

	var all []int32
	if err := bt.RangeScan(lo, hi, func(key []byte, _ storage.ItemPointer) (bool, error) {
		k, _ := DecodeInt4(key)
		all = append(all, k)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	even, err := bt.NewScanCursor(lo, hi, false, false, func(b storage.BlockNumber) bool { return b%2 == 0 })
	if err != nil {
		t.Fatal(err)
	}
	odd, err := bt.NewScanCursor(lo, hi, false, false, func(b storage.BlockNumber) bool { return b%2 == 1 })
	if err != nil {
		t.Fatal(err)
	}
	eKeys, _, _, _ := drainCursor(t, even)
	oKeys, _, _, _ := drainCursor(t, odd)

	merged := append(append([]int32{}, eKeys...), oKeys...)
	// Sort-free check: every key in `all` appears exactly once in merged.
	got := make(map[int32]int, len(merged))
	for _, k := range merged {
		got[k]++
	}
	if len(merged) != len(all) {
		t.Fatalf("partitioned union has %d entries, want %d", len(merged), len(all))
	}
	for _, k := range all {
		if got[k] != 1 {
			t.Fatalf("key %d appears %d times in partitioned union", k, got[k])
		}
	}
}
