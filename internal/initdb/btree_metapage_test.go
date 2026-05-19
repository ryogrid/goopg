package initdb

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestMakeBtreeRootPageMatchesPGMetapage pins the on-disk layout of an
// empty nailed-index file against PG's `_bt_initmetapage`. PG's
// `_bt_getmeta` (postgres/src/backend/access/nbtree/nbtpage.c:152) FATALs
// with `index "%s" is not a btree` unless both
// `P_ISMETA(opaque) == true` and `metad->btm_magic == BTREE_MAGIC`, so
// every nailed index page goopg seeds during bootstrap must satisfy
// these invariants — otherwise PG-standby connects FATAL on the first
// index scan (M0106-0010 Step 3k canary: pg_opclass_oid_index).
func TestMakeBtreeRootPageMatchesPGMetapage(t *testing.T) {
	page := makeBtreeRootPage()
	if got, want := len(page), storage.BlockSize; got != want {
		t.Fatalf("len(page) = %d, want %d", got, want)
	}

	le := binary.LittleEndian

	// PageHeader expectations.
	h := storage.MustHeader(storage.Page(page))
	const sizeofBTMetaPageData = 48
	if got, want := h.Lower(), uint16(storage.SizeOfPageHeaderData+sizeofBTMetaPageData); got != want {
		t.Errorf("pd_lower = %d, want %d (header + sizeof(BTMetaPageData))", got, want)
	}
	if got, want := h.Upper(), uint16(storage.BlockSize-16); got != want {
		t.Errorf("pd_upper = %d, want %d", got, want)
	}
	if got, want := h.Special(), uint16(storage.BlockSize-16); got != want {
		t.Errorf("pd_special = %d, want %d", got, want)
	}

	// BTMetaPageData at PageContents (offset SizeOfPageHeaderData).
	base := storage.SizeOfPageHeaderData
	if got, want := le.Uint32(page[base+0:base+4]), uint32(0x053162); got != want {
		t.Errorf("btm_magic = 0x%x, want BTREE_MAGIC 0x053162", got)
	}
	if got, want := le.Uint32(page[base+4:base+8]), uint32(4); got != want {
		t.Errorf("btm_version = %d, want BTREE_VERSION 4", got)
	}
	if got := le.Uint32(page[base+8 : base+12]); got != 0 {
		t.Errorf("btm_root = %d, want P_NONE (0) for an empty index", got)
	}
	if got := le.Uint32(page[base+12 : base+16]); got != 0 {
		t.Errorf("btm_level = %d, want 0", got)
	}
	if got := le.Uint32(page[base+16 : base+20]); got != 0 {
		t.Errorf("btm_fastroot = %d, want P_NONE (0)", got)
	}
	if got := le.Uint32(page[base+20 : base+24]); got != 0 {
		t.Errorf("btm_fastlevel = %d, want 0", got)
	}
	if got := le.Uint32(page[base+24 : base+28]); got != 0 {
		t.Errorf("btm_last_cleanup_num_delpages = %d, want 0", got)
	}
	// 4 bytes padding at [base+28:base+32] for float8 alignment.
	heapTuples := math.Float64frombits(le.Uint64(page[base+32 : base+40]))
	if heapTuples != -1.0 {
		t.Errorf("btm_last_cleanup_num_heap_tuples = %v, want -1.0", heapTuples)
	}
	if page[base+40] != 0 {
		t.Errorf("btm_allequalimage = %d, want false (0)", page[base+40])
	}

	// BTPageOpaqueData at end of page; only btpo_flags must be BTP_META.
	off := storage.BlockSize - 16
	const btpMeta = 1 << 3
	if got := le.Uint16(page[off+12 : off+14]); got != btpMeta {
		t.Errorf("btpo_flags = 0x%x, want BTP_META 0x%x", got, btpMeta)
	}
	if got := le.Uint32(page[off+0 : off+4]); got != 0 {
		t.Errorf("btpo_prev = %d, want P_NONE (0)", got)
	}
	if got := le.Uint32(page[off+4 : off+8]); got != 0 {
		t.Errorf("btpo_next = %d, want P_NONE (0)", got)
	}
	if got := le.Uint32(page[off+8 : off+12]); got != 0 {
		t.Errorf("btpo_level = %d, want 0", got)
	}
	if got := le.Uint16(page[off+14 : off+16]); got != 0 {
		t.Errorf("btpo_cycleid = %d, want 0", got)
	}
}
