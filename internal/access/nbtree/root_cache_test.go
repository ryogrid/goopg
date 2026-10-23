package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestRootCacheTracksTheRoot pins M0122-0012 slice 61: descents start from a
// cached copy of the metapage's root (rootCache, PG's rd_amcache) and trust
// it only while the pinned page is still the live root at the cached level
// (rootCacheValid). The cache must agree with the metapage once a lookup has
// populated it. A cache poisoned with a non-root leaf block must be detected
// and replaced, without a wrong answer. A cache left stale by a root split
// must be refreshed while every lookup, every cursor scan and further inserts
// (whose descent path must reach the true root) stay correct.
func TestRootCacheTracksTheRoot(t *testing.T) {
	bt, pool, cleanup := newTestTree(t)
	defer cleanup()
	ptr := func(i int) storage.ItemPointer {
		return storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)}
	}
	n := 0
	insertUpTo := func(m int) {
		for ; n < m; n++ {
			if err := bt.Insert(EncodeInt4(int32(n)), ptr(n)); err != nil {
				t.Fatalf("Insert(%d): %v", n, err)
			}
		}
	}
	checkAll := func(stage string) {
		t.Helper()
		for _, k := range []int{0, 1, n / 3, n / 2, n - 1} {
			got, ok, err := bt.Search(EncodeInt4(int32(k)))
			if err != nil || !ok || got != ptr(k) {
				t.Fatalf("%s: Search(%d) = %v ok=%v err=%v, want %v", stage, k, got, ok, err, ptr(k))
			}
		}
		lo, hi := EncodeInt4(int32(n/2)), EncodeInt4(int32(n/2+20))
		cur, err := bt.NewScanCursor(lo, hi, false, false, nil)
		if err != nil {
			t.Fatalf("%s: NewScanCursor: %v", stage, err)
		}
		seen := 0
		for {
			ok, err := cur.Next(func([]byte, storage.ItemPointer, ScanPos) (bool, error) { seen++; return true, nil })
			if err != nil {
				t.Fatalf("%s: cursor: %v", stage, err)
			}
			if !ok {
				break
			}
		}
		if seen != 21 {
			t.Fatalf("%s: cursor over [%d, %d] saw %d entries, want 21", stage, n/2, n/2+20, seen)
		}
	}
	cacheMatchesMeta := func(stage string) {
		t.Helper()
		meta, err := bt.readMeta()
		if err != nil {
			t.Fatal(err)
		}
		blk, level, ok := bt.cachedRoot()
		if !ok || blk != meta.Root || level != meta.Level {
			t.Fatalf("%s: cache (%d, level %d, ok=%v), metapage root (%d, level %d)", stage, blk, level, ok, meta.Root, meta.Level)
		}
	}

	insertUpTo(3000)
	bt.rootCache.Store(0)
	checkAll("fresh")
	cacheMatchesMeta("fresh")
	meta, _ := bt.readMeta()
	if meta.Level == 0 {
		t.Fatal("tree of 3000 keys has a leaf root — the fixture never caches an internal root")
	}

	// A leaf block cached as the root (level 0, has siblings, no BTRoot):
	// the rightmost one, from which right links can never reach key 0.
	leaf, _, err := bt.descendToLeaf(EncodeInt4(int32(n - 1)))
	if err != nil {
		t.Fatal(err)
	}
	bt.rootCache.Store(uint64(leaf))
	checkAll("poisoned")
	cacheMatchesMeta("poisoned")

	// A stale cache across root splits: hold the pre-split root in the
	// cache while inserting until the metapage's level grows.
	before := meta.Level
	staleBlk, staleLevel, _ := bt.cachedRoot()
	for grown := false; !grown; {
		insertUpTo(n + 2000)
		bt.rootCache.Store(uint64(staleLevel)<<32 | uint64(staleBlk))
		m, err := bt.readMeta()
		if err != nil {
			t.Fatal(err)
		}
		grown = m.Level > before
		if n > 400000 {
			t.Fatal("root never split")
		}
	}
	checkAll("after root split")
	cacheMatchesMeta("after root split")
	bt.rootCache.Store(uint64(staleLevel)<<32 | uint64(staleBlk))
	insertUpTo(n + 500)
	checkAll("inserts through a stale cache")

	// Every key is still reachable, in order. (walkPivotInvariants is not
	// used here: a tree this deep trips a pre-existing internal-page pivot
	// divergence unrelated to the root cache — fix_plan M0131-S33.)
	want := int32(0)
	if err := bt.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		k, err := DecodeInt4(key)
		if err != nil || k != want {
			t.Fatalf("full scan: entry %d is %v (err %v)", want, k, err)
		}
		want++
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if int(want) != n {
		t.Fatalf("full scan returned %d entries, want %d", want, n)
	}
	_ = pool
}
