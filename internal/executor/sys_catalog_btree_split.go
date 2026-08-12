package executor

// Runtime leaf-root split for the PG18-canonical system btrees touched by
// user CREATE TABLE (M0106-0010 batched-36 loop 10, a.k.a. batched-39).
//
// Background: M0106-0010 batched-36 loop 9 (batched-38) added IndexTuple
// inserts into pg_class_oid_index (2662), pg_class_relname_nsp_index (2663),
// and pg_attribute_relid_attnum_index (2659) but the bootstrap fills 2663 to
// within a few bytes of the leaf-root page budget so runtime inserts overflow
// and were silently skipped. PG18 standby parse-analyze of
// `SELECT count(*) FROM public.bench_log` then raised 42P01 because the
// runtime entries never landed on the index.
//
// This file implements the split path: when `PageInsertItemRawAt` returns
// `ErrNoSpaceInPage` at block 1 (the bootstrap leaf-root), split the page
// into two leaves (block 1 = left half, freshly-extended block = right half)
// and promote a new internal root (another freshly-extended block) above
// them. Update the metapage at block 0 to point at the new root with
// `btm_level=1`.
//
// The page-build helpers below duplicate `internal/initdb/btree_index_bootstrap.go`
// — duplicated because `executor` cannot import `initdb` (cycle).

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/goopg/goopg/internal/storage"
)

// PG18 btree on-disk constants (mirrors initdb/btree_index_bootstrap.go).
const (
	btpLeafFlag         uint16 = 1 << 0
	btpRootFlag         uint16 = 1 << 1
	btpMetaFlag         uint16 = 1 << 3
	sizeOfBTPageOpaque         = 16
	sizeOfIndexTupleHdr        = 8
	indexAltTIDMask     uint16 = 0x2000
	btOffsetMask        uint16 = 0x0FFF
	indexSizeMask16     uint16 = 0x1FFF
	pNoneBlock          uint32 = 0
	btreeMagicConst     uint32 = 0x053162
	btreeVersionConst   uint32 = 4
)

// btreeIndexKeyMeta describes the on-disk key layout of a system btree
// covered by the runtime split path.
type btreeIndexKeyMeta struct {
	tupleSize int    // total IndexTuple size in bytes (e.g. 16 or 80); 0 when variable
	nkeyatts  uint16 // number of key attributes (1 for oid, 2 for name+oid or oid+int2)
	variable  bool   // true when tuples are variable-length (varlena key column,
	// e.g. pg_proc_proname_args_nsp_index's oidvector) — the rebuild
	// fallback then uses the variable-size bulk layout instead of the
	// fixed-tupleSize one
}

// keyMetaForSysBtree returns the on-disk layout of the supported runtime-
// inserted system btrees. Adding a new index to the runtime insert path
// requires registering its layout here.
func keyMetaForSysBtree(indexOID uint32) (btreeIndexKeyMeta, bool) {
	switch indexOID {
	case pgClassOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgClassRelnameNspIndexOID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 2}, true
	case pgAttributeRelidAttnumIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 2}, true
	case pgNamespaceNspnameIndexOID:
		return btreeIndexKeyMeta{tupleSize: 72, nkeyatts: 1}, true
	case pgNamespaceOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	// B2-prep: pg_proc's two bootstrap indexes are multi-level (3397
	// entries) so runtime maintenance rides the descent path; 2691 is the
	// first variable-length key (proargtypes oidvector).
	case pgProcOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgProcPronameArgsNspIndexOID:
		return btreeIndexKeyMeta{nkeyatts: 3, variable: true}, true
	// B2.1a: pg_type — 2703 bootstraps as a populated leaf-root, 2704 as an
	// empty metapage-only placeholder that the runtime lazily roots.
	case pgTypeOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgTypeTypnameNspIndexOID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 2}, true
	// B2.1d: pg_enum — all three bootstrap as empty metapage-only
	// placeholders (no builtin enums); the runtime lazily roots them.
	case pgEnumOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgEnumTypidLabelIndexOID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 2}, true
	case pgEnumTypidSortOrderIndexID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 2}, true
	// B2.1c: pg_range — both bootstrap as populated leaf-roots (6 builtin
	// range rows each).
	case pgRangeRngtypidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgRangeRngmultitypidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	// B2.2a: pg_cast — both bootstrap-populated leaf-roots.
	case pgCastOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgCastSourceTargetIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 2}, true
	// B2.2 slice 2: pg_aggregate_fnoid_index — bootstrap-populated leaf-root
	// (161 BKI rows).
	case pgAggregateFnoidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	// B2.2 slice 3: pg_operator — both bootstrap-populated (2689 is
	// multi-page: ~800 BKI rows × 88B name+3-oid keys).
	case pgOperatorOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgOperatorOprnameLRNIndexOID:
		return btreeIndexKeyMeta{tupleSize: 88, nkeyatts: 4}, true
	// B3.6: pg_ts_config (3712 oid / 3608 cfgname+cfgnamespace {80,2}) +
	// pg_ts_config_map (3609 mapcfg+maptokentype+mapseqno oid+int4+int4
	// {24,3}). All empty placeholders → lazy-root.
	case pgTSConfigOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgTSConfigNameNspIndexOID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 2}, true
	case pgTSConfigMapIndexOID:
		return btreeIndexKeyMeta{tupleSize: 24, nkeyatts: 3}, true
	// B3.5: pg_ts_dict — 3604 (dictname+dictnamespace name+oid {80,2}) + 3605
	// (oid); both empty placeholders → lazy-root.
	case pgTSDictOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgTSDictNameNspIndexOID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 2}, true
	// B3.4: foreign-data trio — pg_foreign_data_wrapper (112 oid / 548
	// fdwname), pg_foreign_server (113 oid / 549 srvname), pg_user_mapping
	// (174 oid / 175 umuser+umserver). All empty placeholders → lazy-root.
	case pgFdwOidIndexOID, pgForeignServerOidIdxID, pgUserMappingOidIdxID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgFdwNameIndexOID, pgForeignServerNameIdx:
		return btreeIndexKeyMeta{tupleSize: 72, nkeyatts: 1}, true
	case pgUserMappingUserSrvIdx:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 2}, true
	// B3.3: pg_publication (6110 oid / 6111 pubname NameData {72,1}) +
	// pg_publication_rel (6112 oid / 6113 prrelid+prpubid {16,2}). All four
	// ship as empty placeholders the runtime lazily roots.
	case pgPublicationOidIndexOID, pgPublicationRelOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgPublicationPubnameIndexOID:
		return btreeIndexKeyMeta{tupleSize: 72, nkeyatts: 1}, true
	case pgPublicationRelPrrelidPrpubOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 2}, true
	// B3.2: pg_event_trigger — both indexes ship as empty metapage-only
	// placeholders (no builtin event triggers); the runtime lazily roots
	// them. 3467 is a single 64-byte NameData key (evtname), like
	// pg_namespace_nspname_index.
	case pgEventTriggerOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgEventTriggerEvtnameIndexOID:
		return btreeIndexKeyMeta{tupleSize: 72, nkeyatts: 1}, true
	// B3.1: pg_transform — both indexes ship as empty metapage-only
	// placeholders (pg_transform.dat has no builtin rows); the runtime
	// lazily roots them.
	case pgTransformOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgTransformTypeLangIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 2}, true
	// B4.1e: pg_tablespace (SHARED, global/) — 2697 oid / 2698 spcname
	// (name-only). Both bootstrap-populated with the two default rows.
	case pgTablespaceOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgTablespaceSpcnameIndexID:
		return btreeIndexKeyMeta{tupleSize: 72, nkeyatts: 1}, true
	// B2.2 slice 5: pg_opfamily (2754/2755 populated) + pg_opclass (2687
	// populated, 2686 empty placeholder) + pg_amop (2653/2654 empty
	// placeholders) + pg_amproc (2655 populated). The name-bearing keys put
	// the access-method OID FIRST (opfmethod/opcmethod), unlike the
	// name-first pg_class/pg_type family.
	case pgOpfamilyOidIndexOID, pgOpclassOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgOpfamilyAmNameNspIndexOID, pgOpclassAmNameNspIndexOID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 3}, true
	case pgAmopFamStratIndexOID, pgAmprocFamProcIndexOID:
		return btreeIndexKeyMeta{tupleSize: 24, nkeyatts: 4}, true
	case pgAmopOprFamIndexOID:
		return btreeIndexKeyMeta{tupleSize: 24, nkeyatts: 3}, true
	// B2.2 slice 4: pg_collation (3085/3164 bootstrap-populated) +
	// pg_conversion (2670 populated; 2668/2669 empty placeholders that the
	// runtime lazily roots).
	case pgCollationOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgCollationNameEncNspIndexID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 3}, true
	case pgConversionOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgConversionNameNspIndexOID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 2}, true
	case pgConversionDefaultIndexOID:
		return btreeIndexKeyMeta{tupleSize: 24, nkeyatts: 4}, true
	// M-NIGHTLY AI-20260811-014635-002: these nine were wired onto the
	// runtime insert path (insertCanonicalSysBtreeLeaf) without ever being
	// registered here. That is invisible until a leaf-root actually fills:
	// the insert path itself never consults this table, so every one of them
	// worked for the first page of entries and then failed the split with
	// "split: unsupported system btree OID N". receipt-report.spec reached it
	// on 2678 only at permutation 152. Layouts are read off the builders the
	// insert sites call, not inferred: buildIndexTupleOidKey {16,1},
	// buildIndexTupleOidInt2Key {16,2}, buildIndexTupleNameKey {72,1},
	// buildIndexTupleOidNameKey {80,2}.
	case pgIndexIndrelidIndexOID, pgIndexIndexrelidIndexOID,
		pgAttrdefOidIndexOID, pgRewriteOidIndexOID,
		pgSequenceSeqrelidIndexOID, pgExtensionOidIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 1}, true
	case pgAttrdefAdrelidAdnumIndexOID:
		return btreeIndexKeyMeta{tupleSize: 16, nkeyatts: 2}, true
	case pgExtensionNameIndexOID:
		return btreeIndexKeyMeta{tupleSize: 72, nkeyatts: 1}, true
	case pgRewriteRelRulenameIndexOID:
		return btreeIndexKeyMeta{tupleSize: 80, nkeyatts: 2}, true
	default:
		return btreeIndexKeyMeta{}, false
	}
}

// buildSysBtreeLeafPage assembles one 8 KiB BTP_LEAF page. Mirrors
// initdb.pgBuildBtreeLeafPage / pgBuildBtreeLeafRootPage. When `isRoot` is
// true the page also carries BTP_ROOT (used only for the degenerate
// re-root-as-leaf case; the split path emits isRoot=false). When `highKey`
// is non-nil it lands at item slot 1 (P_HIKEY) and data tuples occupy
// slots 2..N+1; when nil the leaf is rightmost and data tuples occupy
// slots 1..N.
func buildSysBtreeLeafPage(sortedTuples [][]byte, highKey []byte, prev, next uint32, isRoot bool) ([]byte, error) {
	page := make([]byte, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}
	h := storage.MustHeader(storage.Page(page))
	h.SetSpecial(uint16(storage.BlockSize - sizeOfBTPageOpaque))

	upper := storage.BlockSize - sizeOfBTPageOpaque
	lower := storage.SizeOfPageHeaderData

	writeItem := func(tuple []byte) error {
		if len(tuple)%8 != 0 {
			return fmt.Errorf("tuple not MAXALIGN'd: len=%d", len(tuple))
		}
		newUpper := upper - len(tuple)
		if newUpper-lower < 4 {
			return fmt.Errorf("split leaf overflow inserting tuple at lower=%d", lower)
		}
		copy(page[newUpper:upper], tuple)
		raw := uint32(uint16(newUpper)&0x7FFF) |
			(uint32(uint8(storage.ItemIDNormal)&0x3) << 15) |
			(uint32(uint16(len(tuple))&0x7FFF) << 17)
		binary.LittleEndian.PutUint32(page[lower:lower+4], raw)
		lower += 4
		upper = newUpper
		return nil
	}

	if highKey != nil {
		if err := writeItem(highKey); err != nil {
			return nil, fmt.Errorf("P_HIKEY: %w", err)
		}
	}
	for i, t := range sortedTuples {
		if err := writeItem(t); err != nil {
			return nil, fmt.Errorf("data tuple %d: %w", i, err)
		}
	}
	h.SetLower(uint16(lower))
	h.SetUpper(uint16(upper))

	off := storage.BlockSize - sizeOfBTPageOpaque
	le := binary.LittleEndian
	le.PutUint32(page[off+0:off+4], prev)
	le.PutUint32(page[off+4:off+8], next)
	le.PutUint32(page[off+8:off+12], 0) // btpo_level
	flags := btpLeafFlag
	if isRoot {
		flags |= btpRootFlag
	}
	le.PutUint16(page[off+12:off+14], flags)
	return page, nil
}

// buildSysBtreeInternalRootPage assembles one 8 KiB BTP_ROOT internal page
// carrying the supplied downlinks at slots 1..N. Mirrors
// initdb.pgBuildBtreeInternalRootPage.
func buildSysBtreeInternalRootPage(downlinks [][]byte) ([]byte, error) {
	page := make([]byte, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}
	h := storage.MustHeader(storage.Page(page))
	h.SetSpecial(uint16(storage.BlockSize - sizeOfBTPageOpaque))

	upper := storage.BlockSize - sizeOfBTPageOpaque
	lower := storage.SizeOfPageHeaderData
	for i, dl := range downlinks {
		if len(dl)%8 != 0 {
			return nil, fmt.Errorf("downlink %d not MAXALIGN'd: len=%d", i, len(dl))
		}
		newUpper := upper - len(dl)
		if newUpper-lower < 4 {
			return nil, fmt.Errorf("internal-root overflow inserting downlink %d", i)
		}
		copy(page[newUpper:upper], dl)
		raw := uint32(uint16(newUpper)&0x7FFF) |
			(uint32(uint8(storage.ItemIDNormal)&0x3) << 15) |
			(uint32(uint16(len(dl))&0x7FFF) << 17)
		binary.LittleEndian.PutUint32(page[lower:lower+4], raw)
		lower += 4
		upper = newUpper
	}
	h.SetLower(uint16(lower))
	h.SetUpper(uint16(upper))

	off := storage.BlockSize - sizeOfBTPageOpaque
	le := binary.LittleEndian
	le.PutUint32(page[off+0:off+4], pNoneBlock)
	le.PutUint32(page[off+4:off+8], pNoneBlock)
	le.PutUint32(page[off+8:off+12], 1) // btpo_level = 1
	le.PutUint16(page[off+12:off+14], btpRootFlag)
	return page, nil
}

// buildSysBtreeMinusInfDownlink mirrors initdb.pgBuildBtreeMinusInfinityDownlink.
func buildSysBtreeMinusInfDownlink(childBlock uint32) []byte {
	out := make([]byte, sizeOfIndexTupleHdr)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(childBlock>>16))
	le.PutUint16(out[2:4], uint16(childBlock&0xFFFF))
	le.PutUint16(out[4:6], 0)
	le.PutUint16(out[6:8], uint16(sizeOfIndexTupleHdr)|indexAltTIDMask)
	return out
}

// buildSysBtreeInternalDownlink mirrors initdb.pgBuildBtreeInternalDownlink.
// dataTuple must be the right leaf's first data tuple verbatim.
func buildSysBtreeInternalDownlink(dataTuple []byte, childBlock uint32, nkeyatts uint16) []byte {
	out := make([]byte, len(dataTuple))
	copy(out, dataTuple)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(childBlock>>16))
	le.PutUint16(out[2:4], uint16(childBlock&0xFFFF))
	le.PutUint16(out[4:6], nkeyatts&btOffsetMask)
	tinfo := le.Uint16(out[6:8])
	le.PutUint16(out[6:8], tinfo|indexAltTIDMask)
	return out
}

// buildSysBtreeLeafHighKey mirrors initdb.pgBuildBtreeLeafHighKey.
// Converts a data tuple into a PG18 V4 heapkeyspace pivot (high key) by
// setting INDEX_ALT_TID_MASK in t_info and writing nkeyatts into ip_posid.
func buildSysBtreeLeafHighKey(dataTuple []byte, nkeyatts uint16) []byte {
	out := make([]byte, len(dataTuple))
	copy(out, dataTuple)
	le := binary.LittleEndian
	le.PutUint16(out[4:6], nkeyatts&btOffsetMask)
	tinfo := le.Uint16(out[6:8])
	le.PutUint16(out[6:8], tinfo|indexAltTIDMask)
	return out
}

// writeSysBtreeMetapageInPlace overwrites the metapage at the start of the
// passed page slice with a fresh metapage pointing at (rootBlk, level). The
// page slice must already be BlockSize bytes (caller pinned the buffer).
func writeSysBtreeMetapageInPlace(page []byte, rootBlk uint32, level uint32) error {
	if len(page) != storage.BlockSize {
		return fmt.Errorf("metapage slice len=%d, want %d", len(page), storage.BlockSize)
	}
	for i := range page {
		page[i] = 0
	}
	if err := storage.InitPage(page); err != nil {
		return err
	}
	const sizeofBTMetaPageData = 48
	h := storage.MustHeader(storage.Page(page))
	h.SetLower(uint16(storage.SizeOfPageHeaderData + sizeofBTMetaPageData))
	h.SetUpper(uint16(storage.BlockSize - sizeOfBTPageOpaque))
	h.SetSpecial(uint16(storage.BlockSize - sizeOfBTPageOpaque))
	h.SetPagesizeVersion(storage.BlockSize | 4)

	le := binary.LittleEndian
	base := storage.SizeOfPageHeaderData
	le.PutUint32(page[base+0:base+4], btreeMagicConst)
	le.PutUint32(page[base+4:base+8], btreeVersionConst)
	le.PutUint32(page[base+8:base+12], rootBlk)
	le.PutUint32(page[base+12:base+16], level)
	le.PutUint32(page[base+16:base+20], rootBlk)
	le.PutUint32(page[base+20:base+24], level)
	le.PutUint64(page[base+32:base+40], math.Float64bits(-1.0))

	off := storage.BlockSize - sizeOfBTPageOpaque
	le.PutUint16(page[off+12:off+14], btpMetaFlag)
	return nil
}

// mergeSortedSlice splices `newKey` into the sorted slice `existing`,
// comparing only the key bytes (offset sizeOfIndexTupleHdr..). Returns the
// merged slice and the 0-based insertion index. The slice contents (each
// entry's tuple bytes) are taken as-is.
func mergeSortedSlice(existing [][]byte, newTuple []byte, cmp keyCompareFn) ([][]byte, int) {
	newKey := newTuple[sizeOfIndexTupleHdr:]
	insertIdx := len(existing)
	for i, e := range existing {
		if cmp(newKey, e[sizeOfIndexTupleHdr:]) < 0 {
			insertIdx = i
			break
		}
	}
	merged := make([][]byte, 0, len(existing)+1)
	merged = append(merged, existing[:insertIdx]...)
	merged = append(merged, newTuple)
	merged = append(merged, existing[insertIdx:]...)
	return merged, insertIdx
}

// splitLeafRootAndInsert performs the PG-canonical leaf-root → 2-leaf +
// internal-root split for the system btree at indexOID. The caller must
// hold the lock on `leafSlot` (block 1) but must NOT have inserted
// `indexTuple` (the insert failed with ErrNoSpaceInPage and triggered the
// split).
//
// Side effects:
//   - Block 1 is rewritten as a leaf-only page (left half of split).
//   - Two new blocks are appended to the relfile: right leaf + new internal root.
//   - Block 0 (metapage) is rewritten to point at the new root, level=1.
//
// On return, leafSlot is still locked and pinned. The caller is responsible
// for unlocking/unpinning it.
func splitLeafRootAndInsert(
	ctx *Context,
	indexOID uint32,
	rel storage.RelFileNode,
	leafSlot *storage.Slot,
	indexTuple []byte,
	cmp keyCompareFn,
) error {
	meta, ok := keyMetaForSysBtree(indexOID)
	if !ok {
		return fmt.Errorf("split: unsupported system btree OID %d", indexOID)
	}
	if meta.variable {
		if len(indexTuple) <= sysIndexTupleHoff || len(indexTuple)%8 != 0 {
			return fmt.Errorf("split: variable tuple size %d invalid for index %d",
				len(indexTuple), indexOID)
		}
	} else if len(indexTuple) != meta.tupleSize {
		return fmt.Errorf("split: tuple size %d does not match index %d expectation %d",
			len(indexTuple), indexOID, meta.tupleSize)
	}

	leafPage := leafSlot.Page()
	count, err := storage.PageLinePointerCount(leafPage)
	if err != nil {
		return fmt.Errorf("split: line pointer count: %w", err)
	}

	// Snapshot existing entries (1..count). Since block 1 was the leaf-root,
	// there is no high key; all slots carry data tuples.
	existing := make([][]byte, 0, count)
	for i := 1; i <= count; i++ {
		raw, err := storage.PageGetItemRawNoCopy(leafPage, uint16(i))
		if err != nil {
			return fmt.Errorf("split: read item %d: %w", i, err)
		}
		if meta.variable {
			if len(raw) <= sysIndexTupleHoff {
				return fmt.Errorf("split: existing item %d size=%d too small", i, len(raw))
			}
		} else if len(raw) != meta.tupleSize {
			return fmt.Errorf("split: existing item %d size=%d, want %d", i, len(raw), meta.tupleSize)
		}
		buf := make([]byte, len(raw))
		copy(buf, raw)
		existing = append(existing, buf)
	}

	merged, insertedIdx := mergeSortedSlice(existing, indexTuple, cmp)
	if len(merged) < 2 {
		return fmt.Errorf("split: cannot split with %d entries", len(merged))
	}
	mid := len(merged) / 2
	if mid == 0 {
		mid = 1
	}

	leftEntries := merged[:mid]
	rightEntries := merged[mid:]

	// Extend the relfile by two blocks: right leaf, then new internal root.
	rightSlot, rightBlk, err := ctx.Pool.PinNew(rel)
	if err != nil {
		return fmt.Errorf("split: extend right leaf: %w", err)
	}
	rightSlot.Lock()
	rootSlot, rootBlk, err := ctx.Pool.PinNew(rel)
	if err != nil {
		rightSlot.Unlock()
		ctx.Pool.Unpin(rightSlot)
		return fmt.Errorf("split: extend new root: %w", err)
	}
	rootSlot.Lock()
	metaSlot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		rootSlot.Unlock()
		ctx.Pool.Unpin(rootSlot)
		rightSlot.Unlock()
		ctx.Pool.Unpin(rightSlot)
		return fmt.Errorf("split: pin metapage: %w", err)
	}
	metaSlot.Lock()

	// Build the new page bytes.
	highKey := buildSysBtreeLeafHighKey(rightEntries[0], meta.nkeyatts)
	leftPageBytes, err := buildSysBtreeLeafPage(leftEntries, highKey, pNoneBlock, uint32(rightBlk), false)
	if err != nil {
		releaseSplitSlots(ctx, metaSlot, rootSlot, rightSlot)
		return fmt.Errorf("split: build left leaf: %w", err)
	}
	rightPageBytes, err := buildSysBtreeLeafPage(rightEntries, nil, uint32(sysBtreeRootBlock), pNoneBlock, false)
	if err != nil {
		releaseSplitSlots(ctx, metaSlot, rootSlot, rightSlot)
		return fmt.Errorf("split: build right leaf: %w", err)
	}
	downlinks := [][]byte{
		buildSysBtreeMinusInfDownlink(uint32(sysBtreeRootBlock)),
		buildSysBtreeInternalDownlink(rightEntries[0], uint32(rightBlk), meta.nkeyatts),
	}
	rootPageBytes, err := buildSysBtreeInternalRootPage(downlinks)
	if err != nil {
		releaseSplitSlots(ctx, metaSlot, rootSlot, rightSlot)
		return fmt.Errorf("split: build new root: %w", err)
	}

	// Install pages into pinned buffers.
	copy(leafSlot.Page(), leftPageBytes)
	copy(rightSlot.Page(), rightPageBytes)
	copy(rootSlot.Page(), rootPageBytes)
	if err := writeSysBtreeMetapageInPlace(metaSlot.Page(), uint32(rootBlk), 1); err != nil {
		releaseSplitSlots(ctx, metaSlot, rootSlot, rightSlot)
		return fmt.Errorf("split: write metapage: %w", err)
	}

	ctx.Pool.MarkDirtyForceFPI(leafSlot)
	ctx.Pool.MarkDirtyForceFPI(rightSlot)
	ctx.Pool.MarkDirtyForceFPI(rootSlot)
	ctx.Pool.MarkDirtyForceFPI(metaSlot)

	metaSlot.Unlock()
	ctx.Pool.Unpin(metaSlot)
	rootSlot.Unlock()
	ctx.Pool.Unpin(rootSlot)
	rightSlot.Unlock()
	ctx.Pool.Unpin(rightSlot)

	_ = insertedIdx // tuple has landed; per-tuple TID tracking deferred.
	return nil
}

func releaseSplitSlots(ctx *Context, slots ...*storage.Slot) {
	for _, s := range slots {
		if s == nil {
			continue
		}
		s.Unlock()
		ctx.Pool.Unpin(s)
	}
}
