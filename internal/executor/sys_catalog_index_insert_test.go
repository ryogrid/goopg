package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestSyncTableInsertsSysCatalogIndexEntries pins the M0106-0010 batched-36
// loop 9 (a.k.a. batched-38) wiring: after `syncTableToCatalogHeap` runs,
// pg_class_oid_index (2662), pg_class_relname_nsp_index (2663) and
// pg_attribute_relid_attnum_index (2659) each carry a new IndexTuple whose
// key matches the heap row's identifying columns and whose TID points back at
// the heap row. The new entries must land in sorted position relative to any
// pre-existing entries on the leaf-root page.
func TestSyncTableInsertsSysCatalogIndexEntries(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Pre-populate the three system btrees with stub leaf-root pages so
	// `insertCanonicalSysBtreeLeaf` finds block 1 and inserts there.
	if err := setupStubSysBtree(ctx, pgClassOidIndexOID, nil); err != nil {
		t.Fatalf("stub pg_class_oid_index: %v", err)
	}
	// Pre-existing entries on the relname_nsp index so we can verify
	// sort-insert places "bench_log" at the correct slot.
	preRelnameTuples := [][]byte{
		buildIndexTupleNameOidKey(0, 1, "aaaa_pre", catalog.PublicNamespaceOID),
		buildIndexTupleNameOidKey(0, 2, "zzzz_post", catalog.PublicNamespaceOID),
	}
	if err := setupStubSysBtree(ctx, pgClassRelnameNspIndexOID, preRelnameTuples); err != nil {
		t.Fatalf("stub pg_class_relname_nsp_index: %v", err)
	}
	if err := setupStubSysBtree(ctx, pgAttributeRelidAttnumIndexOID, nil); err != nil {
		t.Fatalf("stub pg_attribute_relid_attnum_index: %v", err)
	}

	tbl := &catalog.Table{
		Schema: "public",
		Name:   "bench_log",
		OID:    16400,
		Columns: []catalog.Column{
			{Name: "client", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
			{Name: "src", Type: catalog.Type{Name: "text"}, NotNull: true, Ordinal: 1},
		},
	}
	if err := syncTableToCatalogHeap(ctx, tbl); err != nil {
		t.Fatalf("syncTableToCatalogHeap: %v", err)
	}

	// pg_class_oid_index: one new entry keyed by tbl.OID.
	oidTuples := readSysBtreeLeaf(t, ctx, pgClassOidIndexOID)
	if len(oidTuples) != 1 {
		t.Fatalf("pg_class_oid_index: got %d tuples, want 1", len(oidTuples))
	}
	if oid := binary.LittleEndian.Uint32(oidTuples[0][sysIndexTupleHoff : sysIndexTupleHoff+4]); oid != tbl.OID {
		t.Errorf("pg_class_oid_index: oid = %d, want %d", oid, tbl.OID)
	}

	// pg_class_relname_nsp_index: 3 tuples; "bench_log" must sort between
	// "aaaa_pre" (slot 1) and "zzzz_post" (slot 3) → slot 2.
	nameTuples := readSysBtreeLeaf(t, ctx, pgClassRelnameNspIndexOID)
	if len(nameTuples) != 3 {
		t.Fatalf("pg_class_relname_nsp_index: got %d tuples, want 3", len(nameTuples))
	}
	if got := trimNameDataBytes(nameTuples[1][sysIndexTupleHoff : sysIndexTupleHoff+64]); got != "bench_log" {
		t.Errorf("pg_class_relname_nsp_index slot 2 (1-indexed): name = %q, want %q", got, "bench_log")
	}
	if nsOid := binary.LittleEndian.Uint32(nameTuples[1][sysIndexTupleHoff+64 : sysIndexTupleHoff+68]); nsOid != catalog.PublicNamespaceOID {
		t.Errorf("pg_class_relname_nsp_index slot 2: nsoid = %d, want %d", nsOid, catalog.PublicNamespaceOID)
	}

	// pg_attribute_relid_attnum_index: 2 new entries (one per column),
	// each keyed by (tbl.OID, attnum=Ordinal+1).
	attTuples := readSysBtreeLeaf(t, ctx, pgAttributeRelidAttnumIndexOID)
	if len(attTuples) != 2 {
		t.Fatalf("pg_attribute_relid_attnum_index: got %d tuples, want 2", len(attTuples))
	}
	for i, tup := range attTuples {
		gotRelid := binary.LittleEndian.Uint32(tup[sysIndexTupleHoff : sysIndexTupleHoff+4])
		gotAttnum := int16(binary.LittleEndian.Uint16(tup[sysIndexTupleHoff+4 : sysIndexTupleHoff+6]))
		if gotRelid != tbl.OID {
			t.Errorf("pg_attribute_relid_attnum_index slot %d: attrelid = %d, want %d", i+1, gotRelid, tbl.OID)
		}
		if gotAttnum != int16(i+1) {
			t.Errorf("pg_attribute_relid_attnum_index slot %d: attnum = %d, want %d", i+1, gotAttnum, i+1)
		}
	}
}

// TestSyncTableSplitsSysIndexLeafRootWhenFull verifies the M0106-0010
// batched-39 leaf-root split: when the leaf-root for
// pg_class_relname_nsp_index has no room for a new entry, the runtime insert
// path splits block 1 into a 2-leaf + new-internal-root layout in place.
// Post-split invariants checked:
//   - The metapage at block 0 points at the new root (btm_root != 1,
//     btm_level == 1).
//   - The new root is an internal page (BTP_ROOT, btpo_level=1) with 2
//     downlinks: minus-infinity → block 1, normal → new right leaf.
//   - Block 1 is now BTP_LEAF only (no BTP_ROOT) with btpo_next pointing
//     at the new right leaf and carries a P_HIKEY at slot 1.
//   - The new right leaf is BTP_LEAF only, btpo_prev = 1, btpo_next = P_NONE,
//     and starts with data tuples at slot 1.
//   - The merged entry count across both leaves is 98 (97 pre-existing + 1
//     newly inserted "bench_log").
//   - "bench_log" is present in exactly one of the two leaves.
func TestSyncTableSplitsSysIndexLeafRootWhenFull(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// pg_class_oid_index and pg_attribute_relid_attnum_index: empty stub
	// so insertions succeed normally.
	if err := setupStubSysBtree(ctx, pgClassOidIndexOID, nil); err != nil {
		t.Fatalf("stub pg_class_oid_index: %v", err)
	}
	if err := setupStubSysBtree(ctx, pgAttributeRelidAttnumIndexOID, nil); err != nil {
		t.Fatalf("stub pg_attribute_relid_attnum_index: %v", err)
	}
	// pg_class_relname_nsp_index: pack the leaf-root with 97 sorted entries
	// using "aaa_AA".."aaa_FE" — all < "bench_log" so the new insert lands
	// at sort position 98.
	preFull := make([][]byte, 97)
	for i := range preFull {
		// Two-letter suffix keeps every name unique and lexicographically
		// ordered ("aaa_AA","aaa_AB",..., wrapping after Z).
		hi := byte('A' + (i / 26))
		lo := byte('A' + (i % 26))
		name := "aaa_" + string([]byte{hi, lo})
		preFull[i] = buildIndexTupleNameOidKey(0, uint16(i+1), name, catalog.PublicNamespaceOID)
	}
	if err := setupStubSysBtree(ctx, pgClassRelnameNspIndexOID, preFull); err != nil {
		t.Fatalf("stub pg_class_relname_nsp_index: %v", err)
	}

	tbl := &catalog.Table{
		Schema: "public",
		Name:   "bench_log",
		OID:    16400,
		Columns: []catalog.Column{
			{Name: "client", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
		},
	}
	if err := syncTableToCatalogHeap(ctx, tbl); err != nil {
		t.Fatalf("syncTableToCatalogHeap should not error after split: %v", err)
	}

	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgClassRelnameNspIndexOID,
		Fork:   storage.MainFork,
	}

	// NBlocks should now be 4: meta(0), left leaf(1), right leaf(2), root(3).
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	if nBlocks != 4 {
		t.Fatalf("post-split NBlocks = %d, want 4", nBlocks)
	}

	// Metapage: btm_root must point at block 3, btm_level=1.
	rootBlk, level := readMetapageRootAndLevel(t, ctx, rel)
	if rootBlk != 3 {
		t.Errorf("metapage btm_root = %d, want 3", rootBlk)
	}
	if level != 1 {
		t.Errorf("metapage btm_level = %d, want 1", level)
	}

	// Block 1: leaf-only, btpo_next=2, slot 1 is P_HIKEY pivot.
	page1 := readPage(t, ctx, rel, 1)
	prev1, next1, lvl1, flags1 := readBTreeOpaque(page1)
	if flags1&btpRootFlag != 0 {
		t.Errorf("block 1 flags = %#x still has BTP_ROOT", flags1)
	}
	if flags1&btpLeafFlag == 0 {
		t.Errorf("block 1 flags = %#x missing BTP_LEAF", flags1)
	}
	if next1 != 2 {
		t.Errorf("block 1 btpo_next = %d, want 2", next1)
	}
	if prev1 != 0 /* P_NONE */ {
		t.Errorf("block 1 btpo_prev = %d, want P_NONE (0)", prev1)
	}
	if lvl1 != 0 {
		t.Errorf("block 1 btpo_level = %d, want 0 (leaf)", lvl1)
	}

	// Block 2: rightmost leaf, btpo_prev=1, btpo_next=P_NONE.
	page2 := readPage(t, ctx, rel, 2)
	prev2, next2, lvl2, flags2 := readBTreeOpaque(page2)
	if flags2 != btpLeafFlag {
		t.Errorf("block 2 flags = %#x, want BTP_LEAF only", flags2)
	}
	if prev2 != 1 {
		t.Errorf("block 2 btpo_prev = %d, want 1", prev2)
	}
	if next2 != 0 {
		t.Errorf("block 2 btpo_next = %d, want P_NONE (0)", next2)
	}
	if lvl2 != 0 {
		t.Errorf("block 2 btpo_level = %d, want 0", lvl2)
	}

	// Block 3: new internal root, btpo_level=1, 2 downlinks.
	page3 := readPage(t, ctx, rel, 3)
	_, _, lvl3, flags3 := readBTreeOpaque(page3)
	if flags3 != btpRootFlag {
		t.Errorf("block 3 flags = %#x, want BTP_ROOT only", flags3)
	}
	if lvl3 != 1 {
		t.Errorf("block 3 btpo_level = %d, want 1", lvl3)
	}
	rootCount, err := storage.PageLinePointerCount(page3)
	if err != nil {
		t.Fatalf("root line pointer count: %v", err)
	}
	if rootCount != 2 {
		t.Errorf("root downlink count = %d, want 2", rootCount)
	}

	// Verify the merged data-tuple count across both leaves equals 98
	// (97 pre-existing + 1 new). Block 1 holds a P_HIKEY at slot 1 (pivot,
	// not a data tuple); block 2 is rightmost (no high key).
	left := pageLineCount(t, page1)
	right := pageLineCount(t, page2)
	dataTuples := (left - 1) + right // subtract block-1 high key
	if dataTuples != 98 {
		t.Errorf("post-split data tuple total = %d, want 98", dataTuples)
	}

	// "bench_log" must appear on exactly one leaf.
	found := 0
	for _, page := range [][]byte{page1, page2} {
		count, _ := storage.PageLinePointerCount(page)
		startSlot := uint16(1)
		// Block 1 carries a high key at slot 1; data starts at slot 2.
		if page[0] != 0 && pageHasHighKey(page) {
			startSlot = 2
		}
		for slot := startSlot; slot <= uint16(count); slot++ {
			raw, err := storage.PageGetItemRaw(page, slot)
			if err != nil {
				continue
			}
			if len(raw) < sysIndexTupleHoff+64 {
				continue
			}
			if trimNameDataBytes(raw[sysIndexTupleHoff:sysIndexTupleHoff+64]) == "bench_log" {
				found++
			}
		}
	}
	if found != 1 {
		t.Errorf("bench_log occurrences across leaves = %d, want 1", found)
	}
}

// readPage pins block `blk` of `rel`, copies its bytes, and unpins.
func readPage(t *testing.T, ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber) []byte {
	t.Helper()
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		t.Fatalf("pin block %d: %v", blk, err)
	}
	defer ctx.Pool.Unpin(slot)
	out := make([]byte, storage.BlockSize)
	copy(out, slot.Page())
	return out
}

// readBTreeOpaque decodes the 16-byte BTPageOpaqueData trailer at the end of
// the supplied page: (btpo_prev, btpo_next, btpo_level, btpo_flags).
func readBTreeOpaque(page []byte) (prev, next, level uint32, flags uint16) {
	off := storage.BlockSize - sizeOfBTPageOpaque
	prev = binary.LittleEndian.Uint32(page[off+0 : off+4])
	next = binary.LittleEndian.Uint32(page[off+4 : off+8])
	level = binary.LittleEndian.Uint32(page[off+8 : off+12])
	flags = binary.LittleEndian.Uint16(page[off+12 : off+14])
	return
}

// readMetapageRootAndLevel returns (btm_root, btm_level) from the metapage
// at block 0 of rel.
func readMetapageRootAndLevel(t *testing.T, ctx *Context, rel storage.RelFileNode) (uint32, uint32) {
	t.Helper()
	page := readPage(t, ctx, rel, 0)
	base := storage.SizeOfPageHeaderData
	rootBlk := binary.LittleEndian.Uint32(page[base+8 : base+12])
	level := binary.LittleEndian.Uint32(page[base+12 : base+16])
	return rootBlk, level
}

func pageLineCount(t *testing.T, page []byte) int {
	t.Helper()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatalf("line pointer count: %v", err)
	}
	return count
}

// pageHasHighKey heuristic: a leaf page that is non-rightmost (i.e., has a
// high key) is identified by having btpo_next != P_NONE in the opaque area.
func pageHasHighKey(page []byte) bool {
	_, next, _, _ := readBTreeOpaque(page)
	return next != 0
}

// TestBuildIndexTupleOidKeyByteLayout pins the on-disk byte layout of a
// 16-byte uint32-keyed IndexTuple so a future tupdesc-shape regression is
// caught locally rather than in PG-standby integration.
func TestBuildIndexTupleOidKeyByteLayout(t *testing.T) {
	tup := buildIndexTupleOidKey(0x10203040, 0x0506, 0xCAFEBABE)
	if len(tup) != 16 {
		t.Fatalf("tuple length = %d, want 16", len(tup))
	}
	// ItemPointerData: bi_hi (2 bytes, BE-ish: high half of heapBlk),
	// bi_lo (2 bytes), ip_posid (2 bytes).
	if got := binary.LittleEndian.Uint16(tup[0:2]); got != 0x1020 {
		t.Errorf("bi_hi = %#x, want 0x1020", got)
	}
	if got := binary.LittleEndian.Uint16(tup[2:4]); got != 0x3040 {
		t.Errorf("bi_lo = %#x, want 0x3040", got)
	}
	if got := binary.LittleEndian.Uint16(tup[4:6]); got != 0x0506 {
		t.Errorf("ip_posid = %#x, want 0x0506", got)
	}
	if got := binary.LittleEndian.Uint16(tup[6:8]) & sysIndexSizeMask; got != 16 {
		t.Errorf("t_info.size = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(tup[8:12]); got != 0xCAFEBABE {
		t.Errorf("key = %#x, want 0xCAFEBABE", got)
	}
}

// setupStubSysBtree writes a minimal-but-valid PG btree leaf-root page at
// block 1 of the named index relation (and a zero-filled metapage at block 0
// for completeness). Only block 1 is read by the runtime insert helper, so
// the metapage need not be a valid BTREE_MAGIC blob in tests.
//
// `preTuples` are written at their natural insertion order; callers are
// expected to pass them already sorted by key.
func setupStubSysBtree(ctx *Context, indexOID uint32, preTuples [][]byte) error {
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: indexOID,
		Fork:   storage.MainFork,
	}
	// Block 0 — metapage. Zero bytes are acceptable for the in-memory test
	// fixture; the runtime helper only reads block 1.
	slot0, _, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return err
	}
	slot0.Lock()
	// Ensure block 0 is a valid page header to satisfy NBlocks accounting.
	_ = storage.InitPage(slot0.Page())
	ctx.Pool.MarkDirty(slot0)
	slot0.Unlock()
	ctx.Pool.Unpin(slot0)

	// Block 1 — leaf-root. Init a fresh page, then append each preTuple.
	slot1, _, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return err
	}
	slot1.Lock()
	page := slot1.Page()
	if err := storage.InitPage(page); err != nil {
		slot1.Unlock()
		ctx.Pool.Unpin(slot1)
		return err
	}
	// Make room for a 16-byte BTPageOpaque at end of page so subsequent
	// inserts respect PG's special-area convention.
	storage.MustHeader(page).SetSpecial(uint16(storage.BlockSize - 16))
	for _, t := range preTuples {
		if _, err := storage.PageAddItemRaw(page, t); err != nil {
			slot1.Unlock()
			ctx.Pool.Unpin(slot1)
			return err
		}
	}
	ctx.Pool.MarkDirty(slot1)
	slot1.Unlock()
	ctx.Pool.Unpin(slot1)
	return nil
}

// readSysBtreeLeaf returns the raw IndexTuple bytes of every line pointer on
// block 1 of the named index, in slot order (1..N).
func readSysBtreeLeaf(t *testing.T, ctx *Context, indexOID uint32) [][]byte {
	t.Helper()
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: indexOID,
		Fork:   storage.MainFork,
	}
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: sysBtreeRootBlock})
	if err != nil {
		t.Fatalf("pin index %d block 1: %v", indexOID, err)
	}
	defer ctx.Pool.Unpin(slot)
	page := slot.Page()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatalf("line pointer count %d: %v", indexOID, err)
	}
	out := make([][]byte, 0, count)
	for i := 1; i <= count; i++ {
		raw, err := storage.PageGetItemRaw(page, uint16(i))
		if err != nil {
			t.Fatalf("get item %d/%d: %v", indexOID, i, err)
		}
		out = append(out, raw)
	}
	return out
}

// trimNameDataBytes strips the NUL padding from a 64-byte NameData payload.
func trimNameDataBytes(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
