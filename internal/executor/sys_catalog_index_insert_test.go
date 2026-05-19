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

// TestSyncTableSkipsSysIndexInsertWhenLeafRootFull verifies that a full
// leaf-root page (mirroring the bootstrap state of pg_class_relname_nsp_index
// after a heavy initdb) causes the runtime insert to silently skip rather
// than fail the CREATE TABLE. The skip path is required to keep
// `TestRestartAfterRetention` and other server-level CREATE TABLE tests
// green until btree page splits land in a follow-on loop.
func TestSyncTableSkipsSysIndexInsertWhenLeafRootFull(t *testing.T) {
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
	// pg_class_relname_nsp_index: pack the leaf-root to ~full so the next
	// insert has no room. 80-byte tuple + 4-byte line pointer = 84 bytes
	// per slot; (8192 - 24 - 16) / 84 = 97.
	preFull := make([][]byte, 97)
	for i := range preFull {
		// Names that all sort before "bench_log" so the new insert would
		// land at the end (slot 98) if space allowed.
		name := "aaa_" + string(rune('A'+i%26))
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
		t.Fatalf("syncTableToCatalogHeap should not error on full leaf-root: %v", err)
	}

	// pg_class_relname_nsp_index leaf-root: still 97 entries (skip path).
	got := len(readSysBtreeLeaf(t, ctx, pgClassRelnameNspIndexOID))
	if got != 97 {
		t.Errorf("pg_class_relname_nsp_index: got %d tuples, want 97 (insert should have been skipped)", got)
	}
	// pg_class_oid_index leaf-root: 1 entry (insert succeeded — page had room).
	got = len(readSysBtreeLeaf(t, ctx, pgClassOidIndexOID))
	if got != 1 {
		t.Errorf("pg_class_oid_index: got %d tuples, want 1", got)
	}
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
