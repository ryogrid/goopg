// Step 3l: populate PG-conformant btree index tuples for the most critical
// nailed indexes so vanilla PG's RelationInitIndexAccessInfo →
// LookupOpclassInfo path (postgres/src/backend/utils/cache/relcache.c:1766)
// can resolve opclass rows by OID. The empty btree files produced by Step 3k
// (BTREE_MAGIC metapage, btm_root=P_NONE) returned zero rows for every
// catalog lookup, FATALing the standby boot with
//   FATAL: could not find tuple for opclass 1986
//
// This file adds builders for a PG-conformant 2-page btree file (metapage at
// block 0 + leaf-root at block 1) with single-column oid-keyed index tuples
// that point at the heap-row TIDs produced earlier in the bootstrap.

package initdb

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/goopg/goopg/internal/storage"
)

const (
	// PG btree page opaque flag bits — postgres/src/include/access/nbtree.h.
	btpLeaf uint16 = 1 << 0
	btpRoot uint16 = 1 << 1
	// btpMeta is declared in initdb.go via the const block in makeBtreeRootPage;
	// re-declared here to keep this file independently parseable.
	btpMetaFlag uint16 = 1 << 3

	// IndexTupleData size and t_info masks
	// postgres/src/include/access/itup.h.
	sizeOfIndexTupleData = 8
	indexSizeMask        = 0x1FFF

	// BTREE on-disk magic / version (also referenced by makeBtreeRootPage).
	btreeMagicConst   uint32 = 0x053162
	btreeVersionConst uint32 = 4

	// Size of the special area at the end of every btree page.
	sizeOfBTPageOpaque = 16
)

// pgBuildIndexTupleOidKey constructs an 8+4-byte=12-byte IndexTuple,
// MAXALIGN'd to 16 bytes, for a single-column oid-keyed index. The
// returned buffer matches PG's index_form_tuple output for a tuple
// with no nulls and one 4-byte oid key. Layout:
//
//	[0..3]   ItemPointerData.ip_blkid  (heapBlk, LE uint32)
//	[4..5]   ItemPointerData.ip_posid  (heapOff, LE uint16)
//	[6..7]   t_info  (size_low_13_bits | flags=0)
//	[8..11]  oid key data              (oid, LE uint32)
//	[12..15] MAXALIGN padding (zero)
//
// PG's _bt_search calls oidcmp() over the [8..11] window; the trailing
// MAXALIGN pad never participates in comparisons, only in tuple sizing.
func pgBuildIndexTupleOidKey(heapBlk uint32, heapOff uint16, oid uint32) []byte {
	// hoff (data offset) for no-nulls case = MAXALIGN(sizeof(IndexTupleData))
	// = MAXALIGN(8) = 8 on a 64-bit MAXALIGN target. data_size = 4 (oid).
	// size = MAXALIGN(hoff + data_size) = MAXALIGN(12) = 16. The t_info
	// size field stores 16 (no flags set because no nulls and no varlena).
	const (
		hoff     = 8
		dataSize = 4
		size     = 16 // MAXALIGN(hoff + dataSize)
	)
	out := make([]byte, size)
	le := binary.LittleEndian

	// ItemPointerData: 4-byte block id (LE uint32) + 2-byte item offset
	// (LE uint16). PG stores ip_blkid as two uint16 halves (bi_hi, bi_lo)
	// but the on-disk LE encoding is byte-identical to a LE uint32.
	le.PutUint32(out[0:4], heapBlk)
	le.PutUint16(out[4:6], heapOff)

	// t_info: lower 13 bits = size; INDEX_VAR_MASK / INDEX_NULL_MASK both 0.
	le.PutUint16(out[6:8], uint16(size)&indexSizeMask)

	// Key data starts at hoff.
	le.PutUint32(out[hoff:hoff+dataSize], oid)
	// Bytes [12..15] are MAXALIGN padding — already zeroed.
	return out
}

// pgBuildBtreeLeafRootPage assembles an 8192-byte btree leaf-root page
// containing tuples in sorted (caller-provided) order. The caller MUST
// sort the tuples by their key before invoking this — PG's _bt_binsrch
// requires monotonic key ordering across line pointers.
//
// Page layout (postgres/src/include/access/nbtree.h
//
//	+ postgres/src/include/storage/bufpage.h):
//	0..23     PageHeaderData
//	24..      ItemId line pointers (4 bytes each, growing forward)
//	          ... free space ...
//	          IndexTuples (growing backward from upper bound)
//	8176..8191 BTPageOpaqueData (16 bytes)
//	          btpo_prev=0  btpo_next=0  btpo_level=0
//	          btpo_flags=BTP_LEAF|BTP_ROOT  btpo_cycleid=0
//
// On a leaf root there is NO high key, so item indexes start at 1 and
// every entry is a real data tuple.
func pgBuildBtreeLeafRootPage(sortedTuples [][]byte) ([]byte, error) {
	page := make([]byte, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}
	h := storage.MustHeader(storage.Page(page))
	h.SetSpecial(uint16(storage.BlockSize - sizeOfBTPageOpaque))
	// Upper grows down from the special area as tuples are added.
	upper := storage.BlockSize - sizeOfBTPageOpaque
	lower := storage.SizeOfPageHeaderData
	for i, t := range sortedTuples {
		if len(t)%8 != 0 {
			return nil, fmt.Errorf("tuple %d not MAXALIGN'd: len=%d", i, len(t))
		}
		newUpper := upper - len(t)
		needed := 4 // one ItemId line pointer
		if newUpper-lower < needed {
			return nil, fmt.Errorf("btree leaf overflow inserting tuple %d", i)
		}
		copy(page[newUpper:upper], t)
		item := storage.ItemID{
			Offset: uint16(newUpper),
			Flags:  storage.ItemIDNormal,
			Length: uint16(len(t)),
		}
		// writeItemID isn't exported; emulate by packing manually using the
		// same bit layout (Offset:15 + Flags:2 + Length:15) — see
		// internal/storage/heap.go::ItemID.pack.
		raw := uint32(item.Offset&0x7FFF) |
			(uint32(uint8(item.Flags)&0x3) << 15) |
			(uint32(item.Length&0x7FFF) << 17)
		binary.LittleEndian.PutUint32(page[lower:lower+4], raw)
		lower += 4
		upper = newUpper
	}
	h.SetLower(uint16(lower))
	h.SetUpper(uint16(upper))

	// BTPageOpaqueData at end of page: btpo_flags = BTP_LEAF | BTP_ROOT.
	off := storage.BlockSize - sizeOfBTPageOpaque
	binary.LittleEndian.PutUint16(page[off+12:off+14], btpLeaf|btpRoot)
	return page, nil
}

// pgBuildBtreeMetapageWithRoot mirrors makeBtreeRootPage but takes a
// non-empty (btm_root, btm_level) so PG's _bt_getroot returns the
// caller-supplied leaf-root block instead of NULL. Use this for indexes
// that have at least one tuple; makeBtreeRootPage stays for empty indexes.
func pgBuildBtreeMetapageWithRoot(rootBlk uint32, level uint32) []byte {
	const (
		sizeofBTMetaPageData = 48
	)
	page := make([]byte, storage.BlockSize)
	h := storage.MustHeader(storage.Page(page))
	h.SetLower(uint16(storage.SizeOfPageHeaderData + sizeofBTMetaPageData))
	h.SetUpper(uint16(storage.BlockSize - sizeOfBTPageOpaque))
	h.SetSpecial(uint16(storage.BlockSize - sizeOfBTPageOpaque))
	h.SetPagesizeVersion(storage.BlockSize | 4)

	le := binary.LittleEndian
	base := storage.SizeOfPageHeaderData
	le.PutUint32(page[base+0:base+4], btreeMagicConst)
	le.PutUint32(page[base+4:base+8], btreeVersionConst)
	le.PutUint32(page[base+8:base+12], rootBlk)         // btm_root
	le.PutUint32(page[base+12:base+16], level)          // btm_level
	le.PutUint32(page[base+16:base+20], rootBlk)        // btm_fastroot
	le.PutUint32(page[base+20:base+24], level)          // btm_fastlevel
	le.PutUint64(page[base+32:base+40], math.Float64bits(-1.0)) // btm_last_cleanup_num_heap_tuples
	// btm_allequalimage at base+40 stays false; trailing pad zero.

	// BTPageOpaqueData: only btpo_flags = BTP_META.
	off := storage.BlockSize - sizeOfBTPageOpaque
	le.PutUint16(page[off+12:off+14], btpMetaFlag)
	return page
}

// bootstrapPgOpclassOidIndex overwrites the empty btree placeholders at
// base/{1,5}/2687 and global/2687 with a 2-block btree file (metapage +
// populated leaf-root) carrying one IndexTuple per pg_opclass row, keyed
// on opclass OID. Closes the FATAL "could not find tuple for opclass 1986"
// blocker that surfaced after Step 3k.
//
// Heap-row TIDs are computed from pgOpclassInitialEntries' insertion
// order — `bootstrapPgOpclassTuples` writes the rows via
// `writeMultiPageHeapRows` which packs them onto block 0 in order, so
// row index i (0-based) lands at TID (block=0, offset=i+1). All 12 PG18
// pinned opclass rows fit on one page (each ~120 bytes vs 8160-byte payload).
//
// Index tuples are sorted by OID before page assembly so PG's _bt_binsrch
// finds them via the standard ordered search.
func bootstrapPgOpclassOidIndex(dataDir string) error {
	type oidTid struct {
		oid uint32
		tid uint16 // 1-based heap offset; block is always 0 for the 12-row pg_opclass seed
	}
	entries := pgOpclassInitialEntries()
	pairs := make([]oidTid, len(entries))
	for i, e := range entries {
		pairs[i] = oidTid{oid: e.OID, tid: uint16(i + 1)}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].oid < pairs[j].oid })

	tuples := make([][]byte, len(pairs))
	for i, p := range pairs {
		tuples[i] = pgBuildIndexTupleOidKey(0 /* heap block */, p.tid, p.oid)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_opclass_oid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
		filepath.Join(dataDir, "global"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2687, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_opclass_oid_index in %s: %w", dir, err)
		}
	}
	return nil
}

// bootstrapPgClassOidIndex overwrites the empty btree placeholders at
// base/{1,5}/2662 and global/2662 with a 2-block btree file (metapage +
// populated leaf-root) carrying one IndexTuple per pg_class heap row,
// keyed on relation OID. Closes the PANIC "could not open critical system
// index 2671" blocker that surfaced after Step 3l.
//
// Why this is needed: once the 7 local critical indexes finish loading,
// `criticalRelcachesBuilt` flips to true; the immediately-following pass
// over the 6 SHARED critical indexes resolves each shared index's pg_class
// row via `ScanPgRelation(oid, indexOK=true)`, which now switches from the
// sequential-scan fallback to an index lookup against pg_class_oid_index
// (OID 2662). The empty placeholder produced by Step 3k returned zero rows
// for every shared-index lookup, FATALing the standby with
//   PANIC: could not open critical system index 2671
//
// Heap-row TIDs are taken verbatim from `bootstrapPgClassTuples`'s return
// value — `writeMultiPageHeap` packs nailed rels in iteration order across
// however many 8 KiB pages are required and reports the actual (block,
// offset) each row landed at. Index tuples are then sorted by OID before
// leaf-page assembly so PG's `_bt_binsrch` over `oidcmp` finds them via
// the standard ordered search.
func bootstrapPgClassOidIndex(dataDir string, tids map[uint32]heapTID) error {
	type oidEntry struct {
		oid   uint32
		block uint32
		off   uint16
	}
	entries := make([]oidEntry, 0, len(tids))
	for oid, t := range tids {
		entries = append(entries, oidEntry{oid: oid, block: t.Block, off: t.Offset})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].oid < entries[j].oid })

	tuples := make([][]byte, len(entries))
	for i, e := range entries {
		tuples[i] = pgBuildIndexTupleOidKey(e.block, e.off, e.oid)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_class_oid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
		filepath.Join(dataDir, "global"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2662, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_class_oid_index in %s: %w", dir, err)
		}
	}
	return nil
}


// pgBuildIndexTupleOidInt2Key constructs an IndexTuple for the
// (oid attrelid, int2 attnum) composite key used by
// pg_attribute_relid_attnum_index (OID 2659). Mirrors the byte layout
// emitted by PG's `index_form_tuple` for a no-nulls 2-attribute tuple
// where att1.align='i' (oid) and att2.align='s' (int2):
//
//	[0..3]   ItemPointerData.ip_blkid  (heapBlk, LE uint32)
//	[4..5]   ItemPointerData.ip_posid  (heapOff, LE uint16)
//	[6..7]   t_info (size_low_13_bits | flags=0)
//	[8..11]  attrelid (LE uint32, oid_ops compares as unsigned)
//	[12..13] attnum   (LE int16,  int2_ops compares as signed)
//	[14..15] MAXALIGN padding (zero)
//
// Total size = MAXALIGN(IndexTupleHeader + att1.len + att2.len) =
// MAXALIGN(8 + 4 + 2) = 16. The on-disk size stored in t_info's low
// 13 bits is the MAXALIGN'd total (16) so PG `IndexTupleSize` matches
// `len(out)`.
func pgBuildIndexTupleOidInt2Key(heapBlk uint32, heapOff uint16, attrelid uint32, attnum int16) []byte {
	const (
		hoff = 8
		size = 16 // MAXALIGN(hoff + 4 + 2) = MAXALIGN(14) = 16
	)
	out := make([]byte, size)
	le := binary.LittleEndian

	// ItemPointerData.
	le.PutUint32(out[0:4], heapBlk)
	le.PutUint16(out[4:6], heapOff)

	// t_info: lower 13 bits = size; no INDEX_VAR_MASK / INDEX_NULL_MASK.
	le.PutUint16(out[6:8], uint16(size)&indexSizeMask)

	// Key data.
	le.PutUint32(out[hoff:hoff+4], attrelid)
	le.PutUint16(out[hoff+4:hoff+6], uint16(attnum))
	// Bytes [14..15] are MAXALIGN padding — already zero.
	return out
}

// bootstrapPgAttributeRelidAttnumIndex overwrites the empty btree
// placeholders at base/{1,5}/2659 + global/2659 with a 2-block btree file
// (metapage + populated leaf-root) carrying one IndexTuple per pg_attribute
// heap row, keyed on (attrelid, attnum). Closes the FATAL
//
//	pg_attribute catalog is missing N attribute(s) for relation OID …
//
// that surfaces during PG-standby boot once `criticalRelcachesBuilt = true`,
// because `RelationBuildTupleDesc` then drives column lookups through
// `systable_beginscan(AttributeRelidNumIndexId, …)` instead of falling back
// to a sequential pg_attribute heap scan. The empty placeholder produced
// by Step 3k returned zero rows for every (attrelid, attnum>0) probe.
//
// Btree leaves require monotonic key ordering across line pointers (PG
// `_bt_binsrch`). The composite comparator is lexicographic over
// (attrelid, attnum) — both columns ascending — matching the
// `oid_ops, int2_ops` opclass tuple in `pgIndexInitialEntries`.
//
// Only attnum > 0 rows are produced because the bootstrap pg_attribute
// heap currently seeds only user/catalog columns (no system attributes
// such as ctid / xmin), and PG probes attnum > 0 in this code path;
// system columns are resolved from a hardcoded table in
// `SystemAttributeDefinition`.
func bootstrapPgAttributeRelidAttnumIndex(dataDir string, tids map[pgAttrTIDKey]heapTID) error {
	type entry struct {
		attrelid uint32
		attnum   int16
		block    uint32
		off      uint16
	}
	entries := make([]entry, 0, len(tids))
	for k, t := range tids {
		if k.AttNum <= 0 {
			continue
		}
		entries = append(entries, entry{
			attrelid: k.AttRelID,
			attnum:   k.AttNum,
			block:    t.Block,
			off:      t.Offset,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].attrelid != entries[j].attrelid {
			return entries[i].attrelid < entries[j].attrelid
		}
		return entries[i].attnum < entries[j].attnum
	})

	tuples := make([][]byte, len(entries))
	for i, e := range entries {
		tuples[i] = pgBuildIndexTupleOidInt2Key(e.block, e.off, e.attrelid, e.attnum)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_attribute_relid_attnum_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
		filepath.Join(dataDir, "global"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2659, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_attribute_relid_attnum_index in %s: %w", dir, err)
		}
	}
	return nil
}

// bootstrapPgIndexIndexrelidIndex overwrites the empty btree placeholders
// at base/{1,5}/2679 and global/2679 with a 2-block btree file (metapage +
// populated leaf-root) carrying one IndexTuple per Form_pg_index heap row,
// keyed on `indexrelid`. Closes the FATAL "cache lookup failed for index
// 2671" blocker that surfaced after Step 3o.
//
// Why this is needed: after the seven local critical indexes finish loading,
// `criticalRelcachesBuilt` flips to true; the immediately-following pass
// over the six SHARED critical indexes invokes
// `RelationInitIndexAccessInfo(relation)` → `SearchSysCache1(INDEXRELID,
// RelationGetRelid(relation))` (postgres/src/backend/utils/cache/relcache.c:1467,
// :2339) to materialise each index's Form_pg_index. The catcache miss falls
// back to a sysscan against `pg_index_indexrelid_index` (PG18 OID = 2679 —
// `IndexRelidIndexId` in `postgres/src/include/catalog/pg_index_d.h`; Step
// 3q originally targeted 2678 in error, Step 3r restores the correct OID).
// The Step-3k empty btree placeholder returned zero rows for every probe,
// so the very first shared index — `pg_database_datname_index` (2671) —
// FATAL'd with `cache lookup failed for index 2671`.
//
// Index tuples are sorted by `indexrelid` before page assembly so PG's
// `_bt_binsrch` finds them via the standard ordered search.
func bootstrapPgIndexIndexrelidIndex(dataDir string, tids map[uint32]heapTID) error {
	type entry struct {
		oid   uint32
		block uint32
		off   uint16
	}
	entries := make([]entry, 0, len(tids))
	for oid, t := range tids {
		entries = append(entries, entry{oid: oid, block: t.Block, off: t.Offset})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].oid < entries[j].oid })

	tuples := make([][]byte, len(entries))
	for i, e := range entries {
		tuples[i] = pgBuildIndexTupleOidKey(e.block, e.off, e.oid)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_index_indexrelid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
		filepath.Join(dataDir, "global"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2679, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_index_indexrelid_index in %s: %w", dir, err)
		}
	}
	return nil
}
