package nbtree

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestInsertBuiltSerialIndexMatchesPGInternalLevel pins M0146-0216's internal
// split: TPC-DS customer_demographics_pkey (cd_demo_sk 1..1920800, loaded in
// key order) is 5268 pages in PG 18.3 — 5248 leaves, 18 internal pages (17 left
// halves of 285 downlinks plus a high key, and the rightmost with 403) and the
// root. goopg built 5269 with 19 internal pages of 284: it charged the leaf's
// heap-TID allowance to an internal split's new high key and never shrank the
// right half's first item to a minus-infinity pivot, so every rightmost
// internal split left one item fewer on the left (_bt_recsplitloc,
// _bt_pgaddtup's newfirstdataitem).
func TestInsertBuiltSerialIndexMatchesPGInternalLevel(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts 1.9M keys")
	}
	const rows = 1920800
	desc := int4Desc()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 1024})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9217, Fork: storage.MainFork}
	bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	for g := int64(1); g <= rows; g++ {
		tid := storage.ItemPointer{Block: storage.BlockNumber((g - 1) / 200), Offset: uint16((g-1)%200 + 1)}
		v := make([]byte, 4)
		binary.LittleEndian.PutUint32(v, uint32(g))
		if err := bt.Insert(tup(t, desc.Attrs, [][]byte{v}, tid), tid); err != nil {
			t.Fatalf("Insert(%d): %v", g, err)
		}
	}
	n, err := pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	// The internal level, block order: PG's bt_page_items shows 286 line
	// pointers on each left half (285 downlinks plus the high key), 403 on the
	// rightmost, and every page's first downlink is an 8-byte key-less pivot.
	var counts []int
	for b := storage.BlockNumber(1); b < storage.BlockNumber(n); b++ {
		slot, err := bt.pinR(b)
		if err != nil {
			t.Fatalf("pinR(%d): %v", b, err)
		}
		if readOpaque(slot.Page()).Level == 1 {
			c, err := PGDataItemCount(slot.Page())
			if err != nil {
				t.Fatalf("PGDataItemCount(%d): %v", b, err)
			}
			raw, err := pgGetItemRaw(slot.Page(), 1)
			if err != nil {
				t.Fatalf("pgGetItemRaw(%d, 1): %v", b, err)
			}
			if len(raw) != SizeOfIndexTupleData {
				t.Errorf("internal block %d: first downlink is %d bytes, PG stores a %d-byte minus-infinity pivot", b, len(raw), SizeOfIndexTupleData)
			}
			counts = append(counts, c)
		}
		bt.unpinR(slot)
	}
	if len(counts) != 18 || counts[17] != 403 {
		t.Fatalf("internal level %v, PG 18.3 has 18 pages: 17 of 285 downlinks and the rightmost with 403", counts)
	}
	for i, c := range counts[:17] {
		if c != 285 {
			t.Fatalf("internal page %d holds %d downlinks, PG 18.3 holds 285 (%v)", i, c, counts)
		}
	}
	if n != 5268 {
		t.Fatalf("built %d pages, PG 18.3 builds 5268 (5248 leaves, 18 internal, root, meta)", n)
	}
}

// TestFindSplitLocInternalTakesSmallestFirstRight pins _bt_findsplitloc's
// internal-page choice (M0146-0216): within INTERNAL_SPLIT_DISTANCE of the
// balanced split, _bt_bestsplitloc takes the split whose firstright tuple is
// smallest (_bt_split_penalty's internal arm), because that tuple becomes the
// new separator in the parent.
func TestFindSplitLocInternalTakesSmallestFirstRight(t *testing.T) {
	f := indexFormat{desc: int4Desc()}
	items := make([]item, 180)
	for i := range items {
		size := 40
		if i == 93 {
			size = 16
		}
		items[i] = downlinkItem(make([]byte, size), storage.BlockNumber(i+1))
	}
	// 180 items of 44 bytes with line pointers. The left half also pays for
	// its 44-byte high key and the right half gets 32 bytes back from its
	// minus-infinity first item, so the balanced split is m=89; m=93 is 176
	// bytes off per side, inside the 594-byte (7.5%) tolerance.
	m, ok := f.pgFindSplitLocInternal(items, 0, pageDataBudget(), pageDataBudget(), false)
	if !ok || m != 93 {
		t.Fatalf("pgFindSplitLocInternal = %d, %v; PG splits before the 16-byte item at 93", m, ok)
	}
	// Moved past the tolerance it no longer wins: the balanced split does.
	items[93].key, items[110].key = make([]byte, 40), make([]byte, 16)
	if m, ok = f.pgFindSplitLocInternal(items, 0, pageDataBudget(), pageDataBudget(), false); !ok || m != 89 {
		t.Fatalf("pgFindSplitLocInternal = %d, %v; PG takes the balanced split 89 when the short item is out of range", m, ok)
	}
	// A rightmost split weights the free space 70/30 (BTREE_NONLEAF_FILLFACTOR)
	// and charges the new high key as the firstright tuple stands — no heap-TID
	// allowance, which is the leaf's worst case. With an 80-byte first item the
	// deltas at 126 and 127 are 24.4 and 19.6; the leaf's 8 bytes would flip
	// them to 18.8 and 25.2.
	items[110].key, items[0].key = make([]byte, 40), make([]byte, 80)
	if m, ok = f.pgFindSplitLocInternal(items, 0, pageDataBudget(), pageDataBudget(), true); !ok || m != 127 {
		t.Fatalf("pgFindSplitLocInternal = %d, %v; PG's rightmost internal split is 127", m, ok)
	}
}
