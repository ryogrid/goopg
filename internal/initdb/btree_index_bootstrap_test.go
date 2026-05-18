package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPgBuildIndexTupleOidKeyLayoutMatchesPG18 pins the byte-exact layout
// PG's heap_deform_tuple / _bt_compare expect for a no-nulls, single-column
// oid-keyed index tuple — 8 bytes header + 4-byte oid key + 4 bytes
// MAXALIGN pad = 16 bytes total, with t_info storing the size (16).
func TestPgBuildIndexTupleOidKeyLayoutMatchesPG18(t *testing.T) {
	const heapBlk uint32 = 0xDEADBEEF
	out := pgBuildIndexTupleOidKey(heapBlk, 0xCAFE, 1986)
	if len(out) != 16 {
		t.Fatalf("len=%d, want 16 (MAXALIGN(8+4))", len(out))
	}
	le := binary.LittleEndian
	// BlockIdData is two uint16 halves in struct order: bi_hi at [0..1],
	// bi_lo at [2..3]. PG decodes the block number as
	// (bi_hi<<16)|bi_lo, NOT as a single LE uint32. Encoding heapBlk as
	// LE uint32 silently corrupts the TID for any block > 0 (e.g. block
	// 3 → bi_hi=3, bi_lo=0 → 196608) — the Step 3s regression.
	biHi := le.Uint16(out[0:2])
	biLo := le.Uint16(out[2:4])
	if got := (uint32(biHi) << 16) | uint32(biLo); got != heapBlk {
		t.Errorf("BlockIdGetBlockNumber: got %#x, want %#x (bi_hi=%#x bi_lo=%#x)",
			got, heapBlk, biHi, biLo)
	}
	if biHi != uint16(heapBlk>>16) {
		t.Errorf("bi_hi: got %#x, want %#x", biHi, uint16(heapBlk>>16))
	}
	if biLo != uint16(heapBlk&0xFFFF) {
		t.Errorf("bi_lo: got %#x, want %#x", biLo, uint16(heapBlk&0xFFFF))
	}
	if got := le.Uint16(out[4:6]); got != 0xCAFE {
		t.Errorf("ip_posid: got %#x, want %#x", got, uint16(0xCAFE))
	}
	if got := le.Uint16(out[6:8]); got != 16 {
		t.Errorf("t_info: got %d, want 16 (size with no flags)", got)
	}
	if got := le.Uint32(out[8:12]); got != 1986 {
		t.Errorf("oid key: got %d, want 1986", got)
	}
	// Trailing pad must be zero so future flag bits don't get set inadvertently.
	for i := 12; i < 16; i++ {
		if out[i] != 0 {
			t.Errorf("pad byte %d non-zero: got %#x", i, out[i])
		}
	}
}

// TestPgBuildBtreeLeafRootPagePageHeader pins the on-disk page header
// fields PG reads in _bt_getroot and _bt_first: special area at the
// per-block-size boundary, lower past the line pointers, upper above
// the tuples, and the opaque flag set to BTP_LEAF | BTP_ROOT.
func TestPgBuildBtreeLeafRootPagePageHeader(t *testing.T) {
	tuples := [][]byte{
		pgBuildIndexTupleOidKey(0, 1, 100),
		pgBuildIndexTupleOidKey(0, 2, 200),
		pgBuildIndexTupleOidKey(0, 3, 300),
	}
	page, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		t.Fatalf("pgBuildBtreeLeafRootPage: %v", err)
	}
	if len(page) != storage.BlockSize {
		t.Fatalf("len=%d, want %d", len(page), storage.BlockSize)
	}
	h, err := storage.Header(storage.Page(page))
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	wantSpecial := uint16(storage.BlockSize - sizeOfBTPageOpaque)
	if got := h.Special(); got != wantSpecial {
		t.Errorf("special: got %d, want %d", got, wantSpecial)
	}
	wantLower := uint16(storage.SizeOfPageHeaderData + 4*len(tuples))
	if got := h.Lower(); got != wantLower {
		t.Errorf("lower: got %d, want %d", got, wantLower)
	}
	wantUpper := uint16(int(wantSpecial) - 16*len(tuples))
	if got := h.Upper(); got != wantUpper {
		t.Errorf("upper: got %d, want %d", got, wantUpper)
	}
	// Opaque flag at end of page: BTP_LEAF | BTP_ROOT = 0x03.
	off := storage.BlockSize - sizeOfBTPageOpaque
	if got := binary.LittleEndian.Uint16(page[off+12 : off+14]); got != btpLeaf|btpRoot {
		t.Errorf("btpo_flags: got %#x, want %#x (BTP_LEAF|BTP_ROOT)", got, btpLeaf|btpRoot)
	}
	// Level and prev/next must be zero on a leaf-root.
	if got := binary.LittleEndian.Uint32(page[off+0 : off+4]); got != 0 {
		t.Errorf("btpo_prev: got %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint32(page[off+4 : off+8]); got != 0 {
		t.Errorf("btpo_next: got %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint32(page[off+8 : off+12]); got != 0 {
		t.Errorf("btpo_level: got %d, want 0", got)
	}
}

// TestPgBuildBtreeMetapageWithRootEncodesRootPointer asserts the metapage
// declares btm_root, btm_fastroot, btm_level, btm_fastlevel matching the
// caller-supplied root block — PG's _bt_getroot reads these to descend.
func TestPgBuildBtreeMetapageWithRootEncodesRootPointer(t *testing.T) {
	meta := pgBuildBtreeMetapageWithRoot(1, 0)
	if len(meta) != storage.BlockSize {
		t.Fatalf("len=%d, want %d", len(meta), storage.BlockSize)
	}
	base := storage.SizeOfPageHeaderData
	le := binary.LittleEndian
	if got := le.Uint32(meta[base : base+4]); got != btreeMagicConst {
		t.Errorf("btm_magic: got %#x, want %#x", got, btreeMagicConst)
	}
	if got := le.Uint32(meta[base+4 : base+8]); got != btreeVersionConst {
		t.Errorf("btm_version: got %d, want %d", got, btreeVersionConst)
	}
	if got := le.Uint32(meta[base+8 : base+12]); got != 1 {
		t.Errorf("btm_root: got %d, want 1", got)
	}
	if got := le.Uint32(meta[base+12 : base+16]); got != 0 {
		t.Errorf("btm_level: got %d, want 0", got)
	}
	if got := le.Uint32(meta[base+16 : base+20]); got != 1 {
		t.Errorf("btm_fastroot: got %d, want 1", got)
	}
	if got := le.Uint32(meta[base+20 : base+24]); got != 0 {
		t.Errorf("btm_fastlevel: got %d, want 0", got)
	}
	off := storage.BlockSize - sizeOfBTPageOpaque
	if got := le.Uint16(meta[off+12 : off+14]); got != btpMetaFlag {
		t.Errorf("btpo_flags: got %#x, want %#x (BTP_META)", got, btpMetaFlag)
	}
}

// TestBootstrapPgOpclassOidIndexWritesPopulatedBtree end-to-ends the
// Step 3l output: file is 2 blocks, block 0 is a metapage pointing at
// block 1, block 1 is a leaf-root carrying len(pgOpclassInitialEntries)
// tuples sorted ascending by OID. Pins both shared (global/) and per-
// database (base/{1,5}/) copies because PG opens the same OID from
// either path depending on the caller (M0106-0008 / M0106-0009 design).
func TestBootstrapPgOpclassOidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5", "global"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := bootstrapPgOpclassOidIndex(dir); err != nil {
		t.Fatalf("bootstrapPgOpclassOidIndex: %v", err)
	}
	entries := pgOpclassInitialEntries()
	wantOIDs := make([]uint32, len(entries))
	for i, e := range entries {
		wantOIDs[i] = e.OID
	}
	sort.Slice(wantOIDs, func(i, j int) bool { return wantOIDs[i] < wantOIDs[j] })

	for _, path := range []string{
		filepath.Join(dir, "base", "1", "2687"),
		filepath.Join(dir, "base", "5", "2687"),
		filepath.Join(dir, "global", "2687"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw) != 2*storage.BlockSize {
			t.Errorf("%s: len=%d, want %d (2 blocks)", path, len(raw), 2*storage.BlockSize)
			continue
		}
		// Metapage at block 0: btm_root=1, btm_level=0.
		base := storage.SizeOfPageHeaderData
		if got := binary.LittleEndian.Uint32(raw[base+8 : base+12]); got != 1 {
			t.Errorf("%s: btm_root=%d, want 1", path, got)
		}
		// Leaf-root at block 1 — count line pointers and read keys back.
		leaf := raw[storage.BlockSize : 2*storage.BlockSize]
		h, err := storage.Header(storage.Page(leaf))
		if err != nil {
			t.Fatalf("%s: leaf header: %v", path, err)
		}
		nItems := (int(h.Lower()) - storage.SizeOfPageHeaderData) / 4
		if nItems != len(wantOIDs) {
			t.Errorf("%s: leaf items=%d, want %d", path, nItems, len(wantOIDs))
			continue
		}
		for i := 0; i < nItems; i++ {
			raw32 := binary.LittleEndian.Uint32(leaf[storage.SizeOfPageHeaderData+i*4 : storage.SizeOfPageHeaderData+i*4+4])
			off := raw32 & 0x7FFF
			length := (raw32 >> 17) & 0x7FFF
			if length != 16 {
				t.Errorf("%s: item %d length=%d, want 16", path, i, length)
				continue
			}
			// Key is at offset 8 within the tuple (after IndexTupleHeader).
			gotOID := binary.LittleEndian.Uint32(leaf[off+8 : off+12])
			if gotOID != wantOIDs[i] {
				t.Errorf("%s: item %d OID=%d, want %d (sorted)", path, i, gotOID, wantOIDs[i])
			}
		}
	}
}

// TestBootstrapPgClassOidIndexWritesPopulatedBtree end-to-ends Step 3m's
// output: file is 2 blocks at every disk location, block 0 is a metapage
// pointing at block 1, block 1 is a leaf-root carrying one IndexTuple per
// nailed rel (shared + local) sorted ascending by OID with the heap-row
// TID encoded in ItemPointer. Closes the PANIC "could not open critical
// system index 2671" blocker.
func TestBootstrapPgClassOidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5", "global"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	tids, err := bootstrapPgClassTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgClassTuples: %v", err)
	}
	if err := bootstrapPgClassOidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgClassOidIndex: %v", err)
	}

	wantOIDs := make([]uint32, 0, len(tids))
	for oid := range tids {
		wantOIDs = append(wantOIDs, oid)
	}
	sort.Slice(wantOIDs, func(i, j int) bool { return wantOIDs[i] < wantOIDs[j] })

	for _, path := range []string{
		filepath.Join(dir, "base", "1", "2662"),
		filepath.Join(dir, "base", "5", "2662"),
		filepath.Join(dir, "global", "2662"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw) != 2*storage.BlockSize {
			t.Errorf("%s: len=%d, want %d (2 blocks)", path, len(raw), 2*storage.BlockSize)
			continue
		}
		base := storage.SizeOfPageHeaderData
		if got := binary.LittleEndian.Uint32(raw[base+8 : base+12]); got != 1 {
			t.Errorf("%s: btm_root=%d, want 1", path, got)
		}
		leaf := raw[storage.BlockSize : 2*storage.BlockSize]
		h, err := storage.Header(storage.Page(leaf))
		if err != nil {
			t.Fatalf("%s: leaf header: %v", path, err)
		}
		nItems := (int(h.Lower()) - storage.SizeOfPageHeaderData) / 4
		if nItems != len(wantOIDs) {
			t.Errorf("%s: leaf items=%d, want %d", path, nItems, len(wantOIDs))
			continue
		}
		for i := 0; i < nItems; i++ {
			raw32 := binary.LittleEndian.Uint32(leaf[storage.SizeOfPageHeaderData+i*4 : storage.SizeOfPageHeaderData+i*4+4])
			off := raw32 & 0x7FFF
			length := (raw32 >> 17) & 0x7FFF
			if length != 16 {
				t.Errorf("%s: item %d length=%d, want 16", path, i, length)
				continue
			}
			// Block id at offset 0..3, posid at 4..5, oid key at 8..11.
			// BlockIdData = (bi_hi[2], bi_lo[2]) in struct order.
			gotBiHi := binary.LittleEndian.Uint16(leaf[off : off+2])
			gotBiLo := binary.LittleEndian.Uint16(leaf[off+2 : off+4])
			gotBlock := (uint32(gotBiHi) << 16) | uint32(gotBiLo)
			gotOff := binary.LittleEndian.Uint16(leaf[off+4 : off+6])
			gotOID := binary.LittleEndian.Uint32(leaf[off+8 : off+12])
			if gotOID != wantOIDs[i] {
				t.Errorf("%s: item %d OID=%d, want %d (sorted)", path, i, gotOID, wantOIDs[i])
				continue
			}
			// TID must match what bootstrapPgClassTuples reported for this OID.
			wantTID := tids[gotOID]
			if gotBlock != wantTID.Block || gotOff != wantTID.Offset {
				t.Errorf("%s: OID %d TID=(%d,%d), want (%d,%d)",
					path, gotOID, gotBlock, gotOff, wantTID.Block, wantTID.Offset)
			}
		}
	}
}


// TestPgBuildIndexTupleOidInt2KeyLayoutMatchesPG18 pins the byte-exact
// layout PG's index_form_tuple emits for a no-nulls 2-attribute tuple
// keyed on (oid, int2): 8-byte header + 4-byte attrelid + 2-byte attnum
// + 2 bytes MAXALIGN pad = 16 bytes total. t_info stores size 16.
func TestPgBuildIndexTupleOidInt2KeyLayoutMatchesPG18(t *testing.T) {
	const heapBlk uint32 = 0xDEADBEEF
	out := pgBuildIndexTupleOidInt2Key(heapBlk, 0xCAFE, 1259, 7)
	if len(out) != 16 {
		t.Fatalf("len=%d, want 16 (MAXALIGN(8+4+2))", len(out))
	}
	le := binary.LittleEndian
	// BlockIdData = (bi_hi[2], bi_lo[2]) in struct order, NOT LE uint32.
	biHi := le.Uint16(out[0:2])
	biLo := le.Uint16(out[2:4])
	if got := (uint32(biHi) << 16) | uint32(biLo); got != heapBlk {
		t.Errorf("BlockIdGetBlockNumber: got %#x, want %#x (bi_hi=%#x bi_lo=%#x)",
			got, heapBlk, biHi, biLo)
	}
	if biHi != uint16(heapBlk>>16) {
		t.Errorf("bi_hi: got %#x, want %#x", biHi, uint16(heapBlk>>16))
	}
	if biLo != uint16(heapBlk&0xFFFF) {
		t.Errorf("bi_lo: got %#x, want %#x", biLo, uint16(heapBlk&0xFFFF))
	}
	if got := le.Uint16(out[4:6]); got != 0xCAFE {
		t.Errorf("ip_posid: got %#x, want %#x", got, uint16(0xCAFE))
	}
	if got := le.Uint16(out[6:8]); got != 16 {
		t.Errorf("t_info: got %d, want 16 (size with no flags)", got)
	}
	if got := le.Uint32(out[8:12]); got != 1259 {
		t.Errorf("attrelid key: got %d, want 1259", got)
	}
	if got := int16(le.Uint16(out[12:14])); got != 7 {
		t.Errorf("attnum key: got %d, want 7", got)
	}
	// Trailing 2 bytes are MAXALIGN padding.
	for i := 14; i < 16; i++ {
		if out[i] != 0 {
			t.Errorf("pad byte %d non-zero: got %#x", i, out[i])
		}
	}
}

// TestBootstrapPgAttributeRelidAttnumIndexWritesPopulatedBtree
// end-to-ends Step 3o's output: file is 2 blocks at every on-disk
// location; the metapage points at block 1; the leaf-root carries one
// IndexTuple per (attrelid, attnum>0) pair sorted ascending by
// (attrelid, attnum); each line pointer's heap TID matches the
// pg_attribute heap row written by bootstrapPgAttributeTuples for the
// same (attrelid, attnum). Closes the FATAL "pg_attribute catalog is
// missing N attribute(s) for relation OID …".
func TestBootstrapPgAttributeRelidAttnumIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5", "global"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	tids, err := bootstrapPgAttributeTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgAttributeTuples: %v", err)
	}
	if err := bootstrapPgAttributeRelidAttnumIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgAttributeRelidAttnumIndex: %v", err)
	}

	type key struct {
		AttRelID uint32
		AttNum   int16
	}
	wantKeys := make([]key, 0, len(tids))
	for k := range tids {
		if k.AttNum <= 0 {
			continue
		}
		wantKeys = append(wantKeys, key{AttRelID: k.AttRelID, AttNum: k.AttNum})
	}
	sort.Slice(wantKeys, func(i, j int) bool {
		if wantKeys[i].AttRelID != wantKeys[j].AttRelID {
			return wantKeys[i].AttRelID < wantKeys[j].AttRelID
		}
		return wantKeys[i].AttNum < wantKeys[j].AttNum
	})

	for _, path := range []string{
		filepath.Join(dir, "base", "1", "2659"),
		filepath.Join(dir, "base", "5", "2659"),
		filepath.Join(dir, "global", "2659"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw)%storage.BlockSize != 0 || len(raw) < 2*storage.BlockSize {
			t.Errorf("%s: len=%d, want positive multiple of BlockSize ≥ 2 blocks", path, len(raw))
			continue
		}
		base := storage.SizeOfPageHeaderData
		rootBlock := binary.LittleEndian.Uint32(raw[base+8 : base+12])
		// After M0106-0010 step 3aw, pg_extension's 8 new pg_attribute rows push
		// past the 407-tuple single-leaf cap so this file is a multi-leaf
		// bulk-load: metapage at block 0, leaves at blocks 1..rootBlock-1, root
		// at rootBlock. For single-leaf inputs (≤407 tuples) the assertion
		// degenerates to rootBlock==1, one leaf, no P_HIKEY.
		nBlocks := uint32(len(raw) / storage.BlockSize)
		if rootBlock < 1 || rootBlock >= nBlocks {
			t.Errorf("%s: btm_root=%d outside [1, %d)", path, rootBlock, nBlocks)
			continue
		}
		var leafBlocks []uint32
		if rootBlock == 1 {
			leafBlocks = []uint32{1}
		} else {
			for b := uint32(1); b < rootBlock; b++ {
				leafBlocks = append(leafBlocks, b)
			}
		}
		gotItems := make([]struct {
			Block uint32
			Off   uint16
			Rel   uint32
			Num   int16
		}, 0, len(wantKeys))
		for li, lb := range leafBlocks {
			leaf := raw[lb*uint32(storage.BlockSize) : (lb+1)*uint32(storage.BlockSize)]
			h, err := storage.Header(storage.Page(leaf))
			if err != nil {
				t.Fatalf("%s: leaf %d header: %v", path, lb, err)
			}
			nItems := (int(h.Lower()) - storage.SizeOfPageHeaderData) / 4
			// Non-rightmost leaves carry P_HIKEY at item slot 1 (offset 0
			// in line-pointer array). Skip it when collecting data items.
			isRightmost := li == len(leafBlocks)-1
			start := 0
			if !isRightmost && rootBlock != 1 {
				start = 1
			}
			for i := start; i < nItems; i++ {
				raw32 := binary.LittleEndian.Uint32(leaf[storage.SizeOfPageHeaderData+i*4 : storage.SizeOfPageHeaderData+i*4+4])
				off := raw32 & 0x7FFF
				length := (raw32 >> 17) & 0x7FFF
				if length != 16 {
					t.Errorf("%s: leaf %d item %d length=%d, want 16", path, lb, i, length)
					continue
				}
				gotBiHi := binary.LittleEndian.Uint16(leaf[off : off+2])
				gotBiLo := binary.LittleEndian.Uint16(leaf[off+2 : off+4])
				gotBlock := (uint32(gotBiHi) << 16) | uint32(gotBiLo)
				gotOff := binary.LittleEndian.Uint16(leaf[off+4 : off+6])
				gotRel := binary.LittleEndian.Uint32(leaf[off+8 : off+12])
				gotNum := int16(binary.LittleEndian.Uint16(leaf[off+12 : off+14]))
				gotItems = append(gotItems, struct {
					Block uint32
					Off   uint16
					Rel   uint32
					Num   int16
				}{gotBlock, gotOff, gotRel, gotNum})
			}
		}
		if len(gotItems) != len(wantKeys) {
			t.Errorf("%s: total leaf data items=%d, want %d", path, len(gotItems), len(wantKeys))
			continue
		}
		for i, want := range wantKeys {
			g := gotItems[i]
			if g.Rel != want.AttRelID || g.Num != want.AttNum {
				t.Errorf("%s: item %d key=(%d,%d), want (%d,%d)",
					path, i, g.Rel, g.Num, want.AttRelID, want.AttNum)
				continue
			}
			wantTID := tids[pgAttrTIDKey{AttRelID: want.AttRelID, AttNum: want.AttNum}]
			if g.Block != wantTID.Block || g.Off != wantTID.Offset {
				t.Errorf("%s: (%d,%d) TID=(%d,%d), want (%d,%d)",
					path, want.AttRelID, want.AttNum, g.Block, g.Off, wantTID.Block, wantTID.Offset)
			}
		}
	}
}

// TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree end-to-ends Step 3p:
// file is 2 blocks at every disk location, block 0 is a metapage pointing at
// block 1, block 1 is a leaf-root carrying one oid-keyed IndexTuple per
// pgIndexInitialEntries() row sorted ascending by indexrelid, with the heap
// TID encoded in ItemPointer matching what bootstrapPgIndexTuples reported.
// Closes the FATAL "cache lookup failed for index 2671" blocker.
func TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5", "global"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	tids, err := bootstrapPgIndexTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgIndexTuples: %v", err)
	}
	if err := bootstrapPgIndexIndexrelidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgIndexIndexrelidIndex: %v", err)
	}

	wantOIDs := make([]uint32, 0, len(tids))
	for oid := range tids {
		wantOIDs = append(wantOIDs, oid)
	}
	sort.Slice(wantOIDs, func(i, j int) bool { return wantOIDs[i] < wantOIDs[j] })

	// Must cover every nailed index (i.e. the 2671 shared index plus all
	// of nailedLocalRels' index OIDs). If this drops, Phase 3's SHARED
	// critical-index pass FATALs immediately.
	mustHave := []uint32{2671, 2672, 2676, 2677,
		2694, // pg_auth_members_role_member_index ← Step 3z
		2695, 3593,
		827, // pg_default_acl_role_nsp_obj_index ← Step 3al
		828, // pg_default_acl_oid_index ← Step 3am
		2650, // pg_aggregate_fnoid_index ← Step 3x
		2653, // pg_amop_fam_strat_index ← Step 3y
		2660, // pg_cast_oid_index ← Step 3ab
		2661, // pg_cast_source_target_index ← Step 3ac
		2686, // pg_opclass_am_name_nsp_index ← Step 3ad
		3164, // pg_collation_name_enc_nsp_index ← Step 3ae
		3085, // pg_collation_oid_index ← Step 3af
		2668, // pg_conversion_default_index ← Step 3ah
		2669, // pg_conversion_name_nsp_index ← Step 3aj
		2670, // pg_conversion_oid_index ← Step 3ai
		3502, // pg_enum_oid_index ← Step 3ao
		3503, // pg_enum_typid_label_index ← Step 3ap
		3534, // pg_enum_typid_sortorder_index ← Step 3aq
		3467, // pg_event_trigger_evtname_index ← Step 3as
		3468, // pg_event_trigger_oid_index ← Step 3at
		3080, // pg_extension_oid_index ← Step 3ax
		3081, // pg_extension_name_index ← Step 3ay
		548,  // pg_foreign_data_wrapper_name_index ← Step 3bc
		112,  // pg_foreign_data_wrapper_oid_index ← Step 3bd
		549,  // pg_foreign_server_name_index ← Step 3bf
		113,  // pg_foreign_server_oid_index ← Step 3bg
		3119, // pg_foreign_table_relid_index ← Step 3bi
		2681, // pg_language_name_index ← Step 3bj
		2682, // pg_language_oid_index ← Step 3bk
		2689, // pg_operator_oprname_l_r_n_index ← Step 3bl
		2754, // pg_opfamily_am_name_nsp_index ← Step 3bn
		2755, // pg_opfamily_oid_index ← Step 3bo
		6246, // pg_parameter_acl_parname_index ← Step 3bq
		6247, // pg_parameter_acl_oid_index ← Step 3br
		3351, // pg_partitioned_table_partrelid_index ← Step 3bt
		6111, // pg_publication_pubname_index ← Step 3bv
		6110, // pg_publication_oid_index ← Step 3bw
		6238, // pg_publication_namespace_oid_index ← Step 3bx
		6239, // pg_publication_namespace_pnnspid_pnpubid_index ← Step 3bx
		6112, // pg_publication_rel_oid_index ← Step 3by
		6113, // pg_publication_rel_prrelid_prpubid_index ← Step 3by
		6116, // pg_publication_rel_prpubid_index ← Step 3by
		3542, // pg_range_rngtypid_index ← Step 3bz
		2228, // pg_range_rngmultitypid_index ← Step 3bz
		6001, // pg_replication_origin_roiident_index ← Step 3ca
		6002, // pg_replication_origin_roname_index ← Step 3ca
		5002, // pg_sequence_seqrelid_index ← Step 3cb
		3433, // pg_statistic_ext_data_stxoid_inh_index ← Step 3cc
		3380, // pg_statistic_ext_oid_index ← Step 3cd
		3997, // pg_statistic_ext_name_index ← Step 3cd
		3379, // pg_statistic_ext_relid_index ← Step 3cd
		2696, // pg_statistic_relid_att_inh_index ← Step 3ce
		6114, // pg_subscription_oid_index ← Step 3cf
		6115, // pg_subscription_subname_index ← Step 3cf
		6117, // pg_subscription_rel_srrelid_srsubid_index ← Step 3cg
		2697, // pg_tablespace_oid_index ← Step 3ch
		2698, // pg_tablespace_spcname_index ← Step 3ch
		3574, // pg_transform_oid_index ← Step 3ci
		3575, // pg_transform_type_lang_index ← Step 3ci
		3609, // pg_ts_config_map_index ← Step 3cj
		3608, // pg_ts_config_cfgname_index ← Step 3ck
		3712, // pg_ts_config_oid_index ← Step 3ck
		3604, // pg_ts_dict_dictname_index ← Step 3cm
		3605, // pg_ts_dict_oid_index ← Step 3cm
		3606, // pg_ts_parser_prsname_index ← Step 3cn
		3607, // pg_ts_parser_oid_index ← Step 3cn
		3766, // pg_ts_template_tmplname_index ← Step 3co
		3767, // pg_ts_template_oid_index ← Step 3co
		174,  // pg_user_mapping_oid_index ← Step 3cp
		175,  // pg_user_mapping_user_server_index ← Step 3cp
		2654, 2655, 2658, 2659, 2662, 2663, 2667, 2678, 2679, 2680,
		2684, 2685, // pg_namespace_nspname_index, pg_namespace_oid_index ← Step 3t
		2687, 2688, 2690, 2691, 2693, 2701, 2703, 2704}
	for _, w := range mustHave {
		if _, ok := tids[w]; !ok {
			t.Errorf("missing required pg_index TID for OID %d", w)
		}
	}

	for _, path := range []string{
		filepath.Join(dir, "base", "1", "2679"),
		filepath.Join(dir, "base", "5", "2679"),
		filepath.Join(dir, "global", "2679"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw) != 2*storage.BlockSize {
			t.Errorf("%s: len=%d, want %d (2 blocks)", path, len(raw), 2*storage.BlockSize)
			continue
		}
		base := storage.SizeOfPageHeaderData
		if got := binary.LittleEndian.Uint32(raw[base+8 : base+12]); got != 1 {
			t.Errorf("%s: btm_root=%d, want 1", path, got)
		}
		leaf := raw[storage.BlockSize : 2*storage.BlockSize]
		h, err := storage.Header(storage.Page(leaf))
		if err != nil {
			t.Fatalf("%s: leaf header: %v", path, err)
		}
		nItems := (int(h.Lower()) - storage.SizeOfPageHeaderData) / 4
		if nItems != len(wantOIDs) {
			t.Errorf("%s: leaf items=%d, want %d", path, nItems, len(wantOIDs))
			continue
		}
		for i := 0; i < nItems; i++ {
			raw32 := binary.LittleEndian.Uint32(leaf[storage.SizeOfPageHeaderData+i*4 : storage.SizeOfPageHeaderData+i*4+4])
			off := raw32 & 0x7FFF
			length := (raw32 >> 17) & 0x7FFF
			if length != 16 {
				t.Errorf("%s: item %d length=%d, want 16", path, i, length)
				continue
			}
			// BlockIdData = (bi_hi[2], bi_lo[2]) in struct order.
			gotBiHi := binary.LittleEndian.Uint16(leaf[off : off+2])
			gotBiLo := binary.LittleEndian.Uint16(leaf[off+2 : off+4])
			gotBlock := (uint32(gotBiHi) << 16) | uint32(gotBiLo)
			gotOff := binary.LittleEndian.Uint16(leaf[off+4 : off+6])
			gotOID := binary.LittleEndian.Uint32(leaf[off+8 : off+12])
			if gotOID != wantOIDs[i] {
				t.Errorf("%s: item %d OID=%d, want %d (sorted)", path, i, gotOID, wantOIDs[i])
				continue
			}
			wantTID := tids[gotOID]
			if gotBlock != wantTID.Block || gotOff != wantTID.Offset {
				t.Errorf("%s: OID %d TID=(%d,%d), want (%d,%d)",
					path, gotOID, gotBlock, gotOff, wantTID.Block, wantTID.Offset)
			}
		}
	}
}


// TestPgBuildBtreeBulkLoadSingleLeafByteIdenticalToLegacy pins the
// drop-in guarantee from Step 3av: for any input that fits in one leaf
// (≤ 407 fixed-size tuples), pgBuildBtreeBulkLoad emits exactly the same
// bytes as the legacy `pgBuildBtreeMetapageWithRoot(1, 0) +
// pgBuildBtreeLeafRootPage(tuples)` pair. Callers can migrate
// transparently without changing any on-disk artifacts. The byte
// equivalence is what protects every existing index seeded via the
// legacy path (pg_opclass_oid_index, pg_class_oid_index,
// pg_index_indexrelid_index, and pg_attribute_relid_attnum_index when
// sub-407) from regression when the multi-leaf path is added.
func TestPgBuildBtreeBulkLoadSingleLeafByteIdenticalToLegacy(t *testing.T) {
	cases := []struct {
		name string
		n    int
	}{
		{"empty", 0},
		{"single", 1},
		{"twelve_pgopclass", 12},
		{"max_single_leaf_407", 407},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tuples := make([][]byte, c.n)
			for i := range tuples {
				tuples[i] = pgBuildIndexTupleOidInt2Key(uint32(i+1), uint16(i+1), uint32(i+1), int16(i%32+1))
			}
			gotBulk, err := pgBuildBtreeBulkLoad(tuples, 2)
			if err != nil {
				t.Fatalf("pgBuildBtreeBulkLoad: %v", err)
			}
			leaf, err := pgBuildBtreeLeafRootPage(tuples)
			if err != nil {
				t.Fatalf("pgBuildBtreeLeafRootPage: %v", err)
			}
			meta := pgBuildBtreeMetapageWithRoot(1, 0)
			wantLegacy := append(meta, leaf...)
			if len(gotBulk) != len(wantLegacy) {
				t.Fatalf("len: bulk=%d, legacy=%d", len(gotBulk), len(wantLegacy))
			}
			for i := range gotBulk {
				if gotBulk[i] != wantLegacy[i] {
					t.Fatalf("byte %d (block=%d, off=%d): bulk=%#x, legacy=%#x",
						i, i/storage.BlockSize, i%storage.BlockSize,
						gotBulk[i], wantLegacy[i])
				}
			}
		})
	}
}

// TestPgBuildBtreeBulkLoadTwoLeafLayoutMatchesPG18 pins the on-disk
// layout for an input that overflows a single leaf — exactly the
// scenario that blocks the pg_extension nailed-rel seed
// (407 existing pg_attribute_relid_attnum_index entries + 8 from
// pg_extension = 415 tuples). Asserts each on-disk invariant PG18's
// `_bt_getroot` → `_bt_search` → `_bt_binsrch` descent relies on:
//
//   - file size = (1 metapage + N leaves + 1 root) × BlockSize
//   - metapage `btm_root = N+1`, `btm_level = 1`,
//     `btm_fastroot = N+1`, `btm_fastlevel = 1`
//   - leaf 1: `btpo_prev = P_NONE`, `btpo_next = 2`, level 0, BTP_LEAF;
//     P_HIKEY at slot 1 = copy of leaf 1's last data tuple
//   - leaf 2: `btpo_prev = 1`, `btpo_next = P_NONE`, level 0, BTP_LEAF;
//     no high key (rightmost — slid left per `_bt_slideleft`)
//   - root (block N+1): `btpo_flags = BTP_ROOT`, level 1, 2 downlinks
//     - downlink 1 = zero-attribute minus-infinity pivot (8 bytes,
//       INDEX_ALT_TID_MASK set, `ip_blkid` = 1, `ip_posid` = 0)
//     - downlink 2 = leaf 2's first key, 16 bytes, INDEX_ALT_TID_MASK
//       set, `ip_blkid` = 2, `ip_posid` = nkeyatts(2)
func TestPgBuildBtreeBulkLoadTwoLeafLayoutMatchesPG18(t *testing.T) {
	const nTuples = 500
	tuples := make([][]byte, nTuples)
	for i := range tuples {
		tuples[i] = pgBuildIndexTupleOidInt2Key(uint32(i+1), uint16(i+1), uint32(i+1), int16(i%32+1))
	}
	out, err := pgBuildBtreeBulkLoad(tuples, 2)
	if err != nil {
		t.Fatalf("pgBuildBtreeBulkLoad: %v", err)
	}
	// 500 tuples > 407 cap: 406 on leaf 1 (with P_HIKEY) + 94 on leaf 2
	// (rightmost). 1 meta + 2 leaves + 1 root = 4 blocks.
	wantBlocks := 4
	if len(out) != wantBlocks*storage.BlockSize {
		t.Fatalf("file size = %d, want %d (%d blocks)", len(out), wantBlocks*storage.BlockSize, wantBlocks)
	}

	le := binary.LittleEndian

	// ─── Metapage at block 0 ───────────────────────────────────────────
	base := storage.SizeOfPageHeaderData
	if got := le.Uint32(out[base+8 : base+12]); got != 3 {
		t.Errorf("metapage btm_root = %d, want 3 (= N leaves + 1)", got)
	}
	if got := le.Uint32(out[base+12 : base+16]); got != 1 {
		t.Errorf("metapage btm_level = %d, want 1", got)
	}
	if got := le.Uint32(out[base+16 : base+20]); got != 3 {
		t.Errorf("metapage btm_fastroot = %d, want 3", got)
	}
	if got := le.Uint32(out[base+20 : base+24]); got != 1 {
		t.Errorf("metapage btm_fastlevel = %d, want 1", got)
	}

	// ─── Leaf 1 (block 1) ──────────────────────────────────────────────
	leaf1 := out[1*storage.BlockSize : 2*storage.BlockSize]
	opaqueOff := storage.BlockSize - sizeOfBTPageOpaque
	if got := le.Uint32(leaf1[opaqueOff+0 : opaqueOff+4]); got != pNone {
		t.Errorf("leaf1 btpo_prev = %#x, want P_NONE (%#x)", got, pNone)
	}
	if got := le.Uint32(leaf1[opaqueOff+4 : opaqueOff+8]); got != 2 {
		t.Errorf("leaf1 btpo_next = %d, want 2", got)
	}
	if got := le.Uint32(leaf1[opaqueOff+8 : opaqueOff+12]); got != 0 {
		t.Errorf("leaf1 btpo_level = %d, want 0", got)
	}
	if got := le.Uint16(leaf1[opaqueOff+12 : opaqueOff+14]); got != btpLeaf {
		t.Errorf("leaf1 btpo_flags = %#x, want BTP_LEAF (%#x) only (no BTP_ROOT)", got, btpLeaf)
	}
	leaf1Header, err := storage.Header(storage.Page(leaf1))
	if err != nil {
		t.Fatalf("leaf1 header: %v", err)
	}
	leaf1Items := (int(leaf1Header.Lower()) - storage.SizeOfPageHeaderData) / 4
	// 406 data tuples + 1 P_HIKEY = 407 items.
	if leaf1Items != maxTuplesPerNonRightmostLeaf+1 {
		t.Errorf("leaf1 items = %d, want %d (P_HIKEY + %d data)",
			leaf1Items, maxTuplesPerNonRightmostLeaf+1, maxTuplesPerNonRightmostLeaf)
	}
	// P_HIKEY is item slot 1 (= the very first line pointer at offset 24).
	hikeyRaw := le.Uint32(leaf1[storage.SizeOfPageHeaderData : storage.SizeOfPageHeaderData+4])
	hikeyOff := hikeyRaw & 0x7FFF
	hikeyLen := (hikeyRaw >> 17) & 0x7FFF
	if hikeyLen != fixedIndexTupleSize {
		t.Errorf("P_HIKEY length = %d, want %d", hikeyLen, fixedIndexTupleSize)
	}
	// P_HIKEY must be a PG18 V4 heapkeyspace pivot tuple derived from the
	// FIRST data tuple of the NEXT leaf (= tuples[maxTuplesPerNonRightmostLeaf]),
	// matching PG's `_bt_truncate(lastleft, firstright)` semantics
	// (postgres/src/backend/access/nbtree/nbtutils.c:3776). The pivot has
	// INDEX_ALT_TID_MASK set in t_info and ip_posid encoding nkeyatts (2).
	// Using leaf 1's last data tuple here instead breaks forward syscache
	// lookups for that key: PG's _bt_compare (nbtsearch.c:829) treats a
	// scankey with `keysz == ntupatts && heapTid == NULL && scantid == NULL`
	// as STRICTLY GREATER than the pivot and steps right past the matching
	// leaf — manifesting as `cache lookup failed for attribute N of
	// relation R` for any key that happened to land at a leaf boundary.
	srcHikey := tuples[maxTuplesPerNonRightmostLeaf]
	gotHikey := leaf1[hikeyOff : hikeyOff+uint32(fixedIndexTupleSize)]
	// Key payload (bytes [8..]) carries through unchanged from the source data
	// tuple — the pivot transform only touches ip_posid (4..6) and t_info
	// (6..8) so PG's `_bt_compare` finds the same key bytes either way.
	for i := 8; i < fixedIndexTupleSize; i++ {
		if gotHikey[i] != srcHikey[i] {
			t.Errorf("P_HIKEY key byte %d: got %#x, want %#x (preserved from src)",
				i, gotHikey[i], srcHikey[i])
		}
	}
	// ip_posid must encode nkeyatts (2) in the low 12 bits with no status
	// bits set (no BT_IS_POSTING, no BT_PIVOT_HEAP_TID_ATTR). Status bits in
	// the high 4 bits force `BTreeTupleIsPosting` true or change pivot-tuple
	// natts encoding, both of which break `_bt_check_natts`.
	if got := le.Uint16(gotHikey[4:6]); got != 2 {
		t.Errorf("P_HIKEY ip_posid = %#x, want 2 (nkeyatts; no status bits)", got)
	}
	// t_info must have INDEX_ALT_TID_MASK set (required for `BTreeTupleIsPivot`
	// to return true) while preserving the original 16-byte size in the low
	// 13 bits.
	hikeyTInfo := le.Uint16(gotHikey[6:8])
	if hikeyTInfo&indexAltTIDMask == 0 {
		t.Errorf("P_HIKEY t_info = %#x, want INDEX_ALT_TID_MASK (%#x) set", hikeyTInfo, indexAltTIDMask)
	}
	if got := hikeyTInfo & indexSizeMask; got != fixedIndexTupleSize {
		t.Errorf("P_HIKEY t_info size bits = %d, want %d (preserved across pivot transform)",
			got, fixedIndexTupleSize)
	}

	// ─── Leaf 2 (block 2, rightmost) ───────────────────────────────────
	leaf2 := out[2*storage.BlockSize : 3*storage.BlockSize]
	if got := le.Uint32(leaf2[opaqueOff+0 : opaqueOff+4]); got != 1 {
		t.Errorf("leaf2 btpo_prev = %d, want 1", got)
	}
	if got := le.Uint32(leaf2[opaqueOff+4 : opaqueOff+8]); got != pNone {
		t.Errorf("leaf2 btpo_next = %#x, want P_NONE (%#x)", got, pNone)
	}
	if got := le.Uint16(leaf2[opaqueOff+12 : opaqueOff+14]); got != btpLeaf {
		t.Errorf("leaf2 btpo_flags = %#x, want BTP_LEAF (%#x)", got, btpLeaf)
	}
	leaf2Header, err := storage.Header(storage.Page(leaf2))
	if err != nil {
		t.Fatalf("leaf2 header: %v", err)
	}
	leaf2Items := (int(leaf2Header.Lower()) - storage.SizeOfPageHeaderData) / 4
	// 500 − 406 = 94 data tuples, no high key.
	if leaf2Items != nTuples-maxTuplesPerNonRightmostLeaf {
		t.Errorf("leaf2 items = %d, want %d (rightmost, no P_HIKEY)",
			leaf2Items, nTuples-maxTuplesPerNonRightmostLeaf)
	}
	// First data tuple on leaf 2 (slot 1) must equal tuples[406].
	leaf2FirstRaw := le.Uint32(leaf2[storage.SizeOfPageHeaderData : storage.SizeOfPageHeaderData+4])
	leaf2FirstOff := leaf2FirstRaw & 0x7FFF
	leaf2First := leaf2[leaf2FirstOff : leaf2FirstOff+uint32(fixedIndexTupleSize)]
	wantLeaf2First := tuples[maxTuplesPerNonRightmostLeaf]
	for i := range wantLeaf2First {
		if leaf2First[i] != wantLeaf2First[i] {
			t.Errorf("leaf2 first data tuple byte %d: got %#x, want %#x",
				i, leaf2First[i], wantLeaf2First[i])
		}
	}

	// ─── Root (block 3) ────────────────────────────────────────────────
	root := out[3*storage.BlockSize : 4*storage.BlockSize]
	if got := le.Uint32(root[opaqueOff+0 : opaqueOff+4]); got != pNone {
		t.Errorf("root btpo_prev = %#x, want P_NONE", got)
	}
	if got := le.Uint32(root[opaqueOff+4 : opaqueOff+8]); got != pNone {
		t.Errorf("root btpo_next = %#x, want P_NONE", got)
	}
	if got := le.Uint32(root[opaqueOff+8 : opaqueOff+12]); got != 1 {
		t.Errorf("root btpo_level = %d, want 1", got)
	}
	if got := le.Uint16(root[opaqueOff+12 : opaqueOff+14]); got != btpRoot {
		t.Errorf("root btpo_flags = %#x, want BTP_ROOT (%#x) only", got, btpRoot)
	}
	rootHeader, err := storage.Header(storage.Page(root))
	if err != nil {
		t.Fatalf("root header: %v", err)
	}
	rootItems := (int(rootHeader.Lower()) - storage.SizeOfPageHeaderData) / 4
	if rootItems != 2 {
		t.Fatalf("root items = %d, want 2 (one downlink per leaf)", rootItems)
	}

	// Root downlink 1 (slot 1): minus-infinity pivot = 8-byte
	// IndexTupleData with INDEX_ALT_TID_MASK set, ip_blkid=1, ip_posid=0.
	dl1Raw := le.Uint32(root[storage.SizeOfPageHeaderData : storage.SizeOfPageHeaderData+4])
	dl1Off := dl1Raw & 0x7FFF
	dl1Len := (dl1Raw >> 17) & 0x7FFF
	if dl1Len != sizeOfIndexTupleData {
		t.Errorf("root downlink 1 length = %d, want %d (minus-infinity)", dl1Len, sizeOfIndexTupleData)
	}
	dl1 := root[dl1Off : dl1Off+uint32(sizeOfIndexTupleData)]
	dl1BiHi := le.Uint16(dl1[0:2])
	dl1BiLo := le.Uint16(dl1[2:4])
	if got := (uint32(dl1BiHi) << 16) | uint32(dl1BiLo); got != 1 {
		t.Errorf("root downlink 1 ip_blkid = %d, want 1 (leaf 1)", got)
	}
	if got := le.Uint16(dl1[4:6]); got != 0 {
		t.Errorf("root downlink 1 ip_posid = %d, want 0 (zero key attrs)", got)
	}
	if got := le.Uint16(dl1[6:8]); got&indexAltTIDMask == 0 {
		t.Errorf("root downlink 1 t_info = %#x, want INDEX_ALT_TID_MASK (%#x) set", got, indexAltTIDMask)
	}
	if got := le.Uint16(dl1[6:8]) & indexSizeMask; got != sizeOfIndexTupleData {
		t.Errorf("root downlink 1 size bits = %d, want %d", got, sizeOfIndexTupleData)
	}

	// Root downlink 2 (slot 2): copy of leaf 2's first key with
	// INDEX_ALT_TID_MASK set, ip_blkid=2, ip_posid=nkeyatts(2).
	dl2Raw := le.Uint32(root[storage.SizeOfPageHeaderData+4 : storage.SizeOfPageHeaderData+8])
	dl2Off := dl2Raw & 0x7FFF
	dl2Len := (dl2Raw >> 17) & 0x7FFF
	if dl2Len != fixedIndexTupleSize {
		t.Errorf("root downlink 2 length = %d, want %d (full tuple copy)", dl2Len, fixedIndexTupleSize)
	}
	dl2 := root[dl2Off : dl2Off+uint32(fixedIndexTupleSize)]
	dl2BiHi := le.Uint16(dl2[0:2])
	dl2BiLo := le.Uint16(dl2[2:4])
	if got := (uint32(dl2BiHi) << 16) | uint32(dl2BiLo); got != 2 {
		t.Errorf("root downlink 2 ip_blkid = %d, want 2 (leaf 2)", got)
	}
	if got := le.Uint16(dl2[4:6]); got != 2 {
		t.Errorf("root downlink 2 ip_posid = %d, want 2 (nkeyatts)", got)
	}
	if got := le.Uint16(dl2[6:8]); got&indexAltTIDMask == 0 {
		t.Errorf("root downlink 2 t_info = %#x, want INDEX_ALT_TID_MASK set", got)
	}
	// Key payload at offsets [8..14] must match leaf 2's first tuple
	// payload (ip_blkid/posid differ — already verified — so compare
	// the key bytes only).
	for i := 8; i < 16; i++ {
		if dl2[i] != wantLeaf2First[i] {
			t.Errorf("root downlink 2 key byte %d: got %#x, want %#x", i, dl2[i], wantLeaf2First[i])
		}
	}
}


// TestPgBuildBtreeLeafHighKeyMatchesPGPivotEncoding pins the exact byte
// layout `pgBuildBtreeLeafHighKey` produces against a known data tuple,
// guarding the invariants that PG's `_bt_check_natts` (nbtutils.c:4163)
// requires for V4 heapkeyspace leaf P_HIKEY entries:
//
//   - INDEX_ALT_TID_MASK bit set in t_info (BTreeTupleIsPivot == true)
//   - ip_posid == nkeyatts with no high-4-bit status bits
//     (BT_IS_POSTING clear → not a posting list, BT_PIVOT_HEAP_TID_ATTR clear
//     → no heap-TID tiebreaker)
//   - Key payload [8..] preserved byte-for-byte from the source data tuple
//   - Tuple size bits in the low 13 of t_info preserved
//   - Source tuple's other bytes (ip_blkid) untouched — pivots ignore them
//
// Without these, every PG backend SIGABRTs at nbtsearch.c:707 the first
// time `_bt_compare` walks a multi-leaf index — see Step 3az's design doc
// (docs/design/0106-0010-step3az-multi-leaf-btree-leaf-hikey-pivot.md).
func TestPgBuildBtreeLeafHighKeyMatchesPGPivotEncoding(t *testing.T) {
	// Use a composite-key data tuple (the most common multi-leaf user is
	// pg_attribute_relid_attnum_index, nkeyatts=2). Source heapBlk/heapOff
	// must be non-zero so we can verify they're treated as don't-care bytes
	// by the pivot transform (they sit in ip_blkid which pivots ignore).
	src := pgBuildIndexTupleOidInt2Key(0x4242 /* heapBlk */, 0xBEEF /* heapOff */, 12345 /* oid */, 7 /* int2 */)
	hi := pgBuildBtreeLeafHighKey(src, 2)

	if len(hi) != len(src) {
		t.Fatalf("output size = %d, want %d (size must be preserved)", len(hi), len(src))
	}

	le := binary.LittleEndian
	// ip_posid: low 12 bits = nkeyatts, no high status bits.
	if got := le.Uint16(hi[4:6]); got != 2 {
		t.Errorf("ip_posid = %#x, want 2 (nkeyatts, no status bits)", got)
	}
	// t_info: INDEX_ALT_TID_MASK set, size bits preserved.
	tinfo := le.Uint16(hi[6:8])
	if tinfo&indexAltTIDMask == 0 {
		t.Errorf("t_info = %#x, INDEX_ALT_TID_MASK not set", tinfo)
	}
	if got := tinfo & indexSizeMask; got != fixedIndexTupleSize {
		t.Errorf("t_info size bits = %d, want %d", got, fixedIndexTupleSize)
	}
	// Status bits (high 4 bits of ip_posid) must be zero to keep
	// BTreeTupleIsPosting false and BTreeTupleGetHeapTID nil.
	const btStatusOffsetMask uint16 = 0xF000
	if got := le.Uint16(hi[4:6]) & btStatusOffsetMask; got != 0 {
		t.Errorf("ip_posid status bits = %#x, want 0 (no BT_IS_POSTING, no BT_PIVOT_HEAP_TID_ATTR)", got)
	}
	// Key payload (bytes [8..]) preserved verbatim from the source.
	for i := 8; i < len(src); i++ {
		if hi[i] != src[i] {
			t.Errorf("key byte %d: got %#x, want %#x", i, hi[i], src[i])
		}
	}

	// Verify the source tuple itself is unmodified — `pgBuildBtreeLeafHighKey`
	// must allocate a new buffer (the caller passes a tuple that is also
	// stored on the same leaf as a data tuple; mutating it would corrupt the
	// leaf's data area).
	srcRebuilt := pgBuildIndexTupleOidInt2Key(0x4242, 0xBEEF, 12345, 7)
	for i := range src {
		if src[i] != srcRebuilt[i] {
			t.Fatalf("source mutated at byte %d: src=%#x, rebuilt=%#x", i, src[i], srcRebuilt[i])
		}
	}
}
