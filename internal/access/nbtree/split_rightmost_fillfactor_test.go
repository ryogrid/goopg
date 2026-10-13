package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestAscendingInsertsSplitRightmostAtFillfactor pins M0146-0178 against PG
// 18.3. _bt_findsplitloc (nbtsplitloc.c) splits a RIGHTMOST page so the left
// half keeps the fillfactor — 90% on the leaf level, 70% above it — because
// ascending insertions only ever land on the right half again. goopg split
// every page evenly, so a serial int4 key's index came out ~1.75x PG's size
// (TPC-DS item_pkey 90 pages against PG's 52) and stayed half empty.
//
// The page counts are PG 18.3's for `CREATE TABLE a (k int PRIMARY KEY)`
// followed by `INSERT ... SELECT generate_series(1, n)`, measured with
// pg_relation_size on a scratch cluster: 18000 keys -> 52 pages, 500 -> 4
// (meta, root, two leaves). An int4 entry costs the same 20 bytes on a page in
// both engines (a 4-byte line pointer plus a MAXALIGNed 8+4-byte tuple).
func TestAscendingInsertsSplitRightmostAtFillfactor(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want storage.BlockNumber
	}{
		{500, 4},
		{18000, 52},
	} {
		bt, pool, cleanup := newTestTree(t)
		for i := 1; i <= tc.n; i++ {
			ptr := storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)}
			if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
				cleanup()
				t.Fatalf("n=%d: Insert(%d): %v", tc.n, i, err)
			}
		}
		got, err := pool.NBlocks(bt.rel)
		if err != nil {
			cleanup()
			t.Fatalf("NBlocks: %v", err)
		}
		for _, k := range []int32{1, int32(tc.n / 2), int32(tc.n)} {
			if _, ok, err := bt.Search(EncodeInt4(k)); err != nil || !ok {
				t.Errorf("n=%d: Search(%d) = ok %v, err %v", tc.n, k, ok, err)
			}
		}
		cleanup()
		if got != tc.want {
			t.Errorf("%d ascending int4 keys: %d pages, PG 18.3 builds %d", tc.n, got, tc.want)
		}
	}
}

// TestSplitFillFactorMult pins _bt_findsplitloc's fillfactormult choice: the
// leaf fillfactor on a rightmost leaf, BTREE_NONLEAF_FILLFACTOR on a rightmost
// internal page, and the free-space-balancing default (usemult off) elsewhere.
func TestSplitFillFactorMult(t *testing.T) {
	for _, tc := range []struct {
		leaf, rightmost bool
		want            float64
	}{
		{true, true, 0.90},
		{false, true, 0.70},
		{true, false, 0},
		{false, false, 0},
	} {
		if got := splitFillFactorMult(tc.leaf, tc.rightmost); got != tc.want {
			t.Errorf("splitFillFactorMult(leaf=%v, rightmost=%v) = %v, want %v", tc.leaf, tc.rightmost, got, tc.want)
		}
	}
}
