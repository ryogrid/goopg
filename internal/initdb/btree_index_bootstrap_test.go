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
		if nItems != len(wantKeys) {
			t.Errorf("%s: leaf items=%d, want %d", path, nItems, len(wantKeys))
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
			gotAttRelID := binary.LittleEndian.Uint32(leaf[off+8 : off+12])
			gotAttNum := int16(binary.LittleEndian.Uint16(leaf[off+12 : off+14]))
			want := wantKeys[i]
			if gotAttRelID != want.AttRelID || gotAttNum != want.AttNum {
				t.Errorf("%s: item %d key=(%d,%d), want (%d,%d)",
					path, i, gotAttRelID, gotAttNum, want.AttRelID, want.AttNum)
				continue
			}
			wantTID := tids[pgAttrTIDKey{AttRelID: want.AttRelID, AttNum: want.AttNum}]
			if gotBlock != wantTID.Block || gotOff != wantTID.Offset {
				t.Errorf("%s: (%d,%d) TID=(%d,%d), want (%d,%d)",
					path, want.AttRelID, want.AttNum, gotBlock, gotOff, wantTID.Block, wantTID.Offset)
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
