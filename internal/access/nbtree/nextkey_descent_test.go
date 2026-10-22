package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestExclusivePrefixBoundDescendsPastTheGroup pins M0122-0012 slice 41: a
// scan whose lower bound is EXCLUSIVE and names a key prefix descends with
// _bt_first's nextkey rule and lands after the prefix's group, instead of at
// the group's first leaf. Group a=2 here spans many leaves; the cursor must
// reach the first a=3 entry having read at most two leaves (the landing leaf
// and, at a page boundary, its right sibling), and both the cursor and the
// eager scan must return exactly the entries past the group.
func TestExclusivePrefixBoundDescendsPastTheGroup(t *testing.T) {
	desc := int4x2Desc()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 256})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9141, Fork: storage.MainFork}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	groupSize := map[int32]int32{0: 50, 1: 50, 2: 4000, 3: 50, 4: 50}
	for a := int32(0); a < 5; a++ {
		for b := int32(0); b < groupSize[a]; b++ {
			tid := storage.ItemPointer{Block: storage.BlockNumber(a*10000 + b/100), Offset: uint16(b%100 + 1)}
			if err := bt.Insert(tup(t, desc.Attrs, [][]byte{int4Val(a), int4Val(b)}, tid), tid); err != nil {
				t.Fatalf("Insert(%d,%d): %v", a, b, err)
			}
		}
	}
	firstA := func(key []byte) int32 {
		vals, _, derr := DeformPGIndexTuple(key, desc.Physical(), 2)
		if derr != nil {
			t.Fatalf("deform: %v", derr)
		}
		return int32(decodeLEUint32(vals[0]))
	}
	bound := pivot(t, desc.Attrs, [][]byte{int4Val(2)}, 1)

	cur, err := bt.NewScanCursor(bound, nil, true, false, nil)
	if err != nil {
		t.Fatalf("NewScanCursor: %v", err)
	}
	leaves, seen, firstAt := 0, 0, -1
	for {
		ok, err := cur.Next(func(key []byte, _ storage.ItemPointer, _ ScanPos) (bool, error) {
			if a := firstA(key); a <= 2 {
				t.Fatalf("exclusive bound a>2 returned a=%d", a)
			}
			if firstAt < 0 {
				firstAt = leaves
			}
			seen++
			return true, nil
		})
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		leaves++
	}
	if seen != int(groupSize[3]+groupSize[4]) {
		t.Fatalf("cursor returned %d entries past a=2, want %d", seen, groupSize[3]+groupSize[4])
	}
	if firstAt > 1 {
		t.Fatalf("cursor read %d leaves before the first entry past the group: it descended to the group's start, not past it", firstAt)
	}

	// The descent itself: the leaf it lands on for the exclusive bound must
	// be the leaf of the first entry past the group (or its left neighbour,
	// when the group ends exactly at a page boundary) — not the group's
	// first leaf, which the inclusive descent returns.
	nk, err := bt.descendToLowerBound(bound, true)
	if err != nil {
		t.Fatalf("descendToLowerBound: %v", err)
	}
	incl, err := bt.descendToLowerBound(bound, false)
	if err != nil {
		t.Fatalf("descendToLowerBound: %v", err)
	}
	var firstPastBlk storage.BlockNumber
	if err := bt.RangeScanWithPos(bound, nil, true, false, func(_ []byte, _ storage.ItemPointer, pos ScanPos) (bool, error) {
		firstPastBlk = pos.Blk
		return false, nil
	}); err != nil {
		t.Fatalf("RangeScanWithPos: %v", err)
	}
	if nk == incl {
		t.Fatalf("exclusive descent landed on the group's first leaf %d, like the inclusive one", nk)
	}
	if nk != firstPastBlk {
		slot, err := bt.pinR(nk)
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
		next := readOpaque(slot.Page()).Next
		bt.unpinR(slot)
		if next != firstPastBlk {
			t.Fatalf("exclusive descent landed on leaf %d; the first entry past the group is on %d", nk, firstPastBlk)
		}
	}

	eager := 0
	if err := bt.RangeScanWithPos(bound, nil, true, false, func(key []byte, _ storage.ItemPointer, _ ScanPos) (bool, error) {
		if a := firstA(key); a <= 2 {
			t.Fatalf("eager exclusive bound a>2 returned a=%d", a)
		}
		eager++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScanWithPos: %v", err)
	}
	if eager != seen {
		t.Fatalf("eager scan returned %d entries, cursor %d", eager, seen)
	}
}
