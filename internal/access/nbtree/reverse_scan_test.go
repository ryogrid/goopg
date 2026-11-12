package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

type scanEntry struct {
	key int32
	ptr storage.ItemPointer
}

// forwardEntries is RangeScanWithPos' stream for [lo, hi].
func forwardEntries(t *testing.T, bt *BTree, lo, hi []byte, loEx, hiEx bool) []scanEntry {
	t.Helper()
	var out []scanEntry
	if err := bt.RangeScanWithPos(lo, hi, loEx, hiEx, func(key []byte, ptr storage.ItemPointer, _ ScanPos) (bool, error) {
		k, _ := DecodeInt4(key)
		out = append(out, scanEntry{k, ptr})
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScanWithPos: %v", err)
	}
	return out
}

// drainReverse pulls a ReverseScanCursor to exhaustion; between leaves it
// runs between(leafIndex, entriesSoFar) so a test can mutate the tree
// mid-scan.
func drainReverse(t *testing.T, c *ReverseScanCursor, between func(int, []scanEntry)) (out []scanEntry, leaves int) {
	t.Helper()
	for {
		ok, err := c.Next(func(key []byte, ptr storage.ItemPointer, _ ScanPos) (bool, error) {
			k, _ := DecodeInt4(key)
			out = append(out, scanEntry{k, ptr})
			return true, nil
		})
		if err != nil {
			t.Fatalf("ReverseScanCursor.Next: %v", err)
		}
		if !ok {
			return out, leaves
		}
		leaves++
		if between != nil {
			between(leaves, out)
		}
	}
}

// TestReverseScanCursorMirrorsForward pins M0148-0001c3a3's contract: a
// backward scan delivers exactly the forward scan's entries for the same
// bounds, in reverse order — across many leaves and every bound shape.
func TestReverseScanCursorMirrorsForward(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	const N = 3000
	for i := 0; i < N; i++ {
		if err := bt.Insert(EncodeInt4(int32(i*2)), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	k := func(v int32) []byte { return EncodeInt4(v) }
	for _, c := range []struct {
		name       string
		lo, hi     []byte
		loEx, hiEx bool
	}{
		{"unbounded", nil, nil, false, false},
		{"closed", k(137), k(4811), false, false},
		{"closed on keys", k(138), k(4810), false, false},
		{"exclusive on keys", k(138), k(4810), true, true},
		{"lo only", k(5000), nil, false, false},
		{"hi only", nil, k(901), false, false},
		{"hi exclusive only", nil, k(900), false, true},
		{"hi past the end", k(5990), k(99999), false, false},
		{"lo before the start", k(-50), k(10), false, false},
		{"empty", k(1001), k(1001), false, false},
		{"single", k(1000), k(1000), false, false},
	} {
		want := forwardEntries(t, bt, c.lo, c.hi, c.loEx, c.hiEx)
		cur, err := bt.NewReverseScanCursor(c.lo, c.hi, c.loEx, c.hiEx)
		if err != nil {
			t.Fatalf("%s: NewReverseScanCursor: %v", c.name, err)
		}
		got, leaves := drainReverse(t, cur, nil)
		if len(got) != len(want) {
			t.Fatalf("%s: reverse delivered %d entries, forward %d", c.name, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[len(want)-1-i] {
				t.Fatalf("%s: entry %d = %+v, want %+v", c.name, i, got[i], want[len(want)-1-i])
			}
		}
		if c.name == "unbounded" && leaves < 3 {
			t.Fatalf("unbounded scan read %d leaves; the tree should span several", leaves)
		}
	}
}

// TestReverseScanCursorDuplicates: equal keys (posting lists after
// deduplication included) come back as the forward scan's TIDs reversed,
// including a duplicate run that spans leaves and an inclusive hi equal to
// the duplicated key.
func TestReverseScanCursorDuplicates(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	blk := storage.BlockNumber(1)
	for i := 0; i < 400; i++ {
		for _, key := range []int32{5, 7, 9} {
			if err := bt.Insert(EncodeInt4(key), storage.ItemPointer{Block: blk, Offset: uint16(i%200 + 1)}); err != nil {
				t.Fatalf("Insert: %v", err)
			}
			if i%200 == 199 {
				blk++
			}
		}
	}
	for _, c := range []struct{ lo, hi []byte }{
		{nil, nil},
		{EncodeInt4(7), EncodeInt4(7)},
		{EncodeInt4(6), EncodeInt4(7)},
		{nil, EncodeInt4(7)},
	} {
		want := forwardEntries(t, bt, c.lo, c.hi, false, false)
		cur, err := bt.NewReverseScanCursor(c.lo, c.hi, false, false)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := drainReverse(t, cur, nil)
		if len(got) != len(want) || len(want) == 0 {
			t.Fatalf("[%v,%v]: reverse %d entries, forward %d", c.lo, c.hi, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[len(want)-1-i] {
				t.Fatalf("[%v,%v]: entry %d = %+v, want %+v", c.lo, c.hi, i, got[i], want[len(want)-1-i])
			}
		}
	}
}

// TestReverseScanCursorEarlyStop: fn returning false ends the scan.
func TestReverseScanCursorEarlyStop(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	for i := 0; i < 2000; i++ {
		if err := bt.Insert(EncodeInt4(int32(i)), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := bt.NewReverseScanCursor(nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []int32
	for n := 0; ; n++ {
		ok, err := cur.Next(func(key []byte, _ storage.ItemPointer, _ ScanPos) (bool, error) {
			v, _ := DecodeInt4(key)
			got = append(got, v)
			return len(got) < 5, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if n > 3 {
			t.Fatal("the cursor kept reading after fn stopped it")
		}
	}
	if len(got) != 5 || got[0] != 1999 || got[4] != 1995 {
		t.Fatalf("early stop delivered %v, want 1999..1995", got)
	}
}

// TestReverseScanCursorConcurrentSplits is the walk-left hazard: between
// leaves, inserts split the pages the scan has not reached yet (to its
// LEFT), so a left sibling read off the current page is stale by the time
// the cursor steps to it. lockAndValidateLeft must recover: every key
// present when the scan started arrives exactly once, in descending order,
// and nothing already passed comes back.
func TestReverseScanCursorConcurrentSplits(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	const N = 4000
	present := map[int32]bool{}
	for i := 0; i < N; i++ {
		v := int32(i * 10)
		if err := bt.Insert(EncodeInt4(v), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
		present[v] = true
	}
	cur, err := bt.NewReverseScanCursor(nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	extra := 0
	recoveriesBefore := reverseScanLeftRecoveries.Load()
	got, _ := drainReverse(t, cur, func(leaf int, sofar []scanEntry) {
		// After every second leaf, fill gaps just to the left of the scan
		// position — the pages the cursor will step to next — with enough
		// keys to split several of them.
		if leaf%2 != 0 || len(sofar) == 0 {
			return
		}
		top := sofar[len(sofar)-1].key - 20
		for j := 0; j < 400 && top-int32(j*10) > 0; j++ {
			v := top - int32(j*10) + 3 + int32(leaf%7)
			if present[v] {
				continue
			}
			if err := bt.Insert(EncodeInt4(v), storage.ItemPointer{Block: storage.BlockNumber(100000 + extra), Offset: 1}); err != nil {
				t.Fatalf("concurrent Insert: %v", err)
			}
			extra++
		}
	})
	seen := map[int32]int{}
	prev := int32(1 << 30)
	for _, e := range got {
		if e.key > prev {
			t.Fatalf("out of order: %d after %d", e.key, prev)
		}
		prev = e.key
		seen[e.key]++
	}
	for v := range present {
		if seen[v] != 1 {
			t.Fatalf("key %d delivered %d times, want once", v, seen[v])
		}
	}
	for v, n := range seen {
		if n != 1 {
			t.Fatalf("key %d delivered %d times", v, n)
		}
	}
	if extra < 1000 {
		t.Fatalf("only %d concurrent inserts ran; the test did not exercise splits", extra)
	}
	if reverseScanLeftRecoveries.Load() == recoveriesBefore {
		t.Fatal("no left step needed recovery; the inserts did not split the pages ahead of the scan")
	}
}
