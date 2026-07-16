package executor

// B2-prep pins: variable-length IndexTuple support in the descent-insert
// machinery (pg_proc_proname_args_nsp_index, OID 2691, is the first
// variable-key catalog btree with runtime maintenance), plus the pg_proc
// index wiring in syncRoutineToCatalogHeap.

import (
	"bytes"
	"encoding/binary"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestBuildIndexTupleProcNameArgsNspLayout pins the byte layout of the 2691
// IndexTuple against initdb's pgBuildIndexTupleProcKey convention (the two
// builders must agree byte-for-byte; initdb cannot be imported here — cycle).
func TestBuildIndexTupleProcNameArgsNspLayout(t *testing.T) {
	tup := buildIndexTupleProcNameArgsNsp(0x0001_0002, 7, "lower", []uint32{25}, 11)

	// rawSize = 8 (header) + 64 (name) + 28 (oidvector, 1 elem) + 4 (nsp) = 104.
	if len(tup) != 104 {
		t.Fatalf("tuple size = %d, want 104", len(tup))
	}
	le := binary.LittleEndian
	if hi, lo := le.Uint16(tup[0:2]), le.Uint16(tup[2:4]); hi != 1 || lo != 2 {
		t.Errorf("ip_blkid = (%d,%d), want (1,2)", hi, lo)
	}
	if off := le.Uint16(tup[4:6]); off != 7 {
		t.Errorf("ip_posid = %d, want 7", off)
	}
	if ti := le.Uint16(tup[6:8]); ti != (104 | sysIndexVarMask) {
		t.Errorf("t_info = %#x, want %#x", ti, 104|sysIndexVarMask)
	}
	if got := trimNameDataBytes(tup[8:72]); got != "lower" {
		t.Errorf("proname = %q, want %q", got, "lower")
	}
	// oidvector at [72:100]: vl_len_=(28<<2), ndim=1, dataoffset=0,
	// elemtype=26, dim1=1, lbound1=0, elem=25.
	if vl := le.Uint32(tup[72:76]); vl != 28<<2 {
		t.Errorf("vl_len_ = %d, want %d", vl, 28<<2)
	}
	if et := le.Uint32(tup[84:88]); et != 26 {
		t.Errorf("elemtype = %d, want 26", et)
	}
	if d1 := le.Uint32(tup[88:92]); d1 != 1 {
		t.Errorf("dim1 = %d, want 1", d1)
	}
	if el := le.Uint32(tup[96:100]); el != 25 {
		t.Errorf("elem[0] = %d, want 25", el)
	}
	if nsp := le.Uint32(tup[100:104]); nsp != 11 {
		t.Errorf("pronamespace = %d, want 11", nsp)
	}
}

// TestCmpKeyProcNameArgsNsp pins the btoidvectorcmp-compatible ordering:
// proname first, then oidvector length, then elements, then pronamespace.
func TestCmpKeyProcNameArgsNsp(t *testing.T) {
	key := func(name string, args []uint32, nsp uint32) []byte {
		return buildIndexTupleProcNameArgsNsp(0, 1, name, args, nsp)[sysIndexTupleHoff:]
	}
	cases := []struct {
		name string
		a, b []byte
		want int
	}{
		{"name-orders-first", key("a", []uint32{26}, 11), key("b", []uint32{25}, 11), -1},
		{"shorter-vector-first", key("f", []uint32{25}, 11), key("f", []uint32{25, 25}, 11), -1},
		{"element-order", key("f", []uint32{23}, 11), key("f", []uint32{25}, 11), -1},
		{"nsp-last", key("f", []uint32{25}, 11), key("f", []uint32{25}, 2200), -1},
		{"equal", key("f", []uint32{25}, 11), key("f", []uint32{25}, 11), 0},
		{"zero-args-before-one", key("f", nil, 11), key("f", []uint32{25}, 11), -1},
	}
	for _, tc := range cases {
		if got := cmpKeyProcNameArgsNsp(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: cmp = %d, want %d", tc.name, got, tc.want)
		}
		if tc.want != 0 {
			if got := cmpKeyProcNameArgsNsp(tc.b, tc.a); got != -tc.want {
				t.Errorf("%s (reversed): cmp = %d, want %d", tc.name, got, -tc.want)
			}
		}
	}
}

// writeSysBtreeImage installs a bulk-build page image (block 0..N-1) into the
// pool for the named index, mirroring what initdb's bootstrap writes on disk.
func writeSysBtreeImage(t *testing.T, ctx *Context, indexOID uint32, image []byte) {
	t.Helper()
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: indexOID,
		Fork:   storage.MainFork,
	}
	nPages := len(image) / storage.BlockSize
	for blk := 0; blk < nPages; blk++ {
		slot, newBlk, err := ctx.Pool.PinNew(rel)
		if err != nil {
			t.Fatalf("extend index %d blk %d: %v", indexOID, blk, err)
		}
		if int(newBlk) != blk {
			t.Fatalf("PinNew returned blk %d, want %d", newBlk, blk)
		}
		slot.Lock()
		copy(slot.Page(), image[blk*storage.BlockSize:(blk+1)*storage.BlockSize])
		ctx.Pool.MarkDirty(slot)
		slot.Unlock()
		ctx.Pool.Unpin(slot)
	}
}

// TestMultiLevelVariableInsertAndRebuild seeds a multi-level 2691-shaped
// btree with variable-size tuples, then drives both runtime paths: descent +
// in-place leaf insert, and the ErrNoSpaceInPage → full-rebuild fallback
// (forced by pouring enough new entries into one key range to overflow its
// leaf). Every entry must remain reachable and in btree-sorted order.
func TestMultiLevelVariableInsertAndRebuild(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	argSets := [][]uint32{{}, {23}, {23, 25}, {25, 25, 26}}
	seed := make([][]byte, 0, 300)
	for i := 0; i < 300; i++ {
		name := "zzfn" + string(rune('a'+i/26/26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+i%26))
		seed = append(seed, buildIndexTupleProcNameArgsNsp(0, uint16(i+1), name, argSets[i%len(argSets)], 11))
	}
	sort.Slice(seed, func(i, j int) bool {
		return cmpKeyProcNameArgsNsp(seed[i][sysIndexTupleHoff:], seed[j][sysIndexTupleHoff:]) < 0
	})
	image, err := buildBulkSysBtreeLayoutVariable(seed, 3)
	if err != nil {
		t.Fatalf("bulk layout: %v", err)
	}
	if len(image)/storage.BlockSize < 4 {
		t.Fatalf("seed produced %d pages; want a multi-level tree (>=4)", len(image)/storage.BlockSize)
	}
	writeSysBtreeImage(t, ctx, pgProcPronameArgsNspIndexOID, image)

	rel := storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgProcPronameArgsNspIndexOID, Fork: storage.MainFork}
	if _, level, err := readSysBtreeMeta(ctx, rel); err != nil || level != 1 {
		t.Fatalf("seed meta level = %d (err %v), want 1", level, err)
	}

	// Phase 1 — descent + in-place insert (lands on the leftmost leaf,
	// which has spare room from the bulk-load high-key reservation).
	inserted := [][]byte{buildIndexTupleProcNameArgsNsp(5, 1, "aaafirst", []uint32{23}, 2200)}
	if err := insertPgProcPronameArgsNspIndexEntry(ctx, "aaafirst", []uint32{23}, 2200,
		storage.ItemPointer{Block: 5, Offset: 1}); err != nil {
		t.Fatalf("in-place insert: %v", err)
	}

	// Phase 2 — overflow one key range until the rebuild fallback fires.
	// 90 entries of ~104 B each exceed any single 8 KiB leaf's capacity.
	for i := 0; i < 90; i++ {
		name := "aaam" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		tid := storage.ItemPointer{Block: 6, Offset: uint16(i + 1)}
		if err := insertPgProcPronameArgsNspIndexEntry(ctx, name, []uint32{23}, 2200, tid); err != nil {
			t.Fatalf("overflow insert %d (%s): %v", i, name, err)
		}
		inserted = append(inserted, buildIndexTupleProcNameArgsNsp(6, uint16(i+1), name, []uint32{23}, 2200))
	}

	meta, ok := keyMetaForSysBtree(pgProcPronameArgsNspIndexOID)
	if !ok || !meta.variable {
		t.Fatalf("keyMetaForSysBtree(2691) = %+v, %v; want variable", meta, ok)
	}
	rootBlk, level, err := readSysBtreeMeta(ctx, rel)
	if err != nil {
		t.Fatalf("read meta after inserts: %v", err)
	}
	all, err := collectAllLeafTuples(ctx, rel, rootBlk, level, meta)
	if err != nil {
		t.Fatalf("collect leaves: %v", err)
	}
	if want := len(seed) + len(inserted); len(all) != want {
		t.Fatalf("tree holds %d tuples, want %d", len(all), want)
	}
	for i := 1; i < len(all); i++ {
		if cmpKeyProcNameArgsNsp(all[i-1][sysIndexTupleHoff:], all[i][sysIndexTupleHoff:]) > 0 {
			t.Fatalf("tuples %d/%d out of order", i-1, i)
		}
	}
	// Every inserted tuple must be reachable via descent to its leaf.
	for _, want := range inserted {
		leafBlk, err := descendSysBtreeToLeaf(ctx, rel, rootBlk, level, want[sysIndexTupleHoff:], cmpKeyProcNameArgsNsp)
		if err != nil {
			t.Fatalf("descend: %v", err)
		}
		if !leafContainsTuple(t, ctx, rel, leafBlk, want) {
			t.Fatalf("tuple %q not found on its descent leaf %d", trimNameDataBytes(want[8:72]), leafBlk)
		}
	}
}

func leafContainsTuple(t *testing.T, ctx *Context, rel storage.RelFileNode, leafBlk storage.BlockNumber, want []byte) bool {
	t.Helper()
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: leafBlk})
	if err != nil {
		t.Fatalf("pin leaf %d: %v", leafBlk, err)
	}
	defer ctx.Pool.Unpin(slot)
	slot.Lock()
	defer slot.Unlock()
	page := slot.Page()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatalf("line count leaf %d: %v", leafBlk, err)
	}
	for i := 1; i <= count; i++ {
		raw, err := storage.PageGetItemRawNoCopy(page, uint16(i))
		if err != nil {
			t.Fatalf("read item %d leaf %d: %v", i, leafBlk, err)
		}
		if bytes.Equal(raw, want) {
			return true
		}
	}
	return false
}

// TestSyncRoutineInsertsPgProcIndexEntries pins the B2-prep wiring: after
// syncRoutineToCatalogHeap, pg_proc_oid_index (2690) and
// pg_proc_proname_args_nsp_index (2691) each carry an entry keyed by the
// routine and pointing at its heap TID; a second sync (the OR REPLACE /
// ALTER heap-update path) adds fresh entries at the new TID, PG-style.
func TestSyncRoutineInsertsPgProcIndexEntries(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := setupStubSysBtree(ctx, pgProcOidIndexOID, nil); err != nil {
		t.Fatalf("stub 2690: %v", err)
	}
	if err := setupStubSysBtree(ctx, pgProcPronameArgsNspIndexOID, nil); err != nil {
		t.Fatalf("stub 2691: %v", err)
	}

	r := &catalog.Routine{
		OID:        16500,
		Name:       "myfn",
		Schema:     "public",
		Language:   "sql",
		Body:       "SELECT 1",
		ArgTypes:   []catalog.Type{{Name: "int4"}},
		ReturnType: catalog.Type{Name: "int4"},
	}
	if err := syncRoutineToCatalogHeap(ctx, r); err != nil {
		t.Fatalf("syncRoutineToCatalogHeap: %v", err)
	}
	tid, ok := cat.Routines().HeapTID(r.OID)
	if !ok {
		t.Fatal("routine heap TID not recorded")
	}

	le := binary.LittleEndian
	oidTuples := readSysBtreeLeaf(t, ctx, pgProcOidIndexOID)
	if len(oidTuples) != 1 {
		t.Fatalf("2690: got %d tuples, want 1", len(oidTuples))
	}
	if oid := le.Uint32(oidTuples[0][sysIndexTupleHoff : sysIndexTupleHoff+4]); oid != r.OID {
		t.Errorf("2690 key oid = %d, want %d", oid, r.OID)
	}
	if blkLo := le.Uint16(oidTuples[0][2:4]); uint32(blkLo) != uint32(tid.Block)&0xFFFF {
		t.Errorf("2690 TID block = %d, want %d", blkLo, tid.Block)
	}

	nameTuples := readSysBtreeLeaf(t, ctx, pgProcPronameArgsNspIndexOID)
	if len(nameTuples) != 1 {
		t.Fatalf("2691: got %d tuples, want 1", len(nameTuples))
	}
	want := buildIndexTupleProcNameArgsNsp(tid.Block, tid.Offset, "myfn", []uint32{23},
		namespaceOIDForSchema(cat, "public"))
	if !bytes.Equal(nameTuples[0], want) {
		t.Errorf("2691 tuple mismatch:\n got %x\nwant %x", nameTuples[0], want)
	}

	// OR REPLACE / ALTER path: heap update → fresh entries at the new TID.
	if err := syncRoutineToCatalogHeap(ctx, r); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	newTID, _ := cat.Routines().HeapTID(r.OID)
	if newTID == tid {
		t.Fatal("second sync did not move the heap TID")
	}
	if got := len(readSysBtreeLeaf(t, ctx, pgProcOidIndexOID)); got != 2 {
		t.Errorf("2690 after update: %d tuples, want 2", got)
	}
	if got := len(readSysBtreeLeaf(t, ctx, pgProcPronameArgsNspIndexOID)); got != 2 {
		t.Errorf("2691 after update: %d tuples, want 2", got)
	}
}
