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

	// indexAltTIDMask is the t_info flag PG sets on every BTREE v4 pivot
	// tuple (downlinks and leaf high keys). Mirrors INDEX_ALT_TID_MASK =
	// INDEX_AM_RESERVED_BIT (0x2000) per nbtree.h:460.
	indexAltTIDMask uint16 = 0x2000

	// btOffsetMask is the low-12-bit mask used to encode key-attribute
	// count in a pivot tuple's ip_posid. Mirrors BT_OFFSET_MASK = 0x0FFF
	// per nbtree.h:463.
	btOffsetMask uint16 = 0x0FFF

	// pNone is PG's "no sibling / no root / no parent" block-number sentinel
	// in btree-page opaque fields (btpo_prev, btpo_next) and the metapage
	// btm_root field. PG's `P_NONE` is defined as `0`, NOT as
	// `InvalidBlockNumber` (`0xFFFFFFFF`) — the two sentinels coexist in PG
	// but `P_LEFTMOST`/`P_RIGHTMOST` and `_bt_getroot` use `P_NONE == 0`. See
	// `postgres/src/include/access/nbtree.h`:
	//
	//	#define P_NONE		0
	//	#define P_LEFTMOST(opaque)  ((opaque)->btpo_prev == P_NONE)
	//	#define P_RIGHTMOST(opaque) ((opaque)->btpo_next == P_NONE)
	//
	// Writing `0xFFFFFFFF` for btpo_next would make PG read every "rightmost"
	// leaf as non-rightmost: P_FIRSTDATAKEY then treats slot 1 as a high-key
	// pivot, and `_bt_check_natts` rejects the (non-pivot) data tuple there.
	pNone uint32 = 0

	// Bulk-load packing parameters for fixed 16-byte composite-key index
	// tuples (the size emitted by pgBuildIndexTupleOidKey and
	// pgBuildIndexTupleOidInt2Key). Every line pointer takes 4 bytes, so
	// each tuple consumes 20 bytes of the 8152-byte per-page payload
	// (BlockSize − SizeOfPageHeaderData − sizeOfBTPageOpaque = 8152).
	fixedIndexTupleSize          = 16
	leafPayloadBytes             = storage.BlockSize - storage.SizeOfPageHeaderData - sizeOfBTPageOpaque
	maxTuplesPerSingleLeafRoot   = leafPayloadBytes / (fixedIndexTupleSize + 4) // 407 for 16-byte tuples
	maxTuplesPerNonRightmostLeaf = (leafPayloadBytes - (fixedIndexTupleSize + 4)) / (fixedIndexTupleSize + 4)
)

// pgBuildIndexTupleOidKey constructs an 8+4-byte=12-byte IndexTuple,
// MAXALIGN'd to 16 bytes, for a single-column oid-keyed index. The
// returned buffer matches PG's index_form_tuple output for a tuple
// with no nulls and one 4-byte oid key. Layout:
//
//	[0..1]   ItemPointerData.ip_blkid.bi_hi (heapBlk>>16, LE uint16)
//	[2..3]   ItemPointerData.ip_blkid.bi_lo (heapBlk&0xFFFF, LE uint16)
//	[4..5]   ItemPointerData.ip_posid       (heapOff, LE uint16)
//	[6..7]   t_info  (size_low_13_bits | flags=0)
//	[8..11]  oid key data                   (oid, LE uint32)
//	[12..15] MAXALIGN padding (zero)
//
// PG's _bt_search calls oidcmp() over the [8..11] window; the trailing
// MAXALIGN pad never participates in comparisons, only in tuple sizing.
//
// NOTE: PG's BlockIdData is two uint16 halves laid out as (bi_hi, bi_lo)
// in struct order — NOT a single LE uint32. For block 3:
//   correct bytes: [0,0, 3,0]   (bi_hi=0, bi_lo=3)
//   LE uint32  3 : [3,0, 0,0]   (PG decodes as block (3<<16)|0 = 196608)
// See postgres/src/include/storage/block.h::BlockIdSet / BlockIdGetBlockNumber.
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

	// ItemPointerData: bi_hi at [0..1], bi_lo at [2..3], ip_posid at [4..5].
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
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

// pgBuildBtreeBulkLoad packs sortedTuples into a complete on-disk btree
// file ready to be written verbatim under base/{1,5}/<oid> and
// global/<oid>. Output layout depends on input size:
//
//   - len(sortedTuples) ≤ maxTuplesPerSingleLeafRoot (407 for 16-byte
//     fixed-size tuples): metapage at block 0 + leaf-root at block 1.
//     The bytes are identical to the legacy
//     `pgBuildBtreeMetapageWithRoot(1,0) || pgBuildBtreeLeafRootPage(tuples)`
//     pair so this is a transparent drop-in for every existing caller.
//   - len(sortedTuples) > maxTuplesPerSingleLeafRoot: PG18 nbtsort bulk-load
//     format — metapage at block 0, N leaf pages at blocks 1..N with
//     `BTP_LEAF` flag + `btpo_prev`/`btpo_next` sibling links + `P_HIKEY`
//     at item slot 1 on every non-rightmost leaf (rightmost is slid left
//     per `_bt_slideleft`), and the root at block N+1 with `BTP_ROOT` +
//     `btpo_level=1` + N downlinks (leftmost = 8-byte zero-attribute
//     "minus infinity" pivot per `nbtsort.c:1001-1008`; later downlinks
//     each copy the corresponding leaf's first key with INDEX_ALT_TID_MASK
//     set and `ip_blkid` = child block per `BTreeTupleSetDownLink` and
//     `BTreeTupleSetNAtts`).
//
// Authoritative PG18 sources:
//
//	postgres/src/backend/access/nbtree/nbtsort.c (`_bt_buildadd`,
//	`_bt_uppershutdown`, `_bt_slideleft`)
//	postgres/src/include/access/nbtree.h (`BTPageOpaqueData`, `BTP_LEAF`,
//	`BTP_ROOT`, `INDEX_ALT_TID_MASK`, `BT_OFFSET_MASK`, `P_HIKEY`,
//	`BTreeTupleSetDownLink`, `BTreeTupleSetNAtts`)
//
// nkeyatts encodes how many key attributes each leaf tuple carries (1 for
// pure-oid keys, 2 for the oid_int2 composite). It is written into the
// `ip_posid` field of every non-leftmost internal downlink so PG's
// `BTreeTupleGetNAtts` returns the correct value during descent. Pure
// fast-path (single-leaf-root) output does not depend on nkeyatts.
func pgBuildBtreeBulkLoad(sortedTuples [][]byte, nkeyatts uint16) ([]byte, error) {
	for i, t := range sortedTuples {
		if len(t) != fixedIndexTupleSize {
			return nil, fmt.Errorf("pgBuildBtreeBulkLoad: tuple %d size=%d, want %d (only fixed 16-byte tuples supported)",
				i, len(t), fixedIndexTupleSize)
		}
	}

	if len(sortedTuples) <= maxTuplesPerSingleLeafRoot {
		leaf, err := pgBuildBtreeLeafRootPage(sortedTuples)
		if err != nil {
			return nil, fmt.Errorf("bulk-load leaf-root: %w", err)
		}
		meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)
		out := make([]byte, 0, 2*storage.BlockSize)
		out = append(out, meta...)
		out = append(out, leaf...)
		return out, nil
	}

	// Partition into leaf groups: every non-rightmost leaf reserves space
	// for a P_HIKEY (16-byte tuple + 4-byte ItemId).
	var leafGroups [][][]byte
	pos := 0
	for pos < len(sortedTuples) {
		remaining := len(sortedTuples) - pos
		if remaining <= maxTuplesPerSingleLeafRoot {
			leafGroups = append(leafGroups, sortedTuples[pos:])
			break
		}
		end := pos + maxTuplesPerNonRightmostLeaf
		leafGroups = append(leafGroups, sortedTuples[pos:end])
		pos = end
	}
	nLeaves := len(leafGroups)

	// Build leaves at blocks 1..nLeaves.
	leaves := make([][]byte, nLeaves)
	for li, group := range leafGroups {
		isRightmost := li == nLeaves-1
		prevBlock := uint32(pNone)
		nextBlock := uint32(pNone)
		if li > 0 {
			prevBlock = uint32(li) // previous leaf block (li is 0-based, leaves start at block 1, so prev = li)
		}
		if !isRightmost {
			nextBlock = uint32(li + 2) // next leaf block (li+1 in 0-based, +1 for 1-based block)
		}
		// Non-rightmost leaves get a P_HIKEY pivot tuple derived from the
		// FIRST data tuple of the NEXT leaf ("firstright"), matching PG's
		// `_bt_truncate` semantics (postgres/src/backend/access/nbtree/nbtutils.c:3776).
		// PG18 V4 heapkeyspace btrees REQUIRE the high key to satisfy
		// BTreeTupleIsPivot() — INDEX_ALT_TID_MASK in t_info and natts
		// encoded in ip_posid.
		//
		// Using `lastleft` (the last tuple of THIS leaf) here was wrong even
		// when correctly pivot-encoded: PG's _bt_compare at
		// `postgres/src/backend/access/nbtree/nbtsearch.c:829` treats a scan
		// key with `keysz == ntupatts && heapTid == NULL && scantid == NULL`
		// as STRICTLY GREATER than the pivot, so a forward search for the
		// last key on a leaf would step right past the matching leaf into a
		// page that doesn't contain it. Step 3az's INDEX_ALT_TID_MASK fix
		// silenced the assertion but kept this wrong direction; the
		// pg_shseclabel_object_index (OID 3593) lookup blew up under
		// `RelationGetIndexAttOptions → get_attoptions(3593, 2)` because
		// (3593, 2) is the last entry on leaf 1 of
		// pg_attribute_relid_attnum_index. Using firstright = (3593, 3) here
		// makes the key comparison itself settle direction
		// (2 < 3 → scan key < HIKEY → stay on leaf).
		//
		// For our unique 16-byte indexes nkeyatts == nattrs, so there's no
		// suffix to truncate and no heap-TID tiebreaker (firstright always
		// strictly > lastleft on the prior leaf). For a non-unique index
		// where consecutive tuples could tie on all keys, PG would append a
		// heap-TID tiebreaker (BT_PIVOT_HEAP_TID_ATTR) here. None of the
		// nailed indexes currently bulk-loaded need that.
		var highKey []byte
		if !isRightmost {
			highKey = pgBuildBtreeLeafHighKey(leafGroups[li+1][0], nkeyatts)
		}
		page, err := pgBuildBtreeLeafPage(group, highKey, prevBlock, nextBlock)
		if err != nil {
			return nil, fmt.Errorf("leaf %d: %w", li, err)
		}
		leaves[li] = page
	}

	// Build root at block nLeaves+1 with one downlink per leaf.
	downlinks := make([][]byte, nLeaves)
	downlinks[0] = pgBuildBtreeMinusInfinityDownlink(1 /* child block */)
	for li := 1; li < nLeaves; li++ {
		childBlock := uint32(li + 1)
		downlinks[li] = pgBuildBtreeInternalDownlink(leafGroups[li][0], childBlock, nkeyatts)
	}
	rootPage, err := pgBuildBtreeInternalRootPage(downlinks)
	if err != nil {
		return nil, fmt.Errorf("root: %w", err)
	}

	// Metapage points at the root and declares the new tree height.
	rootBlock := uint32(nLeaves + 1)
	meta := pgBuildBtreeMetapageWithRoot(rootBlock, 1 /* root is level 1 */)

	out := make([]byte, 0, (nLeaves+2)*storage.BlockSize)
	out = append(out, meta...)
	for _, leaf := range leaves {
		out = append(out, leaf...)
	}
	out = append(out, rootPage...)
	return out, nil
}

// pgBuildBtreeLeafPage assembles one 8 KiB BTP_LEAF page with the given
// sibling links. When highKey is non-nil, it is written into item slot 1
// (P_HIKEY) and the data tuples occupy slots 2..N+1; when nil, the page
// is rightmost on its level and data tuples occupy slots 1..N (mirroring
// `_bt_slideleft` in nbtsort.c).
//
// All tuples must be the fixed 16-byte size produced by
// pgBuildIndexTupleOidKey / pgBuildIndexTupleOidInt2Key — this is enforced
// by the bulk-load caller.
func pgBuildBtreeLeafPage(sortedTuples [][]byte, highKey []byte, prev, next uint32) ([]byte, error) {
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
			return fmt.Errorf("btree leaf overflow inserting tuple at lower=%d", lower)
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

	// BTPageOpaqueData: btpo_prev | btpo_next | btpo_level=0 | btpo_flags=BTP_LEAF.
	off := storage.BlockSize - sizeOfBTPageOpaque
	le := binary.LittleEndian
	le.PutUint32(page[off+0:off+4], prev)
	le.PutUint32(page[off+4:off+8], next)
	le.PutUint32(page[off+8:off+12], 0) // btpo_level
	le.PutUint16(page[off+12:off+14], btpLeaf)
	// btpo_cycleid at off+14..16 stays zero.
	return page, nil
}

// pgBuildBtreeInternalRootPage assembles one 8 KiB BTP_ROOT internal page
// carrying the supplied downlinks at slots 1..N. The root is always
// rightmost on its level, so there is no P_HIKEY (mirroring
// `_bt_slideleft` in nbtsort.c).
func pgBuildBtreeInternalRootPage(downlinks [][]byte) ([]byte, error) {
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
			return nil, fmt.Errorf("btree root overflow inserting downlink %d", i)
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
	le.PutUint32(page[off+0:off+4], uint32(pNone))
	le.PutUint32(page[off+4:off+8], uint32(pNone))
	le.PutUint32(page[off+8:off+12], 1) // btpo_level = 1 (root above the leaves)
	le.PutUint16(page[off+12:off+14], btpRoot)
	return page, nil
}

// pgBuildBtreeMinusInfinityDownlink builds the leftmost downlink tuple
// in any internal level: an 8-byte IndexTupleData header with zero key
// attributes, `INDEX_ALT_TID_MASK` set (required on BTREE v4 pivots),
// and `ip_blkid` pointing at the target child block. Mirrors
// nbtsort.c:1001-1008 (`palloc0(sizeof(IndexTupleData))` +
// `BTreeTupleSetNAtts(lowkey, 0, false)`).
func pgBuildBtreeMinusInfinityDownlink(childBlock uint32) []byte {
	out := make([]byte, sizeOfIndexTupleData) // 8 bytes
	le := binary.LittleEndian
	// ip_blkid = (bi_hi, bi_lo) struct-order halves, NOT a single LE uint32.
	le.PutUint16(out[0:2], uint16(childBlock>>16))
	le.PutUint16(out[2:4], uint16(childBlock&0xFFFF))
	// ip_posid = nkeyatts & BT_OFFSET_MASK = 0 (minus infinity has zero keys).
	le.PutUint16(out[4:6], 0)
	// t_info: lower 13 bits = tuple size; INDEX_ALT_TID_MASK flag (= 0x2000).
	le.PutUint16(out[6:8], uint16(sizeOfIndexTupleData)|indexAltTIDMask)
	return out
}

// pgBuildBtreeInternalDownlink builds a non-leftmost downlink tuple: a
// full copy of the child page's first data tuple (PG's bulk-load copies
// the leaf's `btps_lowkey` upward when finishing a leaf, see
// `nbtsort.c:960 _bt_buildadd(wstate, state->btps_next, state->btps_lowkey,…)`),
// with the heap-TID `ip_blkid` overwritten to the child block, `ip_posid`
// set to `nkeyatts & BT_OFFSET_MASK`, and `INDEX_ALT_TID_MASK` set in
// `t_info` (required on BTREE v4 pivots — see `BTreeTupleSetNAtts` in
// nbtree.h).
//
// dataTuple must be the leaf's first data tuple verbatim (16 bytes).
func pgBuildBtreeInternalDownlink(dataTuple []byte, childBlock uint32, nkeyatts uint16) []byte {
	out := make([]byte, len(dataTuple))
	copy(out, dataTuple)
	le := binary.LittleEndian
	// Overwrite ip_blkid with child block (struct-order bi_hi/bi_lo).
	le.PutUint16(out[0:2], uint16(childBlock>>16))
	le.PutUint16(out[2:4], uint16(childBlock&0xFFFF))
	// ip_posid encodes nkeyatts in the low 12 bits (BT_OFFSET_MASK = 0x0FFF).
	le.PutUint16(out[4:6], nkeyatts&btOffsetMask)
	// Preserve the tuple's existing data layout; flip on INDEX_ALT_TID_MASK
	// while keeping the size bits intact. t_info size bits cover the whole
	// 16-byte tuple already (set by the data-tuple builder), so we OR in
	// the pivot flag without touching the low 13 bits.
	tinfo := le.Uint16(out[6:8])
	le.PutUint16(out[6:8], tinfo|indexAltTIDMask)
	return out
}

// pgBuildBtreeLeafHighKey transforms a leaf data tuple into a PG18-compatible
// leaf P_HIKEY pivot tuple. PG18 V4 heapkeyspace btrees require leaf high
// keys to satisfy `BTreeTupleIsPivot()` (INDEX_ALT_TID_MASK set in t_info and
// BT_IS_POSTING NOT set in ip_posid), otherwise `_bt_check_natts`
// (`postgres/src/backend/access/nbtree/nbtutils.c:4163`) returns false and
// every PG backend aborts at `nbtsearch.c:707` the first time `_bt_compare`
// descends past the multi-leaf root. Step 3av's `pgBuildBtreeBulkLoad` shipped
// with a verbatim data-tuple copy for the high key, which was silently
// accepted by every prior nailed index because they all fit on a single
// leaf-root (no high key emitted). Step 3aw pushed
// `pg_attribute_relid_attnum_index` (OID 2659) past the 407-tuple cap so the
// slow path activated and the latent bug surfaced as
//
//	TRAP: failed Assert("_bt_check_natts(rel, key->heapkeyspace, page, offnum)"),
//	File: "nbtsearch.c", Line: 707
//
// during `RelationCacheInitializePhase3 → systable_getnext` on every standby
// backend startup.
//
// Encoding (mirrors a non-truncated heapkeyspace pivot with no heap-TID
// tiebreaker — BT_PIVOT_HEAP_TID_ATTR clear, so the original heap TID
// previously held in t_tid.ip_blkid becomes don't-care metadata bytes that
// PG ignores for pivot tuples):
//
//   - t_info |= INDEX_ALT_TID_MASK (preserve low 13 size bits intact)
//   - ip_posid = nkeyatts (low 12 bits; status bits cleared)
//   - Key payload (bytes [8..]) and total tuple size unchanged
//
// `nkeyatts` must match the index's `indnkeyatts` (1 for pure-oid indexes,
// 2 for the oid+int2 composite). Tuples returned by this helper round-trip
// `BTreeTupleIsPivot == true`, `BTreeTupleGetNAtts == nkeyatts`, and
// `BTreeTupleGetHeapTID == NULL`, which is exactly what
// `_bt_check_natts`'s heapkeyspace pivot branch expects.
func pgBuildBtreeLeafHighKey(dataTuple []byte, nkeyatts uint16) []byte {
	out := make([]byte, len(dataTuple))
	copy(out, dataTuple)
	le := binary.LittleEndian
	// ip_posid: low 12 bits = nkeyatts; clear status bits (no BT_IS_POSTING,
	// no BT_PIVOT_HEAP_TID_ATTR).
	le.PutUint16(out[4:6], nkeyatts&btOffsetMask)
	// t_info: OR in INDEX_ALT_TID_MASK; preserve size bits in the low 13.
	tinfo := le.Uint16(out[6:8])
	le.PutUint16(out[6:8], tinfo|indexAltTIDMask)
	return out
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


// bootstrapPgDatabaseOidIndex overwrites the empty btree placeholder at
// global/2672 with a populated 2-page btree (metapage + leaf-root)
// carrying one oid-keyed IndexTuple per pg_database heap row written by
// bootstrapPostgresDatabase.
//
// M0106-0010 step 3cs: surfaced by TestE2E_FailoverGoopgToPG/async after
// step 3cr made pg_class.reltablespace=1664 route shared-catalog file
// paths to global/<relfilenode>. The empty placeholder satisfied
// `BasicOpenFile` but PG's CheckMyDatabase (postinit.c:335) probes
// the syscache DATABASEOID — backed by pg_database_oid_index (PG18
// OID 2672) — to validate that MyDatabaseId references a live row in
// pg_database. With an empty btree the syscache returns NULL and the
// backend FATALs with `cache lookup failed for database 5` (the
// "postgres" database OID that pg_basebackup connected against).
//
// pg_database tuples are written by bootstrapPostgresDatabase in
// deterministic order onto a fresh block 0:
//
//	offset 1: template1 (oid=1)
//	offset 2: postgres  (oid=5)
//
// Keys must be ascending in the leaf page (btree invariant); oids 1 and
// 5 are already sorted so we emit them in the natural order. Index
// lives only under global/ — pg_database is a shared catalog (relisshared
// = true, reltablespace = 1664) and PG resolves it exclusively there.
func bootstrapPgDatabaseOidIndex(dataDir string) error {
	type oidTid struct {
		oid uint32
		tid uint16
	}
	entries := []oidTid{
		{oid: 1, tid: 1}, // template1
		{oid: 5, tid: 2}, // postgres
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].oid < entries[j].oid })

	tuples := make([][]byte, len(entries))
	for i, e := range entries {
		tuples[i] = pgBuildIndexTupleOidKey(0 /* heap block */, e.tid, e.oid)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_database_oid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	return os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(2672, 10)), file, 0o600)
}


// bootstrapPgDatabaseDatnameIndex (M0106-0010 step 3dh) overwrites the empty
// btree placeholder at global/2671 with a populated 2-page btree (metapage +
// leaf-root) carrying one name-keyed IndexTuple per pg_database heap row
// written by bootstrapPostgresDatabase.
//
// Surfaced by TestE2E_FailoverGoopgToPG/async after step 3dg fixed the SEGV
// in pg_authid_rolname_index — the next connect attempt FATALs with
//
//	FATAL: 3D000: database "postgres" does not exist
//
// because InitPostgres' get_db_info() looks up the database by name through
// the DATABASENAME syscache, which is backed by pg_database_datname_index
// (PG18 OID 2671). With an empty btree the lookup misses and the backend
// rejects the connection before any user query runs.
//
// pg_database tuples are written by bootstrapPostgresDatabase in
// deterministic order onto a fresh block 0:
//
//	offset 1: template1
//	offset 2: postgres
//
// Keys must be ascending in the leaf page (btree invariant). The name_ops
// comparator (btnamecmp) does an unsigned byte comparison of the 64-byte
// zero-padded NameData blob, so the lexicographic order is:
//
//	"postgres"  < "template1"
//
// (the third byte 's' (0x73) < 'm' (0x6d) — wait: "postgres"[1]='o' and
// "template1"[1]='e', so the second byte already decides: 'o' (0x6f) >
// 'e' (0x65)). Recheck: 'p' (0x70) > 't' (0x74)? No: 'p'=0x70, 't'=0x74,
// so "postgres" < "template1" iff 'p' < 't' — true. So "postgres" comes
// first. Index lives only under global/ — pg_database is a shared catalog
// (relisshared = true, reltablespace = 1664).
func bootstrapPgDatabaseDatnameIndex(dataDir string) error {
	type nameTid struct {
		name string
		tid  uint16
	}
	entries := []nameTid{
		{name: "template1", tid: 1},
		{name: "postgres", tid: 2},
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	tuples := make([][]byte, len(entries))
	for i, e := range entries {
		tuples[i] = pgBuildIndexTupleNameKey(0 /* heap block */, e.tid, e.name)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_database_datname_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	return os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(2671, 10)), file, 0o600)
}


// bootstrapPgTypeOidIndex (M0106-0010 step 3cz) overwrites the empty btree
// placeholder at base/{1,5}/2703 (pg_type_oid_index) with a populated 2-page
// btree carrying one oid-keyed IndexTuple per pg_type heap row written by
// bootstrapPgTypeTuples. PG's TupleDescInitEntry → typeidType →
// SearchSysCache1(TYPEOID, ObjectIdGetDatum(oid)) takes an indexed path on
// the standby; without populated leaf entries the lookup returns NULL and
// the first client backend FATALs with "XX000: cache lookup failed for
// type 23" at tupdesc.c:896 (int4 OID=23). pg_type is per-database, so
// the file is written under base/1 (template1) and base/5 (postgres) only;
// there is no global/ copy.
func bootstrapPgTypeOidIndex(dataDir string, tids []heapTID) error {
	entries := pgTypeInitialEntries()
	if len(entries) != len(tids) {
		return fmt.Errorf("pg_type_oid_index: entries=%d tids=%d", len(entries), len(tids))
	}
	type indexed struct {
		oid   uint32
		block uint32
		off   uint16
	}
	items := make([]indexed, len(entries))
	for i, e := range entries {
		items[i] = indexed{oid: e.OID, block: tids[i].Block, off: tids[i].Offset}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].oid < items[j].oid })

	tuples := make([][]byte, len(items))
	for i, it := range items {
		tuples[i] = pgBuildIndexTupleOidKey(it.block, it.off, it.oid)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_type_oid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2703, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_type_oid_index in %s: %w", dir, err)
		}
	}
	return nil
}

// bootstrapPgProcOidIndex (M0106-0010 step 3db) overwrites the empty btree
// placeholder at base/{1,5}/2690 (pg_proc_oid_index) with a populated 2-page
// btree carrying one oid-keyed IndexTuple per pg_proc heap row written by
// bootstrapPgProcTuples. PG's SearchSysCache1(PROCOID, ObjectIdGetDatum(oid))
// takes the indexed path on the standby; without populated leaf entries the
// lookup returns NULL and an InitPostgres-stage backend SIGSEGVs when
// downstream code dereferences GETSTRUCT on the NULL tuple. pg_proc is
// per-database, so the file is written under base/1 (template1) and base/5
// (postgres) only; there is no global/ copy.
func bootstrapPgProcOidIndex(dataDir string, tids []heapTID) error {
	entries := pgProcInitialEntries()
	if len(entries) != len(tids) {
		return fmt.Errorf("pg_proc_oid_index: entries=%d tids=%d", len(entries), len(tids))
	}
	type indexed struct {
		oid   uint32
		block uint32
		off   uint16
	}
	items := make([]indexed, len(entries))
	for i, e := range entries {
		items[i] = indexed{oid: e.OID, block: tids[i].Block, off: tids[i].Offset}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].oid < items[j].oid })

	tuples := make([][]byte, len(items))
	for i, it := range items {
		tuples[i] = pgBuildIndexTupleOidKey(it.block, it.off, it.oid)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_proc_oid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1 /* root block */, 0 /* leaf level */)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2690, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_proc_oid_index in %s: %w", dir, err)
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
//	[0..1]   ItemPointerData.ip_blkid.bi_hi (heapBlk>>16, LE uint16)
//	[2..3]   ItemPointerData.ip_blkid.bi_lo (heapBlk&0xFFFF, LE uint16)
//	[4..5]   ItemPointerData.ip_posid       (heapOff, LE uint16)
//	[6..7]   t_info (size_low_13_bits | flags=0)
//	[8..11]  attrelid (LE uint32, oid_ops compares as unsigned)
//	[12..13] attnum   (LE int16,  int2_ops compares as signed)
//	[14..15] MAXALIGN padding (zero)
//
// See pgBuildIndexTupleOidKey for BlockIdData layout rationale.
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

	// ItemPointerData: bi_hi at [0..1], bi_lo at [2..3], ip_posid at [4..5].
	// See pgBuildIndexTupleOidKey for the BlockIdData layout rationale.
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
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
	// Bulk-load builder: byte-identical to the legacy meta+leaf-root pair
	// for sub-407 inputs; emits a 2-level btree (meta + N leaves + root)
	// once the input crosses the single-leaf cap, unblocking pg_extension
	// and any future nailed rel that pushes this index past one page. See
	// docs/design/0106-0010-step3au-multi-leaf-btree-prereq.md.
	// nkeyatts = 2 because the index is composite (attrelid oid, attnum int2).
	file, err := pgBuildBtreeBulkLoad(tuples, 2)
	if err != nil {
		return fmt.Errorf("pg_attribute_relid_attnum_index bulk-load: %w", err)
	}

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

// pgBuildIndexTupleOidOidOidInt2Key constructs an IndexTuple for the
// (oid amprocfamily, oid amproclefttype, oid amprocrighttype,
// int2 amprocnum) composite key used by pg_amproc_fam_proc_index
// (OID 2655). Mirrors the byte layout PG's `index_form_tuple` produces
// for a no-nulls 4-attribute tuple with all-fixed-width keys:
//
//	[0..1]   ItemPointerData.ip_blkid.bi_hi (heapBlk>>16, LE uint16)
//	[2..3]   ItemPointerData.ip_blkid.bi_lo (heapBlk&0xFFFF, LE uint16)
//	[4..5]   ItemPointerData.ip_posid       (heapOff, LE uint16)
//	[6..7]   t_info (size_low_13_bits | flags=0)
//	[8..11]  amprocfamily   (LE uint32)
//	[12..15] amproclefttype (LE uint32)
//	[16..19] amprocrighttype (LE uint32)
//	[20..21] amprocnum       (LE int16)
//	[22..23] MAXALIGN padding (zero)
//
// PG's _bt_compare walks the 4 keys lexicographically using their
// respective opclasses (oid_ops, oid_ops, oid_ops, int2_ops) over the
// fixed windows above; the trailing MAXALIGN pad never participates
// in comparisons, only in tuple sizing.
//
// Total size = MAXALIGN(IndexTupleHeader + 4 + 4 + 4 + 2) =
// MAXALIGN(22) = 24. The on-disk size stored in t_info's low 13 bits
// is the MAXALIGN'd total (24) so PG `IndexTupleSize` matches
// `len(out)`.
//
// See pgBuildIndexTupleOidKey for BlockIdData layout rationale.
func pgBuildIndexTupleOidOidOidInt2Key(heapBlk uint32, heapOff uint16, family, lefttype, righttype uint32, num int16) []byte {
	const (
		hoff = 8
		size = 24 // MAXALIGN(hoff + 4 + 4 + 4 + 2) = MAXALIGN(22) = 24
	)
	out := make([]byte, size)
	le := binary.LittleEndian

	// ItemPointerData: bi_hi at [0..1], bi_lo at [2..3], ip_posid at [4..5].
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)

	// t_info: lower 13 bits = size; no INDEX_VAR_MASK / INDEX_NULL_MASK.
	le.PutUint16(out[6:8], uint16(size)&indexSizeMask)

	// Key data — four packed columns, no inter-column padding (all
	// fixed-width; alignment of int2 after three 4-byte oids is
	// satisfied because the running offset 8+12=20 is already 2-aligned).
	le.PutUint32(out[hoff:hoff+4], family)
	le.PutUint32(out[hoff+4:hoff+8], lefttype)
	le.PutUint32(out[hoff+8:hoff+12], righttype)
	le.PutUint16(out[hoff+12:hoff+14], uint16(num))
	// Bytes [22..23] are MAXALIGN padding — already zero.
	return out
}

// bootstrapPgAmprocFamProcIndex overwrites the empty btree placeholders
// at base/{1,5}/2655 + global/2655 with a populated 2-page btree
// (metapage + leaf-root) carrying one IndexTuple per pg_amproc heap row,
// keyed on (amprocfamily, amproclefttype, amprocrighttype, amprocnum).
//
// M0106-0010 step 3cw: surfaced by TestE2E_FailoverGoopgToPG/async after
// step 3cv cleared the pg_shseclabel attalign FATAL. The next FATAL is
//
//	missing support function 1 for attribute 1 of index "pg_authid_rolname_index"
//
// emitted from `postgres/src/backend/access/index/indexam.c:946` when
// `irel->rd_support[procindex] == InvalidOid`. The rd_support array is
// filled during `IndexSupportInitialize` via a sysscan of
// `pg_amproc_fam_proc_index`. Step 3k's empty btree placeholder returned
// zero rows for every (family, lefttype, righttype, num) probe, so PG
// stored InvalidOid in every slot of rd_support and FATALed on the
// first index dispatch (`pg_authid_rolname_index` opens early during
// `InitPostgres` for client-auth lookups).
//
// pgAmprocInitialEntries currently emits 36 rows (≤ one 8-KiB page even
// at 24-byte tuples + 4-byte line pointers = 28 bytes per item =
// ~1 KiB total) so the simple single-leaf-root builder is sufficient.
// If the entry count later exceeds ~292 rows the bulk-load builder
// must be generalised to 24-byte tuples — left as a future step.
//
// nkeyatts = 4 because the index is fully composite over four
// fixed-width columns (3× oid_ops + int2_ops).
func bootstrapPgAmprocFamProcIndex(dataDir string, tids []heapTID) error {
	entries := pgAmprocInitialEntries()
	if len(entries) != len(tids) {
		return fmt.Errorf("pg_amproc_fam_proc_index: entries=%d tids=%d", len(entries), len(tids))
	}
	type indexed struct {
		family, lefttype, righttype uint32
		num                         int16
		block                       uint32
		off                         uint16
	}
	items := make([]indexed, len(entries))
	for i, e := range entries {
		items[i] = indexed{
			family:    e.Family,
			lefttype:  e.LeftType,
			righttype: e.RightType,
			num:       int16(e.Num),
			block:     tids[i].Block,
			off:       tids[i].Offset,
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.family != b.family {
			return a.family < b.family
		}
		if a.lefttype != b.lefttype {
			return a.lefttype < b.lefttype
		}
		if a.righttype != b.righttype {
			return a.righttype < b.righttype
		}
		return a.num < b.num
	})

	tuples := make([][]byte, len(items))
	for i, it := range items {
		tuples[i] = pgBuildIndexTupleOidOidOidInt2Key(it.block, it.off, it.family, it.lefttype, it.righttype, it.num)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_amproc_fam_proc_index leaf: %w", err)
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
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2655, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_amproc_fam_proc_index in %s: %w", dir, err)
		}
	}
	return nil
}


// pgBuildIndexTupleNameKey returns the on-disk IndexTuple bytes for a single
// fixed-width NAME-keyed btree leaf entry (e.g. pg_authid_rolname_index).
//
// Layout (no nulls, fixed-width NAMEDATALEN=64 key):
//
//	[0..1]   bi_hi          (ItemPointerData)
//	[2..3]   bi_lo
//	[4..5]   ip_posid
//	[6..7]   t_info         (low 13 bits = size; INDEX_VAR/NULL bits clear)
//	[8..71]  NameData       (zero-padded to NAMEDATALEN)
//
// Total = MAXALIGN(IndexTupleHeader + NAMEDATALEN) = MAXALIGN(72) = 72 — no
// trailing pad required because 72 is already 8-byte aligned. NAMEDATALEN is
// the PG18-canonical 64 so this matches `_bt_form_tuple → index_form_tuple`
// for a single NAME column with no nulls.
func pgBuildIndexTupleNameKey(heapBlk uint32, heapOff uint16, name string) []byte {
	const (
		nameDataLen = 64
		hoff        = 8
		size        = hoff + nameDataLen // 72, already MAXALIGN'd
	)
	out := make([]byte, size)
	le := binary.LittleEndian

	// ItemPointerData.
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)

	// t_info: lower 13 bits hold tuple size.
	le.PutUint16(out[6:8], uint16(size)&indexSizeMask)

	// NameData: NAMEDATALEN bytes, zero-padded. PG's NameData is a fixed-
	// size struct (NAMEDATALEN char array); pg_authid.rolname is stored as
	// exactly 64 bytes with trailing zeros for short names. Truncate
	// silently if the caller hands us a longer string — that mirrors PG's
	// `namestrcpy` semantics.
	n := len(name)
	if n > nameDataLen {
		n = nameDataLen
	}
	copy(out[hoff:hoff+n], name[:n])
	return out
}

// pgBuildIndexTupleOidNameKey returns the on-disk IndexTuple bytes for
// a composite (oid, name) btree leaf entry. Used by
// pg_rewrite_rel_rulename_index (OID 2693, indkey=[ev_class, rulename]).
//
// PG's `index_form_tuple` for an (oid att1, name att2) tuple lays out:
//
//	[0..1]   bi_hi          (ItemPointerData)
//	[2..3]   bi_lo
//	[4..5]   ip_posid
//	[6..7]   t_info         (low 13 bits = size, no var/null bits)
//	[8..11]  oid key        (LE uint32 — oid_ops compares as unsigned)
//	[12..75] NameData       (NAMEDATALEN bytes, zero-padded)
//	[76..79] MAXALIGN padding (zero)
//
// Total = MAXALIGN(IndexTupleHeader + sizeof(oid) + NAMEDATALEN) =
// MAXALIGN(8 + 4 + 64) = MAXALIGN(76) = 80. NameData's typalign is
// 'c' so no inter-attribute padding is needed between the oid and the
// name. The trailing MAXALIGN pad never participates in comparisons.
func pgBuildIndexTupleOidNameKey(heapBlk uint32, heapOff uint16, oid uint32, name string) []byte {
	const (
		nameDataLen = 64
		hoff        = 8
		size        = 80 // MAXALIGN(hoff + 4 + nameDataLen) = MAXALIGN(76) = 80
	)
	out := make([]byte, size)
	le := binary.LittleEndian

	// ItemPointerData.
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)

	// t_info: tuple size in low 13 bits.
	le.PutUint16(out[6:8], uint16(size)&indexSizeMask)

	// Key data.
	le.PutUint32(out[hoff:hoff+4], oid)
	n := len(name)
	if n > nameDataLen {
		n = nameDataLen
	}
	copy(out[hoff+4:hoff+4+n], name[:n])
	// Bytes [76..79] are MAXALIGN padding — already zero.
	return out
}

// bootstrapPgRewriteOidIndex overwrites base/{1,5}/2692 with a populated
// 2-block btree (metapage + leaf-root) carrying one oid-keyed
// IndexTuple per Form_pg_rewrite heap row. M0106-0010 Step 3dm phase B.
//
// PG's RelationCacheInitializePhase3 / relcache view-loading path
// fetches the ON-SELECT rule via SearchSysCache1(RULEOID, oid). Without
// this leaf the cache lookup falls through to a sequential heap scan
// (still correct) — but the rel_rulename leaf (OID 2693) IS load-
// bearing because relcache view opening calls SearchSysCache2(
// RULERELNAME, ev_class, rulename). We bootstrap both for parity.
func bootstrapPgRewriteOidIndex(dataDir string, tids map[uint32]heapTID) error {
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
		return fmt.Errorf("pg_rewrite_oid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1, 0)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2692, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_rewrite_oid_index in %s: %w", dir, err)
		}
	}
	return nil
}

// bootstrapPgRewriteRelRulenameIndex overwrites base/{1,5}/2693 with a
// populated 2-block btree carrying one (ev_class, rulename)-keyed
// IndexTuple per Form_pg_rewrite heap row. M0106-0010 Step 3dm phase B.
//
// PG's RelationBuildRuleLock (postgres/src/backend/utils/cache/relcache.c)
// runs systable_beginscan(RewriteRelRulenameIndexId, ev_class=rel_oid)
// every time a relhasrules=true relation is opened, so this leaf must
// exist before PG's first probe of the seeded view.
//
// rewriteEntries supplies (ev_class, rulename) → heap TID for each
// rule; bootstrapPgRewriteTuples produces the OID-keyed map and the
// caller looks up the per-rule columns from pgRewriteInitialEntries.
func bootstrapPgRewriteRelRulenameIndex(dataDir string, tids map[uint32]heapTID) error {
	type entry struct {
		evClass  uint32
		ruleName string
		block    uint32
		off      uint16
	}
	rewriteEntries := pgRewriteInitialEntries()
	entries := make([]entry, 0, len(rewriteEntries))
	for _, e := range rewriteEntries {
		t, ok := tids[e.OID]
		if !ok {
			return fmt.Errorf("pg_rewrite_rel_rulename_index: no heap TID for rule OID %d", e.OID)
		}
		entries = append(entries, entry{
			evClass:  e.EvClass,
			ruleName: e.RuleName,
			block:    t.Block,
			off:      t.Offset,
		})
	}
	// Composite key: ev_class ASC, then rulename ASC (memcmp over the
	// zero-padded NameData — name_ops uses bttextcmp-equivalent byte
	// comparison for the C locale).
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].evClass != entries[j].evClass {
			return entries[i].evClass < entries[j].evClass
		}
		return entries[i].ruleName < entries[j].ruleName
	})

	tuples := make([][]byte, len(entries))
	for i, e := range entries {
		tuples[i] = pgBuildIndexTupleOidNameKey(e.block, e.off, e.evClass, e.ruleName)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_rewrite_rel_rulename_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1, 0)

	file := make([]byte, 0, 2*storage.BlockSize)
	file = append(file, meta...)
	file = append(file, leaf...)

	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(2693, 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_rewrite_rel_rulename_index in %s: %w", dir, err)
		}
	}
	return nil
}

// bootstrapPgAuthidIndexes populates the two pg_authid critical indexes
// (rolname → OID 2676; OID → OID 2677) with one IndexTuple per bootstrapped
// pg_authid heap row.  Both are shared catalogs and live exclusively under
// `global/`; the empty btree placeholders written by
// `bootstrapSharedCatalogPlaceholders` are overwritten in place.
//
// Without these populated indexes, PG's
// `InitializeSessionUserId → SearchSysCache1(AUTHNAME, $USER)` returns
// `(InvalidOid, NULL)` and PG FATALs with `28000: role "<os-user>" does
// not exist` — even though the corresponding heap row is sitting in
// `global/1260`.  Step 3cx (M0106-0010).
func bootstrapPgAuthidIndexes(dataDir string, entries []pgAuthidEntry) error {
	if len(entries) == 0 {
		return fmt.Errorf("pg_authid indexes: no entries")
	}

	// pg_authid_oid_index (2677): sort by OID asc; oid-keyed.
	oidSorted := append([]pgAuthidEntry(nil), entries...)
	sort.Slice(oidSorted, func(i, j int) bool { return oidSorted[i].OID < oidSorted[j].OID })
	oidTuples := make([][]byte, len(oidSorted))
	for i, e := range oidSorted {
		oidTuples[i] = pgBuildIndexTupleOidKey(e.TID.Block, e.TID.Offset, e.OID)
	}
	oidLeaf, err := pgBuildBtreeLeafRootPage(oidTuples)
	if err != nil {
		return fmt.Errorf("pg_authid_oid_index leaf: %w", err)
	}
	oidMeta := pgBuildBtreeMetapageWithRoot(1, 0)
	oidFile := make([]byte, 0, 2*storage.BlockSize)
	oidFile = append(oidFile, oidMeta...)
	oidFile = append(oidFile, oidLeaf...)
	if err := os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(2677, 10)), oidFile, 0o600); err != nil {
		return fmt.Errorf("write pg_authid_oid_index: %w", err)
	}

	// pg_authid_rolname_index (2676): sort by rolname asc (lexicographic on
	// raw bytes — matches `bttextcmp`/`btnamecmp` for ASCII single-byte
	// rolnames, which is what bootstrap seeds).  name-keyed leaf tuples.
	nameSorted := append([]pgAuthidEntry(nil), entries...)
	sort.Slice(nameSorted, func(i, j int) bool { return nameSorted[i].Rolname < nameSorted[j].Rolname })
	nameTuples := make([][]byte, len(nameSorted))
	for i, e := range nameSorted {
		nameTuples[i] = pgBuildIndexTupleNameKey(e.TID.Block, e.TID.Offset, e.Rolname)
	}
	nameLeaf, err := pgBuildBtreeLeafRootPage(nameTuples)
	if err != nil {
		return fmt.Errorf("pg_authid_rolname_index leaf: %w", err)
	}
	nameMeta := pgBuildBtreeMetapageWithRoot(1, 0)
	nameFile := make([]byte, 0, 2*storage.BlockSize)
	nameFile = append(nameFile, nameMeta...)
	nameFile = append(nameFile, nameLeaf...)
	if err := os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(2676, 10)), nameFile, 0o600); err != nil {
		return fmt.Errorf("write pg_authid_rolname_index: %w", err)
	}
	return nil
}
